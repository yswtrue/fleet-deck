import { useState } from 'react'
import { Alert, Button, Card, Form, Input } from 'antd'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { apiClient } from '../apiClient.ts'
import { useAuth } from '../hooks/useAuth.ts'
import { ErrorState } from '../components/ErrorState.tsx'
import { LanguageSwitcher } from '../components/LanguageSwitcher.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { useTranslation } from '../i18n/index.ts'
import styles from './Login.module.css'
import logoUrl from '../assets/fleetdeck-icon.png?w=128&format=webp'

interface LoginValues {
  email: string
  display_name?: string
  password: string
  confirm?: string
}

/**
 * Auth entry point. GET /api/auth/status decides the mode: first-run setup
 * creates the admin account; afterwards it is plain email+password sign-in
 * with deliberately generic failure copy.
 */
export function Login() {
  const [form] = Form.useForm<LoginValues>()
  const [submitError, setSubmitError] = useState<string | null>(null)
  const navigate = useNavigate()
  const { login, setup } = useAuth()
  const { t } = useTranslation('auth')

  const statusQuery = useQuery({
    queryKey: ['auth', 'status'],
    queryFn: () => apiClient.authStatus(),
    retry: false,
  })

  // Hooks above; early returns below.
  if (statusQuery.isError) {
    return (
      <div className={styles.wrap}>
        <ErrorState title={t('login.hubUnreachable')} onRetry={() => void statusQuery.refetch()}>
          {t('login.hubUnreachableBody')}
        </ErrorState>
      </div>
    )
  }
  if (!statusQuery.data) {
    return (
      <div className={styles.wrap}>
        <LoadingSkeleton count={2} />
      </div>
    )
  }

  const needsSetup = statusQuery.data.needs_setup === true
  const submitting = needsSetup ? setup.isPending : login.isPending

  const handleSubmit = async (values: LoginValues) => {
    setSubmitError(null)
    try {
      if (needsSetup) {
        await setup.mutateAsync({
          email: values.email,
          display_name: values.display_name ?? '',
          password: values.password,
        })
      } else {
        await login.mutateAsync({ email: values.email, password: values.password })
      }
      navigate('/', { replace: true })
    } catch (err) {
      // Bad credentials never say which part was wrong; setup failures may
      // carry a useful backend reason (e.g. "setup already completed").
      setSubmitError(
        needsSetup
          ? err instanceof Error
            ? err.message
            : t('login.setupFailed')
          : t('login.signInFailed')
      )
    }
  }

  return (
    <div className={styles.wrap}>
      <div className={styles.panel}>
        <aside className={styles.hero}>
          <div className={styles.heroBrand}>
            <img className={styles.heroMark} src={logoUrl} alt="" />
            <span className={styles.heroName}>FleetDeck</span>
          </div>
          <p className={styles.heroLine}>{t('login.heroLine')}</p>
        </aside>
        <Card className={styles.card}>
          <div className={styles.langRow}>
            <LanguageSwitcher />
          </div>
          <div className={styles.brand}>
            <div className={styles.wordmark}>FleetDeck</div>
            <p className={styles.tagline}>
              {needsSetup ? t('login.setupTagline') : t('login.signInTagline')}
            </p>
          </div>
          {submitError && (
            <Alert
              className={styles.error}
              type="error"
              showIcon
              message={submitError}
              role="alert"
            />
          )}
          <Form
            form={form}
            layout="vertical"
            requiredMark={false}
            onFinish={(values) => void handleSubmit(values)}
          >
            <Form.Item
              label={t('fields.email')}
              name="email"
              rules={[
                { required: true, message: t('validation.emailRequired') },
                { type: 'email', message: t('validation.emailInvalid') },
              ]}
            >
              <Input autoFocus autoComplete="email" placeholder={t('fields.emailPlaceholder')} />
            </Form.Item>
            {needsSetup && (
              <Form.Item
                label={t('fields.displayName')}
                name="display_name"
                rules={[{ required: true, message: t('validation.displayNameRequired') }]}
              >
                <Input autoComplete="name" placeholder={t('fields.namePlaceholder')} />
              </Form.Item>
            )}
            <Form.Item
              label={t('fields.password')}
              name="password"
              rules={
                needsSetup
                  ? [
                      { required: true, message: t('validation.passwordRequired') },
                      { min: 8, message: t('validation.passwordMin') },
                    ]
                  : [{ required: true, message: t('validation.passwordRequired') }]
              }
            >
              <Input.Password
                autoComplete={needsSetup ? 'new-password' : 'current-password'}
                placeholder={t('fields.passwordPlaceholder')}
              />
            </Form.Item>
            {needsSetup && (
              <Form.Item
                label={t('login.confirmPassword')}
                name="confirm"
                dependencies={['password']}
                rules={[
                  { required: true, message: t('login.confirmRequired') },
                  {
                    // Matches the password field; empty values left to the
                    // required rule so only one message shows at a time.
                    validator: (_, value) => {
                      if (!value || form.getFieldValue('password') === value)
                        return Promise.resolve()
                      return Promise.reject(new Error(t('login.passwordMismatch')))
                    },
                  },
                ]}
              >
                <Input.Password
                  autoComplete="new-password"
                  placeholder={t('login.confirmPlaceholder')}
                />
              </Form.Item>
            )}
            <Button
              className={styles.submit}
              type="primary"
              htmlType="submit"
              block
              loading={submitting}
            >
              {needsSetup ? t('login.createAdmin') : t('login.signIn')}
            </Button>
          </Form>
        </Card>
      </div>
    </div>
  )
}
