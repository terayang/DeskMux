/**
 * Renderer-side VNC session controller (stage 5b): owns the noVNC client
 * lifecycle for the desktop panel and exposes its status as a zustand store.
 *
 * Connection path: attachVnc() asks the main process for a VNC bridge
 * (window.anyremote.vnc.startBridge — the bridge terminates the RFB security
 * handshake, including Apple DH, so the renderer sees an auth-free loopback
 * endpoint), then points a noVNC RFB client at the bridge's WebSocket.
 *
 * Error classification: target-side failures reach the renderer as private
 * WebSocket close codes (VNC_BRIDGE_CLOSE_CODES, src/shared/vnc.ts), which
 * noVNC does not surface through its events. attachVnc() therefore hands RFB
 * a WebSocket it constructed itself and records the close event directly;
 * IPC-level startBridge failures are classified from the IpcInvokeError code.
 * Both paths collapse into the same VncErrorKind set for the UI.
 *
 * Multi-session model (F6): the former single module-level live slot is now
 * a map keyed by SessionContext.id — every session owns an independent
 * bridge + RFB instance + status store. React 18 StrictMode double-mounts
 * and tab close/reopen both remount a panel; every attach/detach disposes
 * that session's previous connection, and async continuations plus event
 * handlers verify they still own their session's slot before touching state.
 */

import { create, useStore } from 'zustand'
import { ipcErrorCode } from '../../shared/ipc'
import type { VncBridgeHandle, VncStartBridgeParams } from '../../shared/ipc'
import { VNC_BRIDGE_CLOSE_CODES } from '../../shared/vnc'

/**
 * @novnc/novnc ships no TypeScript declarations, so the constructor is
 * imported untyped and bound to the small surface AnyRemote uses. Note: the
 * package's exports map exposes only the root entry ('@novnc/novnc' ->
 * core/rfb.js); the legacy '@novnc/novnc/lib/rfb.js' subpath no longer
 * exists in 1.7.x.
 */
// @ts-expect-error — @novnc/novnc has no bundled type declarations.
import RFBUntyped from '@novnc/novnc'

/** The noVNC RFB instance: an EventTarget whose events are CustomEvents. */
type RfbInstance = EventTarget & {
  /** Scales the remote framebuffer to fit the local viewport. */
  scaleViewport: boolean
  /** Shows a small dot cursor when the server sends no cursor shape. */
  showDotCursor: boolean
  /** Tight JPEG quality 0-9; the setter re-sends SetEncodings when connected. */
  qualityLevel: number
  /** Zlib compression level 0-9; the setter re-sends SetEncodings when connected. */
  compressionLevel: number
  /** Drops keyboard/mouse input instead of forwarding it when true. */
  viewOnly: boolean
  /** Starts a clean disconnection; a 'disconnect' event follows. */
  disconnect(): void
  /** Sends local clipboard text to the server (RFB ClientCutText). */
  clipboardPasteFrom(text: string): void
  /**
   * Sends a key event (noVNC 1.7 signature): X11 keysym + DOM code; down
   * omitted sends press+release. A no-op unless connected and not viewOnly
   * (noVNC checks both internally). With QEMU extended-key-event support the
   * DOM code's XT scancode is sent; otherwise the keysym goes out as a plain
   * RFB KeyEvent (keysym 0/falsy is dropped, so always pass one).
   */
  sendKey(keysym: number, code: string, down?: boolean): void
  /** Moves keyboard focus to the noVNC canvas. */
  focus(): void
  /** Removes keyboard focus from the noVNC canvas. */
  blur(): void
}

interface RfbConstructor {
  new (
    target: HTMLElement,
    urlOrChannel: string | WebSocket,
    options?: {
      shared?: boolean
      credentials?: { username?: string; password?: string; target?: string }
      wsProtocols?: string[]
    }
  ): RfbInstance
}

const RFB = RFBUntyped as unknown as RfbConstructor

/** Zoom modes of the desktop viewport. */
export type VncScaleMode = 'fit' | 'actual'

/** Failure categories shared by the IPC path and the WS close-code path. */
export type VncErrorKind = 'auth' | 'connection' | 'protocol' | 'timeout' | 'unknown'

export type VncStatus = 'idle' | 'connecting' | 'connected' | 'error'

