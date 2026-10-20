# AGENTS.md — FleetDeck

> Web control plane for AI agent sessions across machines. Hub (Go) ↔ Node daemons (Go, thin WS) ↔ Web UI (React + antd + Refine). Postgres via sqlc.

## Toolchain

- **Go 1.27** (`go.mod`), **Node 22** + **pnpm**, **devenv** for local dev shell + Postgres
- Go deps: `pgx/v5`, `gorilla/websocket`, `invopop/jsonschema`, `modelcontextprotocol/go-sdk v1.8.0`, `golang.org/x/crypto` (bcrypt)
- Web deps: `react 19`, `react-router-dom 7`, `antd 5.29.3`, `@refinedev/{core,antd,react-router}`, `@tanstack/react-query 5`
- Lint/format: `gofmt`, `goimports`, `golangci-lint`, `oxlint`, `prettier`, `nixfmt-rfc-style`; `treefmt.toml` wires goimports/prettier/nixfmt for `treefmt` (the pre-commit hook runs `treefmt --fail-on-change --no-cache`, style in `.prettierrc.json`)

## Local dev (devenv)

The dev shell wires Postgres + hub + node + web as `process-compose` processes. **Everything below assumes you are inside `devenv shell` (or `nix develop`).** All ports/credentials come from `.env` (copy `.env.example` to customize; when `.env` is absent the example values are used) — `devenv.nix` derives `FLEETDECK_DATABASE_URL` / `FLEETDECK_REDIS_URL` / `FLEETDECK_HUB_ADDR` from it, nothing is hardcoded.

```sh
devenv up            # hub + node + web (native process manager)
devenv up hub node web fake-node  # + a second node wired to the fake adapter
db-up                # wait for Postgres on $FLEETDECK_DB_PORT
migrate              # goose up
db-reset             # dropdb + createdb + migrate (fresh schema)
hub-health           # curl /api/health
probe-adapters       # which agent CLIs (pi / codex / npx) are present
```

Local URLs (defaults from `.env.example`; override in `.env`):

| service  | address                                                                                     |
| -------- | ------------------------------------------------------------------------------------------- |
| Postgres | `127.0.0.1:55432`, db `fleetdeck`, user/pass `fleetdeck/fleetdeck`                          |
| Redis    | `127.0.0.1:56379`, cache-only (`FLEETDECK_REDIS_URL`; hub degrades to DB reads when absent) |
| hub      | `http://127.0.0.1:7710` (HTTP + WS)                                                         |
| MCP      | stdio only — `cmd/fleetdeck-mcp` is launched by the agent; no port                          |
| web      | `http://127.0.0.1:7711`                                                                     |

