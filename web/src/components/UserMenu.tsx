import { Button, Dropdown, theme } from 'antd'
import type { MenuProps } from 'antd'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useAuth.ts'
import { useTranslation } from '../i18n/index.ts'
import { RoleTag } from './RoleTag.tsx'
import { UserAvatar } from './UserAvatar.tsx'
import styles from './UserMenu.module.css'

/**
 * Account menu for the app header (mount into the Header's `headerActions`
 * slot). Self-contained: an initials-avatar trigger and a dropdown with the
 * signed-in identity, the admin-only Users link, and Log out. Renders nothing
 * while signed out.
 */
export function UserMenu() {
  const { user, isAdmin, logout } = useAuth()
  const navigate = useNavigate()
  const { t } = useTranslation('auth')
  const { token } = theme.useToken()

  if (!user) return null

  const items: MenuProps['items'] = [
    ...(isAdmin
      ? [{ key: 'users', label: t('users.title'), onClick: () => navigate('/users') }]
      : []),
    { type: 'divider' },
    {
      key: 'logout',
      label: t('userMenu.logout'),
      danger: true,
      // mutateAsync settles regardless of mount state — react-query v5 drops
      // per-mutate callbacks when the calling component unmounts mid-flight.
      onClick: () => {
        void logout
          .mutateAsync()
          .catch(() => {})
          .finally(() => navigate('/login'))
      },
    },
  ]

  return (
    <Dropdown
      trigger={['click']}
      placement="bottomRight"
      popupRender={(menu) => (
        <div className={styles.menu} style={{ background: token.colorBgElevated }}>
          <div className={styles.identity}>
            <div className={styles.name}>{user.display_name}</div>
            <div className={styles.email}>{user.email}</div>
            <RoleTag role={user.role} />
          </div>
          {menu}
        </div>
      )}
      menu={{ items }}
    >
      <Button className={styles.trigger} type="text" aria-label={t('userMenu.accountMenu')}>
        <UserAvatar user={user} size="small" />
      </Button>
    </Dropdown>
  )
}
