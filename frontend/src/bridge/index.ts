/**
 * window.anyremote: the renderer's IPC facade, Wails edition. The AnyRemoteApi
 * surface is verbatim-compatible with the retired Electron preload
 * (src/preload/index.ts, removed in M4), so every renderer component and
 * store keeps working unchanged.
 *
 * Implementation mapping:
 *   - request/response  -> window.go.main.App.* (bound Go methods, bindings.go)
 *   - streaming events  -> window.runtime.EventsOn on the same per-session
 *     channel names the Electron main process used (ssh:data:<id>,
 *     ssh:close:<id>, sftp:progress:<id>); the returned cancel function is the
 *     unsubscribe handle.
 *   - errors            -> Go failures already carry the "[CODE] message"
 *     prefix (bindError in bindings.go) and Wails rejects with a real Error,
 *     so the renderer's ipcErrorCode()/ipcErrorMessage() work untouched.
 *
 * Saved connections are persisted by the Go store (internal/store): bookmark
 * metadata in connections.json under the per-user config dir, secrets in the
 * OS keychain. On save, an omitted secret preserves the stored one and an
 * explicitly empty secret clears it (unlike the retired Electron store, which
 * dropped the secret whenever it was omitted).
 *
 * Under plain `vite dev` (no Wails runtime) the facade falls back to a static
 * mock (./mock) so the UI stays previewable; see the useMock check below.
 */

import {
  ipcErrorCode,
  sftpProgressChannel,
  sshCloseChannel,
  sshDataChannel,
  type SavedConnection,
  type SavedConnectionInput,
  type SavedConnectionSummary,
  type SftpProgressEvent,
  type VncBridgeHandle,
  type VncStartBridgeParams
} from '../../shared/ipc'
import type { TargetScanReport } from '../../shared/scan'
import type { FileEntry, ShellSize, SshAuthConfig } from '../../shared/ssh'
import { createMockApi } from './mock'

/** API surface exposed to the renderer as `window.anyremote`. */
export interface AnyRemoteApi {
  versions: {
    electron: string
    node: string
    chrome: string
  }
  scan(host: string): Promise<TargetScanReport>
  ssh: {
    /** Resolves with the session id; rejects with a [CODE]-prefixed Error. */
    connect(config: SshAuthConfig): Promise<string>
    openShell(sessionId: string, size: ShellSize): Promise<void>
    /** Fire-and-forget keystroke input. */
    write(sessionId: string, data: string): void
    resize(sessionId: string, cols: number, rows: number): void
    close(sessionId: string): Promise<void>
    /** Subscribes to shell output; returns the unsubscribe function. */
    onData(sessionId: string, cb: (data: string) => void): () => void
    /** Subscribes to the shell-closed event; returns the unsubscribe function. */
    onClose(sessionId: string, cb: () => void): () => void
  }
  sftp: {
    homeDir(sessionId: string): Promise<string>
    list(sessionId: string, path: string): Promise<FileEntry[]>
    mkdir(sessionId: string, path: string): Promise<void>
    rename(sessionId: string, oldPath: string, newPath: string): Promise<void>
    deleteFile(sessionId: string, path: string): Promise<void>
    deleteDir(sessionId: string, path: string): Promise<void>
    /** Progress is delivered via onProgress, not the promise. */
    upload(sessionId: string, localPath: string, remotePath: string): Promise<void>
    download(sessionId: string, remotePath: string, localPath: string): Promise<void>
    onProgress(sessionId: string, cb: (progress: SftpProgressEvent) => void): () => void
  }
  vnc: {
    startBridge(params: VncStartBridgeParams): Promise<VncBridgeHandle>
    stopBridge(bridgeId: string): Promise<void>
  }
  localFs: {
    homeDir(): Promise<string>
    list(path: string): Promise<FileEntry[]>
  }
  connections: {
    /** Summaries without secrets, for the saved-connection lists. */
    list(): Promise<SavedConnectionSummary[]>
    /** One connection with its secret; null when the id is unknown. */
    get(id: string): Promise<SavedConnection | null>
    /**
     * Creates/updates a connection, persisted by the Go store (see the header
     * note): an omitted secret preserves the saved one, an explicitly empty
     * secret (empty data) clears it.
     */
    save(input: SavedConnectionInput): Promise<SavedConnectionSummary>
    delete(id: string): Promise<void>
  }
  settings: {
    /** Active secret backend: "keychain" (OS keychain) or "localFile" (encrypted file). */
    getSecretStorage(): Promise<string>
    /**
     * Switches the secret backend and migrates every stored credential;
     * rejects with [REMOTE_ERROR] for an unknown mode or
     * [ENCRYPTION_UNAVAILABLE] when the migration fails.
     */
    setSecretStorage(mode: string): Promise<void>
  }
  dialog: {
    /** Multi-select file picker; resolves with [] when canceled. */
    pickFiles(): Promise<string[]>
    /** Save-as picker; resolves with null when canceled. */
    pickSavePath(defaultName: string): Promise<string | null>
  }
}

/**
 * In-memory saved-connections stand-in feeding ONLY the dev mock (mock.ts);
 * the Wails path calls the Go store (internal/store) instead. Save semantics
 * mirror the Go store: an omitted secret preserves the previous one, an
 * explicitly empty secret (empty data) clears it, and delete of an unknown id
 * is a no-op.
 */
