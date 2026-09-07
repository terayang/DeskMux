/**
 * IPC contract between the main process and the renderer (via the preload
 * bridge in src/preload/index.ts). Single source of truth for channel names,
 * payload types, and the error serialization convention.
 *
 * Three transport styles:
 * - request/response: ipcMain.handle <-> ipcRenderer.invoke
 * - fire-and-forget input (ssh write / resize): ipcRenderer.send -> ipcMain.on
 * - streaming events from main: webContents.send on per-session channels
 *   (ssh:data:<id>, ssh:close:<id>, sftp:progress:<id>)
 *
 * Error convention: a thrown Error cannot cross IPC intact (Electron re-wraps
 * it into a generic invoke error, dropping custom fields), so handlers
 * serialize every failure to an IpcError { code?, message } and throw it as a
 * marker-prefixed JSON Error (toTransportError); the preload invoke wrapper
 * rebuilds an IpcInvokeError carrying the same code and message
 * (rebuildIpcError). Electron-free so both sides can import this module.
 */

import type { TransferProgress } from './ssh'

/** Invoke/send channel names for request/response and fire-and-forget calls. */
export const IPC_CHANNELS = {
  scan: 'scan:scan',
  sshConnect: 'ssh:connect',
  sshOpenShell: 'ssh:openShell',
  sshWrite: 'ssh:write',
  sshResize: 'ssh:resize',
  sshClose: 'ssh:close',
  sftpHomeDir: 'sftp:homeDir',
  sftpList: 'sftp:list',
  sftpMkdir: 'sftp:mkdir',
  sftpRename: 'sftp:rename',
  sftpDeleteFile: 'sftp:deleteFile',
  sftpDeleteDir: 'sftp:deleteDir',
  sftpUpload: 'sftp:upload',
  sftpDownload: 'sftp:download',
  vncStartBridge: 'vnc:startBridge',
  vncStopBridge: 'vnc:stopBridge',
  localFsHomeDir: 'localFs:homeDir',
  localFsList: 'localFs:list',
  dialogPickFiles: 'dialog:pickFiles',
  dialogPickSavePath: 'dialog:pickSavePath',
  connectionsList: 'connections:list',
  connectionsGet: 'connections:get',
  connectionsSave: 'connections:save',
  connectionsDelete: 'connections:delete'
} as const

/** Streaming event channel carrying one SSH shell's output (string chunks). */
export const sshDataChannel = (sessionId: string): string => `ssh:data:${sessionId}`

/** Streaming event channel fired once when one SSH shell closes. */
export const sshCloseChannel = (sessionId: string): string => `ssh:close:${sessionId}`

/** Streaming event channel carrying SFTP transfer progress of one session. */
export const sftpProgressChannel = (sessionId: string): string => `sftp:progress:${sessionId}`

/** Parameters of vnc.startBridge (RFB target + optional credentials). */
export interface VncStartBridgeParams {
  host: string
  port: number
  username?: string
  password?: string
  /**
   * Pixel-encoding preference (RFB encoding numbers, e.g. [16] = ZRLE). When
   * set, the bridge rewrites the client's SetEncodings messages to prefer
   * these encodings; undefined leaves them untouched (full passthrough).
   */
  encodings?: number[]
}

/** Result of vnc.startBridge: the id used to stop it and the loopback WS port. */
export interface VncBridgeHandle {
  bridgeId: string
  wsPort: number
}

/** SFTP upload/download progress event payload (TransferProgress + direction). */
export interface SftpProgressEvent extends TransferProgress {
  direction: 'upload' | 'download'
}

/** What a saved connection's secret protects (F5, src/main/store.ts). */
export type SavedSecretKind = 'password' | 'privateKeyPath'

/**
 * A saved connection's secret as it crosses IPC: connections.save accepts and
 * connections.get returns the DECRYPTED plaintext in `data`. On disk it is
 * always the base64 of safeStorage.encryptString(plaintext) — the plaintext
 * form never leaves the two IPC endpoints.
 */
export interface SavedConnectionSecret {
  kind: SavedSecretKind
  data: string
}

/** One saved connection. `secret` is present only in connections.get results. */
export interface SavedConnection {
  id: string
  name: string
  host: string
  /** Protocol ids (e.g. 'ssh', 'vnc') chosen when the connection was saved. */
  protocols: string[]
  username: string
  secret?: SavedConnectionSecret
}

