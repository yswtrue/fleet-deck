import { useEffect, useState } from 'react'
import { Button, Input, Spin, message } from 'antd'
import styles from './Board.module.css'
import { Column } from './Column.tsx'
import { NewTaskForm } from './NewTaskForm.tsx'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import { ErrorState } from './ErrorState.tsx'
import { EmptyState } from './EmptyState.tsx'
import { useTaskBoard } from '../hooks/useTaskBoard.ts'
import type { Task } from '../types.ts'

const COLUMNS: Task['status'][] = ['waiting', 'running', 'awaiting_user', 'done']

export function Board({ workspaceId }: { workspaceId: string }) {
  const [query, setQuery] = useState('')
  const [debouncedQuery, setDebouncedQuery] = useState('')
  const { tasks, subtasksByTask, isLoading, isFetching, error } = useTaskBoard(workspaceId, debouncedQuery)
  const [showForm, setShowForm] = useState(false)
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
  if (error) return <ErrorState title="Could not load board">{error.message}</ErrorState>

  return (
    <div className={styles.board}>
      {messageHolder}
      <div className={styles.toolbar}>
        <Button type="primary" onClick={() => setShowForm(true)}>
          New task
        </Button>
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search task titles…"
          aria-label="Search task titles"
          aria-busy={isSearching}
          style={{ width: 260 }}
          suffix={
            isSearching ? (
              <Spin size="small" />
            ) : query ? (
              <Button type="text" size="small" className={styles.clearButton} onClick={clearSearch}>
                Clear
              </Button>
            ) : null
          }
        />
      </div>
      {tasks.length === 0 ? (
        debouncedQuery ? (
          <EmptyState title={`No tasks match “${debouncedQuery}”.`}>Try a different search.</EmptyState>
        ) : (
          <EmptyState title="No tasks yet">Start one above.</EmptyState>
        )
      ) : (
        <div className={styles.columns}>
          {COLUMNS.map((status) => (
            <Column
              key={status}
              status={status}
              tasks={tasks.filter((t) => t.status === status)}
              subtasksByTask={subtasksByTask}
              messageApi={messageApi}
            />
          ))}
        </div>
      )}
      {showForm && <NewTaskForm workspaceId={workspaceId} onClose={() => setShowForm(false)} />}
    </div>
  )
}