function createMockConnections(): AnyRemoteApi['connections'] {
  const entries = new Map<string, SavedConnection>()
  const summaryOf = (conn: SavedConnection): SavedConnectionSummary => {
    const { secret: _secret, ...summary } = conn
    return summary
  }
  return {
    list: async () => [...entries.values()].map(summaryOf),
    get: async (id) => entries.get(id) ?? null,
    save: async (input) => {
      const previous = input.id !== undefined ? entries.get(input.id) : undefined
      let secret = input.secret ?? previous?.secret
      if (secret !== undefined && secret.data === '') secret = undefined
      const conn: SavedConnection = {
        id: input.id ?? crypto.randomUUID(),
        name: input.name,
        host: input.host,
        protocols: [...input.protocols],
        username: input.username,
        ...(secret !== undefined ? { secret: { ...secret } } : {})
      }
      entries.set(conn.id, conn)
      return summaryOf(conn)
    },
    delete: async (id) => {
      entries.delete(id)
    }
  }
}

/** window.go.main.App, asserted present (checked before choosing this impl). */
function app(): NonNullable<NonNullable<NonNullable<Window['go']>['main']>['App']> {
  const bound = window.go?.main?.App
  if (bound === undefined) {
    throw new Error('[REMOTE_ERROR] Wails backend is not available')
  }
  return bound
}

/** Subscribes to a Go-emitted event channel; returns the unsubscribe function. */
function subscribe<T extends unknown[]>(channel: string, cb: (...args: T) => void): () => void {
  const wailsRuntime = window.runtime
  if (wailsRuntime === undefined) {
    throw new Error('[REMOTE_ERROR] Wails runtime is not available')
  }
  return wailsRuntime.EventsOn(channel, cb)
}

/** The Wails-backed implementation, delegating to the bound Go methods. */
function createWailsApi(): AnyRemoteApi {
  return {
    // No Electron/Node under Wails; nothing in the renderer reads these.
    versions: { electron: '', node: '', chrome: '' },
    scan: (host) => app().Scan(host),
    ssh: {
      connect: (config) => app().SshConnect(config),
      openShell: (sessionId, size) => app().SshOpenShell(sessionId, size.cols, size.rows),
      write: (sessionId, data) => {
        // Fire-and-forget like the Electron ipcRenderer.send: keystrokes must
        // never block or reject the renderer; a dead shell swallows them.
        void app()
          .SshWrite(sessionId, data)
          .catch(() => undefined)
      },
      resize: (sessionId, cols, rows) => {
        void app()
          .SshResize(sessionId, cols, rows)
          .catch(() => undefined)
      },
      close: (sessionId) => app().SshClose(sessionId),
      onData: (sessionId, cb) => subscribe(sshDataChannel(sessionId), cb),
      onClose: (sessionId, cb) => subscribe(sshCloseChannel(sessionId), cb)
    },
    sftp: {
      homeDir: (sessionId) => app().SftpHomeDir(sessionId),
      list: (sessionId, path) => app().SftpList(sessionId, path),
      mkdir: (sessionId, path) => app().SftpMkdir(sessionId, path),
      rename: (sessionId, oldPath, newPath) => app().SftpRename(sessionId, oldPath, newPath),
      deleteFile: (sessionId, path) => app().SftpDeleteFile(sessionId, path),
      deleteDir: (sessionId, path) => app().SftpDeleteDir(sessionId, path),
      upload: (sessionId, localPath, remotePath) =>
        app().SftpUpload(sessionId, localPath, remotePath),
      download: (sessionId, remotePath, localPath) =>
        app().SftpDownload(sessionId, remotePath, localPath),
      onProgress: (sessionId, cb) => subscribe(sftpProgressChannel(sessionId), cb)
    },
    vnc: {
      startBridge: (params) =>
        app().VncStartBridge(
          params.host,
          params.port,
          params.username ?? '',
          params.password ?? '',
          params.encodings ?? null
        ),
      stopBridge: (bridgeId) => app().VncStopBridge(bridgeId)
    },
    localFs: {
      homeDir: () => app().LocalFsHomeDir(),
      list: (path) => app().LocalFsList(path)
    },
    connections: {
      list: () => app().ConnectionsList(),
      get: async (id) => {
        // The Go store rejects an unknown id with NOT_FOUND; the renderer
        // contract (and the retired Electron store) expects null instead.
        try {
          return await app().ConnectionsGet(id)
        } catch (err) {
          if (ipcErrorCode(err) === 'NOT_FOUND') return null
          throw err
        }
      },
      save: (input) => app().ConnectionsSave(input),
      delete: (id) => app().ConnectionsDelete(id)
    },
    settings: {
      getSecretStorage: () => app().GetSecretStorage(),
      setSecretStorage: (mode) => app().SetSecretStorage(mode)
    },
    dialog: {
      pickFiles: () => app().DialogPickFiles(),
      pickSavePath: async (defaultName) => {
        const path = await app().DialogPickSavePath(defaultName)
        return path === '' ? null : path
      }
    }
  }
}

// Plain `vite dev` has no Wails runtime: window.go is only injected inside
// the Wails webview. The mock keeps the UI usable for frontend-only preview
// and Playwright screenshots; production builds always take the Wails path
// (import.meta.env.DEV is false there).
const useMock = import.meta.env.DEV && window.go === undefined

const api: AnyRemoteApi = useMock ? createMockApi(createMockConnections()) : createWailsApi()

if (useMock) {
  console.warn(
    '[bridge] Wails runtime not found — window.anyremote is backed by the dev mock (vite dev preview only).'
  )
}

window.anyremote = api
