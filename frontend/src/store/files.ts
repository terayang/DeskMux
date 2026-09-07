/**
 * State for one file manager panel (stage 4b): the local (left) and remote
 * (right) directory listings plus the bottom transfer queue. The panel owns
 * the SSH session lifecycle itself; this store holds only view state so the
 * listing survives React re-renders and the transfer queue can be fed by
 * sftp.onProgress events (which carry no file name — transfers run serially,
 * so progress always belongs to the one active item).
 *
 * Multi-session model (F6): one store instance per session (keyed by
 * SessionContext.id), created lazily by the panel and dropped when the
 * session closes (useAppStore.closeSession).
 */

import { create, useStore } from 'zustand'
import type { FileEntry } from '../../shared/ssh'

export type TransferDirection = 'upload' | 'download'
export type TransferStatus = 'active' | 'done' | 'error'

export interface TransferItem {
  id: number
  fileName: string
  direction: TransferDirection
  /** 0-100. */
  percent: number
  status: TransferStatus
}

/** Delay before a completed transfer fades out of the queue. */
const DONE_FADE_MS = 2500

interface FilesState {
  localPath: string | null
  localEntries: FileEntry[]
  localLoading: boolean
  remotePath: string | null
  remoteEntries: FileEntry[]
  remoteLoading: boolean
  /** Names of the selected rows in the remote table. */
  selectedRemote: string[]
  transfers: TransferItem[]
  setLocal: (path: string, entries: FileEntry[]) => void
  setLocalLoading: (loading: boolean) => void
  setRemote: (path: string, entries: FileEntry[]) => void
  setRemoteLoading: (loading: boolean) => void
  setSelectedRemote: (names: string[]) => void
  /** Returns the id identifying the new queue item. */
  addTransfer: (fileName: string, direction: TransferDirection) => number
  setTransferProgress: (id: number, percent: number) => void
  finishTransfer: (id: number, status: 'done' | 'error') => void
  removeTransfer: (id: number) => void
  /** Clears everything; called when the panel unmounts / reconnects. */
  reset: () => void
}

let transferSeq = 0

const INITIAL_STATE = {
  localPath: null,
  localEntries: [],
  localLoading: false,
  remotePath: null,
  remoteEntries: [],
  remoteLoading: false,
  selectedRemote: [],
  transfers: []
} satisfies Pick<
  FilesState,
  | 'localPath'
  | 'localEntries'
  | 'localLoading'
  | 'remotePath'
  | 'remoteEntries'
  | 'remoteLoading'
  | 'selectedRemote'
  | 'transfers'
>

export type FilesStore = ReturnType<typeof createFilesStore>

function createFilesStore() {
  return create<FilesState>((set, get) => ({
    ...INITIAL_STATE,

    setLocal: (path, entries) => set({ localPath: path, localEntries: entries }),

    setLocalLoading: (loading) => set({ localLoading: loading }),

    // Navigating invalidates the previous selection.
    setRemote: (path, entries) =>
      set({ remotePath: path, remoteEntries: entries, selectedRemote: [] }),

    setRemoteLoading: (loading) => set({ remoteLoading: loading }),

    setSelectedRemote: (names) => set({ selectedRemote: names }),

    addTransfer: (fileName, direction) => {
      const id = ++transferSeq
      set((s) => ({
        transfers: [...s.transfers, { id, fileName, direction, percent: 0, status: 'active' }]
      }))
      return id
    },

    setTransferProgress: (id, percent) =>
      set((s) => ({
        transfers: s.transfers.map((tr) => (tr.id === id ? { ...tr, percent } : tr))
      })),

    finishTransfer: (id, status) => {
      set((s) => ({
        transfers: s.transfers.map((tr) =>
          tr.id === id ? { ...tr, status, percent: status === 'done' ? 100 : tr.percent } : tr
        )
      }))
      if (status === 'done') {
        // Completed items linger briefly, then fade out of the queue.
        setTimeout(() => get().removeTransfer(id), DONE_FADE_MS)
      }
    },

    removeTransfer: (id) =>
      set((s) => ({ transfers: s.transfers.filter((tr) => tr.id !== id) })),

    reset: () => set({ ...INITIAL_STATE })
  }))
}

const registry = new Map<string, FilesStore>()

export function getFilesStore(sessionId: string): FilesStore {
  let store = registry.get(sessionId)
  if (store === undefined) {
    store = createFilesStore()
    registry.set(sessionId, store)
  }
  return store
}

export function dropFilesStore(sessionId: string): void {
  registry.delete(sessionId)
}

/** Hook shorthand: subscribes the component to this session's files store. */
export function useFilesStore<T>(sessionId: string, selector: (s: FilesState) => T): T {
  return useStore(getFilesStore(sessionId), selector)
}
