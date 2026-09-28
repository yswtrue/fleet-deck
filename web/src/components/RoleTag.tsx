import { Tag } from 'antd'
import type { UserRole } from '../apiClient.ts'

/** Role badge used by user management and workspace membership lists. */
export function RoleTag({ role }: { role: UserRole }) {
  return (
    <Tag color={role === 'admin' ? 'blue' : 'default'}>{role === 'admin' ? 'Admin' : 'User'}</Tag>
  )
}
