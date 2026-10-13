# Den events

Den keeps a stream of what happens to your agents for scripts:
`~/.local/state/den/events.jsonl` (under `$XDG_STATE_HOME/den/` when set), one
JSON object per line. `den hook` appends to it as Claude Code's hooks fire, so
it fills whether or not Den is running. The running Den adds the two kinds only
it can tell: `stalled` and `still_waiting`. `events.log = false` stops the
stream. When the file passes 4 MB it moves to `events.jsonl.1` and starts again.

An event carries metadata only: what happened, to which session, the agent's
name, when, and a few fields named below. Never a command, tool input or
output, a prompt, a message or file text. The orchestrator's own turns are left
out.

## Reading it

```
den events -json                                # follow the stream from now on
den wait mossy --until done --timeout 30m       # block until Mossy Lemur's turn is done
den send --wait mossy "rebase on main first"    # deliver, then wait for that turn to end
```

`den events -json` prints each event as it is appended. It does not listen on
Den's socket, so it runs beside Den. (Plain `den events` is the older headless
listener for hook debugging and cannot run while Den does.)

`den wait <agent> --until idle|needs-you|done [--timeout 10m]` needs no running
Den. `idle` is Claude Code's own idle status in `~/.claude/sessions/`, reached
at once if the agent is idle already. `needs-you` and `done` are the next
`needs_you` or `done` event for that agent after `den wait` starts. Exit codes:

| Code | Means |
| --- | --- |
| 0 | the state was reached |
| 1 | bad usage, or no agent matches |
| 2 | `--timeout` passed first |
| 3 | the agent's session ended or its process is gone |

`den send --wait` starts following before the message goes out and waits for
the next `done`, with the same exit codes and `--timeout`. `den send name@host`,
for an agent on a remote host, has exit codes of its own, in
[REMOTE.md](REMOTE.md).

## Schema, version 1

Every event has these fields:

| Field | Type | Holds |
| --- | --- | --- |
| `v` | number | the schema version, `1` |
| `time` | string | when, RFC 3339 in UTC, milliseconds |
| `kind` | string | one of the kinds below |
| `session` | string | the Claude Code session id |
| `agent` | string | the agent's name as Den shows it, e.g. `Mossy Lemur` |
| `host` | string | only on an event from a [remote host](REMOTE.md): the host's label. Absent for this machine. A running Den appends remote events to the stream with their time on this machine's clock |

and, by kind:

| Kind | When | Extra fields |
| --- | --- | --- |
| `session_start` | a session started or resumed (`SessionStart`) | `reason`: `startup`, `resume`, `clear` or `compact` |
| `session_end` | a session ended (`SessionEnd`) | `reason`, when Claude Code gives one |
| `turn_start` | a prompt was submitted (`UserPromptSubmit`) | |
| `done` | a turn finished (`Stop`) | |
| `needs_you` | a request started waiting on you (`PermissionRequest`, `AskUserQuestion`, an input notice); one wait is one event, so a question is not reported again as a permission | `request`: `permission`, `plan` or `question`; `tool`: the tool name, for a permission |
| `idle` | Claude Code's idle notice | |
| `turn_failed` | a turn ended on an API error (`StopFailure`) | `error`: the error type, e.g. `rate_limit`, `overloaded`, `authentication_failed`, `unknown` |
| `tool_failed` | a tool call failed (`PostToolUseFailure`) | `tool`: the tool name |
| `stalled` | Den saw the agent working with no hook event for `stuck.stalled` minutes | `minutes` |
| `still_waiting` | a request is still waiting on you after `notify.remind` minutes (Den) | `request`, `minutes` |

```json
{"v":1,"time":"2026-10-11T21:22:46.685Z","kind":"needs_you","session":"11111111-2222-4333-8444-555555555555","agent":"Mossy Lemur","request":"permission","tool":"Bash"}
```

A `needs_you` from `den hook` is written before Den has seen the request, so
one that Den then allows by an earlier "always" still appears here. Fields may
be added within version 1; a field is never renamed or given another meaning
without a new `v`.

## Commands on events

The `[on]` section of `config.toml` runs a shell command (`sh -c`) per event
kind while Den is running. It is empty by default.

```toml
[on]
needs_you = "ntfy publish my-den-topic \"$DEN_AGENT needs you\""
turn_failed = "notify-send \"$DEN_AGENT stopped: $DEN_ERROR\""
```

The command gets the event as one JSON line on stdin and as environment
variables: `DEN_EVENT` (the kind), `DEN_EVENT_VERSION`, `DEN_SESSION`,
`DEN_AGENT`, `DEN_TIME`, and when set `DEN_REQUEST`, `DEN_TOOL`, `DEN_ERROR`,
`DEN_MINUTES`, `DEN_REASON` and, for an event from a remote host, `DEN_HOST`
(its label). The command runs on this machine. Event values reach the command only through
these, never spliced into the command text.

Commands run in the background, so a slow one never holds Den up, and are
killed after 30 seconds. Their output is discarded: an `[on]` command cannot
answer a permission or anything else. A failure is logged to `hooks.log`. Den
runs `needs_you` only for a request that waits on you, not one it allowed by an
earlier "always". `[on]` runs for every event of its kind; the tree-aware
silencing of desktop notifications does not apply.

## What Claude Code sends

The stream is made from Claude Code's hook payloads. What Claude Code 2.1.289
was seen to send, for anyone reading `hooks.log` or writing their own hooks:

- Besides the documented fields, payloads carry `prompt_id` (the turn) and
  `effort`; `SessionStart` and `UserPromptSubmit` carry `session_title`,
  `PostToolUse` carries `duration_ms`, and `Stop` carries `background_tasks`
  and `session_crons`. A `PermissionRequest` has no `tool_use_id`; the
  `PreToolUse` and `PostToolUse` of the same call do.
- Claude Code shows its own permission dialog at once, in parallel with the
  `PermissionRequest` hook, not after the hook exits. While Den holds the
  hook, the request can always be answered in the terminal; whichever answers
  first counts, and Claude Code ends the hook when its dialog is answered or
  dismissed.
- A `Notification` of type `permission_prompt` follows a `PermissionRequest`
  by about 6 seconds, while the hook still holds: it is the same wait, so it
  makes no `needs_you` of its own. An `idle_prompt` follows a `Stop` by about
  5 seconds.
- `AskUserQuestion` fires `PreToolUse` and, about 60 ms later, a
  `PermissionRequest`. The stream reports the question once, from the
  `PreToolUse`.
- A plan (`ExitPlanMode`) is approved by a `PermissionRequest` hook only with
  an allow that carries the call's own `tool_input` as `updatedInput`; a bare
  allow is ignored and the plan dialog stays open. The agent then leaves plan
  mode for the mode it had before. A deny with a message sends the plan back,
  the message as the tool's error, and the agent stays in plan mode.
