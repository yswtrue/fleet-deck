import { useEffect, useRef } from 'react'
import { Modal, Form, Input, Button, AutoComplete } from 'antd'
import { useQueryClient } from '@tanstack/react-query'
import { useForm } from '@refinedev/antd'

interface NewTaskFormProps {
  workspaceId: string
  onClose: () => void
}

const AGENT_TYPE_SUGGESTIONS = ['fake', 'opencode', 'codex', 'claude', 'pi'].map((value) => ({ value }))

/**
 * Uses the app-wide `<Refine>` provider (see providers/RefineProvider.tsx) for
 * `useForm`; no local Refine/DataProvider wrapper is needed.
 */
export function NewTaskForm({ workspaceId, onClose }: NewTaskFormProps) {
  const queryClient = useQueryClient()
  const { form, formProps, saveButtonProps, formLoading } = useForm({
    action: 'create',
    resource: 'tasks',
    redirect: false,
    onMutationSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tasks', workspaceId] })
      // WorkspaceList task counts live under a separate key family; a new task
      // must refresh those too or the node page shows stale counts.
      queryClient.invalidateQueries({ queryKey: ['tasks-by-workspace'] })
      onClose()
    },
  })

  // While the agent-type suggestion list is open, Enter picks the highlighted
  // suggestion instead of submitting the form.
  const agentMenuOpenRef = useRef(false)

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <Modal
      title="New task"
      open
      onCancel={onClose}
      footer={[
        <Button key="cancel" onClick={onClose}>
          Cancel
        </Button>,
        <Button key="submit" type="primary" loading={formLoading} {...saveButtonProps}>
          Create
        </Button>,
      ]}
    >
      <Form {...formProps} layout="vertical" initialValues={{ agent_type: 'fake' }}>
        <Form.Item name="workspace_id" initialValue={workspaceId} hidden>
          <Input />
        </Form.Item>
        <Form.Item
          label="Title"
          name="title"
          rules={[{ required: true, whitespace: true, message: 'Please enter a title' }]}
        >
          <Input placeholder="What should the agent do?" onPressEnter={() => void form.submit()} />
        </Form.Item>
        <Form.Item label="Prompt" name="prompt" rules={[{ required: false }]}>
          <Input.TextArea rows={5} placeholder="Describe the task in detail (optional)..." />
        </Form.Item>
        <Form.Item
          label="Agent type"
          name="agent_type"
          rules={[{ required: true, whitespace: true, message: 'Please enter an agent type' }]}
          extra="Known agents: fake, opencode, codex, claude, pi"
        >
          <div
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || event.shiftKey || agentMenuOpenRef.current) return
              event.preventDefault()
              void form.submit()
            }}
          >
            <AutoComplete
              options={AGENT_TYPE_SUGGESTIONS}
              placeholder="Which agent should run this?"
              onOpenChange={(open) => {
                agentMenuOpenRef.current = open
              }}
              style={{ width: '100%' }}
            />
          </div>
        </Form.Item>
      </Form>
    </Modal>
  )
}
