import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { UseMutationResult, UseQueryResult } from '@tanstack/react-query'
import { apiClient, clearToken, getStoredToken, storeToken } from '../apiClient.ts'
import type { AuthUser, LoginInput, SetupInput } from '../apiClient.ts'
import { disablePushNotifications, enablePushNotifications, pushSupported } from '../push.ts'
import { isNotifyEnabled } from './useTaskNotifications.ts'

/**
 * Re-binds this device's push subscription to the account that just signed in.
 * Logout unsubscribes the browser, so this only matters for a session restored
 * with notifications already enabled (or a stale subscription after an expiry).
 */
function syncPushForCurrentUser(): void {
  if (!pushSupported() || !isNotifyEnabled() || Notification.permission !== 'granted') return
  void enablePushNotifications().catch(() => {})
}

export interface UseAuthResult {
  /** Current user, or undefined while loading / when signed out. */
  user: AuthUser | undefined
  isLoading: boolean
  /** `GET /api/auth/me` failed (non-401: 401 already cleared and redirected). */
  isError: boolean
  isAdmin: boolean
  me: UseQueryResult<AuthUser, Error>
  login: UseMutationResult<AuthUser, Error, LoginInput>
  setup: UseMutationResult<AuthUser, Error, SetupInput>
  logout: UseMutationResult<void, Error, void>
}

/**
 * Auth state and mutations backed by react-query. The current user lives in
 * the shared cache under `['auth', 'me']`; login/setup store the bearer token
 * (localStorage, see `apiClient`) before resolving, so every subsequent hub
 * request — REST and WebSocket — is authenticated. Logout revokes the token
 * server-side, clears it locally, and wipes the whole query cache so no data
 * of the previous user survives into the next session.
 */
export function useAuth(): UseAuthResult {
  const queryClient = useQueryClient()

  const me = useQuery<AuthUser, Error>({
    queryKey: ['auth', 'me'],
    queryFn: () => apiClient.me(),
    // No token yet (login page): asking would just earn a 401.
    enabled: getStoredToken() !== null,
    retry: false,
  })

  const login = useMutation<AuthUser, Error, LoginInput>({
    mutationFn: async (input) => {
      const session = await apiClient.login(input)
      storeToken(session.token, session.expires_at)
      queryClient.setQueryData(['auth', 'me'], session.user)
      // Refetch whatever anonymous queries cached before sign-in.
      await queryClient.invalidateQueries()
      syncPushForCurrentUser()
      return session.user
    },
  })

  const setup = useMutation<AuthUser, Error, SetupInput>({
    mutationFn: async (input) => {
      const session = await apiClient.setup(input)
      storeToken(session.token, session.expires_at)
      queryClient.setQueryData(['auth', 'me'], session.user)
      await queryClient.invalidateQueries()
      syncPushForCurrentUser()
      return session.user
    },
  })

  const logout = useMutation<void, Error, void>({
    mutationFn: async () => {
      // Remove this browser's registration while the token still authorizes
      // the DELETE; a failed request still unsubscribes locally.
      await disablePushNotifications().catch(() => {})
      return apiClient.logout()
    },
    onSettled: () => {
      clearToken()
      queryClient.clear()
    },
  })

  return {
    user: me.data,
    isLoading: me.isLoading,
    isError: me.isError,
    isAdmin: me.data?.role === 'admin',
    me,
    login,
    setup,
    logout,
  }
}
