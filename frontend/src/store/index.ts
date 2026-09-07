import { create } from 'zustand'
import type { ProtocolId } from '../../shared/protocols'
import type { TargetScanReport } from '../../shared/scan'
import { dropFilesStore } from './files'
import type { SessionContext, SessionCredentials } from './session'
import { dropTerminalStore } from './terminal'
import { detachVnc, dropVncStore } from './vnc'

export type TabKind = 'desktop' | 'terminal' | 'files'

/** The two workbench views the ActivityBar switches between. */
export type WorkbenchView = 'sessions' | 'devices'

export interface SessionTab {
  key: TabKind
  /** i18n key for the tab label. */
  titleKey: string
}

/** Session tabs implied by the protocols the user checked: ssh -> terminal +
 * files, vnc -> remote desktop. */
function tabsForProtocols(protocols: readonly ProtocolId[]): SessionTab[] {
  const tabs: SessionTab[] = []
  if (protocols.includes('vnc')) tabs.push({ key: 'desktop', titleKey: 'session.tabs.desktop' })
  if (protocols.includes('ssh')) {
    tabs.push({ key: 'terminal', titleKey: 'session.tabs.terminal' })
    tabs.push({ key: 'files', titleKey: 'session.tabs.files' })
  }
  return tabs
}

/** Protocols the MVP can actually open a session for (others are scan-only). */
const SESSION_PROTOCOLS: readonly ProtocolId[] = ['ssh', 'vnc']

function toSessionProtocols(protocols: readonly string[]): ProtocolId[] {
  return protocols.filter((p): p is ProtocolId =>
    (SESSION_PROTOCOLS as readonly string[]).includes(p)
  )
}

/**
 * One live connection workspace (F6 multi-session model): its context (who /
 * how / as whom), its protocol tabs, and which tab is on top. Sessions
 * coexist; the workspace renders every session's tab subtree and hides the
 * inactive ones with CSS so terminals, file managers and VNC desktops keep
 * their state and connections while switched away.
 */
export interface ActiveSession {
  /** Same id as context.id; keys the per-session panel stores. */
  id: string
  context: SessionContext
  tabs: SessionTab[]
  activeTab: TabKind | null
}

let sessionSeq = 0

/**
 * Releases a closing session's panel resources: the per-session stores are
 * dropped and the live VNC connection (if any) is torn down. The panels' own
 * unmount cleanups close their SSH sessions / detach noVNC; the calls here
 * cover the stores so nothing of the closed session lingers.
 */
function releaseSessionResources(sessionId: string): void {
  dropTerminalStore(sessionId)
  dropFilesStore(sessionId)
  detachVnc(sessionId)
  dropVncStore(sessionId)
}

interface AppState {
  targetAddress: string
  scanning: boolean
  /** Fingerprint report of the last completed scan; null before any scan. */
  scanReport: TargetScanReport | null
  /** Error message of the last failed scan; null when the scan succeeded. */
  scanError: string | null
  selected: ProtocolId[]
  /** All live sessions, in creation order. */
  sessions: ActiveSession[]
  /** The session whose pane is visible; null when no session exists. */
  activeSessionId: string | null
  setTargetAddress: (address: string) => void
  startScan: () => Promise<void>
  toggleProtocol: (id: ProtocolId) => void
  /**
   * Opens a new session for the scanned target + checked protocols. Existing
   * sessions stay alive; the new one becomes active.
   */
  beginSession: (credentials: SessionCredentials) => void
  /**
   * Opens a new session straight from a saved connection (welcome-panel click
   * or workspace-sider click) with its decrypted credentials. The saved
   * entry's id is kept in the session context (SessionContext.savedId).
   */
  beginSavedSession: (
    saved: { host: string; protocols: string[]; id?: string },
    credentials: SessionCredentials
  ) => void
  /** Brings another session's pane to the front. */
  setActiveSession: (id: string) => void
  setActiveTab: (key: TabKind) => void
  /** Closes one protocol tab of the active session; closing its last tab closes the session. */
  closeTab: (key: TabKind) => void
  /** Closes a session entirely, releasing its SSH/VNC/store resources. */
  closeSession: (id: string) => void
  /** Stage-head 断开 button: closes the currently active session. */
  disconnect: () => void
  /** Workbench view shown in the rail/stage (ActivityBar switch). */
  activeView: WorkbenchView
  setActiveView: (view: WorkbenchView) => void
  /** ⌘K command palette visibility. */
  paletteOpen: boolean
  setPaletteOpen: (open: boolean) => void
  /** NewConnectionModal visibility (sider button / ⌘K). */
  newConnectionOpen: boolean
  setNewConnectionOpen: (open: boolean) => void
  /**
   * Opens the new-connection modal. With `scan: true` (the welcome panel's
   * Enter / scan-button path) it also starts scanning the current
   * targetAddress; ⌘K passes no flag so the modal never scans by itself.
   */
  openNewConnection: (options?: { scan?: boolean }) => void
}

