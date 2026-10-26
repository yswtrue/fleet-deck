import { useState } from 'react'
import { Button, Form, Input, Modal } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import { useQueryClient } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import type { PatchTaskInput } from '../apiClient.ts'
import type { Task } from '../types.ts'

/**
 * Edits the title and prompt of a task that has not been dispatched yet (the
 * hub rejects content edits once a session exists). The title is only sent
 * when the operator changed it, so an untouched provisional label keeps
 * following prompt edits (and may still be replaced by the agent's title).
 */
export function EditTaskModal({
  task,
  messageApi,
  onClose,
}: {
  task: Task
  /** Card/board-scoped toast instance: the modal unmounts on close, its own holder would not survive the confirmation. */
  messageApi: MessageInstance
  onClose: () => void
}) {
  const { t } = useTranslation('board')
  const api = useHubApi()
  const queryClient = useQueryClient()
  const [form] = Form.useForm<{ title: string; prompt: string }>()
  const [saving, setSaving] = useState(false)

  const handleSave = async () => {
    const values = await form.validateFields().catch(() => null)
    if (!values) return
    const patch: PatchTaskInput = { prompt: values.prompt }
    const title = (values.title ?? '').trim()
    // Unchanged title: omit it so the hub keeps `title_auto` and re-derives
    // the provisional label from the edited prompt. An emptied field is an
    // explicit reset to that provisional label.
    if (title !== task.title.trim()) patch.title = title

    setSaving(true)
    try {
      await api.patchTask(task.id, patch)
      await queryClient.invalidateQueries({ queryKey: ['tasks'] })
      messageApi.success(t('edit.saved', { title: title || task.title }))
      onClose()
    } catch (err: unknown) {
      messageApi.error(
        err instanceof Error
          ? t('edit.saveErrorDetail', { message: err.message })
          : t('edit.saveError')
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      title={t('edit.title')}
      open
      onCancel={onClose}
      footer={[
        <Button key="cancel" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>,
        <Button key="save" type="primary" loading={saving} onClick={() => void handleSave()}>
          {t('common:actions.save')}
        </Button>,
      ]}
    >
      <Form
        form={form}
        layout="vertical"
        initialValues={{ title: task.title, prompt: task.prompt ?? '' }}
      >
        <Form.Item label={t('edit.titleLabel')} name="title" extra={t('edit.titleHelp')}>
          <Input placeholder={t('form.titlePlaceholder')} />
        </Form.Item>
        <Form.Item
          label={t('edit.promptLabel')}
          name="prompt"
          rules={[{ required: true, whitespace: true, message: t('form.promptRequired') }]}
        >
          <Input.TextArea rows={6} placeholder={t('form.promptPlaceholder')} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
