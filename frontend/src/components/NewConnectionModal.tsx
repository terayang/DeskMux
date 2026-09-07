import { Alert, Button, Input, Modal, Space, Spin, Tag, Typography, message } from 'antd'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ipcErrorMessage } from '../../shared/ipc'
import { PROTOCOLS } from '../../shared/protocols'
import { useAppStore } from '../store'
import { savedToCredentials, useSavedConnectionsStore } from '../store/savedConnections'
import CredentialsModal from './CredentialsModal'
import ProtocolCard from './ProtocolCard'

const { Text } = Typography

interface NewConnectionModalProps {
  open: boolean
  onClose: () => void
}

/**
 * In-workspace "new connection" flow (ux-review v1-A2): the connect flow —
 * target input, protocol cards, credentials — inside a modal, so a new target
 * can be reached from anywhere in the workspace. It deliberately reuses the
 * global scan/session stores (same targetAddress, startScan, toggleProtocol,
 * beginSession, ProtocolCard, CredentialsModal). Opened via the devices
 * view's add-device button, the command palette's actions, or the welcome
 * panel (which can pass `openNewConnection({ scan: true })` to start
 * scanning the entered address immediately).
 *
 * Multi-session model (F6): connecting always ADDS a session — live sessions
 * are never replaced, so no switch confirmation is needed anywhere here.
 */
