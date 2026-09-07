import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { App as AntdApp, Button, Popconfirm } from 'antd'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ipcErrorMessage, type SavedConnectionSummary } from '../../shared/ipc'
import { useConnectSaved } from '../hooks/useConnectSaved'
import { useAppStore } from '../store'
import { useSavedConnectionsStore } from '../store/savedConnections'
import { liveSessionFor } from '../utils/session'
import EditConnectionModal from './EditConnectionModal'

interface DevicesViewProps {
  /** Selected row (highlight synced with the rail in devices view). */
  selectedId: string | null
  onSelect: (id: string | null) => void
}

/**
 * The device fleet: one row per saved connection with per-protocol entry
 * buttons (终端 opens an ssh-only session, 桌面 a vnc-only one — the saved
 * entry's credentials are reused, beginSavedSession filters the protocol
 * subset), plus edit/delete. A device with a live session gets a green dot
 * and a 已连接 badge. Owns the EditConnectionModal (the welcome panel's
 * saved-card grid moved here).
 */
export default function DevicesView({ selectedId, onSelect }: DevicesViewProps) {
  const { t } = useTranslation()
  const { message } = AntdApp.useApp()
  const connections = useSavedConnectionsStore((s) => s.connections)
  const loaded = useSavedConnectionsStore((s) => s.loaded)
  const removeConnection = useSavedConnectionsStore((s) => s.remove)
  const sessions = useAppStore((s) => s.sessions)
  const setNewConnectionOpen = useAppStore((s) => s.setNewConnectionOpen)
  const connectSaved = useConnectSaved()

  // The saved entry open in the edit modal (null = closed).
  const [editing, setEditing] = useState<SavedConnectionSummary | null>(null)

  const remove = async (id: string): Promise<void> => {
    try {
      await removeConnection(id)
    } catch (err) {
      void message.error(`${t('saved.deleteFailed')}: ${ipcErrorMessage(err)}`)
    }
  }

  return (
    <div className="devices-view">
      <div className="devices-head">
        <h2>{t('workbench.devices')}</h2>
        <Button
          id="new-connection-button"
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setNewConnectionOpen(true)}
        >
          {t('devices.add')}
        </Button>
      </div>
      {loaded && connections.length === 0 ? (
        <div className="devices-empty">
          <div>{t('devices.empty')}</div>
          <Button type="primary" onClick={() => setNewConnectionOpen(true)}>
            {t('devices.add')}
          </Button>
        </div>
      ) : (
        <div className="devices-table">
          <div className="devices-row devices-row-head">
            <span>{t('devices.colName')}</span>
            <span>{t('devices.colAddress')}</span>
            <span>{t('devices.colProtocols')}</span>
            <span>{t('devices.colActions')}</span>
          </div>
          {connections.map((c) => {
            const live = liveSessionFor(c, sessions)
            return (
              <div
                key={c.id}
                className={c.id === selectedId ? 'devices-row on' : 'devices-row'}
                onClick={() => onSelect(c.id)}
                onDoubleClick={() => void connectSaved(c.id)}
              >
                <span className="devices-name">
                  {live && <span className="dot-live" />}
                  <span className="nm">{c.name}</span>
                  {live && <span className="connected-badge">{t('session.connected')}</span>}
                </span>
                <span className="devices-addr mono">
                  {c.username}@{c.host}
                </span>
                <span className="devices-protos">
                  {c.protocols.map((p) => (
                    <span key={p} className="proto-tag" data-protocol={p}>
                      {p.toUpperCase()}
                    </span>
                  ))}
                </span>
                <span
                  className="devices-actions"
                  onClick={(e) => e.stopPropagation()}
                  onDoubleClick={(e) => e.stopPropagation()}
                >
                  {c.protocols.includes('ssh') && (
                    <Button size="small" onClick={() => void connectSaved(c.id, ['ssh'])}>
                      {t('devices.terminal')}
                    </Button>
                  )}
                  {c.protocols.includes('vnc') && (
                    <Button size="small" onClick={() => void connectSaved(c.id, ['vnc'])}>
                      {t('devices.desktop')}
                    </Button>
                  )}
                  <Button
                    size="small"
                    type="text"
                    aria-label={t('saved.edit')}
                    icon={<EditOutlined />}
                    onClick={() => setEditing(c)}
                  />
                  <Popconfirm
                    title={t('saved.deleteConfirm')}
                    okText={t('saved.delete')}
                    cancelText={t('saved.cancel')}
                    onConfirm={() => void remove(c.id)}
                  >
                    <Button
                      size="small"
                      type="text"
                      danger
                      aria-label={t('saved.delete')}
                      icon={<DeleteOutlined />}
                    />
                  </Popconfirm>
                </span>
              </div>
            )
          })}
        </div>
      )}
      <EditConnectionModal conn={editing} onClose={() => setEditing(null)} />
    </div>
  )
}
