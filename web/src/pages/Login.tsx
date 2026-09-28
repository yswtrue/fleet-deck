import { useState } from 'react'
import { Alert, Button, Card, Form, Input } from 'antd'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { apiClient } from '../apiClient.ts'
import { useAuth } from '../hooks/useAuth.ts'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import styles from './Login.module.css'

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

  const statusQuery = useQuery({
    queryKey: ['auth', 'status'],
    queryFn: () => apiClient.authStatus(),
    retry: false,
  })

  // Hooks above; early returns below.
  if (statusQuery.isError) {
    return (
      <div className={styles.wrap}>
        <ErrorState title="Cannot reach the hub" onRetry={() => void statusQuery.refetch()}>
          The login service did not respond. Try again in a moment.
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
            : 'Account setup failed. Try again.'
          : 'Sign-in failed. Check your email and password and try again.'
      )
    }
  }

  return (
    <div className={styles.wrap}>
      <Card className={styles.card}>
        <div className={styles.brand}>
          <div className={styles.wordmark}>FleetDeck</div>
          <p className={styles.tagline}>
            {needsSetup
              ? 'Create the first admin account to set up this hub.'
              : 'Sign in to your FleetDeck account.'}
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
            label="Email"
            name="email"
            rules={[
              { required: true, message: 'Email is required' },
              { type: 'email', message: 'Enter a valid email address' },
            ]}
          >
            <Input autoFocus autoComplete="email" placeholder="you@example.com" />
          </Form.Item>
          {needsSetup && (
            <Form.Item
              label="Display name"
              name="display_name"
              rules={[{ required: true, message: 'Display name is required' }]}
            >
              <Input autoComplete="name" placeholder="Ada Lovelace" />
            </Form.Item>
          )}
          <Form.Item
            label="Password"
            name="password"
            rules={
              needsSetup
                ? [
                    { required: true, message: 'Password is required' },
                    { min: 8, message: 'Password must be at least 8 characters' },
                  ]
                : [{ required: true, message: 'Password is required' }]
            }
          >
            <Input.Password
              autoComplete={needsSetup ? 'new-password' : 'current-password'}
              placeholder="At least 8 characters"
            />
          </Form.Item>
          {needsSetup && (
            <Form.Item
              label="Confirm password"
              name="confirm"
              dependencies={['password']}
              rules={[
                { required: true, message: 'Please confirm your password' },
                {
                  // Matches the password field; empty values left to the
                  // required rule so only one message shows at a time.
                  validator: (_, value) => {
                    if (!value || form.getFieldValue('password') === value) return Promise.resolve()
                    return Promise.reject(new Error('Passwords do not match'))
                  },
                },
              ]}
            >
              <Input.Password autoComplete="new-password" placeholder="Repeat your password" />
            </Form.Item>
          )}
          <Button
            className={styles.submit}
            type="primary"
            htmlType="submit"
            block
            loading={submitting}
          >
            {needsSetup ? 'Create admin account' : 'Sign in'}
          </Button>
        </Form>
      </Card>
    </div>
  )
}
