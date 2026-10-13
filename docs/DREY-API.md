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

`v` is 1. `hello {v, client, version, session?}` returns `{version,
versions:[1], host}`; a `v` Den does not serve gets `unsupported`. `session`
is optional: 32 hex digits (128 random bits) the app draws once per process
and sends on each of its connections, so that its asks on all of them count
as one app's (see Asking); any other shape gets `bad_request`. Without it,
each connection is on its own. Added fields never change
`v`; a removed or changed verb does.

## Verbs

| Op | Params | Result |
|---|---|---|
| `hello` | `{v, client, version, session?}` | `{version, versions, host}` |
| `fleet.snapshot` | | `Fleet` |
| `fleet.events` | | `{subscribed:true}`, then a `fleet` event at once and on every change |
| `inbox.list` | | `[Item]`, each with a digest of its body, never the body |
| `inbox.events` | | `{subscribed:true}`, then `inbox` events `{change: added\|answered, item}` |
| `inbox.body` | `{id}` | `{id, kind, text, hash}` for a request on this machine; `unavailable` for one from another machine |
| `mission.list` | | `[Mission]` |
| `focus` | `{target}` an agent's id or the name Den gave it | `{focused}`: that agent's terminal comes to the front. An inbox item answers `unavailable` (open the Inbox in Den), as do an agent on another machine and any focus within 1.5 s of your last key in Den. One per app every 3 s, ten a minute in all. Den's own view, selection and command line never change. |
| `ask` | `{text, session?}` | `{session, ask}`, then `chat` events until a `done` or `error` chunk. `ask` is 128 random bits, told only to the connection that asked: it is what cancels the ask, so keep it to yourself |
| `ask.cancel` | `{ask}` | `{cancelled:true}`; the ask's stream ends with an `error` chunk `"cancelled"`. It takes the ask's id, from the program that asked; any other id is `not_found` |
| `chat.history` | `{session}` | `[Message]` of an ask session |
| `chat.sessions` | | `[{id, title, last, current}]`, newest first, exactly one current |
| `grant.request` | `{manifest}` | `{id}`; the answer comes on `grant.events`, never here |
| `grant.events` | | `{subscribed:true}`, then `grant` events `{id, answer: granted\|denied\|expired, version, hash}` |

Every other op answers `unsupported`, including every verb that would approve,
allow, deny, answer, start, close or message: there are none. This release
also answers `unsupported` for `theme.get`, `mission.start`,
`mission.drop` and `view.data`.

## Grants

`grant.request {manifest: {id, name, version, capabilities, provenance?,
body}}` is the one way an app asks you for something. Den puts a card in its
inbox: a title and lines of its own (which app asked, by the executable Den
read, and the capabilities), then `body` exactly as the app sent it, between
rules Den draws. You answer with `Ctrl+Y` (granted) or `Ctrl+N` (denied), and
`Ctrl+Y` counts only once the whole card has been on screen; there is no
"always". The answer goes to `grant.events` connections of the same
executable that asked, never to another app; an app that is away gets it
when it subscribes again. Its `hash` is `"sha256:"` + the hex SHA-256 of the
body Den showed, so an app records a grant only when that hash is the one it
computed.

Den refuses, with `bad_request`, a body that is empty, over 16 KiB or 200
lines, not valid UTF-8, holds a tab, a control, bidi or invisible character,
or draws rules like Den's own frame: it shows a body exactly or not at all.
An app may have three requests waiting, and Den takes twenty per run; a body
you denied is not asked again until Den restarts. A request expires when its
app has had no connection for 30 seconds, on `/api disconnect`, and when Den
exits.

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
  `{"type":"status","seq":n,"text":"…"}`, `{"type":"done","session":"…"}`
  or `{"type":"error","message":"…"}`, each with the `ask` it belongs to. Status lines are Den's
  own fixed sentences ("Reading the fleet in Den…", "Writing a reply…"),
  never model text; `seq` rises per ask, and an empty `text` clears the line
  before `done`. The fleet's `asks` lists each running and waiting ask's
  latest status; its `ask` and `session` ids are there only for the
  connection that asked, or one with the same hello `session`.

## Asking

`ask` goes to a **read-only lane**: a second Claude Code process Den starts for
apps, apart from the orchestrator you talk to. It has no tools and no MCP
servers, loads no skills, plugins, hooks, slash commands, `CLAUDE.md` or
memory, refuses messages from other sessions, and runs in an empty folder of
its own. Den checks what the process reports at start and stops it unless it
has no tools and no MCP servers and runs a Claude Code release that
`scripts/lane-spike.sh` has passed: that script starts the lane offline with
a hostile user config and checks that it gets no tools, MCP servers, skills,
hooks or `CLAUDE.md`, and that a message from another session never reaches
it. On any other release `ask` answers with an error until the spike is run
and the release added. Whatever an app asks, the lane can only write
text: it cannot message, start, close or approve anything, and nothing it
writes changes Den's state.

The question goes to the lane fenced as an app's request, after the same
roster the orchestrator reads (without agents' recent words and tool calls,
and with what waits on you named by kind and title only). A question with a
control, bidi or invisible character, or over 16 KiB, is refused with
`bad_request`. One question runs at a time and up to four wait; a waiting
question gets a status saying how many are ahead. A connection, or the
connections that sent one hello `session`, have one question at a time: a
newer question from them replaces their earlier one, running or waiting,
whose stream ends with the error `"superseded by a newer question"`.
Another process of the same program, with no session or its own, never
replaces, cancels or sees the ids of their questions; its question waits
its turn. When the
connection that asked closes, or stops reading, its turn stops and the next
question runs. Every ask stream ends with a `done` or an `error` chunk; when
the lane cannot start (a release the spike has not passed, a failed check, the
budget, claude not starting) the error says why within seconds. A reply is
cut at 64 KiB or 3 minutes, and a lane silent for 90 seconds is stopped.
`/api` shows the lane's state (off, starting, ready, failed and why), what
runs, what waits and today's spend, which Den keeps across restarts. A lane
that fails to start waits a second before it tries again, twice as long each
time up to a minute; after three failures in five minutes asks are refused
until `/api resume`. A lane session starts afresh after 50 questions. Asks stop for the day once
`api.ask_budget_usd` is spent. The lane stops after ten minutes without
questions and resumes its session on the next one.

`chat.sessions` and `chat.history` serve these ask sessions only, never your
own orchestrator chat. They are kept in `~/.local/state/den/drey-chat.jsonl`
(mode `0600`).
