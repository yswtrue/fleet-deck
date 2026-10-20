import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Alert, Button, Modal, message } from 'antd'
import { HUB_URL } from '../apiClient.ts'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import styles from './AddNodeModal.module.css'

function CopyIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" focusable="false">
      <rect
        x="8"
        y="8"
        width="12"
        height="12"
        rx="2"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
      />
      <path
        d="M16 5.5A1.5 1.5 0 0 0 14.5 4H5.5A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
    </svg>
  )
}

/** Hub node WebSocket endpoint as reachable from this browser session. */
function nodeHubURL(): string {
  // HUB_URL is normalized to '' for same-origin (the '/' default).
  const base = !HUB_URL || HUB_URL === '/' ? window.location.origin : HUB_URL
  return `${base.replace(/^http/, 'ws')}/ws/node`
}

/**
 * Add-node flow: mint a single-use join token and hand the operator a
 * copy-ready start command. The token is displayed exactly once (only its hash
 * is stored) and the node it registers is bound to the signed-in user.
 */
export function AddNodeModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const api = useHubApi()
  const { t } = useTranslation('pages')
  const [messageApi, messageHolder] = message.useMessage()
  const [token, setToken] = useState<{ token: string; expires_at: string } | null>(null)

  const mint = useMutation({
    mutationFn: () => api.createNodeJoinToken(),
    onSuccess: (data) => setToken(data),
  })

  const close = () => {
    setToken(null)
    mint.reset()
    onClose()
  }

  // Without --config the daemon uses (and creates) the XDG config file, then
  // writes hub_url into it after this first registration.
  const command = token
    ? `./fleetdeck-node --hub-url ${nodeHubURL()} --join-token ${token.token}`
    : ''

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      messageApi.success(t('addNode.copied'))
    } catch {
      messageApi.error(t('addNode.copyError'))
    }
  }

  return (
    <Modal
      open={open}
      onCancel={close}
      title={t('addNode.title')}
      footer={
        token ? (
          <Button onClick={close}>{t('addNode.done')}</Button>
        ) : (
          <Button type="primary" loading={mint.isPending} onClick={() => mint.mutate()}>
            {t('addNode.generate')}
          </Button>
        )
      }
      width={620}
      destroyOnHidden
    >
      {messageHolder}
      <p className={styles.intro}>{t('addNode.intro')}</p>
      {token ? (
        <div className={styles.generated}>
          <Alert
            type="warning"
            showIcon
            message={t('addNode.onceTitle')}
            description={t('addNode.onceBody')}
          />
          <div className={styles.command}>
            <code>{command}</code>
            <Button size="small" icon={<CopyIcon />} onClick={copy}>
              {t('addNode.copy')}
            </Button>
          </div>
          <p className={styles.expiry}>
            {t('addNode.expires', { time: new Date(token.expires_at).toLocaleTimeString() })}
          </p>
          <div className={styles.configHint}>
            <p>{t('addNode.commandHint')}</p>
            <p>{t('addNode.defaultConfigHint')}</p>
            <p>{t('addNode.persistHint')}</p>
          </div>
        </div>
      ) : (
        <ol className={styles.steps}>
          <li>{t('addNode.step1')}</li>
          <li>{t('addNode.step2')}</li>
          <li>{t('addNode.step3')}</li>
        </ol>
      )}
      {mint.error instanceof Error && (
        <Alert
          type="error"
          showIcon
          message={t('addNode.error')}
          description={mint.error.message}
        />
      )}
    </Modal>
  )
}
