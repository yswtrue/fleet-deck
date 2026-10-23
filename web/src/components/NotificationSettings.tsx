import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Modal, Space, Switch, Tag, Typography } from 'antd'
import {
  notificationsSupported,
  isNotifyEnabled,
  setNotifyEnabled,
} from '../hooks/useTaskNotifications.ts'
import {
  pushSupported,
  getPushSubscription,
  enablePushNotifications,
  disablePushNotifications,
} from '../push.ts'
import { useTranslation } from '../i18n/index.ts'
import styles from './NotificationSettings.module.css'

interface NotificationSettingsPanelProps {
  open: boolean
  onClose: () => void
}

type PushState = 'unsupported' | 'off' | 'on' | 'busy'

function readPermission(): NotificationPermission | 'unsupported' {
  if (!notificationsSupported()) return 'unsupported'
  return Notification.permission
}

/**
 * Controlled panel for notification settings. Shows the current permission
 * state, requests permission when needed, and persists the on/off toggle via
 * localStorage. When Web Push is available the same toggle also registers (or
 * removes) this browser's push subscription, so updates arrive while FleetDeck
 * is closed.
 */
export function NotificationSettingsPanel({ open, onClose }: NotificationSettingsPanelProps) {
  const { t } = useTranslation('notify')
  const [permission, setPermission] = useState<NotificationPermission | 'unsupported'>(
    readPermission
  )
  const [enabled, setEnabled] = useState(isNotifyEnabled)
  const [pushState, setPushState] = useState<PushState>(() =>
    pushSupported() ? 'off' : 'unsupported'
  )
  const [pushError, setPushError] = useState<string | null>(null)
  // One migration attempt per mount: a browser that enabled notifications
  // before Web Push existed gets a subscription on first panel open.
  const migrated = useRef(false)

  const startPush = useCallback(async () => {
    setPushState('busy')
    setPushError(null)
    try {
      await enablePushNotifications()
      setPushState('on')
    } catch (err) {
      setPushState('off')
      setPushError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  const stopPush = useCallback(async () => {
    setPushError(null)
    try {
      await disablePushNotifications()
    } catch (err) {
      setPushError(err instanceof Error ? err.message : String(err))
    }
    setPushState('off')
  }, [])

  useEffect(() => {
    if (!open || !pushSupported()) return
    let alive = true
    void (async () => {
      const sub = await getPushSubscription()
      if (!alive) return
      setPushState(sub ? 'on' : 'off')
      if (sub && !isNotifyEnabled()) {
        // The browser subscription is the stronger truth (e.g. storage was
        // cleared): keep the switch consistent so disabling it works.
        setNotifyEnabled(true)
        setEnabled(true)
      }
      if (!sub && !migrated.current && isNotifyEnabled() && Notification.permission === 'granted') {
        migrated.current = true
        await startPush()
      }
    })()
    return () => {
      alive = false
    }
  }, [open, startPush])

  const handleRequest = async () => {
    if (!notificationsSupported()) return
    const result = await Notification.requestPermission()
    setPermission(result)
    if (result === 'granted') {
      setNotifyEnabled(true)
      setEnabled(true)
      if (pushSupported()) await startPush()
    }
  }

  const handleToggle = (checked: boolean) => {
    setNotifyEnabled(checked)
    setEnabled(checked)
    if (!pushSupported()) return
    if (checked) void startPush()
    else void stopPush()
  }

  const permissionLabel: Record<NotificationPermission | 'unsupported', string> = {
    granted: t('settings.permissionGranted'),
    denied: t('settings.permissionDenied'),
    default: t('settings.permissionDefault'),
    unsupported: t('settings.permissionUnsupported'),
  }

  const switchDisabled = permission !== 'granted' || pushState === 'busy'

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
      width={420}
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

        {pushState === 'unsupported' ? (
          <Typography.Text type="secondary">{t('settings.pushUnsupported')}</Typography.Text>
        ) : (
          <Typography.Text type="secondary">{t('settings.pushHelp')}</Typography.Text>
        )}
        {pushState === 'on' && <Tag color="green">{t('settings.pushOn')}</Tag>}
        {pushError && (
          <Alert message={t('settings.pushError', { error: pushError })} type="error" showIcon />
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
