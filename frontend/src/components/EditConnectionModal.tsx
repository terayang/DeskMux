import { Button, Checkbox, Form, Input, Modal, Spin, Typography, message } from 'antd'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ipcErrorMessage,
  type SavedConnectionSecret,
  type SavedConnectionSummary,
  type SavedSecretKind
} from '../../shared/ipc'
import type { ProtocolId } from '../../shared/protocols'
import type { TargetScanReport } from '../../shared/scan'
import { useSavedConnectionsStore } from '../store/savedConnections'

const { Text } = Typography

interface EditConnectionModalProps {
  /** The entry being edited (summary only); null = closed. */
  conn: SavedConnectionSummary | null
  onClose: () => void
}

interface EditConnectionFormValues {
  name: string
  host: string
  username: string
  /** Session-capable protocols (ssh/vnc); other stored protocols are preserved invisibly. */
  protocols: ProtocolId[]
  /** New password, or a new private-key path when the stored secret is one. */
  secretValue?: string
}

/** Protocols a saved device can open a session for; the rest of a stored
 * protocol list (rdp/telnet/…, scan-only) passes through untouched. */
const SESSION_PROTOCOL_IDS: readonly ProtocolId[] = ['ssh', 'vnc']

/**
 * Edit dialog for a saved connection (metadata + session protocols + optional
 * secret rotation). The protocols checkboxes cover the session-capable ids
 * (ssh/vnc); any scan-only protocols already stored are preserved invisibly.
 * The input/button ids (`edit-conn-*`) are a cross-agent contract relied on
 * by smoke tests — do not rename.
 *
 * Secret semantics (mirroring the Go store, internal/store): submitting with
 * the secret field left empty OMITS the secret, preserving the keychain
 * entry; the "clear stored credential" checkbox submits an explicitly empty
 * secret, which clears it; any non-empty value replaces it. A stored
 * private-key path is edited as a plain text path (never echoed back — the
 * current path is shown as a hint) instead of a password box.
 */
