#!/usr/bin/env bash
# Phase 3 Lane C smoke: a user reply must resume the session AND the parent task,
# broadcasting session.message, session.state_changed, and task.status_changed.
#
# Usage: bash scripts/smoke_send_message.sh
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HUB_ADDR=${SMOKE_HUB_ADDR:-127.0.0.1:7710}
DBURL=${FLEETDECK_DATABASE_URL:?set FLEETDECK_DATABASE_URL (run inside 'devenv shell')}
export FLEETDECK_NODE_JOIN_TOKEN=${FLEETDECK_NODE_JOIN_TOKEN:-smoke-join-secret}
BIN=/tmp/opencode/fd-phase3-smoke
export FLEETDECK_NODE_LOCK_FILE="$BIN/node.lock"  # per-smoke daemon; one node per machine
mkdir -p "$BIN"

cd "$ROOT" || exit 1
go build -o "$BIN/hub" ./cmd/hub || exit 1
go build -o "$BIN/node" ./cmd/node || exit 1
go build -o "$BIN/ws_capture" ./scripts/ws_capture || exit 1

psql "$DBURL" -q -c "delete from node where name='fake-node-1'" >/dev/null 2>&1
rm -f .devenv/state/fleetdeck/node-token



HUB_PID=""; NODE_PID=""; CAP_PID=""
# shellcheck disable=SC2329  # invoked via trap on EXIT
cleanup() {
  [ -n "$CAP_PID" ] && kill "$CAP_PID" 2>/dev/null
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
echo "health: $(curl -fsS "http://$HUB_ADDR/api/health")"

FLEETDECK_HUB_ADDR="$HUB_ADDR" "$BIN/node" --config ./examples/fake-node.config.yaml >"$BIN/node.log" 2>&1 &
NODE_PID=$!

NODE_ID=""
for _ in $(seq 1 60); do
  NODE_ID=$(curl -fsS -H "$AUTH_HEADER" "http://$HUB_ADDR/api/nodes" 2>/dev/null | python3 -c "import sys,json; ns=json.load(sys.stdin); print(next((n['id'] for n in ns if n['status']=='online' and n['name']=='fake-node-1'),''))" 2>/dev/null)
  [ -n "$NODE_ID" ] && break
  sleep 0.25
done
if [ -z "$NODE_ID" ]; then echo "SMOKE_FAIL: node did not register"; cat "$BIN/node.log"; exit 1; fi
echo "node_id=$NODE_ID"

WS_ID=$(curl -fsS -H "$AUTH_HEADER" -X POST "http://$HUB_ADDR/api/workspaces" -H 'Content-Type: application/json' \
  -d "{\"node_id\":\"$NODE_ID\",\"name\":\"p3-smoke-ws\",\"path\":\"/tmp/p3-smoke-ws\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")

TASK_JSON=$(curl -fsS -H "$AUTH_HEADER" -X POST "http://$HUB_ADDR/api/tasks" -H 'Content-Type: application/json' \
  -d "{\"workspace_id\":\"$WS_ID\",\"title\":\"p3-smoke-task\",\"prompt\":\"do it\",\"agent_type\":\"fake\"}")
TASK_ID=$(echo "$TASK_JSON" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
SESSION_ID=$(echo "$TASK_JSON" | python3 -c "import sys,json;print(json.load(sys.stdin)['session_id'])")
echo "task_id=$TASK_ID session_id=$SESSION_ID"

# The fake adapter is happy-path only. Let it finish first so it cannot overwrite
# the awaiting_user state we seed below, then park both rows on awaiting_user.
INITIAL=""
for _ in $(seq 1 40); do
  INITIAL=$(psql "$DBURL" -tAc "select status from session where id='$SESSION_ID'")
  { [ "$INITIAL" = "done" ] || [ "$INITIAL" = "failed" ]; } && break
  sleep 0.25
done
echo "initial_session_status=$INITIAL"

psql "$DBURL" -q -c "update session set status='awaiting_user' where id='$SESSION_ID'" >/dev/null
psql "$DBURL" -q -c "update task set status='awaiting_user' where id='$TASK_ID'" >/dev/null

# Subscribe to the web stream before the reply so nothing is missed.
"$BIN/ws_capture" -url "ws://$HUB_ADDR/ws/web?token=$SMOKE_TOKEN" -duration 5s >"$BIN/events.log" 2>&1 &
CAP_PID=$!
sleep 0.5

SEND_CODE=$(curl -fsS -o "$BIN/send.json" -w '%{http_code}' -X POST \
  "http://$HUB_ADDR/api/sessions/$SESSION_ID/send" -H "$AUTH_HEADER" -H 'Content-Type: application/json' \
  -d '{"message":"please continue"}')
echo "send_http_status=$SEND_CODE"

sleep 1.0
kill "$CAP_PID" 2>/dev/null
wait "$CAP_PID" 2>/dev/null
CAP_PID=""

SESSION_STATUS=$(psql "$DBURL" -tAc "select status from session where id='$SESSION_ID'")
TASK_STATUS=$(psql "$DBURL" -tAc "select status from task where id='$TASK_ID'")
echo "session_status=$SESSION_STATUS task_status=$TASK_STATUS"

have_event() { grep -q "\"$1\"" "$BIN/events.log"; }
for evt in session.message session.state_changed task.status_changed; do
  if have_event "$evt"; then echo "broadcast_${evt}=yes"; else echo "broadcast_${evt}=NO"; fi
done

psql "$DBURL" -q -c "delete from node where id='$NODE_ID'" >/dev/null 2>&1

if [ "$SESSION_STATUS" = "running" ] && [ "$TASK_STATUS" = "running" ] \
   && have_event session.message && have_event session.state_changed && have_event task.status_changed; then
  echo "SMOKE_OK"
  exit 0
fi

echo "SMOKE_FAIL"
echo "--- captured events ---"
cat "$BIN/events.log"
exit 1
