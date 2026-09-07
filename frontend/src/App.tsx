import { App as AntdApp, ConfigProvider, theme } from 'antd'
import { useEffect } from 'react'
import SessionPage from './pages/SessionPage'
import { useAppStore } from './store'

export default function App() {
  // Keyboard-first shortcuts (see docs/ARCHITECTURE.md §8).
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey)) return
      const key = e.key.toLowerCase()
      if (key === 'k') {
        e.preventDefault()
        // ⌘K toggles the command palette.
        const { paletteOpen, setPaletteOpen } = useAppStore.getState()
        setPaletteOpen(!paletteOpen)
      } else if (key === 'w') {
        e.preventDefault()
        // Closes the active session's current protocol tab; closing its last
        // tab closes the session (multi-session model, F6).
        const { sessions, activeSessionId, closeTab } = useAppStore.getState()
        const active = sessions.find((s) => s.id === activeSessionId)
        if (active?.activeTab) closeTab(active.activeTab)
      } else if (key >= '1' && key <= '9') {
        e.preventDefault()
        // ⌘1..⌘9 switch to the Nth session.
        const { sessions, setActiveSession, setActiveView } = useAppStore.getState()
        const target = sessions[Number(key) - 1]
        if (target) {
          setActiveView('sessions')
          setActiveSession(target.id)
        }
      }
    }
    // Capture phase: xterm swallows Ctrl+K (kill-line) and friends on its
    // helper textarea, so bubble-phase listeners never see them.
    window.addEventListener('keydown', onKeyDown, true)
    return () => window.removeEventListener('keydown', onKeyDown, true)
  }, [])

  return (
    <ConfigProvider
      theme={{
        algorithm: [theme.darkAlgorithm, theme.compactAlgorithm],
        // Direction-A token layer (docs/design/direction-a-tokens.md):
        // layered dark greys, hairline borders, one restrained brand blue.
        token: {
          colorPrimary: '#5B9BFF',
          colorInfo: '#5B9BFF',
          colorSuccess: '#4CC38A',
          colorError: '#E56363',
          colorBgLayout: '#0E1116',
          colorBgContainer: '#151A21',
          colorBgElevated: '#1C222B',
          colorBorder: 'rgba(255,255,255,0.08)',
          colorBorderSecondary: 'rgba(255,255,255,0.05)',
          colorText: '#E2E8F0',
          colorTextSecondary: '#97A3B4',
          colorTextTertiary: '#5E6B7D',
          borderRadius: 6,
          fontSize: 13,
          fontFamily:
            '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
          fontFamilyCode:
            'ui-monospace, "JetBrains Mono", "SF Mono", SFMono-Regular, Menlo, Consolas, monospace'
        }
      }}
    >
      <AntdApp>
        <SessionPage />
      </AntdApp>
    </ConfigProvider>
  )
}
