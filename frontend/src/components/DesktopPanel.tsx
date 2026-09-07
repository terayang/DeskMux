import {
  DisconnectOutlined,
  EyeOutlined,
  FullscreenExitOutlined,
  FullscreenOutlined,
  KeyOutlined,
  LockOutlined,
  ReloadOutlined,
  SettingOutlined,
  WarningOutlined
} from '@ant-design/icons'
import { Button, Dropdown, Popover, Segmented, Select, Space, Spin, Tooltip, Typography } from 'antd'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSessionScope } from '../store/session'
import {
  VNC_KEY_COMBOS,
  attachVnc,
  detachVnc,
  retryVnc,
  sendKeyCombo,
  useVncStore,
  userDisconnectVnc,
  type VncErrorKind,
  type VncKeyComboName
} from '../store/vnc'
import './vnc.css'

const { Text } = Typography

const VNC_PORT = 5900

export default function DesktopPanel() {
  const { t } = useTranslation()
  const context = useSessionScope()
  const scopeId = context?.id ?? ''
  const status = useVncStore(scopeId, (s) => s.status)
  const errorKind = useVncStore(scopeId, (s) => s.errorKind)
  const desktopName = useVncStore(scopeId, (s) => s.desktopName)
  const scaleMode = useVncStore(scopeId, (s) => s.scaleMode)
  const setScaleMode = useVncStore(scopeId, (s) => s.setScaleMode)
  const cursorMode = useVncStore(scopeId, (s) => s.cursorMode)
  const setCursorMode = useVncStore(scopeId, (s) => s.setCursorMode)
  const encMode = useVncStore(scopeId, (s) => s.encMode)
  const quality = useVncStore(scopeId, (s) => s.quality)
  const compression = useVncStore(scopeId, (s) => s.compression)
  const setEncMode = useVncStore(scopeId, (s) => s.setEncMode)
  const setQuality = useVncStore(scopeId, (s) => s.setQuality)
  const setCompression = useVncStore(scopeId, (s) => s.setCompression)
  const colorDepth = useVncStore(scopeId, (s) => s.colorDepth)
  const setColorDepth = useVncStore(scopeId, (s) => s.setColorDepth)
  const viewOnly = useVncStore(scopeId, (s) => s.viewOnly)
  const setViewOnly = useVncStore(scopeId, (s) => s.setViewOnly)
  const containerRef = useRef<HTMLDivElement>(null)
  const viewportRef = useRef<HTMLDivElement>(null)

  // Fullscreen support is feature-detected once on mount: the Wails webview
  // may not implement the Fullscreen API, in which case the button degrades
  // to disabled-with-tooltip instead of failing mid-click.
  const [fullscreenSupported, setFullscreenSupported] = useState(false)
  const [isFullscreen, setIsFullscreen] = useState(false)
  useEffect(() => {
    const viewport = viewportRef.current
    setFullscreenSupported(
      document.fullscreenEnabled && typeof viewport?.requestFullscreen === 'function'
    )
    const onFullscreenChange = (): void => {
      setIsFullscreen(document.fullscreenElement === viewportRef.current)
    }
    document.addEventListener('fullscreenchange', onFullscreenChange)
    return () => document.removeEventListener('fullscreenchange', onFullscreenChange)
  }, [])

  const toggleFullscreen = (): void => {
    if (isFullscreen) {
      void document.exitFullscreen().catch(() => {})
    } else {
      void viewportRef.current?.requestFullscreen().catch(() => {})
    }
  }

  // The mount lifecycle owns the connection: exactly one attach per mount,
  // detach on unmount. attachVnc() tears down this session's previous live
  // connection, so StrictMode double-mounts and tab close/reopen cannot stack
  // connections; other sessions' connections are untouched.
  useEffect(() => {
    const container = containerRef.current
    if (context === null || container === null) return
    void attachVnc(context.id, container, {
      host: context.target,
      port: VNC_PORT,
      username: context.credentials.username,
      password: context.credentials.password
    })
    return () => {
      detachVnc(context.id)
    }
  }, [context])

  const errorMessages: Record<VncErrorKind, string> = {
    auth: t('vnc.errorAuth', { defaultValue: '认证失败，请检查 macOS 用户名或密码' }),
    connection: t('vnc.errorUnreachable', { defaultValue: '无法连接到目标 VNC' }),
    protocol: t('vnc.errorProtocol', { defaultValue: 'VNC 协议错误' }),
    timeout: t('vnc.errorTimeout', { defaultValue: '连接超时' }),
    unknown: t('vnc.errorUnknown', { defaultValue: '连接已断开' })
  }

  const displaySettings = (
    <Space direction="vertical" size={10} style={{ width: 240 }}>
      <div>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {t('desktop.encMode')}
        </Text>
        <Select
          size="small"
          style={{ width: '100%' }}
          value={encMode}
          onChange={setEncMode}
          options={[
            { value: 'auto', label: t('desktop.encAuto') },
            { value: 'zrle', label: 'ZRLE' },
            { value: 'hextile', label: 'Hextile' },
            { value: 'raw', label: 'Raw' }
          ]}
        />
        <div>
          <Text type="secondary" style={{ fontSize: 11 }}>
            {t('desktop.encHint')}
          </Text>
        </div>
      </div>
      <div>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {t('desktop.colorDepth')}
        </Text>
        <Select
          size="small"
          style={{ width: '100%' }}
          value={colorDepth}
          onChange={(v) => setColorDepth(v as 16 | 24)}
          options={[
            { value: 24, label: t('desktop.depth24') },
            { value: 16, label: t('desktop.depth16') }
          ]}
        />
      </div>
      <div>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {t('desktop.quality')}
        </Text>
        <Select
          size="small"
          style={{ width: '100%' }}
          value={quality}
          onChange={setQuality}
          options={[
            { value: 8, label: t('desktop.qualityHigh') },
            { value: 6, label: t('desktop.qualityMid') },
            { value: 3, label: t('desktop.qualityLow') }
          ]}
        />
        <div>
          <Text type="secondary" style={{ fontSize: 11 }}>
            {t('desktop.qualityHint')}
          </Text>
        </div>
      </div>
      <div>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {t('desktop.compression')}
        </Text>
        <Select
          size="small"
          style={{ width: '100%' }}
          value={compression}
          onChange={setCompression}
          options={[
            { value: 6, label: t('desktop.compSaver') },
            { value: 2, label: t('desktop.compBalanced') },
            { value: 0, label: t('desktop.compLowCpu') }
          ]}
        />
      </div>
    </Space>
  )

  return (
    <div className="desktop-panel">
      <div className="desktop-toolbar">
        <div className="toolbar-group">
          <Select
            size="small"
            value={scaleMode}
            style={{ width: 140 }}
            onChange={setScaleMode}
            options={[
              { value: 'fit', label: t('desktop.zoomFit') },
              { value: 'actual', label: t('desktop.zoomActual') }
            ]}
          />
          <Tooltip title={t('desktop.cursorModeHint')} placement="bottom">
            <Segmented
              size="small"
              value={cursorMode}
              onChange={(value) => setCursorMode(value as 'remote' | 'local')}
              options={[
                { value: 'remote', label: t('desktop.cursorRemote') },
                { value: 'local', label: t('desktop.cursorLocal') }
              ]}
            />
          </Tooltip>
          <Popover
            content={displaySettings}
            title={t('desktop.settings')}
            trigger="click"
            placement="bottomLeft"
          >
            <Button size="small" icon={<SettingOutlined />} />
          </Popover>
        </div>
        <span className="toolbar-divider" />
        <div className="toolbar-group">
          <Tooltip title={t('desktop.viewOnlyHint')} placement="bottom">
            <Button
              id="vnc-viewonly-toggle"
              size="small"
              type={viewOnly ? 'primary' : 'default'}
              icon={viewOnly ? <LockOutlined /> : <EyeOutlined />}
              aria-label={t('desktop.viewOnly')}
              onClick={() => setViewOnly(!viewOnly)}
            />
          </Tooltip>
          <Dropdown
            trigger={['click']}
            disabled={status !== 'connected'}
            menu={{
              items: [
                { key: 'ctrlAltDel', label: t('desktop.sendCtrlAltDel') },
                { key: 'altF4', label: t('desktop.sendAltF4') },
                { key: 'super', label: t('desktop.sendSuper') }
              ],
              onClick: ({ key }) => void sendKeyCombo(scopeId, VNC_KEY_COMBOS[key as VncKeyComboName])
            }}
          >
            <Button
              id="vnc-sendkeys-button"
              size="small"
              icon={<KeyOutlined />}
              aria-label={t('desktop.sendKeys')}
            />
          </Dropdown>
          <Tooltip
            title={
              fullscreenSupported
                ? t(isFullscreen ? 'desktop.fullscreenExit' : 'desktop.fullscreen')
                : t('desktop.fullscreenUnsupported')
            }
            placement="bottom"
          >
            <Button
              id="vnc-fullscreen-button"
              size="small"
              disabled={!fullscreenSupported}
              icon={isFullscreen ? <FullscreenExitOutlined /> : <FullscreenOutlined />}
              aria-label={t('desktop.fullscreen')}
              onClick={toggleFullscreen}
            />
          </Tooltip>
        </div>
        <div className="toolbar-spacer" />
        {desktopName !== '' && (
          <span className="toolbar-desktop-name mono" title={desktopName}>
            {desktopName}
          </span>
        )}
        <Button size="small" danger icon={<DisconnectOutlined />} onClick={() => userDisconnectVnc(scopeId)}>
          {t('desktop.disconnect')}
        </Button>
      </div>
      <div ref={viewportRef} className="desktop-viewport vnc-viewport">
        <div
          ref={containerRef}
          className={cursorMode === 'local' ? 'vnc-container local-cursor' : 'vnc-container'}
        />
        {context === null && (
          <div className="vnc-overlay">
            <Text type="secondary">
              {t('vnc.noContext', { defaultValue: '没有可用的会话信息，请重新连接' })}
            </Text>
          </div>
        )}
        {context !== null && status === 'connecting' && (
          <div className="vnc-overlay" data-testid="vnc-connecting">
            <Spin />
            <Text type="secondary">
              {t('vnc.connecting', { defaultValue: '正在连接远程桌面…' })}
            </Text>
          </div>
        )}
        {context !== null && status === 'idle' && (
          <div className="vnc-overlay">
            <Text type="secondary">{t('vnc.disconnected', { defaultValue: '已断开连接' })}</Text>
            <Button icon={<ReloadOutlined />} onClick={() => void retryVnc(scopeId)}>
              {t('vnc.reconnect', { defaultValue: '重新连接' })}
            </Button>
          </div>
        )}
        {context !== null && status === 'error' && errorKind !== null && (
          <div className="vnc-overlay" data-testid="vnc-error" data-error-kind={errorKind}>
            <WarningOutlined style={{ fontSize: 28, color: '#ff4d4f' }} />
            <Text>{errorMessages[errorKind]}</Text>
            <Button type="primary" icon={<ReloadOutlined />} onClick={() => void retryVnc(scopeId)}>
              {t('vnc.retry', { defaultValue: '重试' })}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
