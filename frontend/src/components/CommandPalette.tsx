import { PlusOutlined, SearchOutlined } from '@ant-design/icons'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useConnectSaved } from '../hooks/useConnectSaved'
import { useAppStore } from '../store'
import { useSavedConnectionsStore } from '../store/savedConnections'
import { deviceProtocol, liveSessionFor, sessionDisplayName, TAB_PROTOCOL } from '../utils/session'

interface PaletteItem {
  key: string
  icon: ReactNode
  title: ReactNode
  sub?: string
  hint?: string
  run: () => void
}

/**
 * ⌘K command palette: one input over a mixed result list — active sessions
 * (switch, with their ⌘N shortcut as the hint), saved devices matching the
 * query by name/host/username (connect; already-live ones switch), a
 * scan-and-connect entry for a non-empty query with no exact saved match, and
 * the new-connection action. ↑↓ cycles, ↵ executes, Esc / overlay click
 * closes, capped at 8 rows.
 */
export default function CommandPalette() {
  const { t } = useTranslation()
  const open = useAppStore((s) => s.paletteOpen)
  const setPaletteOpen = useAppStore((s) => s.setPaletteOpen)
  const sessions = useAppStore((s) => s.sessions)
  const setActiveSession = useAppStore((s) => s.setActiveSession)
  const setActiveView = useAppStore((s) => s.setActiveView)
  const setTargetAddress = useAppStore((s) => s.setTargetAddress)
  const openNewConnection = useAppStore((s) => s.openNewConnection)
  const setNewConnectionOpen = useAppStore((s) => s.setNewConnectionOpen)
  const savedConnections = useSavedConnectionsStore((s) => s.connections)
  const connectSaved = useConnectSaved()

  const [query, setQuery] = useState('')
  const [index, setIndex] = useState(0)

  // Fresh input and selection every time the palette opens.
  useEffect(() => {
    if (open) {
      setQuery('')
      setIndex(0)
    }
  }, [open])

  if (!open) return null

  const q = query.trim().toLowerCase()
  const items: PaletteItem[] = []

  if (q !== '') {
    const exact = savedConnections.some(
      (c) => c.host.trim().toLowerCase() === q || c.name.trim().toLowerCase() === q
    )
    if (!exact) {
      items.push({
        key: 'scan',
        icon: <SearchOutlined className="icon" />,
        title: t('palette.scanAndConnect', { target: query.trim() }),
        run: () => {
          setTargetAddress(query.trim())
          openNewConnection({ scan: true })
        }
      })
    }
  }

  sessions.forEach((session, i) => {
    const name = sessionDisplayName(session, savedConnections)
    if (
      q !== '' &&
      ![name, session.context.target, session.context.credentials.username].some((v) =>
        v.toLowerCase().includes(q)
      )
    ) {
      return
    }
    const proto = session.activeTab ? TAB_PROTOCOL[session.activeTab] : 'other'
    const tabDef = session.tabs.find((tb) => tb.key === session.activeTab)
    items.push({
      key: session.id,
      icon: <span className="dot" style={{ background: `var(--ar-proto-${proto})` }} />,
      title: (
        <>
          {t('palette.switchTo')}
          {tabDef && <span className="k"> {t(tabDef.titleKey)}</span>} {name}
        </>
      ),
      sub: `${session.context.credentials.username}@${session.context.target}`,
      hint: i < 9 ? `⌘${i + 1}` : undefined,
      run: () => {
        setActiveView('sessions')
        setActiveSession(session.id)
      }
    })
  })

  savedConnections.forEach((c) => {
    if (q !== '' && ![c.name, c.host, c.username].some((v) => v.toLowerCase().includes(q))) return
    const live = liveSessionFor(c, sessions)
    items.push({
      key: c.id,
      icon: (
        <span
          className="dot"
          style={{ background: `var(--ar-proto-${deviceProtocol(c.protocols)})` }}
        />
      ),
      title: c.name,
      sub: `${c.username}@${c.host}`,
      hint: c.protocols.map((p) => p.toUpperCase()).join('·') || undefined,
      run: () => {
        if (live) {
          setActiveView('sessions')
          setActiveSession(live.id)
        } else {
          void connectSaved(c.id)
        }
      }
    })
  })

  items.push({
    key: 'new-connection',
    icon: <PlusOutlined className="icon" />,
    title: t('palette.newConnection'),
    run: () => setNewConnectionOpen(true)
  })

  const visible = items.slice(0, 8)
  const active = Math.min(index, visible.length - 1)
  const execute = (item: PaletteItem): void => {
    setPaletteOpen(false)
    item.run()
  }

  return (
    <div className="palette-overlay" onMouseDown={() => setPaletteOpen(false)}>
      <div className="palette" onMouseDown={(e) => e.stopPropagation()}>
        <div className="palette-input">
          <input
            id="command-palette-input"
            autoFocus
            value={query}
            placeholder={t('palette.placeholder')}
            onChange={(e) => {
              setQuery(e.target.value)
              setIndex(0)
            }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') {
                e.preventDefault()
                if (visible.length > 0) setIndex((active + 1) % visible.length)
              } else if (e.key === 'ArrowUp') {
                e.preventDefault()
                if (visible.length > 0) setIndex((active - 1 + visible.length) % visible.length)
              } else if (e.key === 'Enter') {
                e.preventDefault()
                const item = visible[active]
                if (item) execute(item)
              } else if (e.key === 'Escape') {
                e.preventDefault()
                setPaletteOpen(false)
              }
            }}
          />
        </div>
        <div className="palette-list">
          {visible.map((item, i) => (
            <div
              key={item.key}
              className={i === active ? 'pitem on' : 'pitem'}
              onMouseEnter={() => setIndex(i)}
              onClick={() => execute(item)}
            >
              {item.icon}
              <div className="lbl">
                <div className="t">{item.title}</div>
                {item.sub && <div className="s">{item.sub}</div>}
              </div>
              {item.hint && <span className="hint">{item.hint}</span>}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
