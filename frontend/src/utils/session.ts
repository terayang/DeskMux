import type { SavedConnectionSummary } from '../../shared/ipc'
import type { ActiveSession, TabKind } from '../store'

/** Protocol identity color of a tab kind: desktop = VNC, terminal/files = SSH. */
export const TAB_PROTOCOL: Record<TabKind, 'ssh' | 'vnc'> = {
  desktop: 'vnc',
  terminal: 'ssh',
  files: 'ssh'
}

/** Saved entry backing a session: exact savedId match first; a session that
 * was saved only AFTER connecting (scan path) carries no savedId, so fall
 * back to host+username identity. */
function savedForSession(
  session: ActiveSession,
  saved: readonly SavedConnectionSummary[]
): SavedConnectionSummary | undefined {
  return (
    saved.find((c) => c.id === session.context.savedId) ??
    saved.find(
      (c) =>
        c.host === session.context.target &&
        c.username === session.context.credentials.username
    )
  )
}

/** Display name of an active session: the saved entry's name when the session
 * started from one, otherwise the raw target. */
export function sessionDisplayName(
  session: ActiveSession,
  saved: readonly SavedConnectionSummary[]
): string {
  return savedForSession(session, saved)?.name ?? session.context.target
}

/** The live session backing a saved device, if any (same identity rule as
 * savedForSession, mirrored). */
export function liveSessionFor(
  saved: SavedConnectionSummary,
  sessions: readonly ActiveSession[]
): ActiveSession | undefined {
  return (
    sessions.find((s) => s.context.savedId === saved.id) ??
    sessions.find(
      (s) =>
        s.context.target === saved.host && s.context.credentials.username === saved.username
    )
  )
}

/** Identity protocol of a saved device: the first session-capable protocol
 * (ssh before vnc); 'other' when it has none. */
export function deviceProtocol(protocols: readonly string[]): string {
  return ['ssh', 'vnc'].find((p) => protocols.includes(p)) ?? 'other'
}
