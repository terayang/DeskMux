import { CloseOutlined, SearchOutlined } from '@ant-design/icons'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { SavedConnectionSummary } from '../../shared/ipc'
import { useConnectSaved } from '../hooks/useConnectSaved'
import { useAppStore, type ActiveSession } from '../store'
import { useSavedConnectionsStore } from '../store/savedConnections'
import { deviceProtocol, liveSessionFor, sessionDisplayName, TAB_PROTOCOL } from '../utils/session'

interface RailProps {
  /** Selected device in the devices view (highlight synced with DevicesView). */
  selectedDeviceId: string | null
  onSelectDevice: (id: string | null) => void
}

/**
 * The 240px context column: header + filter input + list(s) + summary foot.
 * In the sessions view it lists active sessions (click switches, hover ×
 * closes) above saved devices (click connects — or switches when a live
 * session already exists for that saved id, A5). In the devices view it lists
 * devices only: click selects (highlight synced with the stage table),
 * double-click connects.
 */
export default function Rail({ selectedDeviceId, onSelectDevice }: RailProps) {
  const { t } = useTranslation()
  const activeView = useAppStore((s) => s.activeView)
  const sessions = useAppStore((s) => s.sessions)
  const activeSessionId = useAppStore((s) => s.activeSessionId)
  const setActiveSession = useAppStore((s) => s.setActiveSession)
  const closeSession = useAppStore((s) => s.closeSession)
  const savedConnections = useSavedConnectionsStore((s) => s.connections)
  const connectSaved = useConnectSaved()

  const [filter, setFilter] = useState('')
  const query = filter.trim().toLowerCase()
  const matches = (...values: (string | undefined)[]): boolean =>
    query === '' || values.some((v) => v?.toLowerCase().includes(query))

  const filteredSessions = sessions.filter((s) =>
    matches(sessionDisplayName(s, savedConnections), s.context.target, s.context.credentials.username)
  )
  const filteredSaved = savedConnections.filter((c) => matches(c.name, c.host, c.username))

  const sessionRow = (session: ActiveSession) => {
    const proto = session.activeTab ? TAB_PROTOCOL[session.activeTab] : 'other'
    const tabDef = session.tabs.find((tb) => tb.key === session.activeTab)
    return (
      <div
        key={session.id}
        className={session.id === activeSessionId ? 'ritem on' : 'ritem'}
        data-session-id={session.id}
        onClick={() => setActiveSession(session.id)}
      >
        <span className="sig" style={{ background: `var(--ar-proto-${proto})` }} />
        <div className="lbl">
          <div className="n">
            {tabDef && <span className="k">{t(tabDef.titleKey)}</span>}
            <span className="nm">{sessionDisplayName(session, savedConnections)}</span>
          </div>
          <div className="s">
            {session.context.credentials.username}@{session.context.target}
          </div>
        </div>
        <button
          className="x"
          aria-label={t('session.closeSession')}
          onClick={(e) => {
            e.stopPropagation()
            closeSession(session.id)
          }}
        >
          <CloseOutlined />
        </button>
      </div>
    )
  }

  const deviceRow = (c: SavedConnectionSummary) => {
    const live = liveSessionFor(c, sessions)
    const classes = ['ritem']
    if (activeView === 'devices' && c.id === selectedDeviceId) classes.push('on')
    if (activeView === 'sessions' && live) classes.push('live')
    return (
      <div
        key={c.id}
        className={classes.join(' ')}
        onClick={() => {
          if (activeView === 'devices') onSelectDevice(c.id)
          else if (live) setActiveSession(live.id)
          else void connectSaved(c.id)
        }}
        onDoubleClick={() => {
          if (activeView === 'devices') void connectSaved(c.id)
        }}
      >
        <span className="sig" style={{ background: `var(--ar-proto-${deviceProtocol(c.protocols)})` }} />
        <div className="lbl">
          <div className="n">
            <span className="nm">{c.name}</span>
            {live && <span className="connected-badge">{t('session.connected')}</span>}
          </div>
          <div className="s">{c.host}</div>
        </div>
        <span className="meta">{c.protocols.map((p) => p.toUpperCase()).join('·')}</span>
      </div>
    )
  }

  return (
    <div className="rail">
      <div className="rail-head">
        <h2>{activeView === 'sessions' ? t('workbench.sessions') : t('workbench.devices')}</h2>
        <span>
          {activeView === 'sessions'
            ? t('workbench.activeCount', { count: sessions.length })
            : t('workbench.deviceCount', { count: savedConnections.length })}
        </span>
      </div>
      <div className="rail-search">
        <SearchOutlined />
        <input
          value={filter}
          placeholder={t('workbench.filterPlaceholder')}
          onChange={(e) => setFilter(e.target.value)}
        />
      </div>
      <div className="rail-scroll">
        {activeView === 'sessions' && filteredSessions.length > 0 && (
          <>
            <div className="rail-group">{t('rail.activeGroup')}</div>
            {filteredSessions.map(sessionRow)}
          </>
        )}
        {(filteredSaved.length > 0 || query !== '') && (
          <div className="rail-group">{t('workbench.devices')}</div>
        )}
        {filteredSaved.map(deviceRow)}
        {filteredSaved.length === 0 && query !== '' && (
          <div className="rail-empty">{t('saved.noMatch')}</div>
        )}
      </div>
      <div className="rail-foot">
        {t('rail.foot', { devices: savedConnections.length, sessions: sessions.length })}
      </div>
    </div>
  )
}