export default function EditConnectionModal({ conn, onClose }: EditConnectionModalProps) {
  const { t } = useTranslation()
  const [form] = Form.useForm<EditConnectionFormValues>()
  const save = useSavedConnectionsStore((s) => s.save)
  const [messageApi, contextHolder] = message.useMessage()

  // Kind/path of the stored secret, resolved on open so the dialog can adapt
  // (password box vs. private-key path input, current-path hint).
  const [secretKind, setSecretKind] = useState<SavedSecretKind | null>(null)
  const [currentKeyPath, setCurrentKeyPath] = useState('')
  const [clearSecret, setClearSecret] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  // Background protocol probe of the entry's host (edit-time convenience):
  // a session protocol found open but not yet stored gets an inline "add"
  // hint under the checkboxes. Runs once per open, off the saved host — a
  // host edited in the form does not retrigger it.
  const [scanState, setScanState] = useState<'scanning' | 'done' | 'failed'>('scanning')
  const [scanReport, setScanReport] = useState<TargetScanReport | null>(null)
  const checkedProtocols: ProtocolId[] = Form.useWatch('protocols', form) ?? []

  useEffect(() => {
    if (conn === null) return
    setSecretKind(null)
    setCurrentKeyPath('')
    setClearSecret(false)
    setScanState('scanning')
    setScanReport(null)
    let cancelled = false
    void window.deskmux.scan(conn.host)
      .then((report) => {
        if (cancelled) return
        setScanReport(report)
        setScanState('done')
      })
      .catch(() => {
        if (!cancelled) setScanState('failed')
      })
    void window.deskmux.connections
      .get(conn.id)
      .then((full) => {
        if (cancelled || full?.secret === undefined) return
        setSecretKind(full.secret.kind)
        if (full.secret.kind === 'privateKeyPath') setCurrentKeyPath(full.secret.data)
      })
      .catch(() => {
        // Unreadable secret (keychain locked, etc.): the dialog still edits
        // metadata; the secret controls just fall back to the password shape.
      })
    return () => {
      cancelled = true
    }
  }, [conn, form])

  const handleOk = async (): Promise<void> => {
    if (conn === null) return
    // A validation failure rejects with a plain errorFields object (not an
    // Error); antd already shows the inline messages, so just stay open.
    let values: EditConnectionFormValues
    try {
      values = await form.validateFields()
    } catch {
      return
    }
    let secret: SavedConnectionSecret | undefined
    if (clearSecret) {
      // Explicitly empty data = clear the stored credential (store contract).
      secret = { kind: secretKind ?? 'password', data: '' }
    } else {
      const value = values.secretValue?.trim() ?? ''
      if (value !== '') {
        secret = { kind: secretKind ?? 'password', data: value }
      }
      // Otherwise secret stays undefined: preserve the stored credential.
    }
    setSubmitting(true)
    try {
      // Checked session protocols + any scan-only protocols already stored
      // (they are not editable here but must not be dropped on save).
      const preserved = conn.protocols.filter(
        (p) => !(SESSION_PROTOCOL_IDS as readonly string[]).includes(p)
      )
      await save({
        id: conn.id,
        name: values.name.trim(),
        host: values.host.trim(),
        username: values.username.trim(),
        protocols: [...values.protocols, ...preserved],
        ...(secret !== undefined ? { secret } : {})
      })
      onClose()
    } catch (err) {
      void messageApi.error(`${t('saved.saveFailed')}: ${ipcErrorMessage(err)}`)
    } finally {
      setSubmitting(false)
    }
  }

  const isKeyPath = secretKind === 'privateKeyPath'

  return (
    <Modal
      title={t('saved.editTitle')}
      open={conn !== null}
      width={420}
      okText={t('saved.save')}
      cancelText={t('saved.cancel')}
      okButtonProps={{ id: 'edit-conn-submit', loading: submitting }}
      onOk={() => {
        void handleOk()
      }}
      onCancel={onClose}
      destroyOnHidden
    >
      {contextHolder}
      {/* initialValues (not setFieldsValue) populates the form: with
        * destroyOnHidden the Form mounts with the modal, after conn is set. */}
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={
          conn === null
            ? undefined
            : {
                name: conn.name,
                host: conn.host,
                username: conn.username,
                protocols: SESSION_PROTOCOL_IDS.filter((p) => conn.protocols.includes(p))
              }
        }
      >
        <Form.Item
          name="name"
          label={t('saved.name')}
          rules={[{ required: true, whitespace: true, message: t('saved.nameRequired') }]}
        >
          <Input id="edit-conn-name" autoFocus />
        </Form.Item>
        <Form.Item
          name="host"
          label={t('saved.host')}
          rules={[{ required: true, whitespace: true, message: t('saved.hostRequired') }]}
        >
          <Input id="edit-conn-host" className="mono" />
        </Form.Item>
        <Form.Item
          name="username"
          label={t('credentials.username')}
          rules={[
            { required: true, whitespace: true, message: t('credentials.usernameRequired') }
          ]}
        >
          <Input id="edit-conn-username" autoComplete="username" />
        </Form.Item>
        <div id="edit-conn-protocols">
          <Form.Item
            name="protocols"
            label={t('saved.protocols')}
            rules={[
              {
                validator: (_, value: ProtocolId[] | undefined) =>
                  value !== undefined && value.length > 0
                    ? Promise.resolve()
                    : Promise.reject(new Error(t('saved.protocolsRequired')))
              }
            ]}
          >
            <Checkbox.Group
              options={SESSION_PROTOCOL_IDS.map((p) => ({ label: p.toUpperCase(), value: p }))}
            />
          </Form.Item>
          <div className="edit-conn-proto-hints">
            {scanState === 'scanning' && (
              <Text type="secondary" style={{ fontSize: 12 }}>
                <Spin size="small" style={{ marginRight: 8 }} />
                {t('saved.protoScanning')}
              </Text>
            )}
            {scanState === 'failed' && (
              <Text type="secondary" style={{ fontSize: 12 }}>
                {t('saved.protoScanFailed')}
              </Text>
            )}
            {scanState === 'done' &&
              scanReport !== null &&
              SESSION_PROTOCOL_IDS.map((p) => {
                const hit = scanReport.results.find(
                  (r) => r.protocolId === p && r.status === 'open'
                )
                const checked = checkedProtocols.includes(p)
                return (
                  <div key={p} className="edit-conn-proto-hint">
                    <span
                      className="proto-hint-dot"
                      style={hit === undefined ? { background: 'var(--ar-text-tertiary)', boxShadow: 'none' } : undefined}
                    />
                    <Text type={hit === undefined ? 'secondary' : undefined} style={{ fontSize: 12 }}>
                      {hit !== undefined
                        ? t('saved.protoOpen', { protocol: p.toUpperCase(), port: hit.port })
                        : t('saved.protoClosed', { protocol: p.toUpperCase() })}
                    </Text>
                    {hit !== undefined && !checked && (
                      <Button
                        type="link"
                        size="small"
                        style={{ fontSize: 12, height: 'auto', padding: 0 }}
                        onClick={() =>
                          form.setFieldValue('protocols', [...checkedProtocols, p])
                        }
                      >
                        {t('saved.protoAdd')}
                      </Button>
                    )}
                  </div>
                )
              })}
          </div>
        </div>
        <Form.Item
          name="secretValue"
          label={isKeyPath ? t('credentials.privateKey') : t('credentials.password')}
        >
          {isKeyPath ? (
            <Input
              id="edit-conn-password"
              className="mono"
              disabled={clearSecret}
              placeholder={t('saved.keyKeep')}
            />
          ) : (
            <Input.Password
              id="edit-conn-password"
              autoComplete="new-password"
              disabled={clearSecret}
              placeholder={secretKind === 'password' ? t('saved.passwordKeep') : undefined}
            />
          )}
        </Form.Item>
        {isKeyPath && currentKeyPath !== '' && (
          <Text className="mono" type="secondary" style={{ fontSize: 12, display: 'block', marginTop: -12 }}>
            {t('saved.currentKeyHint', { path: currentKeyPath })}
          </Text>
        )}
        {secretKind !== null && (
          <Form.Item style={{ marginBottom: 0 }}>
            <Checkbox
              id="edit-conn-clear-secret"
              checked={clearSecret}
              onChange={(e) => setClearSecret(e.target.checked)}
            >
              {t('saved.clearSecret')}
            </Checkbox>
          </Form.Item>
        )}
      </Form>
    </Modal>
  )
}
