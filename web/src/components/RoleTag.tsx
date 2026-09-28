import { Tag } from 'antd'
import type { UserRole } from '../apiClient.ts'
import { useTranslation } from '../i18n/index.ts'

/** Role badge used by user management and workspace membership lists. */
export function RoleTag({ role }: { role: UserRole }) {
  const { t } = useTranslation('auth')
  return (
    <Tag color={role === 'admin' ? 'blue' : 'default'}>
      {role === 'admin' ? t('role.admin') : t('role.user')}
    </Tag>
  )
}
