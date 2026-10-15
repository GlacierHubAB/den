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
  `hello`. Den walks the client's parents and matches each against the live
  Claude sessions on this machine by pid and start time, so a pid an ended
  or remote session once had never counts. Den's chat notes the first
  refusal of each program, naming the agent it runs under, then at most one
  count a minute; `/api` lists them all. (Any program running as you can still connect;
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

`v` is 1. `hello {v, client, version, group?}` returns `{version,
versions:[1], host}`; a `v` Den does not serve gets `unsupported`. `group`
is optional: 32 lowercase hex digits (128 random bits) the app draws once
per process and sends on each of its connections, so that its asks on all
of them count as one app's (see Asking); any other shape gets
`bad_request`. Without it, each connection is on its own. A group is not a
secret like an ask id, but whoever holds it can replace the group's
questions: keep it in the process that drew it, and never pass it to a
child process or another program. Added fields never change
`v`; a removed or changed verb does.

## Verbs

| Op | Params | Result |
|---|---|---|
| `hello` | `{v, client, version, group?}` | `{version, versions, host}` |
| `fleet.snapshot` | | `Fleet` |
| `fleet.events` | | `{subscribed:true}`, then a `fleet` event at once and on every change |
| `inbox.list` | | `[Item]`, each with a digest of its body, never the body |
| `inbox.events` | | `{subscribed:true}`, then `inbox` events `{change: added\|answered, item}` |
| `inbox.body` | `{id}` | `{id, kind, text, hash}` for a request on this machine; `unavailable` for one from another machine |
| `mission.list` | | `[Mission]` |
| `focus` | `{target}` an agent's id or the name Den gave it | `{focused}`: that agent's terminal comes to the front. An inbox item answers `unavailable` (open the Inbox in Den), as do an agent on another machine and any focus within 1.5 s of your last key in Den. One per app every 3 s, ten a minute in all. Den's own view, selection and command line never change. |
| `ask` | `{text, session?}` | `{session, ask}`, then `chat` events until a `done` or `error` chunk. `ask` is 128 random bits, told only to the connection that asked: it is what cancels the ask, so keep it to yourself |
| `ask.cancel` | `{ask}` | `{cancelled:true}`; the ask's stream ends with an `error` chunk `"cancelled"`. It takes the ask's id, on any connection of the program that asked; any other id is `not_found` |
| `chat.history` | `{session}` | `[Message]` of an ask session |
| `chat.sessions` | | `[{id, title, last, current}]`, newest first, exactly one current |
| `grant.request` | `{manifest}` | `{id}`; the answer comes on `grant.events`, never here |
| `grant.events` | | `{subscribed:true}`, then `grant` events `{id, answer: granted\|denied\|expired, version, hash}` |

Every other op answers `unsupported`, including every verb that would approve,
allow, deny, answer, start, close or message: there are none. Control's
verbs (`pair.claim`, `pair.challenge`, `pair.auth`, `orch.say`) are under
Control below. This release
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
  latest status, which every app sees; its `ask` and `session` ids are
  there only for the connection that asked, or one in the same hello
  `group`.

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
connections that sent one hello `group`, have one question at a time: a
newer question from them replaces their earlier one, running or waiting,
whose stream ends with the error `"superseded by a newer question"`.
Another process of the same program, with no group or its own, never
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

## Control

Control lets one paired app (Drey's `drey-host`) speak to the orchestrator
you talk to in Den, as you would from Den's chat. It is off until you pair
an app, and everything that would change your fleet still waits for you in
Den: the app never approves, answers or starts anything.

### Pairing

- `/api pair` in Den shows a code of eight words for two minutes, in Den's
  header only, with "Type this only into Drey's pairing window." It is
  never written to Den's chat or logs, and it leaves the screen once it is
  claimed or expires. The words come from a list of 256, so a code is 64
  random bits.
- The app sends it once: `pair.claim {code}`. Words are matched in order,
  in any case, with any separators. Three wrong codes end the attempt;
  `/api pair` starts another.
- A correct claim does not pair yet. Den puts a card in its inbox, "Pair
  <program> (pid N, started HH:MM) for dock control?", which also names the
  program's path, its command line and its parent. Only your `Ctrl+Y` on
  it, once the whole card has been shown, completes the pairing; `Ctrl+N`,
  two minutes without an answer or the app's connection closing refuse it.
  Until then `pair.claim` has no answer, for at most two minutes from the
  claim; afterwards it answers `{pairing, key, control}` (`pairing` and
  `key` 32 lowercase hex digits each, `key` 128 random bits).
- A refused claim answers `forbidden` with one of these messages, exactly:
  `no code waiting`, `wrong code`, `code spent` (this claim reused a code,
  or came within two minutes of another claim), `revoked: the code was used
  by another program too` (a waiting claim, answered at once when another
  claim revoked it), `denied in Den` (`Ctrl+N`) or `not confirmed in time`.
  `Den is busy` comes with `unavailable`.
- The code is single use. A second `pair.claim` within a claimed code's two
  minutes, or a claimed code again within ten minutes (even after another
  `/api pair`), means another program had the code too (anything that can
  read Den's screen can): Den revokes the pairing that code made, closes its
  connections and notes "the pairing code was used by another program
  (<program>, pid N); pair again".
- Den and the app keep the key in memory only; when either restarts, pair
  again. A pairing is bound to the program Den read for the app (its
  executable): no other program can use it.
- `pair.claim`, like every verb, is refused for processes under Den's
  agents, its orchestrator or its lane; and it answers only while a code is
  live.
- If an `ask` or `orch.say` text holds the pairing code (after Unicode NFKC
  and lower case, five or more of its eight words as whole words, in any
  order, while the code is live and for ten minutes after it was claimed),
  Den refuses the text with `bad_request`, cancels the code (it is no longer
  secret), revokes the pairing a claimed code made, and notes "The pairing
  code was typed into a chat; pair again." Den never keeps the refused
  text.

### Proving the key

Each connection that wants control proves the key once, at its start; the
connection is a peer-checked unix socket that cannot be taken over
afterwards, so no later message carries a MAC.

- `pair.challenge {}` answers `{nonce, conn}`, 32 lowercase hex digits each:
  a single-use nonce for this connection, and the connection's name. One
  challenge a second per connection, else `unavailable`.
- `pair.auth {pairing, mac}`, with `mac` the hex HMAC-SHA256 under the key
  of `"pair-auth\napp-to-den\n" + conn + "\n" + nonce`, answers `{paired:
  true, control: true, mac}`, where `mac` is Den's own proof,
  `"pair-auth\nden-to-app\n" + conn + "\n" + nonce` under the same key:
  check it, so you know you reached the Den that holds the key.
- A wrong `mac` answers `forbidden` and closes the connection; three wrong
  ones in five minutes revoke the pairing, with a note in Den. Only a wrong
  `mac` counts. A pairing that is gone (revoked, refused, or Den restarted)
  answers `not_found`; one that belongs to another program `forbidden`; no
  challenge first `bad_request`.
- `orch.say` on a connection that has not passed `pair.auth` answers
  `forbidden`.

### Speaking to the orchestrator

| Op | Params | Result |
|---|---|---|
| `pair.claim` | `{code}` | `{pairing, key, control}`, once you `Ctrl+Y` in Den |
| `pair.challenge` | | `{nonce, conn}` |
| `pair.auth` | `{pairing, mac}` | `{paired, control, mac}` |
| `orch.say` | `{text}` | `{turn}`, then `orch` events until a `done` or `error` chunk |

- Text: at most 16 KiB. Den applies Unicode NFKC, breaks lines at `\n`,
  `\r\n`, `\r`, U+0085, U+2028 and U+2029, removes every C0 and C1 control
  (tab, escape and bracketed-paste markers included), refuses a bidi or
  invisible character, and refuses the whole text with `bad_request` if any
  line starts with `/`, `!` or `#` once leading white space is trimmed. `<`
  and `>` become `‹` and `›`, so the text cannot open or close a block of
  Den's. The lines are joined with `\n`.
- The orchestrator reads the text after Den's fixed preface, at most 512
  bytes, which names the program and says that every tool in the turn waits
  for you in Den; then the roster, as for your own messages.
- One app turn at a time on the whole machine (else `unavailable`), six a
  minute per pairing. `turn` is "t-" and 32 hex digits.
- Chunks, each with its `turn`: `{"type":"text","delta":"…"}`,
  `{"type":"status","seq":n,"text":"…"}` (Den's fixed sentences),
  `{"type":"needs_approval","card":"…","item":"…"}` and `{"type":"done"}` or
  `{"type":"error","message":"…"}`. `card` is the inbox card's title, one
  line of at most 200 runes, already sanitised by Den but written from what
  the orchestrator asked for: show it as text under your own fixed words
  ("Needs approval in Den"), with "Open in Den" as the only action. `item`
  is the card's id in Den's own inbox ("i-" and a number), opaque to the
  app: Den's cards are not served by `inbox.list` or `inbox.body`. `focus`
  with `item:<id>` still answers `unavailable` in this release; a later one
  brings Den forward on that card, under the same guard against your own
  typing.
- Closing the connection ends the turn: Den stops it, and the stream ends
  with `error "cancelled"` if it can still be written. A turn ends with an
  error after ten minutes, or two minutes without a word from the
  orchestrator, and when you type in Den's chat while it runs: your
  message goes first.
- The orchestrator starts in control mode for the app's first turn, on a
  Claude Code release Den has checked control on (2.1.289, 2.1.296); on any
  other release `orch.say` ends with an error that says so, and Den's own
  chat works as before.

### What an app's turn may do

While a pairing lasts, Den runs the orchestrator so that every tool but
`ListAgents` asks Den first (Claude Code's manual mode, with its permission
prompts sent to Den), and Den answers from a path that never waits on its
screen:

- An app's turn lasts from the moment Den sends it until the result that
  follows Claude Code's replay of that very turn: only that result closes
  it; a refused prompt's result does not. Den sends it only when no other
  turn is in flight, and holds every other turn (yours, its own and agents'
  reports) until it ends. Anything Den cannot place counts as the app's.
- In an app's turn every tool request, Den's tools and messages to agents
  alike, becomes a card in Den's inbox, "Requested from <program>: …", with
  the request in full, for your `Ctrl+Y` (once) or `Ctrl+N`. Nothing is
  allowed by default. A request with no answer in a minute is denied.
- In your own turns Den's named tools work as before, without a card; any
  other tool is denied.
- Claude Code's own SendMessage is turned off while a pairing lasts: it
  never asks permission, so it could not be held. Den's `send_message`
  takes its place and asks like any other tool. Den delivers it the way
  SendMessage does, from the orchestrator's own address and name, to live
  sessions on this machine only, so agents see what they always saw and
  report back the same way.
- If Den's guard finds a tool that ran in an app's turn without Den
  allowing it, Den stops the orchestrator, notes why and revokes every
  pairing.
- `/api` lists each pairing (program, pid, paired since, last used, control
  on or waiting for your `Ctrl+Y`); `/api unpair` revokes every pairing at
  once and closes their connections. Den's header shows `paired: <program>
  <path>` while one lasts.

The read-only `ask` lane is a separate process: an app turn never holds an
`ask`, and an `ask` never holds an app turn.

### Limits

- Any program running as you can type into Den's terminal window as if it
  were you, or read Den's screen (and so the pairing code); pairing does
  not change that, and the `Ctrl+Y` card is there so that a code read off
  the screen cannot pair in silence. On Hyprland, its `permission` rules
  for keyboard access and for screen capture (screencopy) can keep other
  programs from both.
- A pairing is bound to the program Den read for the app. An app run by an
  interpreter (`python3`, `node`) is that interpreter to Den, so the card
  also shows its command line: read it before you pair.
- Claude Code's inbox sockets accept messages from any process of your user
  without proof of who sent them; Den's `send_message` relies on that to
  deliver as SendMessage does. It is a property of Claude Code running as
  you, not one Den adds.
