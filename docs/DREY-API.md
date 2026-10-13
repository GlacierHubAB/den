# Den's local API (den-api.sock)

Den can serve a small local API so that apps on your machine can show your
fleet and ask about it. Drey is the first such app. The API is **off** until
you turn it on, and it lets no app approve, answer, start, close or message
anything.

## Turning it on and off

- `/api on` in Den shows what the API lets apps do and asks you to confirm
  with `Ctrl+Y` once you have read it. Den then writes `api.enabled = true` to
  its config. An app never writes Den's config; Drey shows "Turn on the Den
  API: type /api on in Den" when the API is off.
- `/api off` drops every app at once, refuses new ones and writes
  `api.enabled = false`.
- `/api disconnect` drops every app until `/api resume` (or a restart).
- `/api` lists the connected apps: process id, the executable Den read from
  `/proc`, what they subscribe to, and whether one is asking.
- While the API is on, Den's header shows `API on`, or `⇄ <app> connected`
  (the executable's name as Den read it, never the name an app gives itself).
  The first connection of each app also leaves a note in the orchestrator
  chat.

```toml
[api]
enabled = false      # turned on by /api on
ask_budget_usd = 2   # what apps' questions may spend a day
```

## Transport

- A Unix socket at `$XDG_RUNTIME_DIR/den-api.sock`, mode `0600`, served by the
  running Den. Without `XDG_RUNTIME_DIR`, or when that folder is not private
  to you, there is no API. Den binds the socket at a temporary name, sets its
  mode and renames it into place; it never touches a path that holds anything
  but a dead socket of your own, and leaves the path bound while it runs, even
  after `/api off`, so no other program can take it.
- **Same user only.** Before reading a byte, Den reads the peer's credentials
  from the kernel (`SO_PEERCRED`) and refuses another user with one line,
  `{"id":0,"ok":false,"error":{"code":"forbidden"}}`, and a close.
- **Not agents.** A client whose process runs under one of Den's agents, its
  orchestrator or its read-only lane gets `forbidden` for every verb after
  `hello`, and Den notes it. (Any program running as you can still connect;
  every verb is built to be safe for that.)
- JSON lines, UTF-8, one object per line. A line over 1 MiB gets
  `{"id":0,"ok":false,"error":{"code":"too_large"}}` and a close.
- `hello` first: any other op before it fails with `hello_first`.
- At most 16 connections, 6 per process. `hello` must come within 5 seconds.

## Lines

```json
{"v":1,"id":7,"op":"inbox.body","params":{"id":"i-1"}}
{"id":7,"ok":true,"result":{"id":"i-1","kind":"permission","text":"…","hash":"sha256:…"}}
{"id":7,"ok":false,"error":{"code":"not_found","message":"no item i-1"}}
{"event":"fleet","data":{"taken":"…","agents":[…],"windows":[…],"missions":[…],"cost":{…}}}
```

A subscribing op is answered first; then Den pushes `event` lines on that
connection until either side closes it.

Error codes: `unsupported` (an unknown op or version: hide the feature),
`bad_request`, `not_found`, `too_large`, `hello_first`, `forbidden`,
`unavailable` (Den has the verb but cannot serve it now), `internal`.

## Versioning

`v` is 1. `hello {v, client, version}` returns `{version, versions:[1],
host}`; a `v` Den does not serve gets `unsupported`. Added fields never change
`v`; a removed or changed verb does.

## Verbs

| Op | Params | Result |
|---|---|---|
| `hello` | `{v, client, version}` | `{version, versions, host}` |
| `fleet.snapshot` | | `Fleet` |
| `fleet.events` | | `{subscribed:true}`, then a `fleet` event at once and on every change |
| `inbox.list` | | `[Item]`, each with a digest of its body, never the body |
| `inbox.events` | | `{subscribed:true}`, then `inbox` events `{change: added\|answered, item}` |
| `inbox.body` | `{id}` | `{id, kind, text, hash}` for a request on this machine; `unavailable` for one from another machine |
| `mission.list` | | `[Mission]` |
| `ask` | `{text, session?}` | `{session, ask}`, then `chat` events until a `done` or `error` chunk |
| `chat.history` | `{session}` | `[Message]` of an ask session |
| `chat.sessions` | | `[{id, title, last, current}]`, newest first, exactly one current |

Every other op answers `unsupported`, including every verb that would approve,
allow, deny, answer, start, close or message: there are none. This release
also answers `unsupported` for `focus`, `grant.request`, `grant.events`,
`theme.get`, `mission.start`, `mission.drop` and `view.data`.

## Types

Metadata only: names, states, ids, times, counts and digests. No prompt text,
no transcript, and no request body outside `inbox.body`.

- **Fleet** `{taken, agents, windows, missions, cost, asks?}`. An agent is
  `{id, name, role?, state, waiting?, worktree?, model?, since, window?, host?,
  remote?, costUsd, turns, lastSeen}`; `state` is `idle`, `working`, `waiting`
  or `stalled`, and `waiting` names what waits: `permission`, `question` or
  `plan`. Agents on other machines carry `remote: true` and their host's
  label; what their hosts report is passed on as labels, never as Den's own
  words. Windows of agents on other machines are not listed.
- **Item** `{id, kind, agent, since, title, digest, host?}`. A **digest** is
  `{firstLine, bytes, hash}`, with `hash` `"sha256:"` + the hex SHA-256 of the
  body; `inbox.body` returns the same hash. For a request from another machine
  the digest describes it in Den's words ("a permission request") and hashes
  that, never the host's text.
- **Mission** `{id, title, state, agents?, lines, done?, spendUsd?,
  budgetUsd?, proposed}`; `state` is `proposed`, `running`, `done` or
  `dropped`.
- **Chunk**, the `ask` stream: `{"type":"text","delta":"…"}`,
  `{"type":"status","ask":"…","seq":n,"text":"…"}`, `{"type":"done",
  "session":"…"}` or `{"type":"error","message":"…"}`. Status lines are Den's
  own fixed sentences ("Reading the fleet in Den…", "Writing a reply…"),
  never model text; `seq` rises per ask, and an empty `text` clears the line
  before `done`. The fleet's `asks` lists each running ask's latest status;
  only the app that asked sees its session id there.

## Asking

`ask` goes to a **read-only lane**: a second Claude Code process Den starts for
apps, apart from the orchestrator you talk to. It has no tools and no MCP
servers, loads no skills, plugins, hooks, slash commands, `CLAUDE.md` or
memory, refuses messages from other sessions, and runs in an empty folder of
its own. Den checks what the process reports at start and stops it unless it
has no tools and no MCP servers. Whatever an app asks, the lane can only write
text: it cannot message, start, close or approve anything, and nothing it
writes changes Den's state.

The question goes to the lane fenced as an app's request, after the same
roster the orchestrator reads (without agents' recent words and tool calls,
and with what waits on you named by kind and title only). A question with a
control, bidi or invisible character, or over 16 KiB, is refused with
`bad_request`. One question runs at a time; up to four wait, one per app. A
reply is cut at 64 KiB or 3 minutes. Asks stop for the day once
`api.ask_budget_usd` is spent. The lane stops after ten minutes without
questions and resumes its session on the next one.

`chat.sessions` and `chat.history` serve these ask sessions only, never your
own orchestrator chat. They are kept in `~/.local/state/den/drey-chat.jsonl`
(mode `0600`).
