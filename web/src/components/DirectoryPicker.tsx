import { useState } from 'react'
import { Alert, Button, List, Modal } from 'antd'
import { useQuery } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import styles from './DirectoryPicker.module.css'

function FolderIcon() {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true" focusable="false">
      <path
        d="M3.5 6.5A1.5 1.5 0 0 1 5 5h4l2 2h8a1.5 1.5 0 0 1 1.5 1.5v9A1.5 1.5 0 0 1 19 19H5a1.5 1.5 0 0 1-1.5-1.5z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinejoin="round"
      />
    </svg>
  )
}

export interface DirectoryPickerProps {
  open: boolean
  nodeId: string
  /** Starting directory; empty starts at the node user's home directory. */
  initialPath?: string
  onCancel: () => void
  onSelect: (path: string) => void
}

/**
 * Folder picker for workspace creation. The hub proxies each listing to the
 * node that owns the filesystem (GET /api/nodes/{id}/fs), so the browser never
 * guesses at remote paths: only directories that actually exist on the node
 * are shown, and the selected path is the node's canonical absolute path.
 */
export function DirectoryPicker({
  open,
  nodeId,
  initialPath,
  onCancel,
  onSelect,
}: DirectoryPickerProps) {
  const api = useHubApi()
  const { t } = useTranslation('pages')
  // Mounted fresh per opening by WorkspaceForm, so the initial directory is
  // read from the form at that moment; no effect-based re-sync is needed.
  const [currentPath, setCurrentPath] = useState(initialPath ?? '')

  const listing = useQuery({
    queryKey: ['node-fs', nodeId, currentPath],
    queryFn: () => api.listNodeDirs(nodeId, currentPath || undefined),
    enabled: open && !!nodeId,
    retry: false,
  })

  const data = listing.data
  const entries = data?.entries ?? []

  return (
    <Modal
      open={open}
      title={t('workspace.pickerTitle')}
      onCancel={onCancel}
      destroyOnHidden
      width={560}
      footer={[
        <Button key="cancel" onClick={onCancel}>
          {t('common:actions.cancel')}
        </Button>,
        <Button
          key="select"
          type="primary"
          disabled={!data}
          onClick={() => {
            if (data) onSelect(data.path)
          }}
        >
          {t('workspace.pickerSelect')}
        </Button>,
      ]}
    >
      <div className={styles.pathBar}>
        <Button
          size="small"
          disabled={!data?.parent}
          onClick={() => data?.parent && setCurrentPath(data.parent)}
        >
          {t('workspace.pickerUp')}
        </Button>
        <Button size="small" disabled={!currentPath} onClick={() => setCurrentPath('')}>
          {t('workspace.pickerHome')}
        </Button>
        <span className={styles.path} title={data?.path ?? currentPath}>
          {data?.path ?? t('workspace.pickerLoading')}
        </span>
      </div>

      {listing.isError ? (
        <Alert
          type="error"
          showIcon
          message={t('workspace.pickerError', { message: listing.error.message })}
          action={
            <Button size="small" onClick={() => setCurrentPath('')}>
              {t('workspace.pickerHome')}
            </Button>
          }
        />
      ) : (
        <>
          <List
            size="small"
            loading={listing.isPending}
            dataSource={entries}
            locale={{ emptyText: t('workspace.pickerEmpty') }}
            className={styles.list}
            renderItem={(entry) => (
              <List.Item className={styles.item}>
                <Button
                  type="text"
                  block
                  className={styles.entry}
                  onClick={() => setCurrentPath(entry.path)}
                >
                  <FolderIcon />
                  <span className={styles.name}>{entry.name}</span>
                </Button>
              </List.Item>
            )}
          />
          <p className={styles.tip}>{t('workspace.pickerTip')}</p>
        </>
      )}
    </Modal>
  )
}
