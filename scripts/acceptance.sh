#!/usr/bin/env bash
# Clean-machine acceptance, for CI (.github/workflows/acceptance.yml).
#
#   scripts/acceptance.sh <tag> <new-den>
#
# Installs the released den <tag> into a fresh HOME (checking SHA256SUMS and the
# build attestation first), installs its hooks, lists a fake Claude Code
# session, then swaps in <new-den> (the build under test) as an upgrade and
# checks again: the hooks are still there, `den doctor` agrees, `den list`
# still sees the session, and a missing hook is reported. Touches nothing
# outside its own HOME. Needs gh (authenticated by GH_TOKEN), jq and curl.
set -euo pipefail

tag=${1:?usage: acceptance.sh <tag> <new-den>}
new=$(readlink -f "${2:?usage: acceptance.sh <tag> <new-den>}")
repo=${GITHUB_REPOSITORY:-GlacierHubAB/den}
[ -x "$new" ] || { echo "::error::$new is not an executable"; exit 1; }

fail() { echo "::error::$*"; exit 1; }
step() { echo; echo "== $*"; }

# The fresh HOME lives under the runner's real home, not under a temp
# directory: `den install-hooks` refuses a binary in one.
work=$(mktemp -d -p "$HOME" den-acceptance.XXXXXX)
fake_pid=
cleanup() { [ -z "$fake_pid" ] || kill "$fake_pid" 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

step "download $tag and check it"
rel=$work/release
mkdir -p "$rel"
gh release download "$tag" --repo "$repo" --dir "$rel" --pattern den_linux_amd64 --pattern SHA256SUMS
(cd "$rel" && sha256sum -c --ignore-missing SHA256SUMS | tee /dev/stderr | grep -q 'den_linux_amd64: OK') || fail "SHA256SUMS does not match den_linux_amd64"
gh attestation verify "$rel/den_linux_amd64" --repo "$repo" || fail "the build attestation of $tag does not verify"

step "a clean HOME with a fake Claude Code"
h=$work/home
mkdir -p "$h/.local/bin" "$h/.claude/sessions" "$work/bin"
export HOME=$h CLAUDE_CONFIG_DIR=$h/.claude
export XDG_CONFIG_HOME=$h/.config XDG_STATE_HOME=$h/.local/state XDG_DATA_HOME=$h/.local/share XDG_CACHE_HOME=$h/.cache
export XDG_RUNTIME_DIR=$work/run
mkdir -p "$XDG_RUNTIME_DIR" && chmod 700 "$XDG_RUNTIME_DIR"
unset DEN_SOCKET DEN_CONFIG TMUX
printf '#!/bin/sh\necho "2.1.309 (Claude Code)"\n' > "$work/bin/claude"
chmod +x "$work/bin/claude"
export PATH=$work/bin:$h/.local/bin:$PATH

sleep 3600 &
fake_pid=$!
sid=00000000-0000-4000-8000-0000acce9700
now=$(( $(date +%s) * 1000 ))
cat > "$h/.claude/sessions/$fake_pid.json" <<JSON
{"pid": $fake_pid, "sessionId": "$sid", "cwd": "$h", "startedAt": $now, "version": "2.1.309",
 "kind": "interactive", "entrypoint": "cli", "status": "idle", "updatedAt": $now, "statusUpdatedAt": $now}
JSON

den=$h/.local/bin/den
hook_events() { jq -r '[.hooks // {} | to_entries[] | select(any(.value[].hooks[]; .command | contains("/.local/bin/den"))) | .key] | sort | join(",")' "$CLAUDE_CONFIG_DIR/settings.json"; }
sees_session() { "$den" list --json | jq -e --arg id "$sid" 'any(.[]; .Session.sessionId == $id)' > /dev/null; }
doctor_status() { "$den" doctor --json | jq -r --arg id "$1" '.[] | select(.id == $id) | .status'; }

step "released $tag: install hooks, list"
install -m 755 "$rel/den_linux_amd64" "$den"
"$den" version
"$den" install-hooks
old_events=$(hook_events)
[ -n "$old_events" ] || fail "$tag installed no hooks"
echo "hooks: $old_events"
sees_session || fail "$tag does not list the fake session"

step "upgrade to the build under test"
install -m 755 "$new" "$den"
"$den" version
[ "$(hook_events)" = "$old_events" ] || fail "the upgrade changed Den's hooks: was $old_events, now $(hook_events)"
sees_session || fail "the new build does not list the fake session"
"$den" doctor || true
[ "$(doctor_status hooks)" = ok ] || fail "doctor does not report the hooks as ok after the upgrade"
[ "$(doctor_status sessions)" = ok ] || fail "doctor does not report the sessions as ok"

step "install-hooks again is safe"
"$den" install-hooks
[ "$(hook_events)" = "$old_events" ] || fail "a second install-hooks changed the hooks: now $(hook_events)"

step "a missing hook is reported"
"$den" uninstall-hooks
[ -z "$(hook_events)" ] || fail "uninstall-hooks left hooks behind: $(hook_events)"
[ "$(doctor_status hooks)" = fail ] || fail "doctor does not fail when Den's hooks are missing"

echo
echo "clean-machine acceptance passed: $tag, then $("$den" version)"
