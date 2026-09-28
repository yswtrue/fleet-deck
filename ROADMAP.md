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
  sessions (30-day tokens, hashed at rest), workspace-membership visibility
  (non-members get 404 on workspace-scoped reads), first-boot admin setup,
  login/user-management/membership UI, and `?token=` auth on `/ws/web`; node
  auth (`/ws/node`) is unchanged
- **Node lifecycle** — registration + token auth, online status, offline
  cascade (running sessions fail visibly)
- **Node offline event buffering** — opt-in via `FLEETDECK_EVENT_BUFFER_DIR`:
  outbound events are persisted (one file per event, atomic rename) while the
  hub is unreachable and replayed in sequence order on reconnect (FIFO,
  at-least-once); without it, events drop during outages

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
- **Task transition state machine** — PATCH enforces the 4-state table
  (`done` terminal, idempotent writes, 409 on illegal moves)
- **Accessibility** — keyboard-reachable cards (Tab + Enter), distinct role
  labels, WCAG-AA status palette in dark and light themes
- **Deployment** — Docker Compose control plane (Postgres + migrations + hub
  with the embedded web UI + optional Traefik :80/:443 ingress via the
  `traefik` profile); the SPA also deploys to any static host via the runtime
  `config.js`; node daemons run locally — `docs/DEPLOYMENT.md`
- **Test suites** — 91 Go tests (integration against live Postgres) + 47 web
  tests (Vitest + RTL: dispatcher, permission wire, auth client/login,
  components, SPA handler)

## 🗓 Later (known gaps, with triggers)

| Item                             | What it is                                             | Trigger to build it                                                                  |
| -------------------------------- | ------------------------------------------------------ | ------------------------------------------------------------------------------------ |
| Subtask reverse-writeback        | edits on the board flow back into the agent's plan     | product decision on semantics (there is no standard ACP method for this)             |
| ACP `terminal/*` / elicitation   | host-managed terminals and structured forms for agents | an agent that cannot work without host terminals; needs a sandbox threat model first |
| `task.cancelled_at` audit column | audit trail for cancellations                          | compliance/audit need (4-state model does not require it)                            |
| Broader web test coverage        | component/hook tests beyond the current 4 files        | as the UI surface grows — infra (Vitest + jsdom + RTL) is in place                   |
| Multi-hub / HA                   | replicated control plane                               | single-hub is fine for the current scale                                             |

## ❌ Non-goals (for now)

- Replacing agent CLIs or embedding models — FleetDeck orchestrates existing
  agents, it is not an agent
- Editing agent code/files from the board — sessions are conversations; file
  ops belong to the agent (via its sandboxed `fs/*` channel)
- A custom pi RPC bridge — superseded by `pi-acp` (the shared ACP adapter
  drives pi through it); revisit only if pi-acp falls short
