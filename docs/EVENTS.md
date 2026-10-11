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
the next `done`, with the same exit codes and `--timeout`.

## Schema, version 1

Every event has these fields:

| Field | Type | Holds |
| --- | --- | --- |
| `v` | number | the schema version, `1` |
| `time` | string | when, RFC 3339 in UTC, milliseconds |
| `kind` | string | one of the kinds below |
| `session` | string | the Claude Code session id |
| `agent` | string | the agent's name as Den shows it, e.g. `Mossy Lemur` |

and, by kind:

| Kind | When | Extra fields |
| --- | --- | --- |
| `session_start` | a session started or resumed (`SessionStart`) | `reason`: `startup`, `resume`, `clear` or `compact` |
| `session_end` | a session ended (`SessionEnd`) | `reason`, when Claude Code gives one |
| `turn_start` | a prompt was submitted (`UserPromptSubmit`) | |
| `done` | a turn finished (`Stop`) | |
| `needs_you` | a request started waiting on you (`PermissionRequest`, `AskUserQuestion`, an input notice) | `request`: `permission`, `plan` or `question`; `tool`: the tool name, for a permission |
| `idle` | Claude Code's idle notice | |
| `turn_failed` | a turn ended on an API error (`StopFailure`) | `error`: the error type, e.g. `rate_limit`, `overloaded`, `authentication_failed`, `unknown` |
| `tool_failed` | a tool call failed (`PostToolUseFailure`) | `tool`: the tool name |
| `stalled` | Den saw the agent working with no hook event for `stuck.stalled` minutes | `minutes` |
| `still_waiting` | a request is still waiting on you after `notify.remind` minutes (Den) | `request`, `minutes` |

```json
{"v":1,"time":"2026-10-11T21:22:46.685Z","kind":"needs_you","session":"6070ed32-568e-46a3-9690-ac2048b12385","agent":"Mossy Lemur","request":"permission","tool":"Bash"}
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
`DEN_MINUTES` and `DEN_REASON`. Event values reach the command only through
these, never spliced into the command text.

Commands run in the background, so a slow one never holds Den up, and are
killed after 30 seconds. Their output is discarded: an `[on]` command cannot
answer a permission or anything else. A failure is logged to `hooks.log`. Den
runs `needs_you` only for a request that waits on you, not one it allowed by an
earlier "always". `[on]` runs for every event of its kind; the tree-aware
silencing of desktop notifications does not apply.
