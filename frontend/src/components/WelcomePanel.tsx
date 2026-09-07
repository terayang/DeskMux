import { SearchOutlined } from '@ant-design/icons'
import { Alert, Button, Input, Radio, Space, Tooltip, Typography } from 'antd'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useConnectSaved } from '../hooks/useConnectSaved'
import { useAppStore } from '../store'
import { useSavedConnectionsStore } from '../store/savedConnections'

const { Text } = Typography

/**
 * Empty-state home of the sessions stage (no live session): the quick-connect
 * hero. Enter on an address that exactly matches a saved entry connects
 * directly (B1/B6); Enter on a new address hands the target to
 * NewConnectionModal, which scans it there. The saved-device list itself now
 * lives in the rail and the devices view.
 */
export default function WelcomePanel() {
  const { t } = useTranslation()
  const targetAddress = useAppStore((s) => s.targetAddress)
  const setTargetAddress = useAppStore((s) => s.setTargetAddress)
  const openNewConnection = useAppStore((s) => s.openNewConnection)

  const savedConnections = useSavedConnectionsStore((s) => s.connections)
  const savedLoaded = useSavedConnectionsStore((s) => s.loaded)
  const refreshSaved = useSavedConnectionsStore((s) => s.refresh)

  const connectSaved = useConnectSaved()

  // Load the saved-connection list once per mount (drives the B1 banner).
  useEffect(() => {
    void refreshSaved()
  }, [refreshSaved])

  // B1: exact saved-address match (trim + lowercase) driving the quick-connect
  // banner; skipped until the saved list has loaded to avoid a first-paint
  // flicker, and empty input never matches.
  const normalizedInput = targetAddress.trim().toLowerCase()
  const matches =
    savedLoaded && normalizedInput !== ''
      ? savedConnections.filter((c) => c.host.trim().toLowerCase() === normalizedInput)
      : []
  // B3: several identities for one host — the user picks one (first by default).
  const [selectedMatchId, setSelectedMatchId] = useState<string | null>(null)
  const activeMatch = matches.find((m) => m.id === selectedMatchId) ?? matches[0]

  /** New-address path: hand the target to the new-connection modal, which scans it there. */
  const scanTarget = (): void => openNewConnection({ scan: true })

  return (
    <div className="welcome-panel">
      <div className="welcome-axis">
        <div className="welcome-brand">
          <h1 className="welcome-brand-title">{t('app.title')}</h1>
          <div className="welcome-brand-tagline">{t('app.tagline')}</div>
        </div>

        <Space.Compact className="quick-connect">
          <Input
            id="target-address-input"
            className="mono"
            size="large"
            placeholder={t('app.targetPlaceholder')}
            value={targetAddress}
            onChange={(e) => setTargetAddress(e.target.value)}
            onPressEnter={() => {
              // B6: Enter connects directly when the input exactly matches a
              // saved entry; otherwise it scans the address in the
              // new-connection modal.
              if (activeMatch) void connectSaved(activeMatch.id)
              else scanTarget()
            }}
          />
          <Button
            type="primary"
            size="large"
            icon={<SearchOutlined />}
            onClick={scanTarget}
          >
            {t('scan.start')}
          </Button>
        </Space.Compact>
        {matches.length > 0 && activeMatch && (
          <div id="quick-connect-banner" className="quick-connect-banner">
            <Alert
              type="info"
              showIcon
              message={
                <Space direction="vertical" size={6} style={{ width: '100%' }}>
                  <Text style={{ fontSize: 13 }}>
                    {matches.length === 1
                      ? t('scan.savedMatch', {
                          name: activeMatch.name,
                          username: activeMatch.username,
                          host: activeMatch.host
                        })
                      : t('scan.savedMatchMulti', { count: matches.length })}
                  </Text>
                  {matches.length > 1 && (
                    <Radio.Group
                      size="small"
                      value={activeMatch.id}
                      onChange={(e) => setSelectedMatchId(e.target.value as string)}
                    >
                      <Space direction="vertical" size={2}>
                        {matches.map((m) => (
                          <Radio key={m.id} value={m.id}>
                            <Text style={{ fontSize: 12 }}>
                              {m.name}（{m.username}）
                            </Text>
                          </Radio>
                        ))}
                      </Space>
                    </Radio.Group>
                  )}
                  <Space size={8}>
                    <Button
                      size="small"
                      type="primary"
                      onClick={() => void connectSaved(activeMatch.id)}
                    >
                      {t('scan.directConnect')}
                    </Button>
                    <Tooltip title={t('scan.rescanTooltip')}>
                      <Button size="small" type="link" onClick={scanTarget}>
                        {t('scan.rescan')}
                      </Button>
                    </Tooltip>
                  </Space>
                </Space>
              }
            />
          </div>
        )}
        <div className="quick-connect-hint">
          <span>
            <kbd className="kbd">⏎</kbd> {t('welcome.hintConnect')}
          </span>
          <span>
            <kbd className="kbd">⌘K</kbd> {t('welcome.hintNew')}
          </span>
        </div>
      </div>
    </div>
  )
}
