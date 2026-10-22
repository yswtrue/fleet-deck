import { useEffect, useState } from 'react'
import { Button, Input, Spin, message } from 'antd'
import styles from './Board.module.css'
import { Column } from './Column.tsx'
import type { BoardColumn } from './Column.tsx'
import { NewTaskForm } from './NewTaskForm.tsx'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import { ErrorState } from './ErrorState.tsx'
import { EmptyState } from './EmptyState.tsx'
import { useTaskBoard } from '../hooks/useTaskBoard.ts'
import { useTranslation } from '../i18n/index.ts'

const COLUMNS: BoardColumn[] = [
  { key: 'waiting', statuses: ['waiting'], dot: 'waiting' },
  { key: 'running', statuses: ['running'], dot: 'running' },
  {
    key: 'attention',
    statuses: ['awaiting_user', 'failed'],
    dot: 'awaiting_user',
    titleKey: 'column.attention',
  },
  { key: 'done', statuses: ['done'], dot: 'done' },
]

export function Board({ workspaceId, nodeId }: { workspaceId: string; nodeId: string }) {
  const { t } = useTranslation('board')
  const [query, setQuery] = useState('')
  const [debouncedQuery, setDebouncedQuery] = useState('')
  const [showArchived, setShowArchived] = useState(false)
  const [showForm, setShowForm] = useState(false)
  const { tasks, subtasksByTask, isLoading, isFetching, error } = useTaskBoard(
    workspaceId,
    debouncedQuery,
    showArchived
  )
  // Status-move toasts live here, not on the card: a move relocates the card
  // across columns (unmount/remount), and a card-owned holder would take the
  // toast down with it. The static antd message API renders nothing under
  // React 19, so the hook-based instance is the only working surface.
  const [messageApi, messageHolder] = message.useMessage()

  // Debounce so each keystroke is not a round trip.
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedQuery(query.trim()), 300)
    return () => clearTimeout(timer)
  }, [query])

  // Covers both the debounce window and the in-flight refetch, so the search
  // box reflects every wait the user sees.
  const isSearching = query.trim() !== debouncedQuery || isFetching

  const clearSearch = () => {
    setQuery('')
    setDebouncedQuery('')
  }

  if (isLoading) return <LoadingSkeleton count={4} />
  if (error) return <ErrorState title={t('error.load')}>{error.message}</ErrorState>

  return (
    <div className={styles.board}>
      {messageHolder}
      <div className={styles.toolbar}>
        <Button type="primary" onClick={() => setShowForm(true)}>
          {t('toolbar.newTask')}
        </Button>
        <Button
          type={showArchived ? 'default' : 'text'}
          aria-pressed={showArchived}
          onClick={() => setShowArchived((prev) => !prev)}
        >
          {showArchived ? t('toolbar.hideArchived') : t('toolbar.showArchived')}
        </Button>
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('search.placeholder')}
          aria-label={t('search.label')}
          aria-busy={isSearching}
          className={styles.searchInput}
          suffix={
            isSearching ? (
              <Spin size="small" />
            ) : query ? (
              <Button type="text" size="small" className={styles.clearButton} onClick={clearSearch}>
                {t('search.clear')}
              </Button>
            ) : null
          }
        />
      </div>
      {tasks.length === 0 ? (
        debouncedQuery ? (
          <EmptyState title={t('empty.noMatchTitle', { query: debouncedQuery })}>
            {t('empty.noMatchBody')}
          </EmptyState>
        ) : (
          <EmptyState title={t('empty.noTasksTitle')}>{t('empty.noTasksBody')}</EmptyState>
        )
      ) : (
        <div className={styles.columns}>
          {COLUMNS.map((column) => (
            <Column
              key={column.key}
              column={column}
              tasks={tasks.filter((t) => column.statuses.includes(t.status))}
              subtasksByTask={subtasksByTask}
              messageApi={messageApi}
            />
          ))}
        </div>
      )}
      {showForm && (
        <NewTaskForm workspaceId={workspaceId} nodeId={nodeId} onClose={() => setShowForm(false)} />
      )}
    </div>
  )
}
