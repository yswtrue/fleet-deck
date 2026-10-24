import type { Message, Node, Session, Subtask, Task, Workspace } from './gen/protocol/index.ts'

/**
 * StoredEvent mirrors the hub's /api/sessions/{id}/events DTO: the persisted
 * event row, with the JSONB payload exposed raw under `payload` (distinct from
 * the live WS protocol.Event, whose payload field is `data`).
 */
export interface StoredEvent {
  id: string
  session_id: string
  type: string
  payload: unknown
  seq: number
  ts: string
}

/**
 * SessionSnapshot is the body of GET /api/sessions/{id}: the session plus its
 * owning task (null when the task row cannot be resolved). PendingInput is the
 * prompt of the session's unanswered user-input ask ("" when none) — content,
 * served from the hub's ephemeral buffer so a reload mid-pause can restore the
 * picker.
 */
export interface SessionSnapshot {
  session: Session
  task: Task | null
  pending_input: string
  /** Streaming assistant text of the in-progress turn ("" when none). */
  partial_output: string
  /** In-progress reasoning trace (bounded; "" when none). */
  partial_thought: string
}

/** POST /api/nodes/join-tokens response: the plaintext token is shown once. */
export interface NodeJoinTokenResponse {
  token: string
  expires_at: string
}

/** One browser Web Push registration (see POST /api/push/subscriptions). */
export interface PushSubscriptionInput {
  endpoint: string
  /** Receiver public key, base64url uncompressed P-256. */
  p256dh: string
  /** 16-byte auth secret, base64url. */
  auth: string
  /** UI language for the notification copy (`en` / `zh-CN`). */
  lang: string
}

export interface CreateWorkspaceInput {
  node_id: string
  name: string
  path: string
}

/** One directory the node's folder picker can descend into. */
export interface NodeDirEntry {
  name: string
  path: string
}

/**
 * GET /api/nodes/{id}/fs response: one directory on the node, its parent
 * (absent at a filesystem root) and the subdirectories inside it.
 */
export interface NodeDirListing {
  path: string
  parent?: string
  entries: NodeDirEntry[]
}

export interface CreateTaskInput {
  workspace_id: string
  /**
   * Optional board label. When omitted, the hub keeps a provisional title
   * derived from the prompt until the agent reports its own session title.
   */
  title?: string
  prompt?: string
  agent_type: string
  /** False creates a backlog task: it stays `waiting` until explicitly started. */
  auto_start?: boolean
}

export interface PatchTaskInput {
  status?: Task['status']
  /** Archive (true; only `done` tasks) or unarchive (false) the task. */
  archived?: boolean
}

/**
 * Options for the task list endpoints. Archived tasks are excluded by default;
 * pass `archived: true` to list only archived rows (`?archived=1`).
 */
export interface ListTasksOptions {
  q?: string
  archived?: boolean
}

// ── Token usage statistics ───────────────────────────────────────────────────

/**
 * Filters for {@link apiClient.getStatsTokens}. Every field is optional; an
 * unfiltered call aggregates every session the caller can see. `from`/`to`
 * are RFC3339 timestamps bounding the reporting window.
 */
export interface StatsTokensFilters {
  session_id?: string
  workspace_id?: string
  node_id?: string
  from?: string
  to?: string
}

/** Per-model roll-up row; `by_model` is sorted by `total_tokens` descending. */
export interface StatsModelUsage {
  model: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  /** Cache read/write tokens summed over turns that reported cache telemetry. */
  cached_read_tokens: number
  cached_write_tokens: number
  /** Non-cached input subtotal of cache-reporting turns (cache-ratio denominator). */
  cache_prompt_tokens: number
  /** Turns that carried cache telemetry; 0 means the model exposes none. */
  cache_reports: number
  turns: number
}

/** Body of GET /api/stats/tokens. */
export interface StatsTokens {
  total: {
    prompt_tokens: number
    completion_tokens: number
    total_tokens: number
    cached_read_tokens: number
    cached_write_tokens: number
    cache_prompt_tokens: number
    cache_reports: number
  }
  by_model: StatsModelUsage[]
}

