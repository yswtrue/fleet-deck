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

| service  | address                                                            |
| -------- | ------------------------------------------------------------------ |
| Postgres | `127.0.0.1:55432`, db `fleetdeck`, user/pass `fleetdeck/fleetdeck` |
| hub      | `http://127.0.0.1:7710` (HTTP + WS)                                |
| MCP      | stdio only — `cmd/fleetdeck-mcp` is launched by the agent; no port |
| web      | `http://127.0.0.1:7711`                                            |

Env vars (set by `devenv.nix`): `FLEETDECK_DATABASE_URL`, `FLEETDECK_HUB_ADDR`, `FLEETDECK_NODE_TOKEN_FILE`, `FLEETDECK_SCHEMA_OUT`.

## Verification commands

Order matters; do all of these before pushing.

```sh
# Backend
go build ./...
go vet ./...
gofmt -l cmd/ internal/        # must be empty
go test -race ./...            # 88 passed / 15 packages (1 skip; live DB required)
go test -short ./...           # skip live-DB tests

# Frontend (cd web)
pnpm exec tsc -b               # real typecheck (bare `tsc --noEmit` is a no-op on the solution tsconfig; `pnpm build` also runs it)
pnpm lint
pnpm test                      # vitest (47 tests: dispatchEvent contract, auth, components)
pnpm build
```

Composite (from devenv shell): `check` runs gofmt + vet + golangci-lint + `go test -race`.

## Live tests need Postgres

`go test ./...` hits the dev DB at `$FLEETDECK_DATABASE_URL`. Tests that touch `internal/store/`, `internal/hub/`, `internal/mcp/` will fail without Postgres reachable. Use `db-reset` first, or `db-up && migrate` if the schema is intact.

## Architecture invariants

These are locked. Do not change without a Gate review.

- **Single wire contract**: Go structs in `internal/protocol/` are the source of truth. `cmd/fleetctl schema export` → `web/src/gen/protocol/protocol.schema.json` → `pnpm run gen:types` → `web/src/gen/protocol/index.ts`. Run `gen` (or `gen-schema && gen-web-types`) after touching the protocol.
- **Hub is the sole DB writer**. Node daemons never touch Postgres; they speak to the hub over `ws://hub/ws/node`.
- **All `/api/*` + `/ws/web` require Bearer auth** (`internal/hub/{middleware,auth_handlers}.go`): roles `admin`/`user`, workspace visibility via `workspace_members` (invisible reads → 404, unauthorized writes → 403). Open: `/api/health`, `/api/auth/{status,setup,login}`, SPA static. `/ws/node` keeps node-token register-frame auth; `/ws/web` takes `?token=`. Auth is HTTP-layer — never add it to `internal/protocol`. The MCP client sends `FLEETDECK_HUB_TOKEN`.
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
- `PATCH /api/tasks/{id}` enforces the 4-state transition table (`canTransitionTask` in `internal/hub/api.go`): `done` is terminal, same-status is idempotent, illegal moves are 409. The internal session→task derivation does not go through it.
- `GET /api/tasks` filters: exactly one of `workspace_id` / `node_id`; `q=term` switches to pg_trgm fuzzy matching (substring OR trigram similarity, best match first) over task titles.
- Hub `Event.Seq` is the **hub-wide broadcast ordinal** (for WS live gap detection). `store.Event.Seq` is **per-session** (for `/api/sessions/{id}/events?after_seq=` gap-fill). They are intentionally different; do not unify.
- `web/src/hooks/useTaskBoard.ts` cache key is `['tasks', workspaceId]` (an active search extends it with the query). Code that creates tasks must invalidate **both** that prefix and `['tasks-by-workspace']` (the `WorkspaceList` task-count key) — see `NewTaskForm.tsx`.
- Go tests in `internal/store/store_test.go` and `internal/hub/{hub,api}_test.go` use `127.0.0.1:55432`. They skip with `-short`.
- **`internal/hub/static/` is the web UI embed root.** `go build` works with just the committed `.keep`, but a hub that serves the UI needs `web/dist` copied there first (the Dockerfile does this). The SPA also reads `web/public/config.js` at runtime for a custom hub URL (standalone static deploys).
- Web tests: Vitest + RTL (`pnpm test`) covering `dispatchEvent`, `permission.ts`, `PermissionPicker`, `MessageBubble`, `TaskCard` (status moves + toasts), `apiClient` (token lifecycle, 401 policy), `Login` (setup-vs-login mode), plus pure hub SPA-handler tests. RTL auto-cleanup is wired in `src/test/setup.ts` (vitest runs without globals).
- **antd v5 static `message.*`/`Modal.confirm` render nothing under React 19** — the official `@ant-design/v5-patch-for-react-19` is imported first in `web/src/main.tsx`; components may also use `message.useMessage()` + `contextHolder`. Do not remove the patch import.
- **react-query v5 drops per-`mutate` callbacks when the caller unmounts** (`hasListeners()` gate). Use `mutateAsync().then()/.catch()` when the outcome must survive unmount (e.g. toast + navigate after a board-column move).

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

The Phase 6 backlog is **fully implemented** (permission picker UI, `GET /api/tasks?node_id=` + `?q=` title search, ACP fs capability negotiation, Gate 4 m1-m7 cleanup, StateChange dedup, Vitest, session-route code splitting, `POST /api/nodes` capabilities fix, WorkspaceList invalidation, em-dash copy, card keyboard reachability, `fleetctl` `--out` flag order, PATCH transition table, dispatchTask transaction). Do not redo them. Remaining known gaps (fix only on request): ACP `terminal/*`/elicitation support (deliberately refused and unadvertised), node offline event buffering (`FLEETDECK_EVENT_BUFFER_DIR` reserved), subtask reverse-writeback to agents, task `cancelled_at` audit column. Multi-user auth is **implemented** (users/auth_tokens/workspace_members, Bearer sessions, membership visibility, `/login` `/users` + membership UI) — do not rebuild it.
