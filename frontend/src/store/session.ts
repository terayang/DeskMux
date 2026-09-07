import { createContext, useContext } from 'react'

/** Credentials collected by CredentialsModal before a session starts. */
export interface SessionCredentials {
  username: string
  password?: string
  /** Path to a local private-key file (advanced option). */
  privateKey?: string
  /** Passphrase decrypting the private key (advanced option). */
  passphrase?: string
}

/**
 * Everything one session workspace needs to know about its connection: who we
 * connect to, over which protocols, as which user. Written once when the
 * session is created (credentials modal submit / saved-connection connect)
 * and immutable for the session's lifetime.
 *
 * Multi-session model (F6): sessions coexist, so each session's subtree gets
 * its own context via SessionScopeContext instead of a global singleton.
 */
export interface SessionContext {
  /**
   * Frontend-generated session instance id ('session-N'). It keys every
   * per-session resource: the panel stores' registries and the VNC live-slot
   * map. Never reused within a renderer lifetime. (The backend assigns its
   * own ids per SSH connection / VNC bridge; panels keep those locally.)
   */
  id: string
  target: string
  /** Protocol ids the user checked in the new-connection modal. */
  protocols: string[]
  credentials: SessionCredentials
  /**
   * Id of the saved connection this session started from (beginSavedSession
   * only); lets the sider show the saved entry's name for the session.
   */
  savedId?: string
}

/**
 * Per-session scope: each session's pane in SessionPage provides its own
 * SessionContext, and the panels (desktop / terminal / files) read theirs
 * from here instead of a global store, so concurrently open sessions never
 * see each other's target or credentials.
 */
export const SessionScopeContext = createContext<SessionContext | null>(null)

/** The session context of the pane this component renders in (null outside). */
export function useSessionScope(): SessionContext | null {
  return useContext(SessionScopeContext)
}