/**
 * Filters for {@link apiClient.getStatsTasks}. Every field is optional; an
 * unfiltered call aggregates every task the caller can see. `from`/`to` are
 * RFC3339 timestamps bounding the reporting window.
 */
export interface StatsTasksFilters {
  workspace_id?: string
  node_id?: string
  from?: string
  to?: string
}

/** One status bucket of {@link StatsTasks}: the window's tasks by current status. */
export interface StatsTaskStatusCount {
  status: Task['status']
  count: number
}

/**
 * Body of GET /api/stats/tasks: `created` counts tasks opened in the window,
 * `by_status` splits those tasks by their current status, and `completed`
 * counts tasks that finished (reached `done`) inside the window.
 */
export interface StatsTasks {
  created: number
  completed: number
  by_status: StatsTaskStatusCount[]
}

// ── Auth (multi-user) ────────────────────────────────────────────────────────

export type UserRole = 'admin' | 'user'

export interface AuthUser {
  id: string
  email: string
  display_name: string
  role: UserRole
  created_at: string
}

/** AuthSession is the body of /api/auth/setup and /api/auth/login. */
export interface AuthSession {
  token: string
  expires_at: string
  user: AuthUser
}

/** AuthStatus is the body of GET /api/auth/status (unauthenticated). */
export interface AuthStatus {
  needs_setup: boolean
}

export interface SetupInput {
  email: string
  display_name: string
  password: string
}

export interface LoginInput {
  email: string
  password: string
}

export interface CreateUserInput {
  email: string
  display_name: string
  password: string
  role: UserRole
}

export interface PatchUserInput {
  display_name?: string
  role?: UserRole
  password?: string
}

declare global {
  interface Window {
    /** Runtime hub URL override, set by /config.js (see public/config.js). */
    __FLEETDECK_HUB_URL__?: string
  }
}

/**
 * Hub base URL resolution, in order:
 *   1. `window.__FLEETDECK_HUB_URL__` — runtime config for standalone static
 *      deployments (any host can point at any hub without a rebuild);
 *   2. `VITE_HUB_URL` — build-time pin (split hosting);
 *   3. `/` — the page's own origin: the hub serves the SPA itself in the
 *      default deployment, and the vite dev server proxies /api and /ws.
 * Trailing slashes are stripped; an absolute http(s) URL enables cross-origin
 * operation (the hub admits any origin in single-user mode).
 */
export const HUB_URL = (
  (typeof window !== 'undefined' && window.__FLEETDECK_HUB_URL__) ||
  import.meta.env.VITE_HUB_URL ||
  '/'
).replace(/\/$/, '')

/** WebSocket URL derived from {@link HUB_URL}; relative stays same-origin. */
export const HUB_WS_URL = HUB_URL.replace(/^http/, 'ws') + '/ws/web'

// ── Token persistence ────────────────────────────────────────────────────────
// The bearer token lives in localStorage so every hub request (including
// cross-origin ones when `window.__FLEETDECK_HUB_URL__` points elsewhere) can
// carry it without React involvement. `expires_at` is kept alongside so an
// expired token is treated as absent before the hub ever sees it.

const TOKEN_KEY = 'fleetdeck.token'
const EXPIRES_KEY = 'fleetdeck.expires_at'

type TokenListener = (token: string | null) => void
const tokenListeners = new Set<TokenListener>()

function notifyTokenChange(token: string | null) {
  for (const listener of [...tokenListeners]) listener(token)
}

/**
 * Subscribe to token store changes (login/logout/expiry). Used by
 * `WebSocketProvider` to reconnect with (or without) the token. Returns the
 * unsubscribe function.
 */
export function onTokenChange(listener: TokenListener): () => void {
  tokenListeners.add(listener)
  return () => {
    tokenListeners.delete(listener)
  }
}