/** One live VNC connection of one session (frontend session id keyed below). */
interface LiveSession {
  rfb: RfbInstance | null
  bridgeId: string | null
  /** Torn down already: events and late async steps must no-op. */
  disposed: boolean
  /** WebSocket close code observed by our own listener (null while open). */
  closeCode: number | null
  /** Removes the document-level paste listener (null until attached). */
  disposePaste: (() => void) | null
}

/** Live VNC connections, keyed by frontend session id (SessionContext.id). */
const liveBySession = new Map<string, LiveSession>()

/** Last attach arguments per session, kept so error/idle overlays can reconnect. */
const lastAttachBySession = new Map<string, { container: HTMLElement; params: VncStartBridgeParams }>()

/** Maps an IPC invoke failure (startBridge) onto a VncErrorKind. */
function classifyIpcError(err: unknown): VncErrorKind {
  switch (ipcErrorCode(err)) {
    case 'AUTH_FAILED':
      return 'auth'
    case 'UNREACHABLE':
      return 'connection'
    case 'PROTOCOL_ERROR':
      return 'protocol'
    case 'TIMEOUT':
      return 'timeout'
    default:
      return 'unknown'
  }
}

/** Maps a bridge WebSocket close code (src/shared/vnc.ts) onto a VncErrorKind. */
function classifyCloseCode(code: number | null): VncErrorKind {
  switch (code) {
    case VNC_BRIDGE_CLOSE_CODES.auth:
      return 'auth'
    case VNC_BRIDGE_CLOSE_CODES.connection:
      return 'connection'
    case VNC_BRIDGE_CLOSE_CODES.protocol:
      return 'protocol'
    case VNC_BRIDGE_CLOSE_CODES.timeout:
      return 'timeout'
    default:
      return 'unknown'
  }
}

/**
 * Tears down a session's resources exactly once: detaches noVNC (removing
 * its canvas and DOM listeners) and releases the main-process bridge. Local
 * teardowns call rfb.disconnect(); after a server-side disconnect the RFB
 * client has already torn itself down, so only the bridge needs releasing.
 */
function teardown(session: LiveSession, opts: { disconnectRfb: boolean }): void {
  if (session.disposed) return
  session.disposed = true
  session.disposePaste?.()
  session.disposePaste = null
  if (opts.disconnectRfb && session.rfb !== null) {
    try {
      session.rfb.disconnect()
    } catch {
      // RFB already tore itself down (e.g. after a handshake failure).
    }
  }
  if (session.bridgeId !== null) {
    const bridgeId = session.bridgeId
    session.bridgeId = null
    void window.anyremote.vnc.stopBridge(bridgeId).catch(() => {
      // Bridge already gone (e.g. app quitting); nothing to do.
    })
  }
}

/** Cursor display modes: remote = noVNC-managed cursor overlay; local = always show the OS cursor. */
export type VncCursorMode = 'remote' | 'local'

/** Pixel-encoding preference applied via the bridge's SetEncodings rewrite. */
export type VncEncMode = 'auto' | 'zrle' | 'hextile' | 'raw'

/** encMode -> RFB encoding numbers passed to the bridge (undefined = passthrough). */
const ENC_MODE_TO_ENCODINGS: Record<VncEncMode, number[] | undefined> = {
  auto: undefined,
  zrle: [16],
  hextile: [5],
  raw: [0]
}

interface VncState {
  status: VncStatus
  /** Classified failure while status === 'error'; null otherwise. */
  errorKind: VncErrorKind | null
  /** Server-reported desktop name (RFB ServerInit); empty until known. */
  desktopName: string
  scaleMode: VncScaleMode
  cursorMode: VncCursorMode
  /** Watch-only: noVNC drops all keyboard/mouse/clipboard-out input. */
  viewOnly: boolean
  encMode: VncEncMode
  /** Tight JPEG quality 0-9 (effective only with Tight/JPEG-capable servers). */
  quality: number
  /** Zlib compression level 0-9 (higher = less bandwidth, more CPU). */
  compression: number
  /** Pixel depth in bits: 24 = true color, 16 = RGB565 (about half the bandwidth). */
  colorDepth: 16 | 24
  setScaleMode: (mode: VncScaleMode) => void
  setCursorMode: (mode: VncCursorMode) => void
  setViewOnly: (viewOnly: boolean) => void
  setEncMode: (mode: VncEncMode) => void
  setQuality: (level: number) => void
  setCompression: (level: number) => void
  setColorDepth: (depth: 16 | 24) => void
}