/** Saved connection without its secret, as returned by connections.list. */
export type SavedConnectionSummary = Omit<SavedConnection, 'secret'>

/**
 * Payload of connections.save: a new connection (no id — the main process
 * assigns a randomUUID) or an update (existing id). `secret.data` is
 * plaintext here; the main process encrypts it before persisting.
 */
export interface SavedConnectionInput {
  id?: string
  name: string
  host: string
  protocols: string[]
  username: string
  secret?: SavedConnectionSecret
}

/** Serializable error shape crossing IPC; keeps the distinguishable cause. */
export interface IpcError {
  /**
   * Machine-readable cause when one is known: an SshErrorCode
   * ('AUTH_FAILED' / 'TIMEOUT' / 'UNREACHABLE' / ...), a Node errno code
   * ('ENOENT' / 'EACCES' / ...) from local filesystem failures, or a mapped
   * RFB handshake failure ('AUTH_FAILED' / 'TIMEOUT' / 'PROTOCOL_ERROR' /
   * 'UNREACHABLE'). Absent for unexpected plain Errors.
   */
  code?: string
  message: string
}

/** Optional mapper giving a code to errors that do not carry one themselves. */
export type IpcErrorCodeMapper = (err: unknown) => string | undefined

/**
 * Main-process side: reduces any thrown value to an IpcError. An existing
 * string `code` property (SshError, Node system errors) always wins; the
 * mapper only fills in errors without one (e.g. the RFB error classes).
 */
export function serializeError(err: unknown, mapCode?: IpcErrorCodeMapper): IpcError {
  const existing = (err as { code?: unknown } | null | undefined)?.code
  const code = typeof existing === 'string' ? existing : mapCode?.(err)
  const message = err instanceof Error ? err.message : String(err)
  return code === undefined ? { message } : { code, message }
}

/**
 * Marker prefixing the JSON IpcError payload inside the thrown Error's
 * message, so the preload side can recognize and rebuild it. Electron
 * prefixes handler errors with "Error invoking remote method '<channel>':",
 * so the marker is searched for, never anchored at position 0.
 */
const IPC_ERROR_MARKER = '[[deskmux-ipc-error]]'

/** Main-process side: wraps a failure into the transportable Error to throw. */
export function toTransportError(err: unknown, mapCode?: IpcErrorCodeMapper): Error {
  return new Error(IPC_ERROR_MARKER + JSON.stringify(serializeError(err, mapCode)))
}

/** Renderer side: an IPC invoke failure rebuilt as an Error with `code`. */
export class IpcInvokeError extends Error {
  readonly code?: string

  constructor(payload: IpcError) {
    super(payload.message)
    this.name = 'IpcInvokeError'
    if (payload.code !== undefined) this.code = payload.code
  }
}

/**
 * Preload side: converts a rejected ipcRenderer.invoke error back into an
 * IpcInvokeError when it carries the marker; anything else passes through
 * unchanged (e.g. a missing handler).
 */
export function rebuildIpcError(err: unknown): unknown {
  if (err instanceof Error) {
    const at = err.message.indexOf(IPC_ERROR_MARKER)
    if (at !== -1) {
      try {
        const payload = JSON.parse(err.message.slice(at + IPC_ERROR_MARKER.length)) as IpcError
        if (payload !== null && typeof payload.message === 'string') {
          return new IpcInvokeError(payload)
        }
      } catch {
        // Malformed payload: fall through and return the original error.
      }
    }
  }
  return err
}

/**
 * Renderer side: recovers the error code embedded in the message by the
 * preload invoke wrapper as `[CODE] message`. contextBridge clones a thrown
 * Error keeping only `message`, so IpcInvokeError.code never reaches the
 * renderer (see src/preload/index.ts).
 */
export function ipcErrorCode(err: unknown): string | undefined {
  const message = err instanceof Error ? err.message : String(err)
  const match = /^\[([A-Z_]+)\] /.exec(message)
  return match?.[1]
}

/** Renderer side: err.message with the embedded `[CODE] ` prefix stripped. */
export function ipcErrorMessage(err: unknown): string {
  const message = err instanceof Error ? err.message : String(err)
  return message.replace(/^\[[A-Z_]+\] /, '')
}
