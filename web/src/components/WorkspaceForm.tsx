import { useState } from 'react'
import { Button, Form, Input, Modal, message } from 'antd'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import { DirectoryPicker } from './DirectoryPicker.tsx'

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
  const { t } = useTranslation('pages')
  const [open, setOpen] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [pickerInitialPath, setPickerInitialPath] = useState('')
  const [form] = Form.useForm<WorkspaceFormValues>()
  // `message.useMessage()` (not the static `message` API): antd v5's static
  // holders go through the legacy ReactDOM render, which React 19 removed.
  const [messageApi, messageHolder] = message.useMessage()

  const openPicker = () => {
    const current = (form.getFieldValue('path') as string | undefined)?.trim() ?? ''
    setPickerInitialPath(current)
    setPickerOpen(true)
  }

  // Selecting a folder fills the path with the node's canonical absolute path
  // and, when the name is still blank, derives it from the folder name.
  const handlePick = (picked: string) => {
    setPickerOpen(false)
    form.setFieldValue('path', picked)
    const currentName = (form.getFieldValue('name') as string | undefined)?.trim() ?? ''
    if (!currentName) {
      const base = picked.split(/[\\/]/).filter(Boolean).pop()
      if (base) form.setFieldValue('name', base)
    }
  }

  const createWorkspace = useMutation({
    mutationFn: (values: WorkspaceFormValues) =>
      api.createWorkspace({
        node_id: nodeId,
        name: values.name.trim(),
        path: values.path.trim(),
      }),
    onSuccess: (workspace) => {
      messageApi.success(t('workspace.created', { name: workspace.name }))
      queryClient.invalidateQueries({ queryKey: ['workspaces', nodeId] })
      queryClient.invalidateQueries({ queryKey: ['tasks-by-workspace'] })
      form.resetFields()
      setPickerOpen(false)
      setOpen(false)
    },
    onError: (error) => {
      messageApi.error(t('workspace.createFailed', { message: error.message }))
    },
  })

  return (
    <>
      {messageHolder}
      <Button type="primary" icon={<PlusIcon />} onClick={() => setOpen(true)}>
        {t('workspace.new')}
      </Button>
      <Modal
        open={open}
        title={t('workspace.new')}
        okText={t('common:actions.create')}
        cancelText={t('common:actions.cancel')}
        confirmLoading={createWorkspace.isPending}
        onOk={() => form.submit()}
        onCancel={() => {
          form.resetFields()
          createWorkspace.reset()
          setPickerOpen(false)
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
            label={t('workspace.name')}
            rules={[
              { required: true, message: t('workspace.nameRequired') },
              { max: 100, message: t('workspace.nameTooLong') },
            ]}
          >
            <Input placeholder={t('workspace.namePlaceholder')} autoFocus />
          </Form.Item>
          <Form.Item
            name="path"
            label={t('workspace.path')}
            extra={t('workspace.pathHelp')}
            rules={[
              { required: true, message: t('workspace.pathRequired') },
              {
                validator: (_rule, value: string) => {
                  const path = (value ?? '').trim()
                  if (path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path)) return Promise.resolve()
                  return Promise.reject(new Error(t('workspace.pathAbsolute')))
                },
              },
            ]}
          >
            <Input
              placeholder={t('workspace.pathPlaceholder')}
              addonAfter={
                <Button type="link" size="small" style={{ paddingInline: 0 }} onClick={openPicker}>
                  {t('workspace.browse')}
                </Button>
              }
            />
          </Form.Item>
        </Form>
      </Modal>
      {pickerOpen && (
        <DirectoryPicker
          open
          nodeId={nodeId}
          initialPath={pickerInitialPath}
          onCancel={() => setPickerOpen(false)}
          onSelect={handlePick}
        />
      )}
    </>
  )
}