export type VncStore = ReturnType<typeof createVncStore>

/**
 * One status/settings store per session. The setters close over the session
 * id so a live toggle (view-only, quality, ...) only touches that session's
 * RFB instance.
 */
function createVncStore(sessionId: string) {
  const live = (): LiveSession | undefined => liveBySession.get(sessionId)
  return create<VncState>((set) => ({
    status: 'idle',
    errorKind: null,
    desktopName: '',
    scaleMode: 'fit',
    // Default to the local OS cursor: macOS Screen Sharing never sends RFB
    // cursor shapes (noVNC #1430), so a noVNC-managed cursor is either
    // invisible or falls back to the dot — the local cursor is always correct.
    cursorMode: 'local',
    viewOnly: false,
    encMode: 'auto',
    quality: 6,
    compression: 2,
    colorDepth: 24,
    setScaleMode: (mode) => {
      set({ scaleMode: mode })
      // Apply live when a session is up; attachVnc reads the store at creation.
      const rfb = live()?.rfb
      if (rfb) rfb.scaleViewport = mode === 'fit'
    },
    setCursorMode: (mode) => set({ cursorMode: mode }),
    setViewOnly: (viewOnly) => {
      set({ viewOnly })
      // Live toggle: noVNC's setter starts/stops input forwarding immediately.
      const rfb = live()?.rfb
      if (rfb) rfb.viewOnly = viewOnly
    },
    setEncMode: (mode) => {
      set({ encMode: mode })
      // The encoding preference is negotiated at (re)connect time via the
      // bridge's SetEncodings rewrite, so re-attach to apply it.
      if (live() !== undefined) void retryVnc(sessionId)
    },
    setQuality: (level) => {
      set({ quality: level })
      // noVNC re-sends SetEncodings on change when connected (live effect).
      const rfb = live()?.rfb
      if (rfb) rfb.qualityLevel = level
    },
    setCompression: (level) => {
      set({ compression: level })
      const rfb = live()?.rfb
      if (rfb) rfb.compressionLevel = level
    },
    setColorDepth: (depth) => {
      set({ colorDepth: depth })
      // Pixel format is negotiated once during init, so re-attach to apply it.
      if (live() !== undefined) void retryVnc(sessionId)
    }
  }))
}

const storeRegistry = new Map<string, VncStore>()

export function getVncStore(sessionId: string): VncStore {
  let store = storeRegistry.get(sessionId)
  if (store === undefined) {
    store = createVncStore(sessionId)
    storeRegistry.set(sessionId, store)
  }
  return store
}

export function dropVncStore(sessionId: string): void {
  storeRegistry.delete(sessionId)
}

/** Hook shorthand: subscribes the component to this session's VNC store. */
export function useVncStore<T>(sessionId: string, selector: (s: VncState) => T): T {
  return useStore(getVncStore(sessionId), selector)
}

/**
 * Starts a VNC session into `container`: bridge -> WebSocket -> noVNC RFB.
 * Any previous live connection of this session is torn down first, so a
 * remounted panel never stacks connections; other sessions are untouched.
 * Resolves once the attempt is underway; outcomes (connect/error) arrive
 * through the session's store status.
 */
