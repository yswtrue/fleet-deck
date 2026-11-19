import { useQuery } from '@tanstack/react-query'
import { useHubApi } from './useHubApi.ts'

/**
 * Query key for the caller's favorite workspaces. The star toggle and both
 * quick-jump surfaces (dashboard + sidebar) share this one cache entry, so a
 * bookmark change updates every place without a reload.
 */
export const FAVORITES_QUERY_KEY = ['workspace-favorites'] as const

/**
 * The signed-in user's bookmarked workspaces across all visible nodes,
 * newest first (GET /api/workspaces/favorites).
 */
export function useFavoriteWorkspaces() {
  const api = useHubApi()
  return useQuery({
    queryKey: FAVORITES_QUERY_KEY,
    queryFn: () => api.listFavoriteWorkspaces(),
  })
}
