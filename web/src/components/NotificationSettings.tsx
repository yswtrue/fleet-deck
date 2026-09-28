import { useState } from 'react'
import { Alert, Button, Modal, Space, Switch, Typography } from 'antd'
import {
  notificationsSupported,
  isNotifyEnabled,
  setNotifyEnabled,
} from '../hooks/useTaskNotifications.ts'
import { useTranslation } from '../i18n/index.ts'
import styles from './NotificationSettings.module.css'

interface NotificationSettingsPanelProps {
  open: boolean
  onClose: () => void
}

function readPermission(): NotificationPermission | 'unsupported' {
  if (!notificationsSupported()) return 'unsupported'
  return Notification.permission
}

/**
 * Controlled panel for notification settings. Shows the current permission
 * state, requests permission when needed, and persists the on/off toggle
 * via localStorage.
 */
export function NotificationSettingsPanel({ open, onClose }: NotificationSettingsPanelProps) {
  const { t } = useTranslation('notify')
  const [permission, setPermission] = useState<NotificationPermission | 'unsupported'>(
    readPermission
  )
  const [enabled, setEnabled] = useState(isNotifyEnabled)

  const handleRequest = async () => {
    if (!notificationsSupported()) return
    const result = await Notification.requestPermission()
    setPermission(result)
    if (result === 'granted') {
      setNotifyEnabled(true)
      setEnabled(true)
    }
  }

  const handleToggle = (checked: boolean) => {
    setNotifyEnabled(checked)
    setEnabled(checked)
  }

  const permissionLabel: Record<NotificationPermission | 'unsupported', string> = {
    granted: t('settings.permissionGranted'),
    denied: t('settings.permissionDenied'),
    default: t('settings.permissionDefault'),
    unsupported: t('settings.permissionUnsupported'),
  }

  const switchDisabled = permission !== 'granted'

  return (
    <Modal
      open={open}
      title={t('settings.title')}
      onCancel={onClose}
      footer={[
        <Button key="close" onClick={onClose}>
          {t('common:actions.close')}
        </Button>,
      ]}
      width={400}
    >
      <Space direction="vertical" className={styles.panel} size="middle">
        <div className={styles.row}>
          <Typography.Text type="secondary">{t('settings.permission')}</Typography.Text>
          <Typography.Text>{permissionLabel[permission]}</Typography.Text>
        </div>

        {permission === 'unsupported' && (
          <Alert message={t('settings.unsupported')} type="warning" showIcon />
        )}
        {permission === 'denied' && (
          <Alert message={t('settings.deniedHelp')} type="info" showIcon />
        )}
        {permission === 'default' && (
          <Alert message={t('settings.permissionRequired')} type="info" showIcon />
        )}

        {permission === 'default' && (
          <Button type="primary" block onClick={() => void handleRequest()}>
            {t('settings.enable')}
          </Button>
        )}

        {permission !== 'unsupported' && (
          <label className={styles.row}>
            <span className={styles.switchLabel}>{t('settings.enabledLabel')}</span>
            <Switch checked={enabled} disabled={switchDisabled} onChange={handleToggle} />
          </label>
        )}
      </Space>
    </Modal>
  )
}

/**
 * Self-contained trigger + panel. Not used by the app header (the integrator
 * mounts the controlled panel inside the user menu), but kept for callers
 * that need a drop-in notification settings widget.
 */
export function NotificationSettings() {
  const [open, setOpen] = useState(false)
  const { t } = useTranslation('notify')
  return (
    <>
      <Button type="text" onClick={() => setOpen(true)}>
        {t('settings.open')}
      </Button>
      <NotificationSettingsPanel open={open} onClose={() => setOpen(false)} />
    </>
  )
}
