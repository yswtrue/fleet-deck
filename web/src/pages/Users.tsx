import { useEffect, useState } from 'react'
import {
  Button,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import type { TableColumnsType } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '../apiClient.ts'
import type { AuthUser, CreateUserInput, PatchUserInput, UserRole } from '../apiClient.ts'
import type { Node } from '../gen/protocol/index.ts'
import { useAuth } from '../hooks/useAuth.ts'
import { EmptyState } from '../components/EmptyState.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import styles from './Users.module.css'

interface UserFormValues {
  email: string
  display_name: string
  password?: string
  role: UserRole
  node_ids: string[]
}

/** Node option label: name plus host so identically named nodes stay distinct. */
function nodeLabel(node: Node): string {
  return node.host ? `${node.name} — ${node.host}` : node.name
}

const ROLE_OPTIONS = [
  { value: 'user', label: 'User' },
  { value: 'admin', label: 'Admin' },
]

function RoleTag({ role }: { role: UserRole }) {
  return (
    <Tag color={role === 'admin' ? 'blue' : 'default'}>{role === 'admin' ? 'Admin' : 'User'}</Tag>
  )
}

/**
 * Create/edit dialog. `editing` null means create; editing sends only the
 * fields the admin touched (password only when resetting it).
 */
function UserFormModal({
  open,
  editing,
  onClose,
}: {
  open: boolean
  editing: AuthUser | null
  onClose: () => void
}) {
  const [form] = Form.useForm<UserFormValues>()
  const queryClient = useQueryClient()
  // antd's static message API does not render under React 19; useMessage does.
  const [messageApi, messageHolder] = message.useMessage()

  // Node options for the binding multi-select. The Users page is admin-only,
  // so listNodes returns every node in the system.
  const nodes = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiClient.listNodes(),
    enabled: open,
  })
  // Current bindings of the user being edited (create starts unbound).
  const boundNodes = useQuery({
    queryKey: ['user-nodes', editing?.id],
    queryFn: () => apiClient.listUserNodes(editing?.id ?? ''),
    enabled: open && !!editing,
  })

  useEffect(() => {
    if (open) {
      form.setFieldsValue({
        email: editing?.email ?? '',
        display_name: editing?.display_name ?? '',
        password: undefined,
        role: editing?.role ?? 'user',
        // Re-applied once the bindings query resolves (below).
        node_ids: boundNodes.data?.map((node) => node.id) ?? [],
      })
    }
  }, [open, editing, form, boundNodes.data])

  const saveUser = useMutation<AuthUser, Error, void>({
    mutationFn: async () => {
      const values = form.getFieldsValue()
      const nodeIds = values.node_ids ?? []
      let user: AuthUser
      if (editing) {
        const patch: PatchUserInput = { display_name: values.display_name, role: values.role }
        if (values.password) patch.password = values.password
        user = await apiClient.patchUser(editing.id, patch)
      } else {
        const input: CreateUserInput = {
          email: values.email,
          display_name: values.display_name,
          password: values.password ?? '',
          role: values.role,
        }
        user = await apiClient.createUser(input)
      }
      // Full-set replace of the user's node bindings (idempotent).
      await apiClient.setUserNodes(user.id, nodeIds)
      return user
    },
    onSuccess: () => {
      messageApi.success(editing ? 'User updated' : 'User created')
      void queryClient.invalidateQueries({ queryKey: ['users'] })
      void queryClient.invalidateQueries({ queryKey: ['user-nodes'] })
      void queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
      onClose()
    },
    onError: (err) => {
      // 409 guards (e.g. last-admin protection) carry the useful reason.
      messageApi.error(err.message)
    },
  })

  return (
    <Modal
      open={open}
      title={editing ? 'Edit user' : 'New user'}
      okText={editing ? 'Save' : 'Create'}
      confirmLoading={saveUser.isPending}
      onOk={() => void form.submit()}
      onCancel={onClose}
      destroyOnHidden
    >
      {messageHolder}
      <Form form={form} layout="vertical" requiredMark={false} onFinish={() => saveUser.mutate()}>
        <Form.Item
          label="Email"
          name="email"
          rules={[
            { required: true, message: 'Email is required' },
            { type: 'email', message: 'Enter a valid email address' },
          ]}
        >
          <Input autoComplete="off" placeholder="you@example.com" disabled={editing !== null} />
        </Form.Item>
        <Form.Item
          label="Display name"
          name="display_name"
          rules={[{ required: true, message: 'Display name is required' }]}
        >
          <Input autoComplete="off" placeholder="Ada Lovelace" />
        </Form.Item>
        <Form.Item
          label={editing ? 'Reset password' : 'Password'}
          name="password"
          rules={
            editing
              ? [{ min: 8, message: 'Password must be at least 8 characters' }]
              : [
                  { required: true, message: 'Password is required' },
                  { min: 8, message: 'Password must be at least 8 characters' },
                ]
          }
          extra={editing ? 'Leave empty to keep the current password.' : undefined}
        >
          <Input.Password autoComplete="new-password" placeholder="At least 8 characters" />
        </Form.Item>
        <Form.Item
          label="Role"
          name="role"
          rules={[{ required: true, message: 'Role is required' }]}
        >
          <Select options={ROLE_OPTIONS} />
        </Form.Item>
        <Form.Item
          label="Bound nodes"
          name="node_ids"
          extra="Access to a node covers everything beneath it: workspaces, tasks and sessions."
        >
          <Select
            mode="multiple"
            placeholder="Select the nodes this user can access"
            options={(nodes.data ?? []).map((node) => ({ value: node.id, label: nodeLabel(node) }))}
            optionFilterProp="label"
            loading={nodes.isPending}
            notFoundContent={nodes.isPending ? 'Loading nodes…' : 'No nodes registered yet'}
            allowClear
          />
        </Form.Item>
      </Form>
    </Modal>
  )
}