Env vars (set by `devenv.nix`): `FLEETDECK_DATABASE_URL`, `FLEETDECK_REDIS_URL`, `FLEETDECK_HUB_ADDR`, `FLEETDECK_NODE_TOKEN_FILE`, `FLEETDECK_SCHEMA_OUT`. The first three are derived from `.env`; `fleetdeck-mcp` additionally requires `FLEETDECK_HUB_URL` (its REST base URL — set it in the agent's MCP config, see `examples/fleetdeck-mcp.config.yaml`), and the hub/node/mcp binaries refuse to start when their required vars are missing (no baked-in addresses or credentials).

## Verification commands

Order matters; do all of these before pushing.

```sh
# Backend
go build ./...
go vet ./...
gofmt -l cmd/ internal/        # must be empty
go test -race ./...            # 138 passed / 16 packages (1 skip; live DB required)
go test -short ./...           # skip live-DB tests

# Frontend (cd web)
pnpm exec tsc -b               # real typecheck (bare `tsc --noEmit` is a no-op on the solution tsconfig; `pnpm build` also runs it)
pnpm lint
pnpm test                      # vitest (101 tests: dispatchEvent contract, auth, i18n, lifecycle, usage stats, notifications, binding UI, dashboard, admin shell, add-node flow, components)
pnpm build
```

Composite (from devenv shell): `check` runs gofmt + vet + golangci-lint + `go test -race`.

## Live tests need Postgres

`go test ./...` hits Postgres at `$FLEETDECK_DATABASE_URL` (devenv shell derives it from `.env`; when unset, live-DB tests skip). Tests that touch `internal/store/`, `internal/hub/`, `internal/mcp/` need Postgres reachable to actually run. Use `db-reset` first, or `db-up && migrate` if the schema is intact.

**The suite wipes rows (e.g. `DELETE FROM users`) — run it against a scratch DB to keep dev data intact.** Every test package honors `FLEETDECK_DATABASE_URL`; create the DB with the devenv bootstrap superuser (the OS username role, e.g. `yswtrue`), migrate, then point the suite at it:

```sh
psql -h "$FLEETDECK_DB_HOST" -p "$FLEETDECK_DB_PORT" -U "$USER" -d postgres -c "CREATE DATABASE ${FLEETDECK_DB_NAME}_test OWNER $FLEETDECK_DB_USER"
TEST_DSN="postgres://$FLEETDECK_DB_USER:$FLEETDECK_DB_PASSWORD@$FLEETDECK_DB_HOST:$FLEETDECK_DB_PORT/${FLEETDECK_DB_NAME}_test?sslmode=disable"
goose -dir migrations postgres "$TEST_DSN" up
FLEETDECK_DATABASE_URL="$TEST_DSN" go test -race ./...
```

## Architecture invariants

These are locked. Do not change without a Gate review.

- **Single wire contract**: Go structs in `internal/protocol/` are the source of truth. `cmd/fleetctl schema export` → `web/src/gen/protocol/protocol.schema.json` → `pnpm run gen:types` → `web/src/gen/protocol/index.ts`. Run `gen` (or `gen-schema && gen-web-types`) after touching the protocol.
- **Hub is the sole DB writer**. Node daemons never touch Postgres; they speak to the hub over `ws://hub/ws/node`.
- **All `/api/*` + `/ws/web` require Bearer auth** (`internal/hub/{middleware,auth_handlers}.go`): roles `admin`/`user`; authorization is **user↔node binding** (`node_members`, set by admins on the user page) with subtree inheritance — a bound user owns everything under the node (workspaces, tasks, sessions, events). Invisible reads → 404, unauthorized writes → 403; non-admins see only bound nodes. The `/ws/web` hello frame and every broadcast are scoped by the same binding per connection (unresolvable events are admin-only). Open: `/api/health`, `/api/auth/{status,setup,login}`, SPA static. `/ws/node` registration: a name that has never registered must present a single-use join token (`POST /api/nodes/join-tokens`, any signed-in user; or the deployment secret `FLEETDECK_NODE_JOIN_TOKEN`), the token's creator is bound to the new node, and only then is the node token issued (reconnects use it). There is no `POST /api/nodes`; `/ws/web` takes `?token=`. Auth is HTTP-layer — never add it to `internal/protocol`. The MCP client sends `FLEETDECK_HUB_TOKEN`.
- **Adapter interface** lives in `internal/adapters/adapter.go` (`Adapter` + `EventSink` with TextDelta / TodoUpdate / StateChange / UserInputRequested / Done / Error / Message). Real adapters (`internal/adapters/acp/`) and the fake one share this contract.
- **WebSocket envelope + Msg\* constants** live in `internal/protocol/transport.go` (not `internal/node/`). Do not import `internal/node` from `internal/hub/`.
- **Shared QueryClient**: Refine is wired to the **same** `web/src/queryClient.ts` via `options.reactQuery.clientConfig`. Two caches break WS-driven `setQueryData` invalidation.
- **No `liveProvider`**: `dispatchEvent.ts` is the typed WS dispatcher. Refine's `liveProvider` is CRUD-shaped and can't carry streaming deltas / user-input pauses / todo snapshots.
- **`@refinedev/react-router@2.0.4` exports only `routerProvider` + helpers** — no `<Routes>` / `useNavigate`. Route definitions stay on RR7.
- **Dark-first theming**: `AntdProvider` swaps `theme.darkAlgorithm` ⇄ `defaultAlgorithm` based on `matchMedia('(prefers-color-scheme: dark)')`. `index.css` mirrors the OS preference. 5-status palette (neutral/blue/green/amber/red) lives in `providers/theme.ts`.

## Layout cheatsheet

```
cmd/{hub,node,fleetctl,fleetdeck-mcp}/   # entrypoints
internal/protocol/                       # wire contract (Go structs + transport.go)
internal/store/                          # sqlc + pgx; queries + WithTx
internal/cache/                          # read-through cache (Redis + Noop; FLEETDECK_REDIS_URL)
internal/hub/                            # HTTP API + auth (middleware.go, auth_handlers.go) + WS broadcast + node-offline cascade; serves the embedded web UI (static/ is the go:embed root)
internal/node/                           # thin WS client; runs adapters in-process
internal/adapters/{adapter,registry}.go # Adapter/EventSink interfaces
internal/adapters/fake/                  # dev/integration fake
internal/adapters/acp/                   # shared ACP adapter (codex/claude/opencode/pi)
internal/adapters/all/                   # blank-import all adapters (cmd/node only)
internal/mcp/                            # MCP server package
migrations/                              # goose SQL, -- +goose Up/Down
web/src/{pages,components,hooks}/        # React UI (antd + Refine)
web/src/{gen/protocol/,apiClient.ts,dataProvider.ts,providers/}
web/src/i18n/                           # react-i18next bootstrap + locales/{en,zh-CN}/*.json
web/src/hooks/{useWebSocket,dispatchEvent}.ts  # locked Lane C code
scripts/                                 # smoke scripts
.devenv/state/fleetdeck/                 # local: node token, event buffer (gitignored)
docs/IMPLEMENTATION.md                   # design source of truth
```

## Smoke scripts

```sh
bash scripts/smoke_send_message.sh     # hub + node end-to-end, user reply path
bash scripts/smoke_mcp_subtask.sh      # REST + MCP stdio, subtask endpoint
bash scripts/smoke_acp_codex.sh        # ACP codex path (SKIP if codex not installed)
bash scripts/smoke_acp_opencode.sh     # ACP opencode path (SKIP if opencode not installed;
                                       # set FLEETDECK_SMOKE_OPENCODE_MODEL=provider/model for a full model round trip)
```

## Gotchas

- **Terminal `EventSink` signals are `Done` / `Error`** — they carry the session's final state (connSink maps them to `session.state_changed`). Do NOT pair them with a terminal `StateChange`; that double-broadcasts the transition. `StateChange` is for non-terminal transitions only.
- `PATCH /api/tasks/{id}` enforces the manual transition table (`canTransitionTask` in `internal/hub/api.go`): the window is `running → waiting|done` and `awaiting_user → running|done`; `waiting` is start+archive only (dispatch owns `waiting → running`), `awaiting_user` is entered only by the agent's session derivation, `done` is terminal, same-status is idempotent, illegal moves are 409. The internal session→task derivation does not go through it — session resume reopens done tasks via `moveTaskTo` (see the lifecycle gotcha).
- `GET /api/tasks` filters: exactly one of `workspace_id` / `node_id`; `q=term` switches to pg_trgm fuzzy matching (substring OR trigram similarity, best match first) over task titles.
- Hub `Event.Seq` is the **hub-wide broadcast ordinal** (for WS live gap detection). `store.Event.Seq` is **per-session** (for `/api/sessions/{id}/events?after_seq=` gap-fill). They are intentionally different; do not unify.
- `web/src/hooks/useTaskBoard.ts` cache key is `['tasks', workspaceId]` (an active search extends it with the query). Code that creates tasks must invalidate **both** that prefix and `['tasks-by-workspace']` (the `WorkspaceList` task-count key) — see `NewTaskForm.tsx`.
- Go tests in `internal/store/store_test.go` and `internal/hub/{hub,api}_test.go` read `FLEETDECK_DATABASE_URL` (no baked-in DSN — they skip when it is unset). They also skip with `-short`.
- **`internal/hub/static/` is the web UI embed root.** `go build` works with just the committed `.keep`, but a hub that serves the UI needs `web/dist` copied there first (the Dockerfile does this). The SPA also reads `web/public/config.js` at runtime for a custom hub URL (standalone static deploys).
- Web tests: Vitest + RTL (`pnpm test`) covering `dispatchEvent`, `permission.ts`, `PermissionPicker`, `MessageBubble`, `TaskCard` (status moves + toasts, no-session feedback), `apiClient` (token lifecycle, 401 policy), `Login` (setup-vs-login mode), `Users` (node-binding UI), `Dashboard` (today's task/token stats), `AppLayout` (sider menu, admin-gated entry, mobile drawer), `LanguageSwitcher` (language switch + persistence), plus pure hub SPA-handler tests. RTL auto-cleanup is wired in `src/test/setup.ts` (vitest runs without globals).
- **antd v5 static `message.*`/`Modal.confirm` render nothing under React 19** — the official `@ant-design/v5-patch-for-react-19` is imported first in `web/src/main.tsx`; components may also use `message.useMessage()` + `contextHolder`. Do not remove the patch import.
- **react-query v5 drops per-`mutate` callbacks when the caller unmounts** (`hasListeners()` gate). Use `mutateAsync().then()/.catch()` when the outcome must survive unmount (e.g. toast + navigate after a board-column move).
- **Session content is never persisted server-side (privacy model)**: the `message` table is gone. Complete messages and the pending ask's prompt live only in the hub's in-memory text buffer (`internal/hub/textbuffer.go`, 24h TTL, lost on hub restart); `event` rows hold status-class events only — `user_input_requested` persists with an empty `prompt` (the web event contract is unchanged; the real ask travels live and in the session snapshot's `pending_input`). `GET /api/sessions/{id}/messages` (`internal/hub/history.go`) replays the agent-side session through the owning node for finished sessions (`MsgSessionHistory`/`MsgSessionHistoryResp`, `adapters.HistoryReader`, ACP `session/load` without prompt), serves the buffer for live ones, and returns 503 when neither can answer. Replayed messages carry zero timestamps (`MessageBubble` hides the time) and replay never advances the session.
- **Node onboarding** (`internal/hub/join_tokens.go`): `POST /api/nodes/join-tokens` mints a 15-minute, single-use pairing token (sha256 at rest, plaintext shown once). An unknown node name without a valid token is rejected in the `/ws/node` handshake with `register_response.error`; a valid one creates the node, binds the token's creator (`node_members`) and burns the token in one transaction. `FLEETDECK_NODE_JOIN_TOKEN` on the **hub** is a deployment-level alternative (devenv exports a dev value) and binds no owner. The node daemon takes the token from `--join-token` (or the `join_token` config key) — never from the environment — and sends it as `register_request.JoinToken`; `--hub-url` is written back into the `--config` file after the first successful registration, so later starts need only `--config`. The advertised agents are edited with `fleetdeck-node config` (`--agents a,b --default a` for scripts): it probes launchers via `acp.Requirements`, then rewrites `agent_types` + `default_adapter` while preserving the rest of the file; re-run it after installing another agent CLI and restart the daemon. Without `--config`, both the daemon and the wizard use the platform config file — `$XDG_CONFIG_HOME/fleetdeck/node.yaml` (`~/.config/...`) on Linux, `~/Library/Application Support/fleetdeck/node.yaml` on macOS, `%AppData%\fleetdeck\node.yaml` on Windows; `XDG_CONFIG_HOME` is honored on every OS when absolute — and create it when absent (dir `0700`, file `0600`, `name` = hostname, `token_file` beside it). An explicit `--config` path that does not exist is an error. The default credential path is `$XDG_STATE_HOME/fleetdeck/node-token` (`~/.local/state/...`) on Linux, the app-support directory on macOS, `%LocalAppData%\fleetdeck\node-token` on Windows.
- **One node per computer**: the daemon takes a machine-wide OS file lock (`lock_file`, default `$TMPDIR/fleetdeck-node.lock`; `FLEETDECK_NODE_LOCK_FILE`) before connecting, so a second process exits with the holder's pid. Independently, every handshake carries `register_request.MachineID` (sha256 of the OS machine id; override `machine_id`/`FLEETDECK_NODE_MACHINE_ID`), and `node.machine_id` is unique (partial index): a second node name on the same machine is rejected with the existing node's name, and a copied node token cannot reconnect from another machine. `examples/fake-node.config.yaml` overrides both for the dev-only second demo node — do not copy that into production.
- **Node offline event buffering** (`FLEETDECK_EVENT_BUFFER_DIR`, `internal/node/buffer.go`): when set, every outbound envelope is persisted first (one JSON file per event, atomic tmp+rename) and the writer replays the queue in sequence order across reconnects — FIFO, at-least-once, files deleted only after a successful write. When unset, events are dropped while the hub is unreachable (the old behavior). Sessions survive hub restarts either way (adapter ctx is process-level).
- **Token usage**: adapters report per-turn usage via `EventSink.Usage` → `EvSessionUsage` → `token_usage` rows (model + prompt/completion/total). `GET /api/stats/tokens` aggregates per model + totals (node-binding scoped). Model comes from the ACP `session/new` `configOptions` (`model` currentValue); agents that report no model fall back to `agent_type`, agents that report no usage record nothing (never fabricate). The locked `dispatchEvent.ts` returns null for `session.usage` (harmless — the usage UI reads via react-query).
- **Stats endpoints + admin shell**: `GET /api/stats/tasks` counts `created`/`completed` plus the `by_status` split of the window's tasks; it shares `GET /api/stats/tokens`' filter/visibility contract (`workspace_id`/`node_id`/`from`/`to` RFC3339, invisible explicit ids → 404). The Dashboard (`/`, "today" window) reads both; the node list lives at `/nodes`. `web/src/components/AppLayout.tsx` is the shell: collapsible left sider (icon rail when collapsed), drawer menu below 768px. Menu icons MUST forward antd's injected `className`/`style` (`cloneElement` adds `ant-menu-item-icon`, and collapsed icon-only rendering hides labels via `ant-menu-item-icon + span`) — drop the class and the collapsed menu clips labels instead of hiding them.
- **Task notifications**: `useTaskNotifications` (mounted as `TaskNotificationBridge` in App) fires browser Notifications on `task.created`/`task.status_changed`; gated by `Notification.permission === 'granted'` AND `localStorage('fleetdeck.notify') === 'on'` (values are `'on'`/`'off'`, not `'true'`), coalesced per task via `tag`, click focuses the tab.
- **Mobile**: layout is responsive at ≤768px (columns stack, sidebar stacks under the transcript, modals fit, ≥36px touch targets on coarse pointers) — keep new surfaces working at 320px+.
- **Smoke scripts wipe rows in their target DB**: `scripts/smoke_acp_opencode.sh` runs `delete from users` (deterministic setup) and deletes its node on exit (cascading its workspaces/tasks/sessions/usage). Point them at a scratch DB or expect dev data loss. They require `FLEETDECK_DATABASE_URL` (run inside `devenv shell`) and take `SMOKE_HUB_ADDR` to override their hub port.
- **Task lifecycle**: `POST /api/tasks` takes `auto_start:false` for backlog tasks (created but never dispatched until `POST /api/tasks/{id}/start`); `PATCH /api/tasks/{id} {archived:true}` archives `waiting`/`done` tasks — in-flight states 409 and must be resolved first (hidden from default board lists; `?archived=1` lists only archived); `POST /api/sessions/{id}/send` also reaches `done`/`failed` sessions — the task reopens `running`, the node reattaches via `MsgSessionResume` (`session/load`; unresumable sessions restart fresh with the follow-up as prompt). A completion gate parks sessions ending with unfinished mirrored todos in `awaiting_user` instead of `done`.
- **Read-through cache** (`FLEETDECK_REDIS_URL`, `internal/cache`): auth token→user resolution (60s TTL) and per-workspace task lists (30s TTL) are cached in Redis; logout and every task mutation delete their entries (`invalidateTasks`), TTLs bound any missed invalidation. An unset URL is a Noop cache and cache errors always degrade to a database read — never make the cache a request-failure source. Fuzzy search and node-scoped lists (binding-filtered) are deliberately uncached.

- **Web image pipeline**: imported images are resized/re-encoded at build time by `vite-imagetools` (query directives on the import, e.g. `Header.tsx`'s `?w=64&format=webp`); every used directive combo needs a literal `declare module '*.png?…'` entry in `web/src/vite-env.d.ts`. HTML `<link>` references bypass the plugin (Vite's HTML asset pipeline emits the raw file) — the favicon is therefore a small static `web/public/favicon.png`, and the full-res design source lives in `web/src/assets/`. Don't put display-size images in `public/` (copied verbatim, never processed).

## Web copy / i18n

All user-visible UI copy goes through react-i18next (`web/src/i18n/`). Languages: `en` + `zh-CN`; namespaces
`common` (shared atoms: `status.*`, `actions.*`, `time.*`, `misc.*`), `nav`, `auth`, `pages`, `board`, `session`.
Components use `useTranslation('<ns>')`; non-React code uses `i18n.t('<ns>:key')`. **English locale values are the
canonical copy** (tests pin them) — add every key to BOTH locale files. The header and login card carry a
`LanguageSwitcher` (persists to `localStorage('fleetdeck.lang')`; `<html lang>` follows). `util.ts`
(`statusLabel`, `formatRelative`) is already localized — keep calling it. antd strings come from the
ConfigProvider locale wired in `providers/AntdProvider.tsx`.

## Adding a new adapter

1. Add `internal/adapters/<name>/` with `Adapter` implementation.
2. Register via `init()` in `internal/adapters/<name>/<name>.go` calling `adapters.Default.Register("<key>", func() adapters.Adapter { return New() })`.
3. Add the package to `internal/adapters/all/all.go`'s blank imports — this is what `cmd/node` compiles in (`cmd/hub` registers nothing).

## Adding a protocol field

1. Edit the struct in `internal/protocol/{model,messages}.go`.
2. Add `jsonschema:"enum=..."` tags for status/role fields (keeps TS unions generated).
3. Run `gen` to regenerate `web/src/gen/protocol/*`.
4. Run `go build ./... && pnpm build`.

## Deferred (do NOT do unless explicitly asked)

The Phase 6 backlog is **fully implemented** (permission picker UI, `GET /api/tasks?node_id=` + `?q=` title search, ACP fs capability negotiation, Gate 4 m1-m7 cleanup, StateChange dedup, Vitest, session-route code splitting, `POST /api/nodes` capabilities fix, WorkspaceList invalidation, em-dash copy, card keyboard reachability, `fleetctl` `--out` flag order, PATCH transition table, dispatchTask transaction). Do not redo them. Remaining known gaps (fix only on request): ACP `terminal/*`/elicitation support (deliberately refused and unadvertised), subtask reverse-writeback to agents, task `cancelled_at` audit column. Multi-user auth is **implemented** (users/auth_tokens/node_members, Bearer sessions, user↔node binding with subtree visibility, `/login` `/users` + node-binding UI) — do not rebuild it.
