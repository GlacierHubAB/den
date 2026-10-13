#!/usr/bin/env bash
# lane-spike.sh: check a Claude Code release against the read-only lane that
# answers apps on den-api.sock, before Den lets the lane run on it.
#
#   scripts/lane-spike.sh [path/to/claude]
#
# It starts the lane with exactly the command line Den uses (printed by
# `den lane-argv`), offline, against a mock Messages API, with a scratch
# HOME and CLAUDE_CONFIG_DIR holding a hostile user config (an MCP server,
# bypassPermissions, a hook, a CLAUDE.md canary), and checks:
#   1. its init lists no tools, no MCP servers, no skills, no slash commands;
#   2. the user's hook does not run and the request carries no tools and no
#      canary;
#   3. a peer message from another session never reaches the lane's model,
#      while the same message does reach a control session that accepts
#      peer messages (so the check is not passing by accident).
# On success it prints the version to add to orch.LaneVerified. It runs in
# a private network and mount namespace (unshare), so no real session, and
# no real cross-session socket folder, can see it; it never reads the real
# ~/.claude.
set -euo pipefail

main() {
CLAUDE="${1:-$(command -v claude)}"
CLAUDE="$(readlink -f "$CLAUDE")"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/den-lane-spike.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
(cd "$HERE" && go run ./cmd/den lane-argv) > "$WORK/argv" || { echo "den lane-argv failed" >&2; exit 2; }
cat > "$WORK/mock.py" <<'PY'
import json, sys, http.server
LOG = sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        self.send_response(404); self.end_headers()
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("content-length") or 0))
        try: body = json.loads(raw)
        except Exception: body = {}
        system = json.dumps(body.get("system", "")); msgs = json.dumps(body.get("messages", []))
        role = "sender" if "SPIKE_SENDER" in system else "control" if "SPIKE_CONTROL" in system else "lane"
        with open(LOG, "a") as f:
            f.write(json.dumps({"path": self.path, "role": role, "tools": len(body.get("tools") or []), "body": json.dumps(body)}) + "\n")
        usage = {"input_tokens": 10, "output_tokens": 1}
        if "count_tokens" in self.path or not body.get("stream"):
            out = json.dumps({"input_tokens": 10} if "count_tokens" in self.path else {"id": "m", "type": "message", "role": "assistant", "model": "m", "content": [{"type": "text", "text": "ok"}], "stop_reason": "end_turn", "stop_sequence": None, "usage": usage}).encode()
            self.send_response(200); self.send_header("content-type", "application/json"); self.send_header("content-length", str(len(out))); self.end_headers(); self.wfile.write(out); return
        start = ("message_start", {"type": "message_start", "message": {"id": "m", "type": "message", "role": "assistant", "model": body.get("model", "m"), "content": [], "stop_reason": None, "stop_sequence": None, "usage": usage}})
        if role == "sender" and "tool_result" not in msgs:
            to = "den-spike-control" if "SPIKE_TO_CONTROL" in msgs else "den-drey-lane"
            inp = json.dumps({"to": to, "message": "SPIKE_PEER_MESSAGE", "summary": "spike"})
            evs = [start, ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "tool_use", "id": "toolu_1", "name": "SendMessage", "input": {}}}),
                   ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": inp}}),
                   ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                   ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use", "stop_sequence": None}, "usage": {"output_tokens": 1}}),
                   ("message_stop", {"type": "message_stop"})]
        else:
            evs = [start, ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
                   ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "ok"}}),
                   ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                   ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 1}}),
                   ("message_stop", {"type": "message_stop"})]
        self.send_response(200); self.send_header("content-type", "text/event-stream"); self.end_headers()
        for name, data in evs:
            self.wfile.write(f"event: {name}\ndata: {json.dumps(data)}\n\n".encode())
        self.wfile.flush()
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
cat > "$WORK/inner.sh" <<'SH'
#!/usr/bin/env bash
set -u
W=$1; CLAUDE=$2; U=$(id -u)
for d in /run/user/$U/cc-socks /tmp/cc-socks /tmp/cc-socks-$U; do
  if [ -e "$d" ] && [ -n "$(ls -A "$d" 2>/dev/null)" ]; then echo "FAIL isolation: $d is not empty"; exit 3; fi
