import { useEffect, useMemo, useRef } from 'react'
import { Modal, Form, Input, Button, Select, Switch } from 'antd'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from '@refinedev/antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import { deriveTaskTitle } from '../taskTitle.ts'

interface NewTaskFormProps {
  workspaceId: string
  /** Node that owns the workspace; its registered agents fill the picker. */
  nodeId: string
  onClose: () => void
}

/**
 * Uses the app-wide `<Refine>` provider (see providers/RefineProvider.tsx) for
 * `useForm`; no local Refine/DataProvider wrapper is needed.
 */
export function NewTaskForm({ workspaceId, nodeId, onClose }: NewTaskFormProps) {
  const { t } = useTranslation('board')
  const api = useHubApi()
  const queryClient = useQueryClient()
  const { data: node, isLoading: nodeLoading } = useQuery({
    queryKey: ['nodes', nodeId],
    queryFn: () => api.getNode(nodeId),
    enabled: !!nodeId,
  })
  const agentTypes = useMemo(() => node?.agent_types ?? [], [node])
  const agentOptions = useMemo(() => agentTypes.map((value) => ({ value })), [agentTypes])
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

  // Once the operator edits the title, stop mirroring the prompt into it.
  const titleEditedRef = useRef(false)

  // Seed the default agent once the node's registered list arrives. antd only
  // applies `initialValues` at mount, and the node query resolves after it.
  useEffect(() => {
    if (agentTypes.length === 0) return
    if (!form.getFieldValue('agent_type')) {
      form.setFieldValue('agent_type', agentTypes[0])
    }
  }, [agentTypes, form])

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <Modal
      title={t('form.title')}
      open
      onCancel={onClose}
      footer={[
        <Button key="cancel" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>,
        <Button key="submit" type="primary" loading={formLoading} {...saveButtonProps}>
          {t('common:actions.create')}
        </Button>,
      ]}
    >
      <Form
        {...formProps}
        layout="vertical"
        initialValues={{ auto_start: true }}
        onValuesChange={(changed: Record<string, unknown>) => {
          if ('title' in changed) {
            titleEditedRef.current = true
            return
          }
          if ('prompt' in changed && !titleEditedRef.current) {
            form.setFieldValue('title', deriveTaskTitle(String(changed.prompt ?? '')))
          }
        }}
      >
        <Form.Item
          label={t('form.autoStartLabel')}
          name="auto_start"
          valuePropName="checked"
          extra={t('form.autoStartHelp')}
        >
          <Switch aria-label={t('form.autoStartLabel')} />
        </Form.Item>
        <Form.Item name="workspace_id" initialValue={workspaceId} hidden>
          <Input />
        </Form.Item>
        <Form.Item label={t('form.titleLabel')} name="title" extra={t('form.titleHelp')}>
          <Input placeholder={t('form.titlePlaceholder')} onPressEnter={() => void form.submit()} />
        </Form.Item>
        {/* Required: a task without an instruction opens an agent session that
            never receives a prompt, so it would sit in "running" forever. */}
        <Form.Item
          label={t('form.promptLabel')}
          name="prompt"
          rules={[{ required: true, whitespace: true, message: t('form.promptRequired') }]}
        >
          <Input.TextArea rows={5} placeholder={t('form.promptPlaceholder')} />
        </Form.Item>
        <Form.Item
          label={t('form.agentLabel')}
          name="agent_type"
          rules={[{ required: true, whitespace: true, message: t('form.agentRequired') }]}
          extra={t('form.agentHelp')}
        >
          {/* The Select must be Form.Item's direct child: a wrapper element
              swallows the injected value/onChange, so a selection would never
              reach the form store. */}
          <Select
            options={agentOptions}
            showSearch
            placeholder={t('form.agentPlaceholder')}
            notFoundContent={nodeLoading ? undefined : t('form.noAgents')}
            style={{ width: '100%' }}
          />
        </Form.Item>
      </Form>
    </Modal>
  )
}
