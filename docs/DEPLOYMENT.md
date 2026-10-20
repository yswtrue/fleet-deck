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

Open the UI, sign in, then add a node under **Nodes → Add node**: the dialog
mints a single-use join token and shows the exact start command (see below).
Create a workspace and start a task.

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

## Releases (prebuilt artifacts)

Push a tag (`v1.2.3`, or `v1.2.3-rc.1` for a pre-release) and
`.github/workflows/release.yml` publishes:

| Artifact                | Where                                                                                                                               |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| hub image               | `ghcr.io/yswtrue/fleet-deck:v1.2.3` (also `1.2`, `1`, and `latest` for stable tags); `linux/amd64` + `linux/arm64`                  |
| node daemons            | release assets `fleetdeck-node_v1.2.3_{linux,darwin,windows}_{amd64,arm64}.{tar.gz,zip}` — binary plus `node.config.example.yaml`   |
| checksums + release log | `SHA256SUMS` and GitHub-generated release notes (categorised by `.github/release.yml` labels; add `skip-changelog` to omit an item) |

The image is the same `runtime` stage as the local compose build (hub, node,
fleetctl, goose + migrations), so it can be swapped in without changing the
deployment:

```sh
# .env next to docker-compose.yml: use the release instead of building
FLEETDECK_HUB_IMAGE=ghcr.io/yswtrue/fleet-deck:v1.2.3
```

```sh
docker compose pull hub migrate
docker compose up -d          # no --build: the pulled image is used as-is
```

Verify node archives before running them on a machine:

```sh
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf fleetdeck-node_v1.2.3_darwin_arm64.tar.gz   # Windows: unzip
```

## Running node daemons locally

On every machine that should run agents:

```sh
# 1. Build the node daemon (or copy a released binary)
go build -o fleetdeck-node ./cmd/node

# 2. Mint a join token in the UI: Nodes -> Add node -> Generate token

# 3. Configure the node identity (template: deploy/node.config.example.yaml)
cat > node.config.yaml <<'EOF'
name: my-machine
host: my-machine.example.com
token_file: ./node-token
EOF

# 4. Pick the agents this machine offers (re-run whenever you install a new
#    agent CLI); writes agent_types + default_adapter into the file:
./fleetdeck-node config --config node.config.yaml

# 5. Run it with the hub endpoint and the one-time token (valid 15 minutes,
#    single use). Both travel as flags; hub_url is written into the config
#    after the hub accepts the registration.
./fleetdeck-node --config node.config.yaml \
  --hub-url ws://localhost:7710/ws/node \
  --join-token <token from the UI>

# 6. Later starts need only the config file:
./fleetdeck-node --config node.config.yaml
```

### Choosing the agents

`fleetdeck-node config` lists every agent type the build knows, marks which
launchers are actually on this machine (`ready`, `built-in`, or
`missing: <binary>`), and writes the selection back as `agent_types` plus a
`default_adapter`. Everything else in the file is left byte-for-byte intact,
so comments and hand-written settings survive.

```sh
./fleetdeck-node config --config node.config.yaml
#   Agent types this node can run:
#     1) claude     ready
#     2) codex      ready
#     3) fake       built-in
#     4) opencode   ready
#     5) pi         missing: pi
#   Select agents by number ("all", "none"; Enter keeps the current binding): 2,4
#   Default adapter [codex]:

# Non-interactive (CI, provisioning scripts):
./fleetdeck-node config --config node.config.yaml --agents codex,opencode --default codex
```

Selecting an agent whose launcher is missing is allowed (the CLI can be
installed later) and prints a warning. The daemon reads the binding at startup,
so restart it after re-running the wizard to advertise the new agents.

A node name must not be registered yet: the first handshake needs the join
token, which is burned and binds the token's creator to the new node. The hub
then issues the node credential, stored in `token_file` — keep that file
private. Reconnects use `token_file` only, and the join token is never written
to the config file.

**One node per computer.** The daemon takes a machine-wide lock before
connecting (`lock_file`, default `$TMPDIR/fleetdeck-node.lock`), so a second
process exits with the first one's pid. Independently, the hub keeps
`node.machine_id` unique: the daemon derives it from the OS machine identifier
and hashes it, and a second node name from the same computer is rejected with
the existing node's name. Both are overridable for dev/CI only
(`machine_id` / `lock_file` in the YAML, or `FLEETDECK_NODE_MACHINE_ID` /
`FLEETDECK_NODE_LOCK_FILE`); production should leave them unset.

For automated deployments (no UI step), set `FLEETDECK_NODE_JOIN_TOKEN` on the
hub and pass the same value as `--join-token` on the node. That deployment
secret registers nodes without a database token and does not bind an owner. The web UI shows the node as **online**;
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

| Symptom                                           | Likely cause                                                                                                                                                                                     |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| UI loads but shows "Hub offline"                  | `/api`/`/ws` not reachable — with the `traefik` profile check label routing (`docker compose --profile traefik logs traefik`); behind your own proxy make sure `/ws` passes the `Upgrade` header |
| Blank page / "web UI not built"                   | the hub was built without the bundle — rebuild the image (the Dockerfile embeds `web/dist`)                                                                                                      |
| Node stuck "offline" in UI                        | wrong `hub_url`, or the hub port (`FLEETDECK_HUB_PORT`, default 7710) is not reachable from the node machine                                                                                     |
| "join token is required for a new node"           | first registration without a UI token — mint one under Nodes → Add node, or set `FLEETDECK_NODE_JOIN_TOKEN` on the hub and pass the same value as `--join-token`                                 |
| "join token is invalid, expired, or already used" | token older than 15 minutes or already claimed one node; mint a new one                                                                                                                          |
| "invalid token" after re-registering              | `token_file` holds a credential from another hub/database — delete it and join again with a fresh join token                                                                                     |
| Task stays `waiting`                              | its workspace belongs to an offline node                                                                                                                                                         |
| Session fails right after start                   | agent CLI missing/not authenticated **on the node machine** — check the node's logs (`stderr` of the agent is not captured; run the CLI manually to diagnose)                                    |
| 409 on reply                                      | the session is not awaiting input (double reply) — the UI rolls the message back and warns                                                                                                       |
