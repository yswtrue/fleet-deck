#!/usr/bin/env bash
# Phase 4 remediation smoke: GET /api/tasks/{id}/subtasks (empty) and the
# fleetdeck_subtask_list MCP tool over stdio.
#
# A hub-only node is used so the task is never dispatched and therefore has no
# mirrored todos, making the empty-array assertion deterministic.
#
# Usage: bash scripts/smoke_mcp_subtask.sh
set -uo pipefail

ROOT=/Users/yswtrue/Codes/fleet-deck
HUB_ADDR=127.0.0.1:17710
HUB_URL="http://$HUB_ADDR"
DBURL="postgres://fleetdeck:fleetdeck@127.0.0.1:55432/fleetdeck?sslmode=disable"
BIN=/tmp/opencode/fd-mcp-subtask-smoke
mkdir -p "$BIN"

cd "$ROOT" || exit 1
go build -o "$BIN/hub" ./cmd/hub || exit 1
go build -o "$BIN/fleetdeck-mcp" ./cmd/fleetdeck-mcp || exit 1

psql "$DBURL" -q -c "delete from node where name='mcp-subtask-smoke-node'" >/dev/null 2>&1



HUB_PID=""
cleanup() { [ -n "$HUB_PID" ] && kill "$HUB_PID" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

FLEETDECK_HUB_ADDR="$HUB_ADDR" FLEETDECK_DATABASE_URL="$DBURL" "$BIN/hub" >"$BIN/hub.log" 2>&1 &
HUB_PID=$!
for _ in $(seq 1 50); do
  curl -fsS "$HUB_URL/api/health" >/dev/null 2>&1 && break
  sleep 0.2
done

# Auth: every hub REST call below needs Authorization: Bearer. Wipe stray
# users from earlier runs, then set up (first run) or log in and export
# AUTH_HEADER for the curl calls.
psql "$DBURL" -q -c "delete from users" >/dev/null 2>&1
SMOKE_EMAIL="smoke@example.com"
SMOKE_PASSWORD="smoke-password-123"
SETUP_RESP=$(curl -sS -X POST "$HUB_URL/api/auth/setup" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$SMOKE_EMAIL\",\"display_name\":\"smoke\",\"password\":\"$SMOKE_PASSWORD\"}")
if echo "$SETUP_RESP" | grep -q '"token"'; then
  SMOKE_TOKEN=$(echo "$SETUP_RESP" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
else
  SMOKE_TOKEN=$(curl -fsS -X POST "$HUB_URL/api/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$SMOKE_EMAIL\",\"password\":\"$SMOKE_PASSWORD\"}" \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
fi
[ -n "$SMOKE_TOKEN" ] || { echo "SMOKE_FAIL: could not obtain auth token"; exit 1; }
AUTH_HEADER="Authorization: Bearer $SMOKE_TOKEN"
echo "auth=ok"
echo "health: $(curl -fsS "$HUB_URL/api/health")"

# Offline node: no daemon, so dispatchTask leaves the task in waiting.
NODE_ID=$(curl -fsS -H "$AUTH_HEADER" -X POST "$HUB_URL/api/nodes" -H 'Content-Type: application/json' \
  -d '{"name":"mcp-subtask-smoke-node","host":"localhost","capabilities":["fake"]}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
[ -n "$NODE_ID" ] || { echo "SMOKE_FAIL: node create"; exit 1; }

WS_ID=$(curl -fsS -H "$AUTH_HEADER" -X POST "$HUB_URL/api/workspaces" -H 'Content-Type: application/json' \
  -d "{\"node_id\":\"$NODE_ID\",\"name\":\"mcp-subtask-smoke-ws\",\"path\":\"/tmp/mcp-subtask-smoke-ws\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")

TASK_ID=$(curl -fsS -H "$AUTH_HEADER" -X POST "$HUB_URL/api/tasks" -H 'Content-Type: application/json' \
  -d "{\"workspace_id\":\"$WS_ID\",\"title\":\"mcp-subtask-smoke-task\",\"agent_type\":\"fake\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "node_id=$NODE_ID ws_id=$WS_ID task_id=$TASK_ID"

# 1) REST: the new endpoint must return 200 + {"subtasks":[]}.
CODE=$(curl -fsS -H "$AUTH_HEADER" -o "$BIN/subtasks.json" -w '%{http_code}' "$HUB_URL/api/tasks/$TASK_ID/subtasks")
echo "GET /api/tasks/$TASK_ID/subtasks -> $CODE $(cat "$BIN/subtasks.json" 2>/dev/null)"
[ "$CODE" = "200" ] || { echo "SMOKE_FAIL: expected 200, got $CODE"; exit 1; }
python3 -c "import json; d=json.load(open('$BIN/subtasks.json')); assert d=={'subtasks':[]}, d" \
  || { echo "SMOKE_FAIL: body is not {\"subtasks\":[]}"; exit 1; }
echo "rest_ok=1"

# 2) MCP stdio: initialize -> tools/list -> tools/call fleetdeck_subtask_list.
FLEETDECK_HUB_URL="$HUB_URL" FLEETDECK_HUB_TOKEN="$SMOKE_TOKEN" python3 - "$BIN/fleetdeck-mcp" "$TASK_ID" <<'PY' || { echo "SMOKE_FAIL: MCP stdio"; exit 1; }
import json, subprocess, sys

binpath, task_id = sys.argv[1], sys.argv[2]
p = subprocess.Popen([binpath], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                     stderr=subprocess.PIPE, text=True, bufsize=1)

def send(o):
    p.stdin.write(json.dumps(o) + "\n")
    p.stdin.flush()

def recv():
    return json.loads(p.stdout.readline())

try:
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
          "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                     "clientInfo": {"name": "smoke", "version": "0"}}})
    assert "result" in recv(), "initialize failed"

    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    tools = recv()["result"]["tools"]
    names = sorted(t["name"] for t in tools)
    assert "fleetdeck_subtask_list" in names, names

    send({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
          "params": {"name": "fleetdeck_subtask_list", "arguments": {"task_id": task_id}}})
    call = recv()
    assert "result" in call, call
    assert not call["result"].get("isError"), call
    sc = call["result"].get("structuredContent") or {}
    assert sc.get("subtasks") == [], sc
    print("mcp_tools=%d subtask_list=%s" % (len(names), json.dumps(sc)))
finally:
    p.stdin.close()
    p.terminate()
    p.wait(timeout=5)
PY

echo "SMOKE_OK"
