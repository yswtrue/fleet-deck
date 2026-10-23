import { useSyncExternalStore, useState } from 'react'
import { Button, Modal, Typography } from 'antd'
import { installState, promptInstall, subscribeInstall } from '../install.ts'
import { useTranslation } from '../i18n/index.ts'
import styles from './InstallAppButton.module.css'

function InstallIcon() {
  return (
    <svg
      className={styles.icon}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M12 3v11m0 0 4-4m-4 4-4-4" />
      <path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" />
    </svg>
  )
}

/**
 * Header entry for installing the PWA. Chromium fires a deferred prompt, so
 * the button opens the real install dialog; iOS has no such API, so it opens
 * the Share -> Add to Home Screen instructions instead. Renders nothing once
 * installed or when the browser offers no install path.
 */
export function InstallAppButton() {
  const { t } = useTranslation('common')
  const [iosOpen, setIosOpen] = useState(false)
  const state = useSyncExternalStore(subscribeInstall, installState, () => 'installed')

  if (state === 'installed' || state === 'unavailable') return null

  return (
    <>
      <Button
        type="text"
        className={styles.button}
        icon={<InstallIcon />}
        aria-label={t('install.action')}
        onClick={() => {
          if (state === 'ios') setIosOpen(true)
          else void promptInstall()
        }}
      >
        <span className={styles.label}>{t('install.action')}</span>
      </Button>
      <Modal
        open={iosOpen}
        title={t('install.iosTitle')}
        onCancel={() => setIosOpen(false)}
        footer={<Button onClick={() => setIosOpen(false)}>{t('actions.close')}</Button>}
        width={380}
      >
        <Typography.Paragraph>{t('install.iosBody')}</Typography.Paragraph>
        <Typography.Text type="secondary">{t('install.iosHint')}</Typography.Text>
      </Modal>
    </>
  )
}
