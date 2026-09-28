import { Avatar } from 'antd'
import type { AvatarProps } from 'antd'
import type { AuthUser } from '../apiClient.ts'

type UserIdentity = Pick<AuthUser, 'display_name' | 'email'>

/** Avatar showing a user's initials; used in the account menu and user lists. */
export function UserAvatar({ user, ...rest }: { user: UserIdentity } & AvatarProps) {
  // First letters of up to two display-name words; fall back to the email.
  const fromName = user.display_name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0].toUpperCase())
    .join('')
  return <Avatar {...rest}>{fromName || user.email.slice(0, 2).toUpperCase()}</Avatar>
}
