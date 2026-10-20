# FleetDeck

**English** | [简体中文](README.zh-CN.md)

> A web control plane for AI agent sessions across machines. Hub (Go) ↔ local Node daemons (Go) ↔ Web UI (React + antd + Refine), backed by Postgres.

## The problem

Running AI coding agents (OpenCode, Codex, Claude, pi…) today means one
terminal per session, scattered across machines:

1. **Invisible** — plans (todos), streaming output, and blockers live inside
   each terminal; step away from the machine and you are blind.
2. **Stalled** — when an agent needs a permission (run a command, edit a file)
   or asks a question, the session just waits until someone is back at that
   terminal.
3. **Ungovernable** — batch work, cross-machine scheduling, and post-mortems
   all mean SSH-ing around and scrolling through history.

FleetDeck pulls these sessions into one control plane:

- **Board view** — node (machine) → workspace (board) → task (one agent
  session, 4 states) → subtasks (live mirror of the agent's todos); see the
  global picture without being at a terminal.
- **Session view** — click any task for a chat-style session: streamed output,
  full history, reply at any time.
- **No stalls** — permission requests become **structured Allow/Deny pickers**
  in the web UI; questions get the same treatment. Replies flow back through
  the hub and the session continues.
- **One integration, many agents** — OpenCode / Codex / Claude / pi share a
  single ACP adapter; adding an agent means registering a launch config.
- **Agent self-service** — a built-in MCP server (`fleetdeck_*` tools) lets
  agents inspect their own tasks/sessions or resume an awaiting session.

## Architecture

```
             ┌──────────────────────────────┐
             │  Web UI (React + Vite)        │  board / session / permission picker
             └──────────────┬───────────────┘
                            │ HTTP + WebSocket (live push + gap-fill)
             ┌──────────────▼───────────────┐
             │  Hub (Go)                     │  control plane: sole DB writer
             │  ├─ REST API + WS broadcast   │
             │  ├─ node WS (register/cmds)   │
             │  └─ Postgres                  │
             └──────┬───────────────┬────────┘
                    │ WS (outbound) │ WS (outbound)
          ┌─────────▼────┐   ┌──────▼─────────┐
          │ Node (local) │   │ Node (local)   │   …on each target machine
          │ + ACP adapter│   │ + fake adapter │   (agents installed locally)
          └──────────────┘   └────────────────┘
```

- **Hub-and-spoke**: the web talks only to the hub; nodes are thin daemons on
  target machines that dial _out_ — no inbound ports on node machines.
- **Single source of truth for the wire**: Go structs in `internal/protocol/`
  generate the JSON Schema and the web-side TS types.
- **Event stream**: output, todo mirrors, and state transitions are pushed over
  WS and persisted for reconnect gap-fill.

## Highlights

| Capability             | Notes                                                                                                              |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------ |
| 4-state board          | waiting / running / done / awaiting_user; subtasks mirror agent todos                                              |
| Realtime session       | streaming output, history, reply whenever the agent asks                                                           |
| Permission picker      | structured options, exact `option_id` wire format, never auto-approved                                             |
| Fuzzy title search     | pg_trgm trigram matching, typo tolerant, relevance-ranked                                                          |
| Multi-machine          | node registration, online status, offline cascade                                                                  |
| MCP self-introspection | `fleetdeck_session_status` / `task_list` / `subtask_list` / `message_tail` / `event_tail` / `session_send_message` |
| Sandboxed ACP fs       | agents read/write only inside their workspace                                                                      |

## Quick start

### Control plane (Docker Compose)

Tagged releases publish a multi-arch hub image (`linux/amd64` + `linux/arm64`);
`docker-compose.yml` still builds from source, or point the `hub`/`migrate`
services at `ghcr.io/yswtrue/fleet-deck:vX.Y.Z`.

```sh
cp .env.example .env        # ports + DB credentials live here; edit as needed
docker compose up -d --build
# Everything on one port (the hub serves the embedded UI + API + WS):
#   http://localhost:<FLEETDECK_HUB_PORT>   (default .env value: 7710)

# Optional Traefik ingress on :80/:443 (label-discovered routes):
docker compose --profile traefik up -d
```

### Nodes (local, on each machine that runs agents)

```sh
go build -o fleetdeck-node ./cmd/node
./fleetdeck-node config --config node.config.yaml   # pick the agents to offer
# first start: --hub-url ws://<hub>:7710/ws/node --join-token <from the UI>;
# hub_url is written into the config, so later starts need only the file
./fleetdeck-node --config node.config.yaml   # template: deploy/node.config.example.yaml
```

Prebuilt daemons are attached to every [release](https://github.com/yswtrue/fleet-deck/releases)
(`fleetdeck-node_vX.Y.Z_{linux,darwin,windows}_{amd64,arm64}`).

### Development (devenv)

```sh
cp .env.example .env    # optional: customize ports/credentials (defaults work)
devenv shell
devenv up                          # hub + node + web against local Postgres
devenv up hub node web fake-node   # optional: ... plus a deterministic fake agent node
```

Full walkthrough: **[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**.

## Development

```sh
# Backend
go build ./... && go vet ./...
gofmt -l cmd/ internal/        # must be empty
go test -race ./...            # 78 passed / 15 packages (live Postgres via FLEETDECK_DATABASE_URL; devenv shell derives it from .env)

# Frontend (cd web)
pnpm tsc --noEmit && pnpm lint
pnpm test                      # vitest
pnpm build
```

## Roadmap

What is shipped and what is deliberately waiting — with the trigger for each:
**[ROADMAP.md](ROADMAP.md)**.

## Docs

- **[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)** — production deployment guide
- **[docs/IMPLEMENTATION.md](docs/IMPLEMENTATION.md)** — design source of truth
  (data model, protocol, adapter abstraction)
- **[AGENTS.md](AGENTS.md)** — toolchain, verification, invariants, gotchas
