# FleetDeck Roadmap

Status of what is shipped and what is deliberately not (yet). Design history
lives in `docs/IMPLEMENTATION.md` — items in **Later** each carry the trigger
that would move them forward.

## ✅ Shipped

### Core platform

- **Hub-and-spoke control plane** — Go hub as the sole DB writer; thin node
  daemons dial out over WS; React + antd + Refine web UI (dark-first)
- **Single wire contract** — `internal/protocol` Go structs → JSON Schema →
  generated TS types
- **4-state task board** — waiting / running / done / awaiting_user, with
  subtasks mirroring the agent's todo list (one-way)
- **Realtime session view** — streamed output, message history, reconnect
  gap-fill from the persisted event stream, direct-URL deep links
- **Multi-user auth** — accounts with admin/user roles (bcrypt passwords,
  admin-managed creation — no self-registration), revocable opaque bearer
  sessions (30-day tokens, hashed at rest), and **user↔node binding**
  (`node_members`, set on the user page) with subtree inheritance: a bound
  user owns the node's workspaces, tasks and sessions (unauthorized reads
  404, writes 403), first-boot admin setup, login/user-management/binding
  UI, and `?token=` auth on `/ws/web`; node auth (`/ws/node`) is unchanged
- **Node lifecycle** — registration + token auth, online status, offline
  cascade (running sessions fail visibly)
- **Web UI internationalization** — English + Simplified Chinese via
  react-i18next (per-surface namespaces, `en` values are canonical), a
  header/login language switcher with persistence, `<html lang>` sync,
  antd locale packs, and locale-aware relative times
- **Node offline event buffering** — opt-in via `FLEETDECK_EVENT_BUFFER_DIR`:
  outbound events are persisted (one file per event, atomic rename) while the
  hub is unreachable and replayed in sequence order on reconnect (FIFO,
  at-least-once); without it, events drop during outages
- **Redis read-through cache** — opt-in via `FLEETDECK_REDIS_URL` (devenv and
  Compose ship a Redis): auth token resolution and board task lists are
  cached with explicit invalidation + short TTLs; an absent or failing Redis
  degrades to direct database reads

### Agent integrations

- **Shared ACP adapter** — OpenCode, Codex, Claude, pi (via `pi-acp`) behind
  one adapter; process lifecycle, cancel grace, resume via `session/load`
- **Permission picker** — agent permission requests render as structured
  Allow/Deny buttons; replies carry the exact `option_id` wire format;
  never auto-approved (unparseable/unknown replies fall back to deny)
- **ACP fs capability negotiation** — `fs/read_text_file` / `fs/write_text_file`
  served inside a workspace sandbox (symlink-escape proof); unimplemented
  capabilities (`terminal/*`, elicitation) are refused and never advertised
- **MCP server** — `fleetdeck_*` tools for agent self-introspection and
  resuming awaiting sessions

### Product polish

- **Task title fuzzy search** — pg_trgm trigram matching (typo tolerant,
  relevance-ranked) with a board search box
- **Token usage statistics** — per-turn capture from the agents
  (ACP prompt results carry real usage; model from the session's config
  options) with per-model and aggregate dashboards, session-level usage,
  and node-binding-scoped visibility
- **Mobile-ready web UI** — responsive layout below 768px (stacked
  columns/panels, full-width cards, usable composer, touch targets)
- **Task notifications** — browser notifications on task creation and
  status changes (permission + user setting gated, coalesced per task)
- **Task lifecycle** — backlog tasks (`auto_start:false` + explicit Start,
  never auto-dispatched), task archiving (hidden by default, `?archived=1`
  view), resumable finished sessions (chat with a done/failed session: it
  reopens and continues via `session/load`, falling back to a fresh run),
  and a completion gate (turns ending with unfinished todos park in
  `awaiting_user` for review)
- **Task transition state machine** — PATCH enforces the 4-state table
  (`done` terminal, idempotent writes, 409 on illegal moves)
- **Cancellation audit** — nullable `task.cancelled_at` records the first manual
  running/awaiting_user → done transition atomically; retained across resumes,
  retries and archiving. Natural completion/failure does not set it. Exposed
  in task JSON; no historical backfill or change to agent stop behavior.
- **Accessibility** — keyboard-reachable cards (Tab + Enter), distinct role
  labels, WCAG-AA status palette in dark and light themes
- **Deployment** — Docker Compose control plane (Postgres + migrations + hub
  with the embedded web UI + optional Traefik :80/:443 ingress via the
  `traefik` profile); the SPA also deploys to any static host via the runtime
  `config.js`; node daemons run locally — `docs/DEPLOYMENT.md`
- **Test suites** — 133 Go tests (integration against live Postgres) + 99 web
  tests (Vitest + RTL: dispatcher, permission wire, auth client/login,
  components, SPA handler)

## 🗓 Later (known gaps, with triggers)

| Item                           | What it is                                             | Trigger to build it                                                                  |
| ------------------------------ | ------------------------------------------------------ | ------------------------------------------------------------------------------------ |
| Subtask reverse-writeback      | edits on the board flow back into the agent's plan     | product decision on semantics (there is no standard ACP method for this)             |
| ACP `terminal/*` / elicitation | host-managed terminals and structured forms for agents | an agent that cannot work without host terminals; needs a sandbox threat model first |
| Broader web test coverage      | component/hook tests beyond the current 4 files        | as the UI surface grows — infra (Vitest + jsdom + RTL) is in place                   |
| Multi-hub / HA                 | replicated control plane                               | single-hub is fine for the current scale                                             |

## ❌ Non-goals (for now)

- Replacing agent CLIs or embedding models — FleetDeck orchestrates existing
  agents, it is not an agent
- Editing agent code/files from the board — sessions are conversations; file
  ops belong to the agent (via its sandboxed `fs/*` channel)
- A custom pi RPC bridge — superseded by `pi-acp` (the shared ACP adapter
  drives pi through it); revisit only if pi-acp falls short