export async function attachVnc(
  sessionId: string,
  container: HTMLElement,
  params: VncStartBridgeParams
): Promise<void> {
  const store = getVncStore(sessionId)
  const previous = liveBySession.get(sessionId)
  if (previous !== undefined) {
    teardown(previous, { disconnectRfb: true })
    liveBySession.delete(sessionId)
  }
  const session: LiveSession = {
    rfb: null,
    bridgeId: null,
    disposed: false,
    closeCode: null,
    disposePaste: null
  }
  liveBySession.set(sessionId, session)
  lastAttachBySession.set(sessionId, { container, params })
  store.setState({ status: 'connecting', errorKind: null, desktopName: '' })

  const ownsSlot = (): boolean => liveBySession.get(sessionId) === session && !session.disposed

  let handle: VncBridgeHandle
  try {
    const encodings = ENC_MODE_TO_ENCODINGS[store.getState().encMode]
    handle = await window.anyremote.vnc.startBridge({ ...params, encodings })
  } catch (err) {
    if (ownsSlot()) {
      store.setState({ status: 'error', errorKind: classifyIpcError(err) })
      teardown(session, { disconnectRfb: false })
    }
    return
  }
  if (!ownsSlot()) {
    // Unmounted while the bridge was starting: release it immediately.
    void window.anyremote.vnc.stopBridge(handle.bridgeId).catch(() => {})
    return
  }
  session.bridgeId = handle.bridgeId

  // Construct the WebSocket ourselves so the bridge's private close codes
  // (4001 auth / 4002 unreachable / 4003 protocol / 4008 timeout) get
  // recorded; noVNC's events do not carry them. RFB accepts a ready-made
  // channel in place of a URL and adopts it.
  const ws = new WebSocket(`ws://127.0.0.1:${handle.wsPort}`, 'binary')
  ws.addEventListener('close', (event) => {
    session.closeCode = event.code
  })

  const rfb = new RFB(container, ws)
  session.rfb = rfb
  const { scaleMode, quality, compression, colorDepth, viewOnly } = store.getState()
  rfb.scaleViewport = scaleMode === 'fit'
  rfb.viewOnly = viewOnly
  // Picked up by the initial SetEncodings (noVNC reads them in _sendEncodings).
  rfb.qualityLevel = quality
  rfb.compressionLevel = compression
  // Requested pixel depth: no public API, but _fbDepth is read when noVNC
  // sends SetPixelFormat during init (noVNC 1.x), which happens after the
  // socket opens — always later than this synchronous assignment.
  ;(rfb as unknown as { _fbDepth: number })._fbDepth = colorDepth

  rfb.addEventListener('connect', () => {
    if (ownsSlot()) store.setState({ status: 'connected' })
  })
  rfb.addEventListener('desktopname', (event) => {
    if (ownsSlot()) {
      store.setState({
        desktopName: (event as CustomEvent<{ name: string }>).detail.name
      })
    }
  })
  // Clipboard sync, both directions over plain RFB CutText (the bridge
  // forwards those messages untouched): a remote copy lands on the local
  // clipboard; a local paste inside the desktop goes to the remote side.
  rfb.addEventListener('clipboard', (event) => {
    if (!ownsSlot()) return
    const { text } = (event as CustomEvent<{ text: string }>).detail
    void navigator.clipboard?.writeText(text).catch(() => {
      // Clipboard permission denied or insecure context: skip this sync.
    })
  })
  // Local paste: browsers only fire 'paste' on editable elements, which the
  // noVNC canvas is not — and noVNC preventDefault()s every canvas keydown
  // (keyboard.js stopEvent), which would cancel the browser's paste command
  // anyway. The workaround: capture the paste shortcut before the canvas
  // sees it, stop it from propagating, and move focus to a hidden textarea
  // so the browser's paste command lands there; the sink's paste event then
  // carries the text and focus returns to the canvas.
  //
  // Multi-session: every live session registers its own document-level
  // capture listener, and routing to the right session needs no explicit
  // active-session check — the guard below (this session's container must
  // hold the focus) can only match for the visible session, because hidden
  // session panes are display:none and cannot contain the focused element.
  const pasteSink = document.createElement('textarea')
  pasteSink.style.cssText =
    'position:fixed;top:-200px;left:0;width:1px;height:1px;opacity:0;pointer-events:none'
  pasteSink.setAttribute('aria-hidden', 'true')
  container.appendChild(pasteSink)
  const onPasteKey = (event: KeyboardEvent): void => {
    if (!ownsSlot()) return
    if (event.key.toLowerCase() !== 'v' || !(event.ctrlKey || event.metaKey) || event.repeat) {
      return
    }
    // Only hijack the shortcut while this session's desktop itself has focus;
    // pasting into real inputs (toolbar select, credentials modal) or into
    // another session's desktop stays untouched.
    if (!container.contains(document.activeElement)) return
    event.stopPropagation()
    pasteSink.value = ''
    pasteSink.focus()
    // If no paste follows (empty clipboard, denied command) the keyboard
    // focus must not be left stranded in the sink.
    setTimeout(() => {
      if (ownsSlot() && document.activeElement === pasteSink) rfb.focus()
    }, 200)
  }
  const onSinkPaste = (event: ClipboardEvent): void => {
    if (!ownsSlot()) return
    const text = event.clipboardData?.getData('text/plain')
    if (text) rfb.clipboardPasteFrom(text)
    pasteSink.value = ''
    rfb.focus()
  }
  document.addEventListener('keydown', onPasteKey, true)
  pasteSink.addEventListener('paste', onSinkPaste)
  session.disposePaste = () => {
    document.removeEventListener('keydown', onPasteKey)
    pasteSink.removeEventListener('paste', onSinkPaste)
    pasteSink.remove()
  }
  rfb.addEventListener('disconnect', (event) => {
    if (!ownsSlot()) return
    // Only non-intentional disconnects reach here: every local teardown goes
    // through teardown() first, which flips `disposed` and detaches us.
    const { clean } = (event as CustomEvent<{ clean: boolean }>).detail
    const kind = clean ? 'unknown' : classifyCloseCode(session.closeCode)
    teardown(session, { disconnectRfb: false })
    store.setState({ status: 'error', errorKind: kind })
  })
}

