/**
 * Static development mock for window.anyremote, active only under plain
 * `vite dev` when the Wails runtime is absent (import.meta.env.DEV &&
 * window.go === undefined, see ./index.ts). It exists so the UI can render
 * for frontend-only preview and Playwright screenshots without any Go
 * backend. Every method behaves predictably and nothing here touches the
 * network, the filesystem, or a real SSH/VNC server.
 *
 * The fixture mirrors the developer's LAN machine 192.168.50.43: SSH and VNC
 * detected, everything else closed.
 */

import {
  sftpProgressChannel,
  sshCloseChannel,
  sshDataChannel,
  type SftpProgressEvent
} from '../../shared/ipc'
import type { TargetScanReport } from '../../shared/scan'
import type { FileEntry } from '../../shared/ssh'
import type { AnyRemoteApi } from './index'

type Listener = (...args: never[]) => void

/** Minimal per-channel event bus standing in for window.runtime.EventsOn. */
const listeners = new Map<string, Set<Listener>>()

function on(channel: string, cb: Listener): () => void {
  let set = listeners.get(channel)
  if (set === undefined) {
    set = new Set()
    listeners.set(channel, set)
  }
  set.add(cb)
  return () => {
    set.delete(cb)
    if (set.size === 0) listeners.delete(channel)
  }
}

function emit(channel: string, ...args: unknown[]): void {
  for (const cb of listeners.get(channel) ?? []) {
    ;(cb as (...a: unknown[]) => void)(...args)
  }
}

/** Simulates a small backend delay so loading states stay visible. */
function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

const MOCK_SCAN_RESULTS: TargetScanReport['results'] = [
  { protocolId: 'ssh', port: 22, status: 'open', detected: true, banner: 'SSH-2.0-OpenSSH_9.9', latencyMs: 3 },
  { protocolId: 'vnc', port: 5900, status: 'open', detected: true, banner: 'RFB 003.889', latencyMs: 2 },
  { protocolId: 'rdp', port: 3389, status: 'closed', detected: false, latencyMs: 1 },
  { protocolId: 'telnet', port: 23, status: 'closed', detected: false, latencyMs: 1 },
  { protocolId: 'ftp', port: 21, status: 'closed', detected: false, latencyMs: 1 },
  { protocolId: 'smb', port: 445, status: 'closed', detected: false, latencyMs: 1 },
  { protocolId: 'http', port: 80, status: 'closed', detected: false, latencyMs: 2 },
  { protocolId: 'https', port: 443, status: 'closed', detected: false, latencyMs: 2 }
]

const MOCK_HOME = '/Users/mock'

const MOCK_FILES: FileEntry[] = [
  { name: 'Desktop', type: 'directory', size: 0, mtimeMs: 1750000000000, mode: 0o755 },
  { name: 'Documents', type: 'directory', size: 0, mtimeMs: 1750000000000, mode: 0o755 },
  { name: 'notes.txt', type: 'file', size: 1286, mtimeMs: 1750000000000, mode: 0o644 },
  { name: 'photo.png', type: 'file', size: 248_930, mtimeMs: 1750000000000, mode: 0o644 }
]

/**
 * Unique mock session ids (multi-session model, F6): several sessions can be
 * live at once, and the per-session event channels (ssh:data:<id>, ...)
 * would collide if every connection returned the same id.
 */
let mockSessionSeq = 0

/** sessionId -> target host, so the mock shell greeting can tag its session. */
const mockHosts = new Map<string, string>()

/**
 * Demo/capture hook: `?vncbridge=<wsPort>` points desktop panels at a real
 * bridge (e.g. scripts/mockvnc) under vite dev. For multi-session VNC tests
 * the value also accepts per-host mappings `?vncbridge=<host>=<wsPort>,...`
 * (one bridge serves only one client, so each session needs its own mockvnc
 * instance — run them with different -addr ports). Mapping by host (instead
 * of call order) keeps StrictMode's double-attach harmless: both attempts of
 * one session resolve to the same port, and only the surviving one opens a
 * WebSocket.
 */
