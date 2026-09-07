import { ApiOutlined, HddOutlined, SettingOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { Tooltip } from 'antd'
import { useTranslation } from 'react-i18next'
import { useAppStore } from '../store'

interface ActivityBarProps {
  onOpenSettings: () => void
}

/**
 * The 48px icon column on the workbench's left edge: view switcher on top
 * (sessions carries a live-session count badge), global actions pinned to the
 * bottom (⌘K quick-connect palette, settings).
 */
export default function ActivityBar({ onOpenSettings }: ActivityBarProps) {
  const { t } = useTranslation()
  const activeView = useAppStore((s) => s.activeView)
  const setActiveView = useAppStore((s) => s.setActiveView)
  const sessionCount = useAppStore((s) => s.sessions.length)
  const setPaletteOpen = useAppStore((s) => s.setPaletteOpen)

  return (
    <div className="activitybar">
      <Tooltip title={t('workbench.sessions')} placement="right">
        <button
          className={activeView === 'sessions' ? 'abtn on' : 'abtn'}
          aria-label={t('workbench.sessions')}
          onClick={() => setActiveView('sessions')}
        >
          <ApiOutlined />
          {sessionCount > 0 && <span className="badge">{sessionCount}</span>}
        </button>
      </Tooltip>
      <Tooltip title={t('workbench.devices')} placement="right">
        <button
          className={activeView === 'devices' ? 'abtn on' : 'abtn'}
          aria-label={t('workbench.devices')}
          onClick={() => setActiveView('devices')}
        >
          <HddOutlined />
        </button>
      </Tooltip>
      <div className="grow" />
      <Tooltip title={`${t('workbench.quickConnect')} ⌘K`} placement="right">
        <button
          className="abtn"
          aria-label={t('workbench.quickConnect')}
          onClick={() => setPaletteOpen(true)}
        >
          <ThunderboltOutlined />
        </button>
      </Tooltip>
      <Tooltip title={t('settings.open')} placement="right">
        <button className="abtn" aria-label={t('settings.open')} onClick={onOpenSettings}>
          <SettingOutlined />
        </button>
      </Tooltip>
    </div>
  )
}