/**
 * Node chips for one users-table row: the row's bound nodes (kept fresh via
 * the `user-nodes` query cache shared with the edit modal).
 */
function UserNodeChips({ userId }: { userId: string }) {
  const boundNodes = useQuery({
    queryKey: ['user-nodes', userId],
    queryFn: () => apiClient.listUserNodes(userId),
  })
  if (boundNodes.isPending) return <Typography.Text type="secondary">…</Typography.Text>
  const nodes = boundNodes.data ?? []
  if (nodes.length === 0) return <Typography.Text type="secondary">None</Typography.Text>
  return (
    <Space size={[4, 4]} wrap>
      {nodes.map((node) => (
        <Tag key={node.id}>{node.name}</Tag>
      ))}
    </Space>
  )
}

/**
 * Admin-only user management: list, create, edit (incl. password reset and
 * node bindings) and delete. Deleting yourself is disabled here and rejected
 * with 409 server-side; last-admin protections surface as error toasts.
 */
export function Users() {
  const { user: currentUser } = useAuth()
  const queryClient = useQueryClient()
  const [messageApi, messageHolder] = message.useMessage()
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<AuthUser | null>(null)

  const users = useQuery({
    queryKey: ['users'],
    queryFn: () => apiClient.listUsers(),
  })

  const deleteUser = useMutation<void, Error, AuthUser>({
    mutationFn: (user) => apiClient.deleteUser(user.id),
    onSuccess: (_res, user) => {
      messageApi.success(`Deleted ${user.email}`)
      void queryClient.invalidateQueries({ queryKey: ['users'] })
      void queryClient.invalidateQueries({ queryKey: ['user-nodes'] })
    },
    onError: (err) => {
      messageApi.error(err.message)
    },
  })

  const columns: TableColumnsType<AuthUser> = [
    { title: 'Email', dataIndex: 'email', key: 'email' },
    { title: 'Display name', dataIndex: 'display_name', key: 'display_name' },
    {
      title: 'Role',
      key: 'role',
      render: (_: unknown, user: AuthUser) => <RoleTag role={user.role} />,
    },
    {
      title: 'Bound nodes',
      key: 'nodes',
      render: (_: unknown, user: AuthUser) => <UserNodeChips userId={user.id} />,
    },
    {
      title: 'Created',
      key: 'created_at',
      render: (_: unknown, user: AuthUser) => new Date(user.created_at).toLocaleDateString(),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, user: AuthUser) => {
        const isSelf = user.id === currentUser?.id
        return (
          <Space>
            <Button
              onClick={() => {
                setEditing(user)
                setModalOpen(true)
              }}
            >
              Edit
            </Button>
            <Popconfirm
              title={`Delete ${user.email}?`}
              description="This removes their access immediately."
              okText="Delete"
              okButtonProps={{ danger: true }}
              onConfirm={() => deleteUser.mutate(user)}
              disabled={isSelf}
            >
              <Button
                danger
                disabled={isSelf}
                title={isSelf ? 'You cannot delete your own account' : undefined}
              >
                Delete
              </Button>
            </Popconfirm>
          </Space>
        )
      },
    },
  ]

  if (users.isLoading) return <LoadingSkeleton />
  if (users.error) {
    return (
      <ErrorState title="Could not load users" onRetry={() => void users.refetch()}>
        {users.error.message}
      </ErrorState>
    )
  }

  return (
    <div>
      {messageHolder}
      <div className={styles.toolbar}>
        <Typography.Title level={2} style={{ margin: 0 }}>
          Users
        </Typography.Title>
        <Button
          type="primary"
          onClick={() => {
            setEditing(null)
            setModalOpen(true)
          }}
        >
          New user
        </Button>
      </div>
      <Table<AuthUser>
        rowKey="id"
        columns={columns}
        dataSource={users.data ?? []}
        pagination={false}
        locale={{
          emptyText: <EmptyState title="No users yet">Create the first user above.</EmptyState>,
        }}
      />
      <UserFormModal open={modalOpen} editing={editing} onClose={() => setModalOpen(false)} />
    </div>
  )
}
