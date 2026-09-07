import { App as AntdApp } from 'antd'
import { useTranslation } from 'react-i18next'
import { ipcErrorMessage } from '../../shared/ipc'
import { useAppStore } from '../store'
import { savedToCredentials, useSavedConnectionsStore } from '../store/savedConnections'

/**
 * Connects to a saved device by id: fetches the decrypted credentials via
 * connections.get and opens a NEW session (the multi-session model never
 * replaces a live one). `protocols` narrows the session to a protocol subset
 * (e.g. ['ssh'] for the device view's 终端 button); beginSavedSession further
 * filters it to session-capable protocols. On success the workbench switches
 * to the sessions view so the new pane is visible. Stale entries (deleted
 * elsewhere) just reload the saved list; IPC failures toast.
 */
export function useConnectSaved(): (id: string, protocols?: string[]) => Promise<void> {
  const { t } = useTranslation()
  const { message } = AntdApp.useApp()
  const beginSavedSession = useAppStore((s) => s.beginSavedSession)
  const setActiveView = useAppStore((s) => s.setActiveView)
  const refreshSaved = useSavedConnectionsStore((s) => s.refresh)

  return async (id, protocols) => {
    try {
      const conn = await window.anyremote.connections.get(id)
      if (!conn) {
        await refreshSaved()
        return
      }
      beginSavedSession(
        { id: conn.id, host: conn.host, protocols: protocols ?? conn.protocols },
        savedToCredentials(conn)
      )
      setActiveView('sessions')
    } catch (err) {
      void message.error(`${t('saved.loadFailed')}: ${ipcErrorMessage(err)}`)
    }
  }
}