/** Current bearer token, or null when absent or expired. */
export function getStoredToken(): string | null {
  if (typeof localStorage === 'undefined') return null
  const token = localStorage.getItem(TOKEN_KEY)
  if (!token) return null
  const expiresAt = localStorage.getItem(EXPIRES_KEY)
  if (expiresAt && Date.parse(expiresAt) <= Date.now()) {
    clearToken()
    return null
  }
  return token
}

export function storeToken(token: string, expiresAt: string): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(TOKEN_KEY, token)
  localStorage.setItem(EXPIRES_KEY, expiresAt)
  notifyTokenChange(token)
}

export function clearToken(): void {
  if (typeof localStorage === 'undefined') return
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(EXPIRES_KEY)
  notifyTokenChange(null)
}

/**
 * The hub WebSocket URL with the current token attached as `?token=` (the hub
 * accepts the auth token as a query param on /ws/web). Unauthenticated when no
 * token is stored.
 */
export function authenticatedWsUrl(): string {
  const token = getStoredToken()
  return token ? `${HUB_WS_URL}?token=${encodeURIComponent(token)}` : HUB_WS_URL
}

/**
 * Pure: a 401 must send the user to /login — except when already there, so a
 * failed sign-in can never loop the redirect.
 */
export function shouldRedirectToLogin(pathname: string): boolean {
  return pathname !== '/login'
}

/** Clear the token and send the user to /login (never re-redirect from /login). */
function handleUnauthorized(): void {
  clearToken()
  if (typeof window === 'undefined') return
  if (!shouldRedirectToLogin(window.location.pathname)) return
  window.location.assign('/login')
}