export default function NewConnectionModal({ open, onClose }: NewConnectionModalProps) {
  const { t } = useTranslation()
  const targetAddress = useAppStore((s) => s.targetAddress)
  const setTargetAddress = useAppStore((s) => s.setTargetAddress)
  const scanning = useAppStore((s) => s.scanning)
  const scanReport = useAppStore((s) => s.scanReport)
  const scanError = useAppStore((s) => s.scanError)
  const selected = useAppStore((s) => s.selected)
  const startScan = useAppStore((s) => s.startScan)
  const toggleProtocol = useAppStore((s) => s.toggleProtocol)
  const beginSession = useAppStore((s) => s.beginSession)
  const beginSavedSession = useAppStore((s) => s.beginSavedSession)

  const savedConnections = useSavedConnectionsStore((s) => s.connections)
  const refreshSaved = useSavedConnectionsStore((s) => s.refresh)
  const saveConnection = useSavedConnectionsStore((s) => s.save)

  const [messageApi, messageHolder] = message.useMessage()
  const [credsOpen, setCredsOpen] = useState(false)

  // Reload the saved-connection list every time the modal opens.
  useEffect(() => {
    if (open) void refreshSaved()
  }, [open, refreshSaved])

  // Elapsed-time ticker shown next to the spinner while a scan runs.
  const [elapsedMs, setElapsedMs] = useState(0)
  useEffect(() => {
    if (!scanning) {
      setElapsedMs(0)
      return
    }
    const startedAt = Date.now()
    const timer = setInterval(() => setElapsedMs(Date.now() - startedAt), 100)
    return () => clearInterval(timer)
  }, [scanning])

  // Toast a scan failure once (the inline Alert below keeps it visible).
  const lastScanError = useRef<string | null>(null)
  useEffect(() => {
    if (open && scanError && lastScanError.current !== scanError) {
      void messageApi.error(`${t('scan.failed')}: ${scanError}`)
    }
    lastScanError.current = scanError
  }, [open, scanError, messageApi, t])

  /** Direct connect from a saved entry: opens a new session (same logic as the session sider). */
  const connectSaved = (id: string): void => {
    void (async () => {
      try {
        const conn = await window.deskmux.connections.get(id)
        if (!conn) {
          await refreshSaved() // stale entry (deleted elsewhere): reload the list
          return
        }
        beginSavedSession(
          { id: conn.id, host: conn.host, protocols: conn.protocols },
          savedToCredentials(conn)
        )
        onClose()
      } catch (err) {
        void messageApi.error(`${t('saved.loadFailed')}: ${ipcErrorMessage(err)}`)
      }
    })()
  }

  return (
    <Modal
      title={t('session.newConnection')}
      open={open}
      onCancel={onClose}
      footer={null}
      width={720}
      destroyOnHidden
    >
      {messageHolder}
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Space.Compact style={{ width: '100%' }}>
          <Input
            id="quick-target-input"
            className="mono"
            placeholder={t('app.targetPlaceholder')}
            value={targetAddress}
            disabled={scanning}
            onChange={(e) => setTargetAddress(e.target.value)}
            onPressEnter={() => void startScan()}
          />
          <Button type="primary" loading={scanning} onClick={() => void startScan()}>
            {t('scan.start')}
          </Button>
        </Space.Compact>

        {scanning && (
          <Space size={8}>
            <Spin size="small" />
            <Text type="secondary" style={{ fontSize: 12 }}>
              {t('scan.scanning')} ·{' '}
              {t('scan.elapsed', { seconds: (elapsedMs / 1000).toFixed(1) })}
            </Text>
          </Space>
        )}

        {scanError && (
          <Alert type="error" showIcon message={`${t('scan.failed')}: ${scanError}`} />
        )}

        {savedConnections.length > 0 && (
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              {t('session.saved')}
            </Text>
            <div className="newconn-saved-list">
              {savedConnections.map((conn) => (
                <div key={conn.id} className="saved-item" onClick={() => connectSaved(conn.id)}>
                  <Space size={6}>
                    <Text style={{ fontSize: 13 }}>{conn.name}</Text>
                    {conn.protocols.map((p) => (
                      <Tag key={p} style={{ marginInlineEnd: 0 }}>
                        {p.toUpperCase()}
                      </Tag>
                    ))}
                  </Space>
                  <Text className="mono" type="secondary" style={{ fontSize: 12 }}>
                    {conn.username}@{conn.host}
                  </Text>
                </div>
              ))}
            </div>
          </div>
        )}

        {scanReport && (
          <div className="protocol-grid" style={{ marginTop: 0 }}>
            {PROTOCOLS.map((meta) => {
              const result = scanReport.results.find((r) => r.protocolId === meta.id)
              if (!result) return null
              return (
                <ProtocolCard
                  key={meta.id}
                  meta={meta}
                  result={result}
                  selected={selected.includes(meta.id)}
                  onToggle={toggleProtocol}
                />
              )
            })}
          </div>
        )}

        {scanReport && (
          <Space size={16} className="newconn-footer" style={{ width: '100%', justifyContent: 'flex-end' }}>
            <Text type="secondary">{t('scan.selectedCount', { count: selected.length })}</Text>
            <Button
              type="primary"
              disabled={selected.length === 0}
              onClick={() => setCredsOpen(true)}
            >
              {t('scan.connect')}
            </Button>
          </Space>
        )}
      </Space>

      <CredentialsModal
        open={credsOpen}
        target={targetAddress.trim()}
        onCancel={() => setCredsOpen(false)}
        onSubmit={(credentials, saveRequest) => {
          setCredsOpen(false)
          // Save first (failures toast but never block connecting), then
          // go. B7: saveRequest.existingId updates the same-host+username
          // entry instead of creating a duplicate.
          void (async () => {
            if (saveRequest) {
              try {
                await saveConnection({
                  ...(saveRequest.existingId ? { id: saveRequest.existingId } : {}),
                  name: saveRequest.name,
                  host: targetAddress.trim(),
                  protocols: [...selected],
                  username: credentials.username,
                  secret: credentials.password
                    ? { kind: 'password', data: credentials.password }
                    : credentials.privateKey
                      ? { kind: 'privateKeyPath', data: credentials.privateKey }
                      : undefined
                })
              } catch (err) {
                void messageApi.error(`${t('saved.saveFailed')}: ${ipcErrorMessage(err)}`)
              }
            }
            beginSession(credentials)
          })()
          onClose()
        }}
      />
    </Modal>
  )
}