done
python3 "$W/mock.py" 18090 "$W/requests.jsonl" 2> "$W/mock.err" & MOCK=$!
sleep 1
mkdir -p "$W/home" "$W/cfg" "$W/run" "$W/lane" "$W/control"; chmod 700 "$W/run" "$W/lane"
echo '{"hasCompletedOnboarding":true,"mcpServers":{"evil":{"command":"/bin/true"}}}' > "$W/cfg/.claude.json"
echo '{"permissions":{"allow":["mcp__*"],"defaultMode":"bypassPermissions"},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo HOOKRAN=${DEN_DREY_LANE:-other} >> '"$W"'/hook.txt"}]}]}}' > "$W/cfg/settings.json"
echo "SPIKE_CANARY_CLAUDE_MD" > "$W/cfg/CLAUDE.md"
envs() { echo PATH=/usr/bin:/bin HOME="$W/home" CLAUDE_CONFIG_DIR="$W/cfg" XDG_RUNTIME_DIR="$W/run" ANTHROPIC_BASE_URL=http://127.0.0.1:18090 ANTHROPIC_API_KEY=sk-ant-spike DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1; }
mapfile -d '' -t ARGV < "$W/argv"
# The lane, with Den's argv; its first turn arrives at once, then it waits.
mkfifo "$W/lane.in" "$W/control.in"
(cd "$W/lane" && env -i $(envs) DEN_DREY_LANE=1 ENABLE_CLAUDEAI_MCP_SERVERS=0 timeout 60 "$CLAUDE" "${ARGV[@]}" < "$W/lane.in" > "$W/lane.out" 2> "$W/lane.err") & LANE=$!
exec 7> "$W/lane.in"
printf '%s\n' '{"type":"user","message":{"role":"user","content":"<den_fleet>\n(no other agents running)\n</den_fleet>\n\n<client_request client=\"drey\">\nwho is busy?\n</client_request>"}}' >&7
# The control: an ordinary session that accepts peer messages.
(cd "$W/control" && env -i $(envs) timeout 60 "$CLAUDE" -p --input-format stream-json --output-format stream-json --verbose --name den-spike-control --tools ListAgents --append-system-prompt SPIKE_CONTROL < "$W/control.in" > "$W/control.out" 2> "$W/control.err") & CONTROL=$!
exec 8> "$W/control.in"
printf '%s\n' '{"type":"user","message":{"role":"user","content":"hello"}}' >&8
sleep 8
for target in lane control; do
  note="send it"; [ $target = control ] && note="send it SPIKE_TO_CONTROL"
  (cd "$W/control" && env -i $(envs) timeout 40 "$CLAUDE" -p --output-format stream-json --verbose --name den-spike-sender-$target --tools SendMessage,ListAgents --allowedTools SendMessage,ListAgents --append-system-prompt SPIKE_SENDER "$note" > "$W/sender-$target.out" 2> "$W/sender-$target.err")
done
sleep 10
exec 7>&- 8>&-
wait $LANE $CONTROL
kill $MOCK
SH
U=$(id -u); G=$(id -g)
unshare -rnm sh -c "ip link set lo up && mount -t tmpfs tmpfs /run/user/$U && for d in /tmp/cc-socks /tmp/cc-socks-$U; do [ -d \$d ] && mount -t tmpfs tmpfs \$d; done; exec unshare -U --map-user=$U --map-group=$G bash '$WORK/inner.sh' '$WORK' '$CLAUDE'" > "$WORK/inner.log" 2>&1 || true
if grep -q '^FAIL' "$WORK/inner.log"; then cat "$WORK/inner.log"; exit 1; fi
python3 - "$WORK" <<'PY'
import json, sys, os
W = sys.argv[1]
fail = []
init = None
for l in open(os.path.join(W, "lane.out")):
    try: o = json.loads(l)
    except Exception: continue
    if o.get("type") == "system" and o.get("subtype") == "init": init = o
if not init: fail.append("the lane did not start: " + open(os.path.join(W, "lane.err")).read()[:300])
else:
    for k in ("tools", "mcp_servers", "skills", "slash_commands"):
        if init.get(k): fail.append(f"init {k} = {init.get(k)}")
lane_reqs, control_got, lane_got = 0, False, False
for l in open(os.path.join(W, "requests.jsonl")):
    r = json.loads(l)
    if "count_tokens" in r["path"]: continue
    if r["role"] == "lane":
        lane_reqs += 1
        if r["tools"]: fail.append("a lane request carried tools")
        if "SPIKE_CANARY_CLAUDE_MD" in r["body"]: fail.append("CLAUDE.md reached the lane")
        if "SPIKE_PEER_MESSAGE" in r["body"]: lane_got = True
    if r["role"] == "control" and "SPIKE_PEER_MESSAGE" in r["body"]: control_got = True
if lane_reqs == 0: fail.append("the lane made no request")
if lane_got: fail.append("a peer message reached the lane")
if not control_got: fail.append("the control session never got the peer message: the check proves nothing")
hooks = open(os.path.join(W, "hook.txt")).read() if os.path.exists(os.path.join(W, "hook.txt")) else ""
if "HOOKRAN=1" in hooks: fail.append("the user's hook ran in the lane")
if "HOOKRAN=other" not in hooks: fail.append("the user's hook ran in no session: the hook check proves nothing")
version = (init or {}).get("claude_code_version", "?")
if fail:
    print("FAIL claude " + version); [print("  " + f) for f in fail]; sys.exit(1)
print("PASS claude " + version + ": add \"" + version + "\" to orch.LaneVerified")
PY
}
main "$@"
