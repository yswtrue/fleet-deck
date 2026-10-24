import { useState } from 'react'
import { Button, Dropdown, Form, Input, Modal, Space, message } from 'antd'
import type { MenuProps } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import type { Workspace } from '../types.ts'

function MoreIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="15"
      height="15"
      fill="currentColor"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="5" cy="12" r="1.4" />
      <circle cx="12" cy="12" r="1.4" />
      <circle cx="19" cy="12" r="1.4" />
    </svg>
  )
}

interface WorkspaceActionsProps {
  workspace: Workspace
  /** `menu` is the compact card trigger; `buttons` is the workspace header. */
  variant?: 'menu' | 'buttons'
  /** Parent-owned toast holder: deletion unmounts a card, so a local holder
   * would drop the confirmation. */
  messageApi?: MessageInstance
  /** Called after a successful delete (e.g. navigate off a deleted board). */
  onDeleted?: () => void
}

/**
 * Rename/delete actions for one workspace. Both mutations go through the hub
 * REST API; the cache updates keep the node's workspace list, the board
 * breadcrumb and the workspace header in sync without a reload.
 */
export function WorkspaceActions({
  workspace,
  variant = 'menu',
  messageApi: externalMessageApi,
  onDeleted,
}: WorkspaceActionsProps) {
  const api = useHubApi()
  const queryClient = useQueryClient()
  const { t } = useTranslation('pages')
  const [localMessageApi, localMessageHolder] = message.useMessage()
  const messageApi = externalMessageApi ?? localMessageApi
  const [renameOpen, setRenameOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [renameForm] = Form.useForm<{ name: string }>()

  const renameWorkspace = useMutation({
    mutationFn: (name: string) => api.renameWorkspace(workspace.id, name),
    onSuccess: (updated) => {
      queryClient.setQueryData(['workspace', updated.id], updated)
      queryClient.setQueriesData<Workspace[]>({ queryKey: ['workspaces'] }, (rows) =>
        rows?.map((row) => (row.id === updated.id ? updated : row))
      )
      void queryClient.invalidateQueries({ queryKey: ['workspaces'] })
      messageApi.success(t('workspace.renamed', { name: updated.name }))
      setRenameOpen(false)
    },
    onError: (error) => {
      messageApi.error(t('workspace.renameFailed', { message: error.message }))
    },
  })

  const deleteWorkspace = useMutation({
    mutationFn: () => api.deleteWorkspace(workspace.id),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: ['workspace', workspace.id] })
      queryClient.removeQueries({ queryKey: ['tasks', workspace.id] })
      void queryClient.invalidateQueries({ queryKey: ['workspaces'] })
      void queryClient.invalidateQueries({ queryKey: ['tasks-by-workspace'] })
      messageApi.success(t('workspace.deleted', { name: workspace.name }))
      setDeleteOpen(false)
      onDeleted?.()
    },
    onError: (error) => {
      messageApi.error(t('workspace.deleteFailed', { message: error.message }))
    },
  })

  const openRename = () => {
    renameWorkspace.reset()
    renameForm.setFieldsValue({ name: workspace.name })
    setRenameOpen(true)
  }

  const openDelete = () => {
    deleteWorkspace.reset()
    setDeleteOpen(true)
  }

  const menuItems: MenuProps['items'] = [
    { key: 'rename', label: t('workspace.rename') },
    { type: 'divider' },
    { key: 'delete', label: t('common:actions.delete'), danger: true },
  ]

  return (
    <>
      {externalMessageApi ? null : localMessageHolder}
      {variant === 'menu' ? (
        <Dropdown
          menu={{
            items: menuItems,
            onClick: ({ key }) => (key === 'rename' ? openRename() : openDelete()),
          }}
          trigger={['click']}
          placement="bottomRight"
        >
          <Button
            size="small"
            type="text"
            aria-label={t('workspace.actionsAria', { name: workspace.name })}
            onClick={(event) => event.stopPropagation()}
            icon={<MoreIcon />}
          />
        </Dropdown>
      ) : (
        <Space>
          <Button onClick={openRename}>{t('workspace.rename')}</Button>
          <Button danger onClick={openDelete}>
            {t('common:actions.delete')}
          </Button>
        </Space>
      )}

      <Modal
        open={renameOpen}
        title={t('workspace.renameTitle')}
        okText={t('common:actions.save')}
        cancelText={t('common:actions.cancel')}
        confirmLoading={renameWorkspace.isPending}
        onOk={() => renameForm.submit()}
        onCancel={() => {
          renameForm.resetFields()
          renameWorkspace.reset()
          setRenameOpen(false)
        }}
        destroyOnHidden
      >
        <Form
          form={renameForm}
          layout="vertical"
          requiredMark={false}
          onFinish={({ name }) => renameWorkspace.mutate(name.trim())}
        >
          <Form.Item
            name="name"
            label={t('workspace.name')}
            rules={[
              { required: true, message: t('workspace.nameRequired') },
              { max: 100, message: t('workspace.nameTooLong') },
            ]}
          >
            <Input autoFocus />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={deleteOpen}
        title={t('workspace.deleteConfirmTitle', { name: workspace.name })}
        okText={t('common:actions.delete')}
        cancelText={t('common:actions.cancel')}
        okButtonProps={{ danger: true }}
        confirmLoading={deleteWorkspace.isPending}
        onOk={() => deleteWorkspace.mutate()}
        onCancel={() => {
          deleteWorkspace.reset()
          setDeleteOpen(false)
        }}
      >
        <p>{t('workspace.deleteConfirmBody')}</p>
      </Modal>
    </>
  )
}
