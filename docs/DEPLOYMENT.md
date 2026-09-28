# Deploying FleetDeck

FleetDeck has two moving parts with very different deployment models:

| Part                                               | Where it runs                      | In `docker-compose.yml`? |
| -------------------------------------------------- | ---------------------------------- | ------------------------ |
| **Control plane** (Postgres, hub, web UI, Traefik) | one host / VM                      | yes                      |
| **Node daemons** (run the agent CLIs)              | **locally on each target machine** | **no — by design**       |

Nodes dial _out_ to the hub over WebSocket, so a node machine needs no inbound
port and nothing containerized: it only needs the agent CLIs (OpenCode, Codex,
Claude, pi) installed locally. The compose stack is purely the control plane.

## Requirements

- Docker Engine 20+ with the Compose plugin (or `docker-compose` v2)
- For nodes: Go 1.27 (to build `cmd/node`) and the agent CLIs on that machine

## Quick start (control plane)

```sh
cp .env.example .env        # ports + DB credentials; required (compose interpolates it)
docker compose up -d --build
# Everything on one port — the hub serves the embedded web UI, the REST API,
# and the WebSocket endpoints:
#   UI + API + WS:       http://localhost:7710   (FLEETDECK_HUB_PORT from .env)
```

Optional ingress with Traefik (label-discovered routes, ports 80/443) — it is
**off by default** and enabled with the `traefik` profile:

```sh
docker compose --profile traefik up -d
# UI + API + WS:   http://localhost:80 / https://localhost:443 (self-signed
#                  until a certificate resolver is configured)
```

Open the UI, register a node (or run one — see below), create a workspace, and
start a task.

## Services

| Service    | Image / target                                    | Role                                                                |
| ---------- | ------------------------------------------------- | ------------------------------------------------------------------- |
| `postgres` | `postgres:17-alpine`                              | storage (internal only; volume `pgdata`)                            |
| `redis`    | `redis:7-alpine`                                  | hub read-through cache (no persistence; `FLEETDECK_REDIS_URL`)      |
| `migrate`  | `Dockerfile` target `runtime`                     | runs `goose up` once per deploy                                     |
| `hub`      | `Dockerfile` target `runtime`                     | everything user-facing: embedded web UI + REST + WS, sole DB writer |
| `traefik`  | `traefik:v3.7.13` (profile `traefik`, **opt-in**) | ingress on :80/:443; routes discovered from container labels        |

### Routing

The hub serves the SPA itself and the SPA calls its own origin, so there is
exactly one origin to reach (no CORS, no extra proxy):

```
default:   browser ── http://host:7710 ──▶ hub  (UI + /api + /ws)

traefik:   browser ── http(s)://host:80,443 ──▶ traefik ──▶ hub
 (profile)          (labels on the hub container pick the routes up)
```

With the `traefik` profile, edit the `traefik.http.routers.*` labels on the
`hub` service in `docker-compose.yml` to change routing.

### Hosting the SPA on any static server

The web UI is a plain static bundle with a runtime configuration file — it can
be deployed to any file server or CDN and pointed at any hub:

```sh
cd web && pnpm install && pnpm build     # produces web/dist
# upload web/dist/ to your static host, then edit dist/config.js there:
#   window.__FLEETDECK_HUB_URL__ = "https://hub.example.com";
```

The hub admits any origin in single-user mode, so a cross-origin setup works
without extra configuration. The same build is what the hub embeds
(`Dockerfile` copies `web/dist` into the binary); when `config.js` leaves the
override unset, the app simply uses the page's own origin.

### TLS