async function fetchJson<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getStoredToken()
  const res = await fetch(`${HUB_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers ?? {}),
    },
  })
  if (!res.ok) {
    // Expired/revoked token: drop it and bounce to the login page.
    if (res.status === 401) handleUnauthorized()
    // Surface the hub's {"error": "..."} body so callers can show a useful
    // message (e.g. the 409 "session is not awaiting user input").
    let detail = res.statusText
    try {
      const body = (await res.json()) as { error?: string }
      if (body?.error) detail = body.error
    } catch {
      // keep statusText
    }
    throw new Error(`HTTP ${res.status}: ${detail}`)
  }
  // 204 and other empty bodies (logout, delete) have no JSON to parse.
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

/**
 * apiClient is the plain (non-hook) hub REST client. It is the single place the
 * wire contract lives: every path/query below mirrors a hub endpoint and must
 * not change without a matching backend change. Consumed by `useHubApi` (thin
 * hook) and by Refine's `dataProvider`.
 */
export const apiClient = {
  baseUrl: HUB_URL,

  // ── Auth ──────────────────────────────────────────────────────────────────

  /** GET /api/auth/status needs no token; it selects login vs first-run setup. */
  authStatus: () => fetchJson<AuthStatus>('/api/auth/status'),

  /** POST /api/auth/setup creates the first admin; 409 once any user exists. */
  setup: (input: SetupInput) =>
    fetchJson<AuthSession>('/api/auth/setup', { method: 'POST', body: JSON.stringify(input) }),

  login: (input: LoginInput) =>
    fetchJson<AuthSession>('/api/auth/login', { method: 'POST', body: JSON.stringify(input) }),

  /** POST /api/auth/logout revokes the token server-side (204). */
  logout: () => fetchJson<void>('/api/auth/logout', { method: 'POST' }),

  me: () => fetchJson<AuthUser>('/api/auth/me'),

  // ── Users (admin) ─────────────────────────────────────────────────────────

  listUsers: () => fetchJson<AuthUser[]>('/api/users'),

  createUser: (input: CreateUserInput) =>
    fetchJson<AuthUser>('/api/users', { method: 'POST', body: JSON.stringify(input) }),

  patchUser: (id: string, patch: PatchUserInput) =>
    fetchJson<AuthUser>(`/api/users/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),

  deleteUser: (id: string) =>
    fetchJson<void>(`/api/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // ── User ↔ node bindings (admin) ──────────────────────────────────────────

  /** GET /api/users/{id}/nodes returns the nodes bound to the user. */
  listUserNodes: (userId: string) =>
    fetchJson<Node[]>(`/api/users/${encodeURIComponent(userId)}/nodes`),

  /** PUT /api/users/{id}/nodes replaces the whole binding set; the response
   * carries the resulting bound nodes. */
  setUserNodes: (userId: string, nodeIds: string[]) =>
    fetchJson<Node[]>(`/api/users/${encodeURIComponent(userId)}/nodes`, {
      method: 'PUT',
      body: JSON.stringify({ node_ids: nodeIds }),
    }),

  getHealth: () => fetchJson<{ status: string; version?: string }>('/api/health'),

  // ── Web Push ──────────────────────────────────────────────────────────────

  /** GET /api/push/public-key returns the VAPID applicationServerKey. */
  getPushPublicKey: () => fetchJson<{ public_key: string }>('/api/push/public-key'),

  /** POST /api/push/subscriptions registers (or re-owns) this browser. */
  savePushSubscription: (sub: PushSubscriptionInput) =>
    fetchJson<void>('/api/push/subscriptions', {
      method: 'POST',
      body: JSON.stringify(sub),
    }),

  /** DELETE /api/push/subscriptions?endpoint= removes this browser's row. */
  deletePushSubscription: (endpoint: string) =>
    fetchJson<void>(`/api/push/subscriptions?endpoint=${encodeURIComponent(endpoint)}`, {
      method: 'DELETE',
    }),

  listNodes: () => fetchJson<Node[]>('/api/nodes'),

  getNode: (id: string) => fetchJson<Node>(`/api/nodes/${id}`),

  /**
   * GET /api/nodes/{id}/fs lists the directories on the node; an empty path
   * starts at the node user's home directory. 503 when the node is offline.
   */
  listNodeDirs: (nodeId: string, path?: string) =>
    fetchJson<NodeDirListing>(
      `/api/nodes/${encodeURIComponent(nodeId)}/fs${path ? `?path=${encodeURIComponent(path)}` : ''}`
    ),

  /** Mint a single-use pairing token for a node's first registration. */
  createNodeJoinToken: () =>
    fetchJson<NodeJoinTokenResponse>('/api/nodes/join-tokens', { method: 'POST' }),

  listWorkspaces: (nodeId: string) =>
    fetchJson<Workspace[]>(`/api/workspaces?node_id=${encodeURIComponent(nodeId)}`),

  getWorkspace: (id: string) => fetchJson<Workspace>(`/api/workspaces/${id}`),

  createWorkspace: (workspace: CreateWorkspaceInput) =>
    fetchJson<Workspace>('/api/workspaces', { method: 'POST', body: JSON.stringify(workspace) }),

  /** PATCH /api/workspaces/{id} with a new display name. */
  renameWorkspace: (id: string, name: string) =>
    fetchJson<Workspace>(`/api/workspaces/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ name }),
    }),

  /** DELETE /api/workspaces/{id} removes the workspace and its tasks/sessions;
   * files on the node are untouched. */
  deleteWorkspace: (id: string) =>
    fetchJson<void>(`/api/workspaces/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  /** PATCH /api/workspaces/{id} sets the workspace's permission auto-allow
   * range (applies to sessions started or resumed afterwards). */
  setWorkspaceAutoAllowScope: (id: string, scope: Workspace['auto_allow_scope']) =>
    fetchJson<Workspace>(`/api/workspaces/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ auto_allow_scope: scope }),
    }),

  listTasks: (workspaceId: string, q?: string, options?: ListTasksOptions) =>
    fetchJson<Task[]>(
      `/api/tasks?workspace_id=${encodeURIComponent(workspaceId)}${(options?.q ?? q) ? `&q=${encodeURIComponent(options?.q ?? (q as string))}` : ''}${options?.archived ? '&archived=1' : ''}`
    ),

  /** GET /api/tasks?node_id= spans every workspace on the node in one call. */
  listTasksByNode: (nodeId: string, options?: ListTasksOptions) =>
    fetchJson<Task[]>(
      `/api/tasks?node_id=${encodeURIComponent(nodeId)}${options?.archived ? '&archived=1' : ''}`
    ),

  createTask: (task: CreateTaskInput) =>
    fetchJson<Task>('/api/tasks', { method: 'POST', body: JSON.stringify(task) }),

  /**
   * POST /api/tasks/{id}/start dispatches a waiting task (the same dispatch
   * the hub runs on creation when `auto_start` is true). 409 when the task
   * was already started or is no longer waiting.
   */
  startTask: (id: string) => fetchJson<Task>(`/api/tasks/${id}/start`, { method: 'POST' }),

  patchTask: (id: string, patch: PatchTaskInput) =>
    fetchJson<Task>(`/api/tasks/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  getSession: (id: string) => fetchJson<SessionSnapshot>(`/api/sessions/${id}`),

  /**
   * GET /api/sessions/{id}/messages returns the whole transcript unless a
   * limit is given; a limit windows the newest N rows. The session view wants
   * everything (tool calls are rows, so a real task exceeds a single page).
   */
  getMessages: (sessionId: string, limit?: number) =>
    fetchJson<Message[]>(
      `/api/sessions/${sessionId}/messages${limit && limit > 0 ? `?limit=${limit}` : ''}`
    ),

  getEvents: (sessionId: string, afterSeq?: number, limit = 100) =>
    fetchJson<StoredEvent[]>(
      `/api/sessions/${sessionId}/events?${afterSeq !== undefined ? `after_seq=${afterSeq}&` : ''}limit=${limit}`
    ),

  /** GET /api/tasks/{id}/subtasks returns a {"subtasks":[...]} envelope; unwrap it. */
  listSubtasks: async (taskId: string): Promise<Subtask[]> =>
    (await fetchJson<{ subtasks: Subtask[] }>(`/api/tasks/${taskId}/subtasks`)).subtasks,

  // ── Token usage statistics ──────────────────────────────────────────────────

  /**
   * GET /api/stats/tokens aggregates token usage over the optional filters
   * (session, workspace, node, RFC3339 time window); absent filters are left
   * out of the query string so the hub sums everything visible.
   */
  getStatsTokens: (filters?: StatsTokensFilters) => {
    const params = new URLSearchParams()
    for (const [key, value] of [
      ['session_id', filters?.session_id],
      ['workspace_id', filters?.workspace_id],
      ['node_id', filters?.node_id],
      ['from', filters?.from],
      ['to', filters?.to],
    ] as const) {
      if (value) params.set(key, value)
    }
    const query = params.toString()
    return fetchJson<StatsTokens>(`/api/stats/tokens${query ? `?${query}` : ''}`)
  },

  /**
   * GET /api/stats/tasks aggregates task counts over the optional filters
   * (workspace, node, RFC3339 time window); absent filters are left out of the
   * query string so the hub counts everything visible.
   */
  getStatsTasks: (filters?: StatsTasksFilters) => {
    const params = new URLSearchParams()
    for (const [key, value] of [
      ['workspace_id', filters?.workspace_id],
      ['node_id', filters?.node_id],
      ['from', filters?.from],
      ['to', filters?.to],
    ] as const) {
      if (value) params.set(key, value)
    }
    const query = params.toString()
    return fetchJson<StatsTasks>(`/api/stats/tasks${query ? `?${query}` : ''}`)
  },

  sendMessage: (sessionId: string, message: string) =>
    fetchJson<void>(`/api/sessions/${sessionId}/send`, {
      method: 'POST',
      body: JSON.stringify({ message }),
    }),
}

export type HubApi = typeof apiClient
