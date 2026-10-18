# den stats

`den stats` counts what Den's own state files prove about one window of time:
how much the agents worked, how often they waited on you, what you said to the
orchestrator, and what landed. It reads the files and writes nothing, and it
needs no running Den.

It does not measure hours saved, quality or the value of the work. Those need
a baseline the files do not hold, so no figure here claims them.

```
den stats                                  the last 7 days
den stats --days 30
den stats --since 2026-10-11 --until 2026-10-18
den stats --since 2026-10-11T00:00:00Z --json
```

The window is `[since, until)`. `--since` and `--until` take an RFC 3339 time,
`2006-01-02 15:04` or a date (local time, the start of that day); `--until`
defaults to now. `--days N` counts back from now and cannot be combined with
the others. The output prints the exact window and the command that repeats
it, so a figure can be reproduced from the same files. The files only grow,
except that the event stream keeps its last 4 to 8 MB (`events.jsonl` and
`events.jsonl.1`), so an old window can lose its start; the last section of the
output says where each file begins.

## What it counts

| Figure | From | Meaning and limits |
| --- | --- | --- |
| turns finished | `events.jsonl` | `done` events in the window. Turns that ended on an API error (`turn_failed`) are counted apart |
| prompts submitted | `events.jsonl` | `turn_start` events. A prompt sent while an agent works, or one that interrupted a turn, is a prompt but not a turn of its own, so prompts outnumber turns |
| working time, turn length | `events.jsonl` | for each finished turn, its `done` less the session's latest `turn_start` before it. A turn whose start is not recorded (Claude Code started it without a prompt) is counted as finished but not timed; one cut off by an `idle` notice, a session end or a restart is not timed. A prompt sent mid-turn makes the turn count from the later prompt. So the time is a lower bound |
| agents at once | `events.jsonl` | the most timed turns running at one moment, and the timed working time over the length of the window. A lower bound too |
| requests that waited on you | `events.jsonl` | `needs_you` events by request. One that a standing "always" then allowed is in the stream too |
| wait | `events.jsonl`, `hooks.log` | from the request to the session's next hook run in `hooks.log` (a tool result, a stop, a prompt or a session end) stamped at or after the request and before the session's next request. It is when the agent was next heard from: it includes the time the answered tool then ran, and a question left open overnight counts the whole night. A request with no such run is left out ("measured for N of M"). `hooks.log` keeps whole seconds in local time and needs `hooks.log = true`, the default |
| from you | `orchestrator.jsonl` | chat records of kind `you` outside a mission thread, which include review comments you handed to the orchestrator |
| mission lines by Den | `orchestrator.jsonl` | records of kind `you` inside a mission thread: the lines Den writes to start a run or ask for its judging. Not your words, so not in "from you" |
| from paired apps | `orchestrator.jsonl` | records of kind `app` |
| merges, branch lifetime | `missions.jsonl` | the branches Den saw land on a mission's integration branch in the window, and how long each lived from its oldest commit of its own. Den keeps the newest 500 per mission |
| cost per merge, overlaps, nudges | `metrics.jsonl` | for each mission with a record in the window, its latest record there, summed. These are totals since each mission began, not figures of the window. The cost is Den's estimate over the merged changes, "not recorded" with none merged |

Agents' own reports to the orchestrator are not in the chat log, so they are
in none of these counts.

## Not recorded

- **Merged pull requests.** Den records branches it saw land, not pull requests.
- **Den's own cards** (merge, close, mission approval and the rest of the cards
  that ask you). Nothing is kept of how many there were or how long they waited.
- **CI results.** Den cannot see CI.

A section whose file is missing or cannot be read says "not recorded" too, with
the reason in the last section. In `--json` output, `null` means not recorded,
and `not_recorded` lists the three above.

## JSON

Top-level keys: `since`, `until` (UTC), `turns`, `waits`, `messages`, `merges`,
`metrics`, `not_recorded` and `sources`. Durations are in seconds, except
`branch_hours_*`. A figure that was not recorded is `null`. `sources` has, for
each file read, how many records it held, how many lines were skipped, the
first and last record time, and why it could not be read, if it could not.

## What it reads, and how

The files are untrusted data. `den stats` opens each one read-only, refuses a
symlink or anything but a regular file, reads at most the newest 256 MiB of it,
skips a line over 4 MiB, and skips and counts a line that does not parse. It
does not use the loaders that tidy a file as they read it. It prints counts,
durations and times, never a message, a prompt, a file path, a session id, an
agent's name or an agent's output.
