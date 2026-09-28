import { useState } from 'react'
import { Button, List, Modal, Popconfirm, Select, Space, message } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '../apiClient.ts'
import type { AuthUser } from '../apiClient.ts'
import { useAuth } from '../hooks/useAuth.ts'
import { EmptyState } from './EmptyState.tsx'
import { ErrorState } from './ErrorState.tsx'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import { RoleTag } from './RoleTag.tsx'
import { UserAvatar } from './UserAvatar.tsx'
import styles from './WorkspaceMembers.module.css'

/**
 * Workspace membership panel: self-contained trigger button + modal listing
 * the workspace members (avatar, name, email, role). Admins can add members
 * from the hub's user list and remove them; everyone else gets a read-only
 * list. Mount anywhere on the workspace page:
 *
 *   <WorkspaceMembers workspaceId={workspace.id} />
 */
export function WorkspaceMembers({ workspaceId }: { workspaceId: string }) {
  const { isAdmin } = useAuth()
  const queryClient = useQueryClient()
  // antd's static message API does not render under React 19; useMessage does.
  const [messageApi, messageHolder] = message.useMessage()
  const [open, setOpen] = useState(false)
  const [selectedUserId, setSelectedUserId] = useState<string | undefined>(undefined)

  const members = useQuery({
    queryKey: ['workspace-members', workspaceId],
    queryFn: () => apiClient.listWorkspaceMembers(workspaceId),
    enabled: open,
  })

  const allUsers = useQuery({
    queryKey: ['users'],
    queryFn: () => apiClient.listUsers(),
    enabled: open && isAdmin,
  })

  const invalidateMembers = () => {
    void queryClient.invalidateQueries({ queryKey: ['workspace-members', workspaceId] })
  }

  const addMember = useMutation<void, Error, string>({
    mutationFn: (userId) => apiClient.addWorkspaceMember(workspaceId, userId),
    onSuccess: () => {
      messageApi.success('Member added')
      setSelectedUserId(undefined)
      invalidateMembers()
    },
    onError: (err) => {
      messageApi.error(err.message)
    },
  })

  const removeMember = useMutation<void, Error, AuthUser>({
    mutationFn: (member) => apiClient.removeWorkspaceMember(workspaceId, member.id),
    onSuccess: (_res, member) => {
      messageApi.success(`Removed ${member.display_name}`)
      invalidateMembers()
    },
    onError: (err) => {
      messageApi.error(err.message)
    },
  })

  const memberIds = new Set((members.data ?? []).map((member) => member.id))
  const addableUsers = (allUsers.data ?? []).filter((user) => !memberIds.has(user.id))

  return (
    <>
      {messageHolder}
      <Button onClick={() => setOpen(true)}>Members</Button>
      <Modal
        open={open}
        title="Workspace members"
        footer={<Button onClick={() => setOpen(false)}>Close</Button>}
        onCancel={() => setOpen(false)}
      >
        {isAdmin && (
          <div className={styles.addRow}>
            <Select
              style={{ flex: 1 }}
              showSearch
              optionFilterProp="label"
              placeholder="Add a user by name or email"
              value={selectedUserId}
              onChange={(value) => setSelectedUserId(value)}
              options={addableUsers.map((user) => ({
                value: user.id,
                label: `${user.display_name} (${user.email})`,
              }))}
            />
            <Button
              type="primary"
              disabled={!selectedUserId}
              loading={addMember.isPending}
              onClick={() => selectedUserId && addMember.mutate(selectedUserId)}
            >
              Add
            </Button>
          </div>
        )}
        {members.isLoading ? (
          <LoadingSkeleton count={2} />
        ) : members.error ? (
          <ErrorState title="Could not load members" onRetry={() => void members.refetch()}>
            {members.error.message}
          </ErrorState>
        ) : (members.data ?? []).length === 0 ? (
          <EmptyState title="No members yet">Admins can add users to this workspace.</EmptyState>
        ) : (
          <List
            dataSource={members.data ?? []}
            renderItem={(member: AuthUser) => (
              <List.Item
                actions={
                  isAdmin
                    ? [
                        <Popconfirm
                          key="remove"
                          title={`Remove ${member.display_name} from this workspace?`}
                          okText="Remove"
                          okButtonProps={{ danger: true }}
                          onConfirm={() => removeMember.mutate(member)}
                        >
                          <Button
                            danger
                            size="small"
                            loading={
                              removeMember.isPending && removeMember.variables?.id === member.id
                            }
                          >
                            Remove
                          </Button>
                        </Popconfirm>,
                      ]
                    : undefined
                }
              >
                <List.Item.Meta
                  avatar={<UserAvatar user={member} />}
                  title={
                    <Space>
                      <span className={styles.name}>{member.display_name}</span>
                      <RoleTag role={member.role} />
                    </Space>
                  }
                  description={member.email}
                />
              </List.Item>
            )}
          />
        )}
      </Modal>
    </>
  )
}