let mockBridgeSeq = 0

function nextVncBridge(host: string): { bridgeId: string; wsPort: number } {
  const raw = new URLSearchParams(location.search).get('vncbridge') ?? ''
  let defaultPort: number | undefined
  const byHost = new Map<string, number>()
  for (const entry of raw.split(',')) {
    const [h, p] = entry.split('=')
    const port = Number((p ?? h).trim())
    if (!Number.isInteger(port) || port <= 0) continue
    if (p === undefined) defaultPort = port
    else byHost.set(h.trim(), port)
  }
  const wsPort = byHost.get(host) ?? defaultPort
  if (wsPort === undefined) {
    throw new Error('[UNREACHABLE] mock: no VNC backend under vite dev')
  }
  mockBridgeSeq += 1
  return { bridgeId: `dev-bridge-${mockBridgeSeq}`, wsPort }
}

/**
 * Builds the mock facade. The saved-connections part is injected so mock and
 * Wails modes share the same in-memory implementation (list starts empty).
 */
export function createMockApi(connections: AnyRemoteApi['connections']): AnyRemoteApi {
  return {
    versions: { electron: '', node: '', chrome: '' },
    scan: async (host) => {
      await delay(400)
      const startedAt = Date.now() - 400
      return { host, startedAt, durationMs: 400, results: MOCK_SCAN_RESULTS }
    },
    ssh: {
      connect: async (config) => {
        await delay(200)
        mockSessionSeq += 1
        const id = `mock-session-${mockSessionSeq}`
        mockHosts.set(id, config.host)
        return id
      },
      openShell: async (sessionId) => {
        // A greeting chunk so the terminal panel shows life; no echo after.
        // The host tag tells parallel mock sessions apart in tests.
        const host = mockHosts.get(sessionId) ?? 'unknown'
        setTimeout(
          () => emit(sshDataChannel(sessionId), `mock shell [${host}] — no backend connected\r\n$ `),
          50
        )
      },
      write: () => undefined,
      resize: () => undefined,
      close: async (sessionId) => {
        mockHosts.delete(sessionId)
      },
      onData: (sessionId, cb) => on(sshDataChannel(sessionId), cb),
      onClose: (sessionId, cb) => on(sshCloseChannel(sessionId), cb)
    },
    sftp: {
      homeDir: async () => MOCK_HOME,
      list: async () => MOCK_FILES,
      mkdir: async () => undefined,
      rename: async () => undefined,
      deleteFile: async () => undefined,
      deleteDir: async () => undefined,
      upload: async (sessionId, _localPath, _remotePath) => {
        emitProgress(sessionId, 'upload')
      },
      download: async (sessionId, _remotePath, _localPath) => {
        emitProgress(sessionId, 'download')
      },
      onProgress: (sessionId, cb) => on(sftpProgressChannel(sessionId), cb)
    },
    vnc: {
      startBridge: async (params) => {
        await delay(100)
        return nextVncBridge(params.host)
      },
      stopBridge: async () => undefined
    },
    localFs: {
      homeDir: async () => MOCK_HOME,
      list: async () => MOCK_FILES
    },
    connections,
    settings: {
      // No secret backend under vite dev: report the keychain default and
      // accept switches as no-ops.
      getSecretStorage: async () => 'keychain',
      setSecretStorage: async () => undefined
    },
    dialog: {
      pickFiles: async () => [],
      pickSavePath: async () => null
    }
  }
}

/** One synthetic 100% progress event so the transfer queue completes. */
function emitProgress(sessionId: string, direction: SftpProgressEvent['direction']): void {
  const progress: SftpProgressEvent = { transferred: 1024, total: 1024, percent: 100, direction }
  setTimeout(() => emit(sftpProgressChannel(sessionId), progress), 50)
}
