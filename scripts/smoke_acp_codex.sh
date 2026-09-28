#!/usr/bin/env bash
# Phase 4 Lane B smoke: drive a one-line prompt through the shared ACP adapter
# with agent_type "codex", end to end via the node daemon + hub.
#
# Best-effort: if the `codex` CLI is not installed on this host the smoke prints
# SMOKE_SKIP and exits 0. It never fails the lane.
#
# Prerequisites on a host where this actually runs:
#   - npm i -g @agentclientprotocol/codex-acp   (the `codex-acp` launcher)
#   - the codex CLI + CODEX_API_KEY / OPENAI_API_KEY (or a prior login)
#   - cmd/node importing _ "fleetdeck/internal/adapters/acp" so the node
#     binary resolves the "codex" agent_type (Lane B registers it in
#     adapters.Default; the blank import lives in cmd/node and is outside this
#     lane's scope).
#
# Usage: bash scripts/smoke_acp_codex.sh
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HUB_ADDR=${SMOKE_HUB_ADDR:-127.0.0.1:7710}
DBURL=${FLEETDECK_DATABASE_URL:?set FLEETDECK_DATABASE_URL (run inside 'devenv shell')}
BIN=/tmp/opencode/fd-acp-smoke
NODE_NAME=acp-codex-node
mkdir -p "$BIN"
cd "$ROOT" || exit 1

if ! command -v codex >/dev/null 2>&1; then
  echo "SMOKE_SKIP: 'codex' binary not found on PATH; skipping ACP codex smoke."
  echo "  install with:  npm i -g @agentclientprotocol/codex-acp"
  echo "  (plus the codex CLI) then re-run this script."
  exit 0
fi

go build -o "$BIN/hub" ./cmd/hub || exit 1
go build -o "$BIN/node" ./cmd/node || exit 1

psql "$DBURL" -q -c "delete from node where name='$NODE_NAME'" >/dev/null 2>&1
rm -f .devenv/state/fleetdeck/node-token

cat > "$BIN/codex-node.config.yaml" <<EOF
name: $NODE_NAME
host: localhost
capabilities:
  - codex
agent_types:
  - codex
default_adapter: codex
hub_url: ws://$HUB_ADDR/ws/node
EOF



HUB_PID=""; NODE_PID=""
# shellcheck disable=SC2329  # invoked via trap on EXIT
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$HUB_PID" ] && kill "$HUB_PID" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

FLEETDECK_HUB_ADDR="$HUB_ADDR" FLEETDECK_DATABASE_URL="$DBURL" "$BIN/hub" >"$BIN/hub.log" 2>&1 &
HUB_PID=$!
for _ in $(seq 1 50); do
  curl -fsS "http://$HUB_ADDR/api/health" >/dev/null 2>&1 && break
  sleep 0.2
done

# Auth: every hub REST call below needs Authorization: Bearer. Wipe stray
# users from earlier runs, then set up (first run) or log in and export
# AUTH_HEADER for the curl calls.
psql "$DBURL" -q -c "delete from users" >/dev/null 2>&1
SMOKE_EMAIL="smoke@example.com"
SMOKE_PASSWORD="smoke-password-123"
SETUP_RESP=$(curl -sS -X POST "http://$HUB_ADDR/api/auth/setup" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$SMOKE_EMAIL\",\"display_name\":\"smoke\",\"password\":\"$SMOKE_PASSWORD\"}")
if echo "$SETUP_RESP" | grep -q '"token"'; then
  SMOKE_TOKEN=$(echo "$SETUP_RESP" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
else
  SMOKE_TOKEN=$(curl -fsS -X POST "http://$HUB_ADDR/api/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$SMOKE_EMAIL\",\"password\":\"$SMOKE_PASSWORD\"}" \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
fi
[ -n "$SMOKE_TOKEN" ] || { echo "SMOKE_FAIL: could not obtain auth token"; exit 1; }
AUTH_HEADER="Authorization: Bearer $SMOKE_TOKEN"
echo "auth=ok"

"$BIN/node" --config "$BIN/codex-node.config.yaml" >"$BIN/node.log" 2>&1 &
NODE_PID=$!

NODE_ID=""
for _ in $(seq 1 60); do
  NODE_ID=$(curl -fsS -H "$AUTH_HEADER" "http://$HUB_ADDR/api/nodes" 2>/dev/null \
    | python3 -c "import sys,json; ns=json.load(sys.stdin); print(next((n['id'] for n in ns if n['status']=='online' and n['name']=='$NODE_NAME'),''))" 2>/dev/null)
  [ -n "$NODE_ID" ] && break
  sleep 0.25
done
if [ -z "$NODE_ID" ]; then
  echo "SMOKE_FAIL: node did not register"
  cat "$BIN/node.log"
  exit 1
fi
echo "node_id=$NODE_ID"

WS_PATH=/tmp/fd-acp-smoke-ws
mkdir -p "$WS_PATH"

WS_ID=$(curl -fsS -H "$AUTH_HEADER" -X POST "http://$HUB_ADDR/api/workspaces" -H 'Content-Type: application/json' \
  -d "{\"node_id\":\"$NODE_ID\",\"name\":\"acp-smoke-ws\",\"path\":\"$WS_PATH\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")

TASK_JSON=$(curl -fsS -H "$AUTH_HEADER" -X POST "http://$HUB_ADDR/api/tasks" -H 'Content-Type: application/json' \
  -d "{\"workspace_id\":\"$WS_ID\",\"title\":\"acp-smoke-task\",\"prompt\":\"Reply with the single word: pong\",\"agent_type\":\"codex\"}")
TASK_ID=$(echo "$TASK_JSON" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
SESSION_ID=$(echo "$TASK_JSON" | python3 -c "import sys,json;print(json.load(sys.stdin)['session_id'])")
echo "task_id=$TASK_ID session_id=$SESSION_ID"

STATUS=""
for _ in $(seq 1 240); do
  STATUS=$(psql "$DBURL" -tAc "select status from session where id='$SESSION_ID'" 2>/dev/null)
  { [ "$STATUS" = "done" ] || [ "$STATUS" = "failed" ]; } && break
  sleep 0.5
done
echo "session_status=$STATUS"

MSGS=$(curl -fsS -H "$AUTH_HEADER" "http://$HUB_ADDR/api/sessions/$SESSION_ID/messages?limit=20" 2>/dev/null)
echo "messages=$MSGS"

psql "$DBURL" -q -c "delete from node where id='$NODE_ID'" >/dev/null 2>&1

if [ "$STATUS" = "done" ] && echo "$MSGS" | grep -q '"role":"assistant"'; then
  echo "SMOKE_OK"
  exit 0
fi

echo "SMOKE_FAIL"
echo "--- node log ---"
cat "$BIN/node.log"
echo "--- hub log tail ---"
tail -30 "$BIN/hub.log"
exit 1
