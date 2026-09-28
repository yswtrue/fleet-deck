# AGENTS.md — FleetDeck

> Web control plane for AI agent sessions across machines. Hub (Go) ↔ Node daemons (Go, thin WS) ↔ Web UI (React + antd + Refine). Postgres via sqlc.

## Toolchain

- **Go 1.27** (`go.mod`), **Node 22** + **pnpm**, **devenv** for local dev shell + Postgres
- Go deps: `pgx/v5`, `gorilla/websocket`, `invopop/jsonschema`, `modelcontextprotocol/go-sdk v1.8.0`, `golang.org/x/crypto` (bcrypt)
- Web deps: `react 19`, `react-router-dom 7`, `antd 5.29.3`, `@refinedev/{core,antd,react-router}`, `@tanstack/react-query 5`
- Lint/format: `gofmt`, `goimports`, `golangci-lint`, `oxlint`, `prettier`, `nixfmt-rfc-style`; `treefmt.toml` wires goimports/prettier/nixfmt for `treefmt` (the pre-commit hook runs `treefmt --fail-on-change --no-cache`, style in `.prettierrc.json`)

## Local dev (devenv)

The dev shell wires Postgres + hub + node + web as `process-compose` processes. **Everything below assumes you are inside `devenv shell` (or `nix develop`).**

```sh
devenv up            # hub + node + web (process-compose)
devenv up fake-node  # + a second node wired to the fake adapter
db-up                # wait for Postgres on :55432
migrate              # goose up
db-reset             # dropdb + createdb + migrate (fresh schema)
hub-health           # curl /api/health
probe-adapters       # which agent CLIs (pi / codex / npx) are present
```

Local URLs (fixed by `devenv.nix`):

| service  | address                                                                                     |
| -------- | ------------------------------------------------------------------------------------------- |
| Postgres | `127.0.0.1:55432`, db `fleetdeck`, user/pass `fleetdeck/fleetdeck`                          |
| Redis    | `127.0.0.1:56379`, cache-only (`FLEETDECK_REDIS_URL`; hub degrades to DB reads when absent) |
| hub      | `http://127.0.0.1:7710` (HTTP + WS)                                                         |
| MCP      | stdio only — `cmd/fleetdeck-mcp` is launched by the agent; no port                          |
| web      | `http://127.0.0.1:7711`                                                                     |

Env vars (set by `devenv.nix`): `FLEETDECK_DATABASE_URL`, `FLEETDECK_REDIS_URL`, `FLEETDECK_HUB_ADDR`, `FLEETDECK_NODE_TOKEN_FILE`, `FLEETDECK_SCHEMA_OUT`.

## Verification commands

Order matters; do all of these before pushing.

```sh
# Backend
go build ./...
go vet ./...
gofmt -l cmd/ internal/        # must be empty
go test -race ./...            # 123 passed / 16 packages (1 skip; live DB required)
go test -short ./...           # skip live-DB tests

# Frontend (cd web)
pnpm exec tsc -b               # real typecheck (bare `tsc --noEmit` is a no-op on the solution tsconfig; `pnpm build` also runs it)
pnpm lint
pnpm test                      # vitest (90 tests: dispatchEvent contract, auth, i18n, lifecycle, usage stats, notifications, binding UI, components)
pnpm build
```

Composite (from devenv shell): `check` runs gofmt + vet + golangci-lint + `go test -race`.

## Live tests need Postgres

`go test ./...` hits Postgres at `$FLEETDECK_DATABASE_URL` (default: the dev DB on `127.0.0.1:55432`). Tests that touch `internal/store/`, `internal/hub/`, `internal/mcp/` will fail without Postgres reachable. Use `db-reset` first, or `db-up && migrate` if the schema is intact.

**The suite wipes rows (e.g. `DELETE FROM users`) — run it against a scratch DB to keep dev data intact.** Every test package honors `FLEETDECK_DATABASE_URL`; create the DB with the devenv bootstrap superuser (the OS username role, e.g. `yswtrue`), migrate, then point the suite at it:

```sh
psql -h 127.0.0.1 -p 55432 -U "$USER" -d postgres -c "CREATE DATABASE fleetdeck_test OWNER fleetdeck"
goose -dir migrations postgres postgres://fleetdeck:fleetdeck@127.0.0.1:55432/fleetdeck_test?sslmode=disable up
FLEETDECK_DATABASE_URL=postgres://fleetdeck:fleetdeck@127.0.0.1:55432/fleetdeck_test?sslmode=disable go test -race ./...
```

## Architecture invariants

These are locked. Do not change without a Gate review.

