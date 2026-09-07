import { CloseOutlined } from '@ant-design/icons'
import { Button, Popconfirm } from 'antd'
import { useEffect, useState, type ComponentType } from 'react'
import { useTranslation } from 'react-i18next'
import ActivityBar from '../components/ActivityBar'
import CommandPalette from '../components/CommandPalette'
import DesktopPanel from '../components/DesktopPanel'
import DevicesView from '../components/DevicesView'
import FileManagerPanel from '../components/FileManagerPanel'
import NewConnectionModal from '../components/NewConnectionModal'
import Rail from '../components/Rail'
import SettingsModal from '../components/SettingsModal'
import TerminalPanel from '../components/TerminalPanel'
import WelcomePanel from '../components/WelcomePanel'
import { useAppStore, type ActiveSession, type TabKind } from '../store'
import { useSavedConnectionsStore } from '../store/savedConnections'
import { SessionScopeContext } from '../store/session'
import { sessionDisplayName, TAB_PROTOCOL } from '../utils/session'

const TAB_PANELS: Record<TabKind, ComponentType> = {
  desktop: DesktopPanel,
  terminal: TerminalPanel,
  files: FileManagerPanel
}

/**
 * One session's workspace pane: its own protocol tab strip + panels, scoped by
 * SessionScopeContext so the panels read this session's target/credentials.
 * Panes stay mounted while their session lives — SessionPage hides the
 * inactive ones with display:none — so xterm scrollback, SFTP listings and
 * noVNC canvases survive session switches and SSH/VNC connections stay up.
 * Tab panels lazy-mount on first activation (matching the retired antd Tabs
 * behavior) and then stay mounted, hidden with display:none.
 */
function SessionPane({ session }: { session: ActiveSession }) {
  const { t } = useTranslation()
  const setActiveTab = useAppStore((s) => s.setActiveTab)
  const closeTab = useAppStore((s) => s.closeTab)
  const [visited, setVisited] = useState<ReadonlySet<TabKind>>(
    () => new Set(session.activeTab ? [session.activeTab] : [])
  )

  useEffect(() => {
    if (session.activeTab && !visited.has(session.activeTab)) {
      setVisited((prev) => new Set(prev).add(session.activeTab as TabKind))
    }
  }, [session.activeTab, visited])

  return (
    <SessionScopeContext.Provider value={session.context}>
      <div className="tabstrip">
        {session.tabs.map((tab) => (
          <div
            key={tab.key}
            className={tab.key === session.activeTab ? 'tabpill on' : 'tabpill'}
            onClick={() => setActiveTab(tab.key)}
          >
            <span>{t(tab.titleKey)}</span>
            <button
              className="x"
              aria-label={t('session.closeSession')}
              onClick={(e) => {
                e.stopPropagation()
                closeTab(tab.key)
              }}
            >
              <CloseOutlined />
            </button>
          </div>
        ))}
      </div>
      <div className="session-panels">
        {session.tabs.map((tab) => {
          if (!visited.has(tab.key)) return null
          const Panel = TAB_PANELS[tab.key]
          return (
            <div
              key={tab.key}
              className="session-panel-slot"
              style={{ display: tab.key === session.activeTab ? 'flex' : 'none' }}
            >
              <Panel />
            </div>
          )
        })}
      </div>
    </SessionScopeContext.Provider>
  )
}

/** 44px stage header for the active session: protocol dot + breadcrumb +
 * user@host + the disconnect action (with its Popconfirm). */
function StageHead({ session }: { session: ActiveSession }) {
  const { t } = useTranslation()
  const disconnect = useAppStore((s) => s.disconnect)
  const savedConnections = useSavedConnectionsStore((s) => s.connections)
  const proto = session.activeTab ? TAB_PROTOCOL[session.activeTab] : 'other'
  const tabDef = session.tabs.find((tb) => tb.key === session.activeTab)

  return (
    <div className="stage-head">
      <span className="dot" style={{ background: `var(--ar-proto-${proto})` }} />
      <span className="crumb">
        {tabDef && <span className="k">{t(tabDef.titleKey)} </span>}
        {sessionDisplayName(session, savedConnections)}
      </span>
      <span className="who">
        {session.context.credentials.username}@{session.context.target}
      </span>
      <div className="grow" />
      <Popconfirm
        title={t('session.disconnectConfirm', { target: session.context.target })}
        okText={t('session.disconnect')}
        cancelText={t('saved.cancel')}
        placement="bottomRight"
        onConfirm={() => disconnect()}
      >
        <Button id="disconnect-button" type="text" danger size="small">
          {t('session.disconnect')}
        </Button>
      </Popconfirm>
    </div>
  )
}

/**
 * The Operator Console workbench: ActivityBar (view switch + global actions)
 * | Rail (context list) | Stage (active session / welcome / device fleet).
 * Every session's pane stays mounted (hidden with display:none) so terminals,
 * file managers and VNC desktops keep their state and connections.
 */
export default function SessionPage() {
  const activeView = useAppStore((s) => s.activeView)
  const sessions = useAppStore((s) => s.sessions)
  const activeSessionId = useAppStore((s) => s.activeSessionId)
  const newConnectionOpen = useAppStore((s) => s.newConnectionOpen)
  const setNewConnectionOpen = useAppStore((s) => s.setNewConnectionOpen)
  const refreshSaved = useSavedConnectionsStore((s) => s.refresh)
  const activeSession = sessions.find((s) => s.id === activeSessionId)

  const [settingsOpen, setSettingsOpen] = useState(false)
  // Selected device in the devices view; rail rows and the stage table share it.
  const [selectedDeviceId, setSelectedDeviceId] = useState<string | null>(null)

  useEffect(() => {
    void refreshSaved()
  }, [refreshSaved])

  return (
    <div className="workbench">
      <ActivityBar onOpenSettings={() => setSettingsOpen(true)} />
      <Rail selectedDeviceId={selectedDeviceId} onSelectDevice={setSelectedDeviceId} />
      <div className="stage">
        {activeView === 'sessions' && activeSession && <StageHead session={activeSession} />}
        {/* Session panes stay mounted in every view (hiding the wrapper with
            display:none) so switching to the device fleet never tears down
            xterm/SFTP/noVNC state. */}
        {sessions.length > 0 && (
          <div
            className="stage-sessions"
            style={{ display: activeView === 'sessions' ? 'flex' : 'none' }}
          >
            {sessions.map((session) => (
              <div
                key={session.id}
                className="session-pane"
                style={{ display: session.id === activeSessionId ? 'flex' : 'none' }}
              >
                <SessionPane session={session} />
              </div>
            ))}
          </div>
        )}
        {activeView === 'sessions' && sessions.length === 0 && <WelcomePanel />}
        {activeView === 'devices' && (
          <DevicesView selectedId={selectedDeviceId} onSelect={setSelectedDeviceId} />
        )}
      </div>
      <NewConnectionModal open={newConnectionOpen} onClose={() => setNewConnectionOpen(false)} />
      <SettingsModal open={settingsOpen} onClose={() => setSettingsOpen(false)} />
      <CommandPalette />
    </div>
  )
}
