/**
 * File manager panel (stage 4b): local filesystem on the left, remote SFTP
 * listing on the right. The panel establishes its own SSH session from the
 * session context on mount (SFTP operations reuse that sessionId) and tears
 * it down on unmount. Transfers run through one serial queue so the
 * filename-less sftp.onProgress events always map to the active queue item.
 */

import {
  ArrowUpOutlined,
  CloseOutlined,
  DeleteOutlined,
  DownloadOutlined,
  FileOutlined,
  FolderAddOutlined,
  FolderOutlined,
  LinkOutlined,
  ReloadOutlined,
  UploadOutlined
} from '@ant-design/icons'
import {
  Breadcrumb,
  Button,
  Input,
  Modal,
  Popconfirm,
  Progress,
  Result,
  Space,
  Spin,
  Table,
  Typography,
  message
} from 'antd'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { FileEntry } from '../../shared/ssh'
import { ipcErrorCode, ipcErrorMessage } from '../../shared/ipc'
import { useSessionScope } from '../store/session'
import { getFilesStore, useFilesStore } from '../store/files'
import './files.css'

const { Text } = Typography

type Phase = 'no-context' | 'connecting' | 'ready' | 'error'

/* ---------- path helpers (POSIX remote; local may be Windows) ---------- */

function sepOf(p: string): string {
  return p.includes('\\') ? '\\' : '/'
}

function joinPath(base: string, name: string): string {
  const sep = sepOf(base)
  return base.endsWith(sep) ? base + name : base + sep + name
}

function parentPath(p: string): string {
  const sep = sepOf(p)
  const trimmed = p.replace(/[\\/]+$/, '')
  const idx = trimmed.lastIndexOf(sep)
  if (idx < 0) return p
  if (idx === 0) return sep // POSIX root
  const head = trimmed.slice(0, idx)
  if (/^[A-Za-z]:$/.test(head)) return head + sep // Windows drive root
  return head
}

interface Crumb {
  label: string
  path: string
}

function crumbsOf(p: string): Crumb[] {
  const sep = sepOf(p)
  const parts = p.replace(/[\\/]+$/, '').split(/[\\/]+/).filter(Boolean)
  if (sep === '\\') {
    // Windows: the first part is the drive letter ("C:").
    const [drive, ...rest] = parts
    if (drive === undefined) return [{ label: p, path: p }]
    const items: Crumb[] = [{ label: drive, path: `${drive}\\` }]
    let cur = drive
    for (const part of rest) {
      cur = `${cur}\\${part}`
      items.push({ label: part, path: cur })
    }
    return items
  }
  const items: Crumb[] = [{ label: '/', path: '/' }]
  let cur = ''
  for (const part of parts) {
    cur = `${cur}/${part}`
    items.push({ label: part, path: cur })
  }
  return items
}

function baseName(p: string): string {
  const parts = p.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? p
}

/* ---------- display helpers ---------- */