- **Single wire contract**: Go structs in `internal/protocol/` are the source of truth. `cmd/fleetctl schema export` → `web/src/gen/protocol/protocol.schema.json` → `pnpm run gen:types` → `web/src/gen/protocol/index.ts`. Run `gen` (or `gen-schema && gen-web-types`) after touching the protocol.
- **Hub is the sole DB writer**. Node daemons never touch Postgres; they speak to the hub over `ws://hub/ws/node`.
- **All `/api/*` + `/ws/web` require Bearer auth** (`internal/hub/{middleware,auth_handlers}.go`): roles `admin`/`user`; authorization is **user↔node binding** (`node_members`, set by admins on the user page) with subtree inheritance — a bound user owns everything under the node (workspaces, tasks, sessions, events). Invisible reads → 404, unauthorized writes → 403; non-admins see only bound nodes. The `/ws/web` hello frame and every broadcast are scoped by the same binding per connection (unresolvable events are admin-only). Open: `/api/health`, `/api/auth/{status,setup,login}`, SPA static. `/ws/node` keeps node-token register-frame auth (binding is management-plane, decoupled from registration); `/ws/web` takes `?token=`. Auth is HTTP-layer — never add it to `internal/protocol`. The MCP client sends `FLEETDECK_HUB_TOKEN`.
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
integrations/pi/                         # (deferred) pi bridge
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
- `PATCH /api/tasks/{id}` enforces the 4-state transition table (`canTransitionTask` in `internal/hub/api.go`): `done` is terminal, same-status is idempotent, illegal moves are 409. The internal session→task derivation does not go through it — session resume reopens done tasks via `moveTaskTo` (see the lifecycle gotcha).
- `GET /api/tasks` filters: exactly one of `workspace_id` / `node_id`; `q=term` switches to pg_trgm fuzzy matching (substring OR trigram similarity, best match first) over task titles.
- Hub `Event.Seq` is the **hub-wide broadcast ordinal** (for WS live gap detection). `store.Event.Seq` is **per-session** (for `/api/sessions/{id}/events?after_seq=` gap-fill). They are intentionally different; do not unify.
- `web/src/hooks/useTaskBoard.ts` cache key is `['tasks', workspaceId]` (an active search extends it with the query). Code that creates tasks must invalidate **both** that prefix and `['tasks-by-workspace']` (the `WorkspaceList` task-count key) — see `NewTaskForm.tsx`.
- Go tests in `internal/store/store_test.go` and `internal/hub/{hub,api}_test.go` use `127.0.0.1:55432`. They skip with `-short`.
- **`internal/hub/static/` is the web UI embed root.** `go build` works with just the committed `.keep`, but a hub that serves the UI needs `web/dist` copied there first (the Dockerfile does this). The SPA also reads `web/public/config.js` at runtime for a custom hub URL (standalone static deploys).
- Web tests: Vitest + RTL (`pnpm test`) covering `dispatchEvent`, `permission.ts`, `PermissionPicker`, `MessageBubble`, `TaskCard` (status moves + toasts, no-session feedback), `apiClient` (token lifecycle, 401 policy), `Login` (setup-vs-login mode), `Users` (node-binding UI), `LanguageSwitcher` (language switch + persistence), plus pure hub SPA-handler tests. RTL auto-cleanup is wired in `src/test/setup.ts` (vitest runs without globals).
- **antd v5 static `message.*`/`Modal.confirm` render nothing under React 19** — the official `@ant-design/v5-patch-for-react-19` is imported first in `web/src/main.tsx`; components may also use `message.useMessage()` + `contextHolder`. Do not remove the patch import.
- **react-query v5 drops per-`mutate` callbacks when the caller unmounts** (`hasListeners()` gate). Use `mutateAsync().then()/.catch()` when the outcome must survive unmount (e.g. toast + navigate after a board-column move).
- **Node offline event buffering** (`FLEETDECK_EVENT_BUFFER_DIR`, `internal/node/buffer.go`): when set, every outbound envelope is persisted first (one JSON file per event, atomic tmp+rename) and the writer replays the queue in sequence order across reconnects — FIFO, at-least-once, files deleted only after a successful write. When unset, events are dropped while the hub is unreachable (the old behavior). Sessions survive hub restarts either way (adapter ctx is process-level).
- **Token usage**: adapters report per-turn usage via `EventSink.Usage` → `EvSessionUsage` → `token_usage` rows (model + prompt/completion/total). `GET /api/stats/tokens` aggregates per model + totals (node-binding scoped). Model comes from the ACP `session/new` `configOptions` (`model` currentValue); agents that report no model fall back to `agent_type`, agents that report no usage record nothing (never fabricate). The locked `dispatchEvent.ts` returns null for `session.usage` (harmless — the usage UI reads via react-query).
- **Task notifications**: `useTaskNotifications` (mounted as `TaskNotificationBridge` in App) fires browser Notifications on `task.created`/`task.status_changed`; gated by `Notification.permission === 'granted'` AND `localStorage('fleetdeck.notify') === 'on'` (values are `'on'`/`'off'`, not `'true'`), coalesced per task via `tag`, click focuses the tab.
- **Mobile**: layout is responsive at ≤768px (columns stack, sidebar stacks under the transcript, modals fit, ≥36px touch targets on coarse pointers) — keep new surfaces working at 320px+.
- **Smoke scripts wipe rows in their target DB**: `scripts/smoke_acp_opencode.sh` runs `delete from users` (deterministic setup) and deletes its node on exit (cascading its workspaces/tasks/sessions/usage). Point them at a scratch DB or expect dev data loss.
- **Task lifecycle**: `POST /api/tasks` takes `auto_start:false` for backlog tasks (created but never dispatched until `POST /api/tasks/{id}/start`); `PATCH /api/tasks/{id} {archived:true}` archives `done` tasks (hidden from default board lists; `?archived=1` lists only archived); `POST /api/sessions/{id}/send` also reaches `done`/`failed` sessions — the task reopens `running`, the node reattaches via `MsgSessionResume` (`session/load`; unresumable sessions restart fresh with the follow-up as prompt). A completion gate parks sessions ending with unfinished mirrored todos in `awaiting_user` instead of `done`.
- **Read-through cache** (`FLEETDECK_REDIS_URL`, `internal/cache`): auth token→user resolution (60s TTL) and per-workspace task lists (30s TTL) are cached in Redis; logout and every task mutation delete their entries (`invalidateTasks`), TTLs bound any missed invalidation. An unset URL is a Noop cache and cache errors always degrade to a database read — never make the cache a request-failure source. Fuzzy search and node-scoped lists (binding-filtered) are deliberately uncached.

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
