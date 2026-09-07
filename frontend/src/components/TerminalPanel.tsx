import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { Button, Spin } from 'antd'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useSessionScope } from '../store/session'
import {
  getTerminalStore,
  toTerminalError,
  useTerminalStore,
  type TerminalError,
  type TerminalStatus
} from '../store/terminal'
import './terminal.css'

const SSH_PORT = 22

/** Pseudo-TTY size used when the panel mounts hidden and cannot be measured. */
const FALLBACK_COLS = 80
const FALLBACK_ROWS = 24

const TERMINAL_OPTIONS = {
  fontSize: 13,
  fontFamily:
    "'JetBrains Mono', 'SF Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
  cursorBlink: true,
  scrollback: 5000,
  theme: {
    background: '#141414',
    foreground: '#c9d1d9',
    cursor: '#7ee787',
    cursorAccent: '#141414',
    selectionBackground: 'rgba(76, 141, 255, 0.35)',
    black: '#484f58',
    red: '#ff7b72',
    green: '#7ee787',
    yellow: '#f2cc60',
    blue: '#79c0ff',
    magenta: '#d2a8ff',
    cyan: '#56d4dd',
    white: '#b1bac4',
    brightBlack: '#6e7681',
    brightRed: '#ffa198',
    brightGreen: '#56d364',
    brightYellow: '#e3b341',
    brightBlue: '#79c0ff',
    brightMagenta: '#d2a8ff',
    brightCyan: '#56d4dd',
    brightWhite: '#f0f6fc'
  }
}

/** Error codes that get a plain-language Chinese overlay instead of the raw message. */
const KNOWN_ERROR_CODES = new Set(['AUTH_FAILED', 'UNREACHABLE', 'TIMEOUT'])

export default function TerminalPanel() {
  const { t } = useTranslation()
  const context = useSessionScope()
  const scopeId = context?.id ?? ''
  const status = useTerminalStore(scopeId, (s) => s.status)
  const error = useTerminalStore(scopeId, (s) => s.error)
  const attempt = useTerminalStore(scopeId, (s) => s.attempt)
  const hostRef = useRef<HTMLDivElement>(null)

  const errorText = (err: TerminalError): string => {
    switch (err.code) {
      case 'AUTH_FAILED':
        return t('terminal.error.authFailed', { defaultValue: '认证失败，请检查用户名或密码' })
      case 'UNREACHABLE':
        return t('terminal.error.unreachable', { defaultValue: '无法连接到目标' })
      case 'TIMEOUT':
        return t('terminal.error.timeout', { defaultValue: '连接超时' })
      default:
        return err.message
    }
  }

  useEffect(() => {
    const store = getTerminalStore(scopeId).getState()
    const host = hostRef.current
    if (!context || !host) {
      store.reset()
      return
    }

    let cancelled = false
    let sessionId: string | null = null
    const cleanups: Array<() => void> = []
    store.markConnecting()

    const term = new Terminal(TERMINAL_OPTIONS)
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)

    // Measure only when visible; a hidden pane (inactive tab) has no layout
    // box, in which case the shell opens with a fallback size and the
    // ResizeObserver below refits once the pane is shown.
    if (host.clientWidth > 0 && host.clientHeight > 0) fit.fit()
    const size = { cols: term.cols || FALLBACK_COLS, rows: term.rows || FALLBACK_ROWS }
    let lastSize = { ...size }

    const ro = new ResizeObserver(() => {
      if (host.clientWidth === 0 || host.clientHeight === 0) return
      fit.fit()
      if (sessionId === null) return
      if (term.cols === lastSize.cols && term.rows === lastSize.rows) return
      lastSize = { cols: term.cols, rows: term.rows }
      window.deskmux.ssh.resize(sessionId, term.cols, term.rows)
    })
    ro.observe(host)

    void (async () => {
      try {
        const id = await window.deskmux.ssh.connect({
          host: context.target,
          port: SSH_PORT,
          username: context.credentials.username,
          password: context.credentials.password,
          privateKey: context.credentials.privateKey,
          passphrase: context.credentials.passphrase
        })
        if (cancelled) {
          void window.deskmux.ssh.close(id).catch(() => undefined)
          return
        }
        sessionId = id
        // Subscribe before opening the shell so no output chunk is lost.
        cleanups.push(
          window.deskmux.ssh.onData(id, (data) => term.write(data)),
          window.deskmux.ssh.onClose(id, () => {
            if (!cancelled) getTerminalStore(scopeId).getState().markClosed()
          })
        )
        await window.deskmux.ssh.openShell(id, size)
        if (cancelled) return
        const input = term.onData((data) => window.deskmux.ssh.write(id, data))
        cleanups.push(() => input.dispose())
        getTerminalStore(scopeId).getState().markConnected()
        term.focus()
      } catch (err) {
        if (!cancelled) getTerminalStore(scopeId).getState().markError(toTerminalError(err))
      }
    })()

    return () => {
      cancelled = true
      ro.disconnect()
      for (const undo of cleanups) undo()
      term.dispose()
      if (sessionId !== null) void window.deskmux.ssh.close(sessionId).catch(() => undefined)
    }
  }, [context, scopeId, attempt])

  const overlay = (statusKey: TerminalStatus, children: React.ReactNode) => (
    <div className="terminal-overlay" data-terminal-status={statusKey}>
      {children}
    </div>
  )

  const retry = () => getTerminalStore(scopeId).getState().retry()

  return (
    <div className="terminal-root">
      <div ref={hostRef} className="terminal-host" />
      {status === 'idle' &&
        overlay(
          'idle',
          <span className="terminal-overlay-text">
            {t('terminal.noSession', { defaultValue: '未建立会话' })}
          </span>
        )}
      {status === 'connecting' &&
        overlay(
          'connecting',
          <>
            <Spin size="large" />
            <span className="terminal-overlay-text">
              {t('terminal.connecting', { defaultValue: '正在连接…' })}
            </span>
          </>
        )}
      {status === 'error' &&
        error &&
        overlay(
          'error',
          <>
            <span className="terminal-overlay-text terminal-overlay-error">
              {errorText(error)}
            </span>
            {error.code !== undefined && KNOWN_ERROR_CODES.has(error.code) && (
              <span className="terminal-overlay-detail">{error.message}</span>
            )}
            <Button type="primary" onClick={retry}>
              {t('terminal.retry', { defaultValue: '重试' })}
            </Button>
          </>
        )}
      {status === 'closed' &&
        overlay(
          'closed',
          <>
            <span className="terminal-overlay-text">
              {t('terminal.closed', { defaultValue: '会话已断开' })}
            </span>
            <Button type="primary" onClick={retry}>
              {t('terminal.reconnect', { defaultValue: '重新连接' })}
            </Button>
          </>
        )}
    </div>
  )
}
