/**
 * Renderer-side view of the saved-connection store (F5): mirrors the
 * safeStorage-backed list owned by the main process (src/main/store.ts) for
 * the workbench rail and the devices view.
 * Secrets never live here — list() returns summaries only; connecting fetches
 * the decrypted secret on demand via window.deskmux.connections.get.
 */

import { create } from 'zustand'
import type {
  SavedConnection,
  SavedConnectionInput,
  SavedConnectionSummary
} from '../../shared/ipc'
import type { SessionCredentials } from './session'

/**
 * Maps a decrypted saved connection (connections.get result) onto session
 * credentials: a password secret feeds password auth; a privateKeyPath secret
 * feeds the credentials' privateKey field, which SessionCredentials documents
 * as a local key-file path (resolved to key content in the main process).
 */
export function savedToCredentials(conn: SavedConnection): SessionCredentials {
  return {
    username: conn.username,
    password: conn.secret?.kind === 'password' ? conn.secret.data : undefined,
    privateKey: conn.secret?.kind === 'privateKeyPath' ? conn.secret.data : undefined
  }
}

interface SavedConnectionsState {
  connections: SavedConnectionSummary[]
  /** True once the first refresh resolved (drives the empty state). */
  loaded: boolean
  refresh: () => Promise<void>
  /** Persists via IPC and refreshes the local list. Throws on IPC failure. */
  save: (input: SavedConnectionInput) => Promise<void>
  remove: (id: string) => Promise<void>
}

export const useSavedConnectionsStore = create<SavedConnectionsState>((set, get) => ({
  connections: [],
  loaded: false,

  refresh: async () => {
    const connections = await window.deskmux.connections.list()
    set({ connections, loaded: true })
  },

  save: async (input) => {
    await window.deskmux.connections.save(input)
    await get().refresh()
  },

  remove: async (id) => {
    await window.deskmux.connections.delete(id)
    await get().refresh()
  }
}))
