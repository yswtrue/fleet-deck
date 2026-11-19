import { Button, Tooltip, message } from 'antd'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { FAVORITES_QUERY_KEY, useFavoriteWorkspaces } from '../hooks/useFavoriteWorkspaces.ts'
import { useTranslation } from '../i18n/index.ts'
import { StarIcon } from './StarIcon.tsx'
import type { Workspace } from '../types.ts'
import styles from './FavoriteButton.module.css'

/**
 * Star toggle for one workspace. Reads the shared favorites cache so the card,
 * the board header and both quick-jump lists stay in sync; the mutation is
 * optimistic and refetches on settle (the hub list is the source of truth).
 */
export function FavoriteButton({
  workspace,
  size = 'small',
}: {
  workspace: Workspace
  size?: 'small' | 'middle'
}) {
  const api = useHubApi()
  const queryClient = useQueryClient()
  const { t } = useTranslation('pages')
  const [messageApi, messageHolder] = message.useMessage()
  const { data } = useFavoriteWorkspaces()
  const favorites = data ?? []
  const isFavorite = favorites.some((row) => row.id === workspace.id)

  const toggle = useMutation({
    mutationFn: (next: boolean) =>
      next ? api.addWorkspaceFavorite(workspace.id) : api.removeWorkspaceFavorite(workspace.id),
    onMutate: async (next) => {
      await queryClient.cancelQueries({ queryKey: FAVORITES_QUERY_KEY })
      const previous = queryClient.getQueryData<Workspace[]>(FAVORITES_QUERY_KEY)
      queryClient.setQueryData<Workspace[]>(FAVORITES_QUERY_KEY, (rows = []) =>
        next
          ? [...rows.filter((row) => row.id !== workspace.id), workspace]
          : rows.filter((row) => row.id !== workspace.id)
      )
      return { previous }
    },
    onError: (error, _next, context) => {
      if (context?.previous) {
        queryClient.setQueryData(FAVORITES_QUERY_KEY, context.previous)
      }
      messageApi.error(t('workspace.favoriteFailed', { message: error.message }))
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: FAVORITES_QUERY_KEY })
    },
  })

  const label = isFavorite ? t('workspace.favoriteRemove') : t('workspace.favoriteAdd')

  return (
    <>
      {messageHolder}
      <Tooltip title={label}>
        <Button
          type="text"
          size={size}
          className={isFavorite ? `${styles.star} ${styles.active}` : styles.star}
          aria-label={label}
          aria-pressed={isFavorite}
          loading={toggle.isPending}
          icon={<StarIcon filled={isFavorite} />}
          onClick={(event) => {
            // The star lives inside clickable cards: never navigate on toggle.
            event.stopPropagation()
            toggle.mutate(!isFavorite)
          }}
        />
      </Tooltip>
    </>
  )
}