The `websecure` entrypoint (:443) serves Traefik's default self-signed
certificate until a resolver is configured. For production, add a certificate
resolver (e.g. ACME) to the `hub-secure` router — see the
[Traefik docs](https://doc.traefik.io/traefik/https/overview/). Node URLs
become `wss://…/ws/node` once TLS is on.

## Running node daemons locally

On every machine that should run agents:

```sh
# 1. Build the node daemon (or copy a released binary)
go build -o fleetdeck-node ./cmd/node

# 2. Configure it (template: deploy/node.config.example.yaml)
cat > node.config.yaml <<'EOF'
name: my-machine
host: my-machine.example.com
capabilities: [opencode]
agent_types: [opencode]
default_adapter: opencode
hub_url: ws://localhost:7710/ws/node      # hub address as reachable from here
token_file: ./node-token
EOF

# 3. Run it
./fleetdeck-node --config node.config.yaml
```

On first start the node registers with the hub and stores its credential in
`token_file` — keep that file private. The web UI shows the node as **online**;
create a workspace pointing at an absolute path on that machine and tasks will
be dispatched there.

Notes:

- **Agents must be installed on the node machine** (`opencode`, `codex`,
  `npx @agentclientprotocol/codex-acp`, `claude-agent-acp`, `pi-acp`, …). The
  node spawns them as subprocesses with the workspace as working directory.
- The connection is **outbound-only** (node → hub), so no firewall holes are
  needed on node machines.
- Several nodes can point at the same hub; each gets its own board(s).
- `default_adapter: fake` gives a deterministic no-agent node for demos and UI
  testing.

## MCP server (optional)

Agents can introspect their own tasks via the `fleetdeck_*` MCP tools. The
`fleetdeck-mcp` binary speaks stdio MCP and calls the hub's REST API, so run it
where the agent runs (the node machine) with `FLEETDECK_HUB_URL` set, and
register it in the agent's MCP config (or via the adapter's `mcpServers`
injection). See `examples/fleetdeck-mcp.config.yaml`.

## Database lifecycle

- Migrations run automatically via the `migrate` service (one-shot, runs before
  the hub starts). To migrate manually: `docker compose run --rm migrate`.
- Data lives in the `pgdata` volume. Backup: `docker compose exec postgres
pg_dump -U fleetdeck fleetdeck > backup.sql`.
- `docker compose down` keeps data; `docker compose down -v` destroys it.

## Upgrades

```sh
git pull
docker compose up -d --build     # migrate runs first, then hub restarts
```

Nodes are independent: rebuild/restart them at will; the hub marks sessions of
disconnected nodes as failed (offline cascade) and nodes re-register on
startup.

## Smoke checks

```sh
curl -s http://localhost:7710/api/health          # {"status":"ok"}
curl -s http://localhost:7710/ | head -3          # SPA (embedded)
curl -s http://localhost:7710/config.js | head -3 # runtime config file
# with --profile traefik:
curl -s  http://localhost:80/api/health           # label routing
curl -sk https://localhost:443/api/health         # TLS entrypoint
```

End-to-end scripts (from a checkout): `scripts/smoke_send_message.sh`,
`scripts/smoke_mcp_subtask.sh`, `scripts/smoke_acp_opencode.sh` /
`scripts/smoke_acp_codex.sh` (SKIP when the agent CLI is absent).

## Troubleshooting

| Symptom                          | Likely cause                                                                                                                                                                                     |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| UI loads but shows "Hub offline" | `/api`/`/ws` not reachable — with the `traefik` profile check label routing (`docker compose --profile traefik logs traefik`); behind your own proxy make sure `/ws` passes the `Upgrade` header |
| Blank page / "web UI not built"  | the hub was built without the bundle — rebuild the image (the Dockerfile embeds `web/dist`)                                                                                                      |
| Node stuck "offline" in UI       | wrong `hub_url`, or the hub port (`FLEETDECK_HUB_PORT`, default 7710) is not reachable from the node machine                                                                                     |
| Task stays `waiting`             | its workspace belongs to an offline node                                                                                                                                                         |
| Session fails right after start  | agent CLI missing/not authenticated **on the node machine** — check the node's logs (`stderr` of the agent is not captured; run the CLI manually to diagnose)                                    |
| 409 on reply                     | the session is not awaiting input (double reply) — the UI rolls the message back and warns                                                                                                       |
