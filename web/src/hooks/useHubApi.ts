import { apiClient } from '../apiClient.ts'

export { apiClient, HUB_URL } from '../apiClient.ts'
export type {
  CreateNodeInput,
  CreateTaskInput,
  CreateWorkspaceInput,
  HubApi,
  ListTasksOptions,
  PatchTaskInput,
  SessionSnapshot,
  StoredEvent,
} from '../apiClient.ts'

/**
 * Thin compatibility hook over the shared {@link apiClient} singleton. Callers
 * (useTaskBoard, useSession, WorkspaceList, Header, Home) keep calling
 * `useHubApi()` and receive the same object every render.
 */
export function useHubApi() {
  return apiClient
}
