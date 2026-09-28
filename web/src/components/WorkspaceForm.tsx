import { useState } from 'react'
import { Button, Form, Input, Modal, message } from 'antd'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'

interface WorkspaceFormValues {
  name: string
  path: string
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" focusable="false">
      <path d="M12 5v14M5 12h14" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
    </svg>
  )
}

/**
 * "New workspace" trigger + modal form for the node detail page. Plain
 * mutation against `apiClient.createWorkspace` (no Refine): on success the
 * node's workspace list and the node-wide task counts are invalidated.
 */
export function WorkspaceForm({ nodeId }: { nodeId: string }) {
  const api = useHubApi()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm<WorkspaceFormValues>()
  // `message.useMessage()` (not the static `message` API): antd v5's static
  // holders go through the legacy ReactDOM render, which React 19 removed.
  const [messageApi, messageHolder] = message.useMessage()

  const createWorkspace = useMutation({
    mutationFn: (values: WorkspaceFormValues) =>
      api.createWorkspace({
        node_id: nodeId,
        name: values.name.trim(),
        path: values.path.trim(),
      }),
    onSuccess: (workspace) => {
      messageApi.success(`Workspace “${workspace.name}” created`)
      queryClient.invalidateQueries({ queryKey: ['workspaces', nodeId] })
      queryClient.invalidateQueries({ queryKey: ['tasks-by-workspace'] })
      form.resetFields()
      setOpen(false)
    },
    onError: (error) => {
      messageApi.error(`Could not create workspace: ${error.message}`)
    },
  })

  return (
    <>
      {messageHolder}
      <Button type="primary" icon={<PlusIcon />} onClick={() => setOpen(true)}>
        New workspace
      </Button>
      <Modal
        open={open}
        title="New workspace"
        okText="Create"
        confirmLoading={createWorkspace.isPending}
        onOk={() => form.submit()}
        onCancel={() => {
          form.resetFields()
          createWorkspace.reset()
          setOpen(false)
        }}
        destroyOnHidden
      >
        <Form<WorkspaceFormValues>
          form={form}
          layout="vertical"
          requiredMark={false}
          onFinish={(values) => createWorkspace.mutate(values)}
        >
          <Form.Item
            name="name"
            label="Name"
            rules={[
              { required: true, message: 'Enter a workspace name' },
              { max: 100, message: 'Keep the name under 100 characters' },
            ]}
          >
            <Input placeholder="my-project" autoFocus />
          </Form.Item>
          <Form.Item
            name="path"
            label="Absolute path"
            extra="Checked out on the node, e.g. /home/you/code/my-project"
            rules={[
              { required: true, message: 'Enter the workspace path' },
              {
                validator: (_rule, value: string) => {
                  const path = (value ?? '').trim()
                  if (path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path)) return Promise.resolve()
                  return Promise.reject(new Error('Path must be absolute (start with / or a drive letter)'))
                },
              },
            ]}
          >
            <Input placeholder="/home/you/code/my-project" />
          </Form.Item>
        </Form>
      </Modal>
    </>
  )
}