function sortEntries(entries: FileEntry[]): FileEntry[] {
  return [...entries].sort((a, b) => {
    if (a.type === 'directory' && b.type !== 'directory') return -1
    if (a.type !== 'directory' && b.type === 'directory') return 1
    return a.name.localeCompare(b.name)
  })
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let u = 0
  while (value >= 1024 && u < units.length - 1) {
    value /= 1024
    u++
  }
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[u]}`
}

function formatTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function errorMessage(err: unknown): string {
  return ipcErrorMessage(err)
}

/**
 * Recovers the SshErrorCode from an IPC failure: the preload embeds it as a
 * `[CODE] message` prefix (see ipcErrorCode); the sshService.mapConnectError
 * message prefixes are kept as a fallback for errors that never got a code.
 */
function codeFromMessage(err: unknown): string | undefined {
  const embedded = ipcErrorCode(err)
  if (embedded !== undefined) return embedded
  const msg = errorMessage(err)
  if (msg.startsWith('Authentication failed')) return 'AUTH_FAILED'
  if (msg.startsWith('Connection timed out')) return 'TIMEOUT'
  if (
    msg.startsWith('Host unreachable') ||
    msg.startsWith('Connection closed') ||
    msg.startsWith('Connection failed') ||
    msg.startsWith('Failed to initiate connection')
  ) {
    return 'UNREACHABLE'
  }
  return undefined
}

function EntryIcon({ entry }: { entry: FileEntry }) {
  if (entry.type === 'directory') return <FolderOutlined style={{ color: '#e3b341' }} />
  if (entry.type === 'symlink') return <LinkOutlined />
  return <FileOutlined />
}

export default function FileManagerPanel() {
  const { t } = useTranslation()
  const context = useSessionScope()
  const scopeId = context?.id ?? ''
  /** This session's files store (per-session registry, keyed by scopeId). */
  const filesStore = getFilesStore(scopeId)

  const localPath = useFilesStore(scopeId, (s) => s.localPath)
  const localEntries = useFilesStore(scopeId, (s) => s.localEntries)
  const localLoading = useFilesStore(scopeId, (s) => s.localLoading)
  const remotePath = useFilesStore(scopeId, (s) => s.remotePath)
  const remoteEntries = useFilesStore(scopeId, (s) => s.remoteEntries)
  const remoteLoading = useFilesStore(scopeId, (s) => s.remoteLoading)
  const selectedRemote = useFilesStore(scopeId, (s) => s.selectedRemote)
  const transfers = useFilesStore(scopeId, (s) => s.transfers)

  const [phase, setPhase] = useState<Phase>(context ? 'connecting' : 'no-context')
  const [errorText, setErrorText] = useState('')
  const [retryCount, setRetryCount] = useState(0)
  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [mkdirName, setMkdirName] = useState('')
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameValue, setRenameValue] = useState('')

  const [messageApi, contextHolder] = message.useMessage()

  const sessionIdRef = useRef<string | null>(null)
  /** Queue item currently receiving progress events (transfers are serial). */
  const activeTransferRef = useRef<number | null>(null)
  /** Serial chain: at most one transfer runs at a time. */
  const chainRef = useRef<Promise<void>>(Promise.resolve())
  /** Guards against stale list responses landing after a newer navigation. */
  const localReqRef = useRef(0)
  const remoteReqRef = useRef(0)

  const connectErrorText = useCallback(
    (err: unknown): string => {
      // Prefer the structured SshErrorCode when it survives IPC. Note: the
      // contextBridge boundary currently re-creates thrown errors as plain
      // Errors (only message/stack survive), so fall back to the message
      // prefixes mapConnectError (src/main/ssh/sshService.ts) produces.
      const code = (err as { code?: unknown } | null | undefined)?.code ?? codeFromMessage(err)
      switch (code) {
        case 'AUTH_FAILED':
          return t('files.error.authFailed', { defaultValue: '认证失败，请检查用户名或密码' })
        case 'UNREACHABLE':
          return t('files.error.unreachable', { defaultValue: '无法连接到目标' })
        case 'TIMEOUT':
          return t('files.error.timeout', { defaultValue: '连接超时' })
        default:
          return errorMessage(err)
      }
    },
    [t]
  )

  const loadLocal = useCallback(
    async (path: string) => {
      const req = ++localReqRef.current
      filesStore.getState().setLocalLoading(true)
      try {
        const entries = await window.deskmux.localFs.list(path)
        if (req === localReqRef.current) {
          filesStore.getState().setLocal(path, sortEntries(entries))
        }
      } catch (err) {
        if (req === localReqRef.current) messageApi.error(errorMessage(err))
      } finally {
        if (req === localReqRef.current) filesStore.getState().setLocalLoading(false)
      }
    },
    [messageApi]
  )

  const loadRemote = useCallback(
    async (path: string) => {
      const sid = sessionIdRef.current
      if (sid === null) return
      const req = ++remoteReqRef.current
      filesStore.getState().setRemoteLoading(true)
      try {
        const entries = await window.deskmux.sftp.list(sid, path)
        if (req === remoteReqRef.current) {
          filesStore.getState().setRemote(path, sortEntries(entries))
        }
      } catch (err) {
        if (req === remoteReqRef.current) messageApi.error(errorMessage(err))
      } finally {
        if (req === remoteReqRef.current) filesStore.getState().setRemoteLoading(false)
      }
    },
    [messageApi]
  )

  // Session lifecycle: connect on mount / context change / retry; close on
  // unmount. StrictMode-safe: a session that resolves after cleanup is closed
  // immediately instead of leaking.
  useEffect(() => {
    if (!context) {
      setPhase('no-context')
      return
    }
    let cancelled = false
    let unsubProgress: (() => void) | null = null
    setPhase('connecting')
    filesStore.getState().reset()

    const { username, password, privateKey, passphrase } = context.credentials
    window.deskmux.ssh
      .connect({ host: context.target, port: 22, username, password, privateKey, passphrase })
      .then(async (sid) => {
        if (cancelled) {
          void window.deskmux.ssh.close(sid)
          return
        }
        sessionIdRef.current = sid
        unsubProgress = window.deskmux.sftp.onProgress(sid, (progress) => {
          const id = activeTransferRef.current
          if (id !== null) filesStore.getState().setTransferProgress(id, progress.percent)
        })
        const [localHome, remoteHome] = await Promise.all([
          window.deskmux.localFs.homeDir(),
          window.deskmux.sftp.homeDir(sid)
        ])
        if (cancelled) return
        setPhase('ready')
        await Promise.all([loadLocal(localHome), loadRemote(remoteHome)])
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setErrorText(connectErrorText(err))
        setPhase('error')
      })

    return () => {
      cancelled = true
      unsubProgress?.()
      const sid = sessionIdRef.current
      sessionIdRef.current = null
      if (sid !== null) void window.deskmux.ssh.close(sid)
      filesStore.getState().reset()
    }
  }, [context, retryCount, connectErrorText, loadLocal, loadRemote])

  /** Enqueues one transfer job; jobs run strictly one at a time. */
  const enqueueTransfer = useCallback((job: () => Promise<void>) => {
    chainRef.current = chainRef.current.then(job).catch(() => undefined)
  }, [])

  /** Wraps one transfer with queue bookkeeping + error toast. */
  const runTransfer = useCallback(
    (fileName: string, direction: 'upload' | 'download', op: () => Promise<void>) =>
      async () => {
        const id = filesStore.getState().addTransfer(fileName, direction)
        activeTransferRef.current = id
        try {
          await op()
          filesStore.getState().finishTransfer(id, 'done')
        } catch (err) {
          filesStore.getState().finishTransfer(id, 'error')
          messageApi.error(errorMessage(err))
        } finally {
          activeTransferRef.current = null
        }
      },
    [messageApi]
  )

  const refreshRemote = useCallback(() => {
    const dir = filesStore.getState().remotePath
    if (dir !== null) void loadRemote(dir)
  }, [loadRemote])

  /* ---------- toolbar actions (operate on the remote current dir) ---------- */

  const doUpload = useCallback(async () => {
    const sid = sessionIdRef.current
    const dir = filesStore.getState().remotePath
    if (sid === null || dir === null) return
    const paths = await window.deskmux.dialog.pickFiles()
    if (paths.length === 0) return
    for (const localPath of paths) {
      const name = baseName(localPath)
      const remote = joinPath(dir, name)
      enqueueTransfer(
        runTransfer(name, 'upload', () => window.deskmux.sftp.upload(sid, localPath, remote))
      )
    }
    // Refresh the listing once the batch has drained.
    enqueueTransfer(async () => {
      const cur = filesStore.getState().remotePath
      if (cur !== null) await loadRemote(cur)
    })
  }, [enqueueTransfer, runTransfer, loadRemote])

  const doDownload = useCallback(async () => {
    const sid = sessionIdRef.current
    const { remotePath: dir, selectedRemote: sel, remoteEntries: entries } =
      filesStore.getState()
    if (sid === null || dir === null || sel.length !== 1) return
    const entry = entries.find((e) => e.name === sel[0])
    if (!entry || entry.type === 'directory') return
    const localPath = await window.deskmux.dialog.pickSavePath(entry.name)
    if (localPath === null) return
    const remote = joinPath(dir, entry.name)
    enqueueTransfer(
      runTransfer(entry.name, 'download', () =>
        window.deskmux.sftp.download(sid, remote, localPath)
      )
    )
  }, [enqueueTransfer, runTransfer])

  const doMkdir = useCallback(async () => {
    const name = mkdirName.trim()
    const sid = sessionIdRef.current
    const dir = filesStore.getState().remotePath
    if (sid === null || dir === null || name === '') return
    try {
      await window.deskmux.sftp.mkdir(sid, joinPath(dir, name))
      setMkdirOpen(false)
      setMkdirName('')
      await loadRemote(dir)
    } catch (err) {
      messageApi.error(errorMessage(err))
    }
  }, [mkdirName, loadRemote, messageApi])

  const doRename = useCallback(async () => {
    const name = renameValue.trim()
    const sid = sessionIdRef.current
    const { remotePath: dir, selectedRemote: sel } = filesStore.getState()
    if (sid === null || dir === null || sel.length !== 1 || name === '') return
    try {
      await window.deskmux.sftp.rename(sid, joinPath(dir, sel[0]), joinPath(dir, name))
      setRenameOpen(false)
      await loadRemote(dir)
    } catch (err) {
      messageApi.error(errorMessage(err))
    }
  }, [renameValue, loadRemote, messageApi])

  const doDelete = useCallback(async () => {
    const sid = sessionIdRef.current
    const { remotePath: dir, selectedRemote: sel, remoteEntries: entries } =
      filesStore.getState()
    if (sid === null || dir === null || sel.length === 0) return
    for (const entry of entries.filter((e) => sel.includes(e.name))) {
      const p = joinPath(dir, entry.name)
      try {
        // rmdir on a non-empty directory fails; the server message is toasted
        // verbatim so the user sees the real reason.
        if (entry.type === 'directory') await window.deskmux.sftp.deleteDir(sid, p)
        else await window.deskmux.sftp.deleteFile(sid, p)
      } catch (err) {
        messageApi.error(errorMessage(err))
      }
    }
    await loadRemote(dir)
  }, [loadRemote, messageApi])

  /* ---------- table columns ---------- */

  const columns = [
    {
      title: t('files.name', { defaultValue: '名称' }),
      dataIndex: 'name',
      key: 'name',
      ellipsis: true,
      render: (_: string, entry: FileEntry) => (
        <Space size={6}>
          <EntryIcon entry={entry} />
          <span className="mono">{entry.name}</span>
        </Space>
      )
    },
    {
      title: t('files.size', { defaultValue: '大小' }),
      dataIndex: 'size',
      key: 'size',
      width: 100,
      render: (_: number, entry: FileEntry) =>
        entry.type === 'directory' ? '—' : formatSize(entry.size)
    },
    {
      title: t('files.modified', { defaultValue: '修改时间' }),
      dataIndex: 'mtimeMs',
      key: 'mtimeMs',
      width: 140,
      render: (ms: number) => formatTime(ms)
    }
  ]

  /* ---------- nav bars ---------- */

  const renderNav = (
    path: string | null,
    navigate: (p: string) => void,
    refresh: () => void
  ) => (
    <div className="files-nav">
      <Button
        size="small"
        icon={<ArrowUpOutlined />}
        disabled={path === null}
        title={t('files.up', { defaultValue: '上级' })}
        onClick={() => {
          if (path !== null) navigate(parentPath(path))
        }}
      />
      <Breadcrumb
        items={
          path === null
            ? []
            : crumbsOf(path).map((crumb) => ({
                key: crumb.path,
                title: (
                  <Typography.Link onClick={() => navigate(crumb.path)}>
                    {crumb.label}
                  </Typography.Link>
                )
              }))
        }
      />
      <Button
        size="small"
        icon={<ReloadOutlined />}
        disabled={path === null}
        title={t('files.refresh', { defaultValue: '刷新' })}
        onClick={refresh}
      />
    </div>
  )

  /* ---------- placeholders ---------- */

  if (phase === 'no-context') {
    return (
      <div className="files-panel files-placeholder">
        <Text type="secondary">
          {t('files.noContext', { defaultValue: '无会话上下文，请先从扫描页建立连接' })}
        </Text>
      </div>
    )
  }

  if (phase === 'connecting') {
    return (
      <div className="files-panel files-placeholder">
        <Space>
          <Spin size="small" />
          <Text type="secondary">{t('files.connecting', { defaultValue: '正在连接…' })}</Text>
        </Space>
      </div>
    )
  }

  if (phase === 'error') {
    return (
      <div className="files-panel files-placeholder files-error">
        <Result
          status="error"
          title={errorText}
          extra={
            <Button type="primary" onClick={() => setRetryCount((n) => n + 1)}>
              {t('files.retry', { defaultValue: '重试' })}
            </Button>
          }
        />
      </div>
    )
  }

  /* ---------- ready ---------- */

  const selectedEntries = remoteEntries.filter((e) => selectedRemote.includes(e.name))
  const canDownload = selectedEntries.length === 1 && selectedEntries[0].type !== 'directory'

  return (
    <div className="files-panel">
      {contextHolder}
      <Space style={{ marginBottom: 8 }} wrap>
        <Button
          size="small"
          icon={<UploadOutlined />}
          disabled={remotePath === null}
          onClick={() => void doUpload()}
        >
          {t('files.upload', { defaultValue: '上传' })}
        </Button>
        <Button
          size="small"
          icon={<DownloadOutlined />}
          disabled={!canDownload}
          onClick={() => void doDownload()}
        >
          {t('files.download', { defaultValue: '下载' })}
        </Button>
        <Button
          size="small"
          icon={<FolderAddOutlined />}
          disabled={remotePath === null}
          onClick={() => setMkdirOpen(true)}
        >
          {t('files.newFolder', { defaultValue: '新建文件夹' })}
        </Button>
        <Button
          size="small"
          disabled={selectedRemote.length !== 1}
          onClick={() => {
            setRenameValue(selectedRemote[0] ?? '')
            setRenameOpen(true)
          }}
        >
          {t('files.rename', { defaultValue: '重命名' })}
        </Button>
        <Popconfirm
          title={t('files.deleteConfirm', {
            defaultValue: '确认删除选中的 {{count}} 项？',
            count: selectedRemote.length
          })}
          okText={t('files.delete', { defaultValue: '删除' })}
          cancelText={t('files.cancel', { defaultValue: '取消' })}
          okButtonProps={{ danger: true }}
          onConfirm={() => void doDelete()}
          disabled={selectedRemote.length === 0}
        >
          <Button
            size="small"
            danger
            icon={<DeleteOutlined />}
            disabled={selectedRemote.length === 0}
          >
            {t('files.delete', { defaultValue: '删除' })}
          </Button>
        </Popconfirm>
      </Space>

      <div className="files-columns">
        <div className="files-column">
          <Text type="secondary" style={{ fontSize: 12 }}>
            {t('files.local', { defaultValue: '本地' })}
          </Text>
          {renderNav(localPath, (p) => void loadLocal(p), () => {
            if (localPath !== null) void loadLocal(localPath)
          })}
          <div className="files-table-wrap">
            <Table<FileEntry>
              size="small"
              columns={columns}
              dataSource={localEntries}
              rowKey="name"
              pagination={false}
              loading={localLoading}
              locale={{ emptyText: t('files.empty', { defaultValue: '空目录' }) }}
              onRow={(entry) => ({
                onDoubleClick: () => {
                  if (entry.type === 'directory' && localPath !== null) {
                    void loadLocal(joinPath(localPath, entry.name))
                  }
                }
              })}
            />
          </div>
        </div>

        <div className="files-column">
          <Text type="secondary" style={{ fontSize: 12 }}>
            {t('files.remote', { defaultValue: '远程' })}
          </Text>
          {renderNav(remotePath, (p) => void loadRemote(p), refreshRemote)}
          <div className="files-table-wrap">
            <Table<FileEntry>
              size="small"
              columns={columns}
              dataSource={remoteEntries}
              rowKey="name"
              pagination={false}
              loading={remoteLoading}
              locale={{ emptyText: t('files.empty', { defaultValue: '空目录' }) }}
              rowSelection={{
                selectedRowKeys: selectedRemote,
                onChange: (keys) => filesStore.getState().setSelectedRemote(keys.map(String))
              }}
              onRow={(entry) => ({
                onDoubleClick: () => {
                  if (entry.type === 'directory' && remotePath !== null) {
                    void loadRemote(joinPath(remotePath, entry.name))
                  }
                }
              })}
            />
          </div>
        </div>
      </div>

      {transfers.length > 0 && (
        <div className="transfer-queue">
          {transfers.map((tr) => (
            <div key={tr.id} className={`transfer-item transfer-${tr.status}`}>
              {tr.direction === 'upload' ? <UploadOutlined /> : <DownloadOutlined />}
              <span className="mono transfer-name" title={tr.fileName}>
                {tr.fileName}
              </span>
              <Progress
                percent={Math.round(tr.percent)}
                size="small"
                status={
                  tr.status === 'error' ? 'exception' : tr.status === 'done' ? 'success' : 'active'
                }
              />
              {tr.status !== 'active' && (
                <Button
                  type="text"
                  size="small"
                  icon={<CloseOutlined />}
                  onClick={() => filesStore.getState().removeTransfer(tr.id)}
                />
              )}
            </div>
          ))}
        </div>
      )}

      <Modal
        open={mkdirOpen}
        title={t('files.newFolder', { defaultValue: '新建文件夹' })}
        okText={t('files.ok', { defaultValue: '确定' })}
        cancelText={t('files.cancel', { defaultValue: '取消' })}
        onOk={() => void doMkdir()}
        onCancel={() => setMkdirOpen(false)}
        destroyOnHidden
      >
        <Input
          autoFocus
          value={mkdirName}
          placeholder={t('files.folderName', { defaultValue: '文件夹名称' })}
          onChange={(e) => setMkdirName(e.target.value)}
          onPressEnter={() => void doMkdir()}
        />
      </Modal>

      <Modal
        open={renameOpen}
        title={t('files.rename', { defaultValue: '重命名' })}
        okText={t('files.ok', { defaultValue: '确定' })}
        cancelText={t('files.cancel', { defaultValue: '取消' })}
        onOk={() => void doRename()}
        onCancel={() => setRenameOpen(false)}
        destroyOnHidden
      >
        <Input
          autoFocus
          value={renameValue}
          placeholder={t('files.newName', { defaultValue: '新名称' })}
          onChange={(e) => setRenameValue(e.target.value)}
          onPressEnter={() => void doRename()}
        />
      </Modal>
    </div>
  )
}