/** Unmount cleanup: tears down this session's connection and retry context. */
export function detachVnc(sessionId: string): void {
  const session = liveBySession.get(sessionId)
  liveBySession.delete(sessionId)
  lastAttachBySession.delete(sessionId)
  if (session !== undefined) teardown(session, { disconnectRfb: true })
  getVncStore(sessionId).setState({ status: 'idle', errorKind: null, desktopName: '' })
}

/** 断开 button: drops the session's connection on purpose, back to idle. */
export function userDisconnectVnc(sessionId: string): void {
  const session = liveBySession.get(sessionId)
  if (session !== undefined) teardown(session, { disconnectRfb: true })
  getVncStore(sessionId).setState({ status: 'idle', errorKind: null })
}

/** Reconnects with the last attach arguments (the 重试/重新连接 buttons). */
export async function retryVnc(sessionId: string): Promise<void> {
  const lastAttach = lastAttachBySession.get(sessionId)
  if (lastAttach === undefined) return
  await attachVnc(sessionId, lastAttach.container, lastAttach.params)
}

/** One physical key in a combo: X11 keysym + DOM KeyboardEvent.code. */
export interface VncKeyStroke {
  keysym: number
  code: string
}

/**
 * Special-key combos for the desktop toolbar dropdown (desktop.sendKeys).
 * X11 keysyms from noVNC's KeyTable; DOM codes so the QEMU extended-key-event
 * path (scancodes) also works when the server supports it. The labels stay
 * neutral: Ctrl+Alt+Del is meaningless to macOS Screen Sharing but harmless.
 */
export const VNC_KEY_COMBOS = {
  ctrlAltDel: [
    { keysym: 0xffe3, code: 'ControlLeft' }, // XK_Control_L
    { keysym: 0xffe9, code: 'AltLeft' }, // XK_Alt_L
    { keysym: 0xffff, code: 'Delete' } // XK_Delete
  ],
  altF4: [
    { keysym: 0xffe9, code: 'AltLeft' }, // XK_Alt_L
    { keysym: 0xffc4, code: 'F4' } // XK_F4
  ],
  super: [{ keysym: 0xffeb, code: 'MetaLeft' }] // XK_Super_L
} as const satisfies Record<string, readonly VncKeyStroke[]>

export type VncKeyComboName = keyof typeof VNC_KEY_COMBOS

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * Sends a key combo through the given session's live connection: focuses the
 * noVNC canvas, then presses every key down in order and releases them in
 * reverse, ~40ms apart so slow servers do not coalesce the events. No-ops
 * unless the session is connected; noVNC itself drops the events in
 * view-only mode.
 */
export async function sendKeyCombo(
  sessionId: string,
  keys: readonly VncKeyStroke[]
): Promise<void> {
  const rfb = liveBySession.get(sessionId)?.rfb
  if (rfb === undefined || rfb === null || getVncStore(sessionId).getState().status !== 'connected') {
    return
  }
  rfb.focus()
  for (const key of keys) {
    rfb.sendKey(key.keysym, key.code, true)
    await sleep(40)
  }
  for (let i = keys.length - 1; i >= 0; i--) {
    rfb.sendKey(keys[i].keysym, keys[i].code, false)
    await sleep(40)
  }
}