export const useAppStore = create<AppState>((set, get) => ({
  targetAddress: '',
  scanning: false,
  scanReport: null,
  scanError: null,
  selected: [],
  sessions: [],
  activeSessionId: null,
  newConnectionOpen: false,
  activeView: 'sessions',
  paletteOpen: false,

  setTargetAddress: (address) => set({ targetAddress: address }),

  startScan: async () => {
    const host = get().targetAddress.trim()
    if (get().scanning || host === '') return
    set({ scanning: true, scanReport: null, scanError: null, selected: [] })
    try {
      const scanReport = await window.deskmux.scan(host)
      set({ scanning: false, scanReport })
    } catch (err) {
      set({
        scanning: false,
        scanError: err instanceof Error ? err.message : String(err)
      })
    }
  },

  toggleProtocol: (id) =>
    set((state) => ({
      selected: state.selected.includes(id)
        ? state.selected.filter((p) => p !== id)
        : [...state.selected, id]
    })),

  beginSession: (credentials) => {
    const { targetAddress, selected, sessions } = get()
    const id = `session-${++sessionSeq}`
    const context: SessionContext = {
      id,
      target: targetAddress.trim(),
      protocols: [...selected],
      credentials
    }
    const tabs = tabsForProtocols(selected)
    set({
      sessions: [...sessions, { id, context, tabs, activeTab: tabs[0]?.key ?? null }],
      activeSessionId: id,
      newConnectionOpen: false
    })
  },

  beginSavedSession: (saved, credentials) => {
    const protocols = toSessionProtocols(saved.protocols)
    const id = `session-${++sessionSeq}`
    const context: SessionContext = {
      id,
      target: saved.host,
      protocols,
      credentials,
      ...(saved.id ? { savedId: saved.id } : {})
    }
    const tabs = tabsForProtocols(protocols)
    set((state) => ({
      sessions: [...state.sessions, { id, context, tabs, activeTab: tabs[0]?.key ?? null }],
      activeSessionId: id,
      targetAddress: saved.host,
      selected: protocols,
      newConnectionOpen: false
    }))
  },

  setActiveSession: (id) => {
    if (get().sessions.some((s) => s.id === id)) set({ activeSessionId: id })
  },

  setActiveTab: (key) =>
    set((state) => ({
      sessions: state.sessions.map((s) =>
        s.id === state.activeSessionId ? { ...s, activeTab: key } : s
      )
    })),

  closeTab: (key) => {
    const { sessions, activeSessionId } = get()
    const session = sessions.find((s) => s.id === activeSessionId)
    if (session === undefined) return
    const tabs = session.tabs.filter((t) => t.key !== key)
    if (tabs.length === 0) {
      // Closing the last tab ends the session (releasing its resources); with
      // no session left the workspace falls back to the welcome panel.
      get().closeSession(session.id)
      return
    }
    const activeTab =
      session.activeTab === key ? tabs[tabs.length - 1].key : session.activeTab
    set({
      sessions: sessions.map((s) => (s.id === session.id ? { ...s, tabs, activeTab } : s))
    })
  },

  closeSession: (id) => {
    const { sessions, activeSessionId } = get()
    const index = sessions.findIndex((s) => s.id === id)
    if (index < 0) return
    releaseSessionResources(id)
    const remaining = sessions.filter((s) => s.id !== id)
    // Activate the neighbour that took the closed session's slot (or the new
    // last one); null when nothing remains.
    const nextActive =
      activeSessionId === id
        ? (remaining[Math.min(index, remaining.length - 1)]?.id ?? null)
        : activeSessionId
    set({
      sessions: remaining,
      activeSessionId: nextActive,
      // Once nothing is live, the target input resets too: on the welcome
      // panel it acts as a live filter over the saved list (B5), and a stale
      // address would hide every other device.
      ...(remaining.length === 0 ? { targetAddress: '', selected: [] } : {})
    })
  },

  disconnect: () => {
    const { activeSessionId } = get()
    if (activeSessionId !== null) get().closeSession(activeSessionId)
  },

  setNewConnectionOpen: (open) => set({ newConnectionOpen: open }),

  setActiveView: (view) => set({ activeView: view }),

  setPaletteOpen: (open) => set({ paletteOpen: open }),

  openNewConnection: (options) => {
    set({ newConnectionOpen: true })
    if (options?.scan) void get().startScan()
  }
}))
