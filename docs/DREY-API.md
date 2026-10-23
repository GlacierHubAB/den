# Den's local API (den-api.sock)

Den can serve a small local API so that apps on your machine can show your
fleet and ask about it. Drey is the first such app. The API is **off** until
you turn it on, and it lets no app approve, answer, start, close or message
anything, except the one console you pair yourself (see Console).

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
  (the executable's name as Den read it, never the name an app gives itself),
  or, for an app whose executable Den cannot read, `⇄ 1 app connected ·
  unidentified`. The full description is in `/api` and on its cards.
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
versions:[1], host, control?, features?, mode?}`; `mode` is `{global,
mixed}` (see Permission mode); `features` lists `"console"`
when Den serves a console (see Console); a `v` Den does not serve gets
`unsupported`. `control` is `"on"` when an app may pair for control (see
Control), `"off"` when you turned control off in Den (`/api control off`:
`/api pair` shows no new code, a pairing made then starts off, and
`orch.say` is refused, until `/api control on`); a Den without
control sends none. A Den with no den-api.sock at all (v0.4.2 and older)
is told by the missing socket, never by this field. `group`
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
| `hello` | `{v, client, version, group?}` | `{version, versions, host, control?, features?, mode?, view?, view_workspaces?}` |
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
allow, deny, answer, start, close or message: there are none but the
console's. Control's verbs (`pair.claim`, `pair.challenge`, `pair.auth`,
`orch.say`) are under Control below, the console's (`console.*`, `card.*`,
`cmd.*`) under Console. This release
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

- **Fleet** `{taken, agents, windows, missions, cost, asks?, mode?, view?}`. An agent is
  `{id, name, role?, state, waiting?, worktree?, model?, since, window?, host?,
  remote?, costUsd, turns, lastSeen, color?, permission_mode?, doing}`; `state` is `idle`, `working`, `waiting`
  or `stalled`, and `waiting` names what waits: `permission`, `question` or
  `plan`. `hooks` says whether Den can see the agent's prompts (see
  "Switching views"): `on` when Den has had a hook event from the session since
  Den started, `off` when Den's hooks are not installed, `unknown` when they are
  but the session has not reported yet (new, or started before the hooks or
  before this Den); an agent on another machine carries none. Treat `unknown`
  like `off`. Agents on other machines carry `remote: true` and their host's
  label; what their hosts report is passed on as labels, never as Den's own
  words. Windows of agents on other machines are not listed. `color` is the
  colour Den draws an agent of this machine in, its critter and an `@Name`
  in Den's own text, as `#rrggbb`; agents on other machines have none.
  Den colours a name only where it is Den's word that the agent is in the
  fleet: never inside an agent's or a host's own text, and never a name two
  local agents share. `permission_mode` is the mode an agent of this machine
  runs in as its own hooks last reported it (`default`, `acceptEdits`,
  `plan`, `auto`, `dontAsk`, `bypassPermissions`, or `unknown` for a value
  Den does not know); it is absent when none reported one. The fleet's
  `mode` is the same as hello's. `doing` `{text, at}` says what an agent is doing
  now, in at most 80 runes, from Den's own facts. Only the paired console's
  connections get it; every other app gets it empty (`{"text": "", "at":
  "0001-01-01T00:00:00Z"}`), and a change in it alone sends such an app nothing.
  It reads as a person would say it, in sentence case, and never names a
  program. Its state comes first: `Waiting for you…` (or `Waiting for you:
  permission`, `: question` or `: plan`), `Stuck, needs a look` or `Idle`. For
  a working agent of this machine, its latest tool comes next, as an activity
  ending in `…`: `Editing team.go…`, `Writing NOTES.md…` or `Reading team.go…`
  with the file's base name (`Editing a file…`, `Reading files…` without a
  plain one, or for a name that looks like an id, token or secret), `Searching the code…`, `Fetching a web page…`, `Searching the
  web…` or `Handing work to a helper…`. A shell command says what it does:
  `Navigating files and folders…`, `Reading` a file, `Searching the code…`,
  `Running the tests…`, `Checking the code…`, `Building the project…`,
  `Installing packages…`, `Looking over the changes…`, `Committing changes…`,
  `Pushing changes…`, `Working with git…`, `Working on GitHub…`, `Fetching a
  web page…`, `Working with containers…`, `Managing services…`, or else
  `Running a command…`; of a chain, the first command that does more than move
  around says it (`cd x && go test` is `Running the tests…`). With none of
  those, it is `Working on` and the task label Den was given for the agent
  (never its window title), with every word that could be a path, address or
  id dropped, then `…`; or just `Working…`. An agent on another machine only
  ever shows its state or `Working…`: its host's text is never used. Every
  `text` is one of these lead phrases, a closed set, alone or followed by
  ` ` and a target, `:` and a reason, or `…`: `Waiting for you`, `Stuck, needs
  a look`, `Idle`, `Navigating files and folders`, `Reading`, `Editing`,
  `Writing`, `Searching the code`, `Running the tests`, `Checking the code`,
  `Building the project`, `Installing packages`, `Looking over the changes`,
  `Committing changes`, `Pushing changes`, `Working with git`, `Working on
  GitHub`, `Fetching a web page`, `Searching the web`, `Handing work to a
  helper`, `Working with containers`, `Managing services`, `Running a
  command`, `Working on`, `Working`. Style by the lead phrase, and show a text
  with an unknown lead plainly. `at` (RFC3339, Den's clock) is when `text` last changed.
  It changes at most once every 10 seconds per agent and arrives with the usual
  fleet changes.
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
own orchestrator chat. They are kept in `~/.local/state/den/api-chat.jsonl`
(mode `0600`).

## Control

Control lets one paired app (Drey's `drey-host`) speak to the orchestrator
you talk to in Den, as you would from Den's chat. It is off until you pair
an app, and everything that would change your fleet still waits for you in
Den: the app never approves, answers or starts anything.

### Pairing

- `/api pair` in Den shows a code of eight words for two minutes, in Den's
  header only, with "Type this only into the app's own pairing window." It is
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
  `Den is busy` comes with `unavailable` when Den cannot take the card now
  (its queue to its screen is full): that claim did not count, the code is
  still live, and the same claim may be sent again.
- The code is single use. A second `pair.claim` within a claimed code's two
  minutes, or a claimed code again within ten minutes (even after another
  `/api pair`), means another program had the code too (anything that can
  read Den's screen can): Den revokes the pairing that code made, closes its
  connections and notes "two programs used the pairing code: <program>,
  pid N claimed it first, then <program>, pid M; the pairing is revoked,
  pair again", naming both, since either may be the one that should not
  have had it.
- Den and the app keep the key in memory only; when either restarts, pair
  again. A pairing is bound to the program Den read for the app (its
  executable): no other program can use it. When Den cannot read the
  executable (the app made itself non-dumpable), the pairing is bound to
  that one process instead, and the card says Den cannot check which
  program it is.
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
- `orch.say` on a connection that has passed neither `pair.auth` nor
  `console.auth` answers `forbidden`. A console's turn is an app's turn:
  the same fence, preface and limits, every tool waiting for you in Den,
  refused while control is off, and never a standing allowance (`/always`
  is not offered on its cards).

### Speaking to the orchestrator

| Op | Params | Result |
|---|---|---|
| `pair.claim` | `{code}` | `{pairing, key, control}`, once you `Ctrl+Y` in Den |
| `pair.challenge` | | `{nonce, conn}` |
| `pair.auth` | `{pairing, mac}` | `{paired, control, mac}` |
| `orch.say` | `{text}` | `{turn}`, then `orch` events until a `done` or `error` chunk |
| `view.show` | | `{view, shown, total, reason?}` |
| `view.hide` | | `{view, shown, total, reason?}` |

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
  minute per pairing, and six for the console. `turn` is "t-" and 32 hex digits.
- Chunks, each with its `turn`: `{"type":"text","delta":"…"}`,
  `{"type":"status","seq":n,"text":"…"}` (Den's fixed sentences),
  `{"type":"needs_approval","card":"…","item":"…"}` and `{"type":"done"}` or
  `{"type":"error","message":"…"}`. `card` is the inbox card's title, one
  line of at most 200 runes, already sanitised by Den but written from what
  the orchestrator asked for: show it as text under your own fixed words
  ("Needs approval in Den"), with "Open in Den" as the only action. `item`
  is the card's id in Den's own inbox ("i" and a number), opaque to the
  app: Den's cards are not served by `inbox.list` or `inbox.body`. `focus`
  with `item:<id>` still answers `unavailable` in this release; a later one
  brings Den forward on that card, under the same guard against your own
  typing.
- A turn is over for Den just before its `done` or `error` goes out, so you
  may say again as soon as you read it. The next turn's first chunks can
  then reach you before the last turn's `done` or `error`: tell turns
  apart by `turn`, never by order.
- Closing the connection ends the turn: Den stops it, and the stream ends
  with `error "cancelled"` if it can still be written. A turn ends with an
  error after ten minutes, or two minutes without a word from the
  orchestrator, and when you type in Den's chat while it runs: your
  message goes first.
- The orchestrator starts in control mode for the app's first turn, on a
  Claude Code release Den has checked control on (2.1.289, 2.1.296); on any
  other release `orch.say` ends with an error that says so, and Den's own
  chat works as before.

### Switching views

Den shows each session, every agent and Den's own, in a terminal window of
its own that the window manager tiles: **Agent view**, how Den always was.
In **User view** those windows are hidden, and you work from the dock.
Hiding moves a window to the Hyprland special workspace `special:den`; the
window stays alive, and showing moves it back to the workspace it came from.
There is never one terminal with panes: each session keeps a window of its
own.

- `hello` carries `view` (`agent` or `user`) and `view_workspaces`
  (`["special:den"]`, the one workspace Den hides windows on: a launcher
  that lists workspaces skips exactly that name), and so does every `fleet`.
  A Den that cannot switch (no Hyprland to move windows with) carries
  neither, and answers both verbs `unavailable`.
- `view.show` and `view.hide` take no params, and only the console, proved on
  the connection (`console.auth`), may send them: any other connection, an
  app's D1 pairing (chat only) included, gets `forbidden` "prove first". They are
  the same when asked again: asking for the view Den is in moves whatever
  is out of place (an agent's window that came up since) and nothing else.
  A call is never an approval of anything.
- The result is `{view, shown, total, reason?}`. `view` is the view asked
  for. `shown` counts Den's own windows still visible once the switch ended,
  whichever verb it was: a show succeeded when `shown == total`, a hide when
  `shown == 0`, and `total == 0` means Den has no windows of its own, which
  is done. `reason` is one plain line of at most 40 characters, absent when
  there is nothing to say ("1 of 4 not hidden", "2 not Den's").
- After a switch, connections that follow `fleet.events` get a `view` event
  `{view, shown, total}`, and so do the console's connections that took
  `console.events`; a slow reader may miss it, and `hello`, `console.auth`
  and the fleet carry the view too.
- Den claims User view only while a window it hid is hidden. It checks its
  record against the windows when it starts and every few seconds while it
  is in User view (one listing of the windows): when none of the windows it
  recorded hiding is on `special:den` any more (the compositor started
  afresh, or they were moved back by hand), it saves Agent view, notes it in
  its chat and sends a `view` event `{view: "agent", shown, total}` with no
  call behind it, `shown` and `total` both the number of windows it had
  recorded. If it cannot list the windows, it changes nothing.
- One switch at a time, and at most three a minute for the console. A call
  while another switch, or the closing of an agent's window, is running
  answers `busy` and does not count; past three a minute it answers
  `unavailable` "too many view switches". A switch ends within about four
  seconds: if Hyprland is slow, the reply says "ran out of time" and the
  next call finishes the job.
- Which windows: an agent's window is the one carrying the class Den chose
  when it spawned the agent (`io.glacierhub.den.<name>`, kept in Den's state
  with the agent's session), when that agent's own process runs in it.
  For a hide only, an agent with no such record (one Den's older spawn path
  started, with no class) is found by the exact title Den gave its terminal,
  `den · <name>`, when exactly one window has it, its terminal process has
  no other window and the agent's process runs under that terminal. A hide
  is undone by a show, and a show moves back only the windows Den recorded
  hiding, never one found by title or any other guess. Directory and every
  other guess find nothing. An agent Den cannot tie to a window either way
  is not touched, and counts in `reason` as "not Den's". Two windows with
  one class or title, or a marked window run by someone else, are left
  alone and counted the same way. When agents run but none has a window
  Den can tie, `view.hide` answers `unavailable` ("no agent window is Den's
  to hide: …") and moves nothing, Den's own window included, and the view is
  what it was: Den never says User view with every agent still on screen.
  Den's own window is hidden only when it is the one window of a process
  above Den's; when Den cannot tell, it stays and `reason` says so.
- **What `view.hide` leaves visible.** It never takes anything out of sight
  that waits on you at the moment it runs:
  - Den's own window stays up while any card waits on you in Den, whatever
    its kind: a tool permission, a plan or a question held in Den, and every
    `Ctrl+Y` question (a close, a merge, a cleanup, a grant, a pairing, a
    control ask, turning the API on, and the rest). No card, approval or
    confirmation surface of Den's is ever hidden by it. When Den cannot tell
    which window is its own, it hides none of its own, and `reason` says "Den's
    own window not found".
  - The window of an agent whose permission prompt, plan or question only its
    own terminal can answer stays up too; it counts in `shown`, and `reason`
    says "1 waits on a prompt". Ask `view.hide` again once it is answered.
  - The guarantee covers what Den knows about: its own cards, and the
    prompts its hooks have seen. A prompt in a Den-spawned agent whose hooks
    are off (Den's header says so) is not known to Den, so that agent's window
    is hidden like any other; a session started by hand is never touched.
  - God mode's warning is never hidden: God mode (stage 2) is not in this
    release, and when it lands it joins this same check, with its own tests.
  - What a hide cannot do is bring something back: a card or a prompt that
    appears after the hide stays out of sight until `view.show`. The fleet's
    `waiting` tells the dock that an agent needs a person, and a dock can
    offer `view.show` for it. A trust dialog is an
    agent that has not registered yet: Den has not hidden its window (a new
    agent comes up hidden only once Den has tied its window to it, which
    happens after the dialog).
  - With no dock connected: if Den is in User view, a card waits on you, and
    no proved connection is open to call `view.show` (the dock died, unpaired
    or lost its connection), Den brings back its own window, and only that
    one, within about five seconds. The agents' windows stay hidden and the
    view stays `user`; it does this once per spell of waiting, and a dock that
    connects again can hide the window once more. A card that waits while a
    dock is connected is the dock's to show.
  - A new agent spawned in User view comes up hidden once Den has tied its
    window to it; asking `view.hide` again hides any that came up otherwise.
- Nothing is closed, signalled or typed into. Closing an agent works on a
  hidden window as on any other, and a switch and a close never run at the
  same time. Where each window came from is kept in `view.json` in Den's
  state before it moves, so a restarted Den knows the view and can bring
  every window back. If one is stranded on `special:den` (a window with the
  same class as an agent's makes Den leave both alone), `/view rescue` in
  Den's own terminal brings back every window there; no verb reaches it,
  and Hyprland's own binding for the special workspace works too.
- Hyprland: a window is moved with the Lua dispatcher
  `hl.dsp.window.move({ workspace = …, window = "address:…", follow = false })`,
  which keeps the focus and the special workspace where they are (checked on
  Hyprland 0.56.2 in a throwaway nested session; `silent = true` is ignored
  there). The classic `movetoworkspacesilent` stays as the fallback for older
  Hyprland; on 0.56 it is parsed as Lua and fails, and on older releases it
  is untested here. A Hyprland that ignored `follow = false` would show
  `special:den` after a hide: a visible fault, not a silent one.

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
  report back the same way. Den ends each message with one fixed line of
  its own asking the agent to report back to the sender when it is done or
  blocked; the line names no one and carries no authority.
- If Den's guard finds a tool that ran in an app's turn without Den
  allowing it, Den stops the orchestrator, notes why, revokes every
  pairing and unpairs the console.
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
- A standing allowance to message an agent is offered and used only while
  that agent reports `default` mode through its own hooks: in plan, auto
  and acceptEdits an agent acts on some things without asking. Den takes
  a mode only from the agent's own hook processes, so no other program
  can claim one for it, but the agent itself could report a mode it is
  not in (by running Den's hook from its own shell). The card shows the
  mode "as the session reports it".
- A pairing is bound to the program Den read for the app. An app run by an
  interpreter (`python3`, `node`) is that interpreter to Den, so the card
  also shows its command line: read it before you pair.
- A process can make itself non-dumpable (`PR_SET_DUMPABLE` 0), and then
  the kernel keeps its executable from Den. Any program can do this, so it
  is no evidence of which program it is. Den then knows the app as that one
  process in this boot (pid and start time): its grants, groups, pairing
  and limits are that process's alone, and a new process of the same app
  asks again. Cards say "a program Den cannot identify (pid N, started
  HH:MM:SS)" and show its command line as the process reports it, which it
  can make up; match the pid and start time with what the app shows.
- Claude Code's inbox sockets accept messages from any process of your user
  without proof of who sent them; Den's `send_message` relies on that to
  deliver as SendMessage does. It is a property of Claude Code running as
  you, not one Den adds.

## Console

A console is the one program you pair, in Den's own window, as yours:
Drey's dock. It may read Den's cards and answer the ones Den lets it, and
send Den's commands, as you would in Den. It is not Control's app pairing:
the code never crosses the socket, both sides sign every proof with
Ed25519, and the console is bound to one process. Den's `hello` lists
`"console"` in `features` when it serves one.

### Pairing a console

- `den pair` in a terminal, or `/api console` in Den, shows a code of eight
  words for two minutes, in Den's header only. `den pair` is refused under
  Claude Code, under one of Den's agents, and from `den mcp` or `den hook`.
- Type the code only into the app's own pairing window. The app uses it as
  its eight words in lower case, joined by single spaces.
- On one connection: `console.challenge {}` answers `{nonce, conn,
  claim_salt}`, each 64 lowercase hex digits but `conn` (32), at most one a
  second on each connection. Then `console.claim {public_key, proof, pages_sha256?}`:
  - `public_key` is the app's Ed25519 public key, 64 hex digits, drawn for
    this pairing and kept in memory only;
  - `proof` is the hex HMAC-SHA256, under HKDF-SHA256(code, salt =
    `claim_salt`, info = `den-console-bind-app-v1`, 32 bytes), of
    `"den-console-bind-app-v1\n" + public_key`;
  - `pages_sha256`, optional, is the hash of the app's pages, shown on the
    card.
- A code takes one claim, right or wrong. A right proof does not pair yet:
  Den puts a card in its inbox naming the program (path, pid, start time,
  parent, command line), and only your `Ctrl+Y` on it pairs. The claim then
  answers `{pairing, den_public_key, bind, console: true}`, where `bind` is
  the hex HMAC-SHA256 under HKDF-SHA256(code, salt = `pairing`, info =
  `den-console-bind-v1`) of `"den-console-bind-v1\n" + public_key + "\n" +
  den_public_key + "\n" + pairing`. Check it: it proves this Den saw the
  code, and pins Den's key.
- After a right proof, before the card, Den also reads the claimant as
  "Pairing with no code" does. If it is the main process of a
  `drey-host.service` that passes every check there, Den keeps what it
  read, the card says the dock pairs again by itself after a restart, and
  `console.reconnect` works for it as for a pairing with no code. Any
  other claimant pairs as before, with no word of the read on the card
  and no strike. The read can take up to ten seconds; a code whose two
  minutes end meanwhile answers `no code waiting`.
- A refused claim answers `forbidden`: `no code waiting`, `wrong code`,
  `code spent` (the code was claimed already, right or wrong, and its two
  minutes are not over), `denied in Den`, `not confirmed in time` or `revoked: the code was used
  by another program too`. `unavailable` comes with `Den is busy` or, for
  30 seconds after a `Ctrl+N`, `wait: a pairing was just declined`.
- One console at a time: a new one replaces the old, which hears
  `unpaired`. Den keeps its side (the console's identity and public key)
  across its own clean restarts, and says so (see Events). A console
  whose process ended, a new boot, or a record Den did not write unpairs.
  A console restored after Den restarted gets its powers back once Den
  reads it clean as `drey-host.service`; any other waits for your
  `Ctrl+Y` in Den (see Powers).

### Pairing with no code

Drey's own service pairs with no code, at once, and pairs again by itself
after it restarts: you chose to treat the dock as part of Den. Den checks,
through your systemd user manager, that the program asking is
`drey-host.service`'s main process and how the manager runs it, and says
in its chat what it paired. There is no card to pair and nothing to
match. It comes with its powers when the program, its unit and its pins
are what you last gave them to in this Den's run; the first pairing after
Den starts, and one after any such change (a new Drey build, say), waits
for one `Ctrl+Y` on Den's powers card. `api.console_powers = "always"` in
Den's config gives them whatever changed.

- `hello`'s reply carries `console_public_key`, Den's console key (64 hex
  digits): the key a request's reply names, and the one `den_sig` is
  under.
- `console.request {public_key, pages_sha256?}`, on any connection, at most
  one a second on each. Den reads the unit from the manager's own socket
  (`$XDG_RUNTIME_DIR/systemd/private`); a manager slow to answer, as just
  after a restart, is read again for up to ten seconds, so allow Den
  that long before it answers, here and in `console.reconnect`. It checks
  that:
  - the manager is yours, a child of init, in its own scope, and running
    a root-owned systemd; where the kernel keeps the manager's program
    from you (a manager started with `CAP_WAKE_ALARM`, as systemd's own
    `user@.service` does), it must hold exactly that capability, in the
    machine's own user namespace, and Den must not hold it;
  - the asking process is the unit's main process, the manager's child,
    in the unit's cgroup, started when the manager says, and not traced
    (and `kernel.yama.ptrace_scope` is at least 1);
  - the program the unit runs is one regular file of yours with one link,
    unchanged since the process started;
  - the unit is `Type=simple` or `exec`, `NotifyAccess=none`, with no
    `PIDFile`, not transient, with no drop-ins, no `EnvironmentFile` and
    no `PassEnvironment`;
  - its `Environment=` sets no name that loads code (`LD_*`, `GTK_*`,
    `GIO_*`, `GDK_PIXBUF_*`, `GST_*`, `WEBKIT_*`, `LIBGL_*`, `__EGL_*`,
    `VK_*`, `GBM_BACKENDS_PATH`) but these pins, all of them:
    `GST_PLUGIN_PATH=` (empty), `GTK_MODULES=` (empty),
    `VK_LOADER_LAYERS_DISABLE=~implicit~`, `GST_REGISTRY_UPDATE=yes`,
    `GST_REGISTRY=%t/drey-host/gst-registry.bin` (as the manager expands
    it), `GST_PLUGIN_SYSTEM_PATH` one directory and `GST_PLUGIN_SCANNER`
    one file, each an absolute path owned by root at every step, with no
    symlink and nothing writable by you or your group;
  - and your manager's own environment sets none of those names, and no
    GTK 3 `settings.ini` it would read (`$XDG_CONFIG_HOME/gtk-3.0`, each
    of `$XDG_CONFIG_DIRS`' `gtk-3.0`, `/etc/gtk-3.0`,
    `/usr/share/gtk-3.0`) sets `gtk-modules`.

  Anything else, or a manager that did not answer through every try,
  answers `forbidden` with `not drey-host.service:` and what failed:
  never a card, never a pairing. Otherwise it pairs at once, replacing any
  console paired before: `{pairing, den_public_key, console: true,
  powers}`, where `den_public_key` is `hello`'s `console_public_key`, and
  `powers` is false, with a card in Den for them, when Den has not given
  them to this program, unit and pins in its run (unless
  `api.console_powers = "always"`), while you have locked them (`/api
  lock`), or while God mode is on. Then prove as above. Den notes in
  its chat the unit, pid, start, the program and its SHA-256, and what
  changed since it last read the service; the first connection that
  subscribes to `console.events` hears it as a `paired` event (see
  Events).

  It is refused while a claim by code waits for you (`unavailable`, `Den
  is busy`); after you, the console itself or Den's own guard unpaired it,
  until you start a pairing in Den (`/api console`, `den pair` or `/api
  pair`): `forbidden`, `unpaired in Den: pair again there (/api console or
  den pair)`; and for ten minutes after three tries from other programs,
  three wrong proofs or a burst of answers unpaired it: `unavailable`,
  `the console was unpaired for tries from other programs: pair by code,
  or wait ten minutes`. A pairing by code works meanwhile.
- `console.reconnect {public_key, nonce, pages_sha256?}`, after the
  service restarted with a fresh key, at most one a second on each
  connection. It may be the first request on a connection, before
  `hello`, so the dock sends nothing until Den has proved itself; a Den
  that has only just started answers `unavailable`, `Den is starting`:
  try again shortly.
  `nonce` is the dock's own: 64 hex digits, fresh each time. Den reads
  the unit again, as a request does:
  - read clean: a new pairing at once, `{pairing, den_sig, powers}`, with
    `powers` as for a request, also after Den restarted and whatever
    changed (the program, the unit, the dock's pages). Den notes what it
    paired and what changed, and the first subscribed connection hears a
    `reconnected` event;
  - nothing to reconnect to, another boot, or a check that fails:
    `forbidden`, `pair again: …`. The last, when a console is paired,
    counts toward the three tries that unpair it, and takes its powers;
  - and a request's refusals.

  `den_sig` is Den's Ed25519 signature, 128 hex digits, under the console
  key the dock pinned at pairing (the one `hello` publishes as
  `console_public_key`), of these lines joined by `\n`:
  `den-console-reconnect-v1`, `nonce`, `pairing`, the dock's new
  `public_key`. Check it before anything else, and drop the connection
  if it does not verify: it is not your Den. Then prove each connection
  with `console.auth` as always.

  A known answer, for both sides' tests: Den's key from the Ed25519 seed
  of 32 bytes `0x01` is
  `8a88e3dd7409f195fd52db2d3cba5d72ca6709bf1d94121bf3748801b40f6f5c`;
  with `nonce` 64 × `5a`, `pairing` 32 × `0a` (as hex text: `5a5a…`,
  `0a0a…`) and the dock's key 64 × `0b`, `den_sig` is
  `7a3ce0532530d6793e45ebbf3360530055b842dbb42c30278e5fd70e0fe77c285967f693c26f10e4996d2ead6e8990a2729b50a525e3ac3e3de6bfa35927dd05`.
- Den's console key is the same across its restarts. Den never makes a
  new one in silence: a new key, or one that does not read (console
  pairing is then off, with no new key), is said in Den's chat. A key that
  changed means pairing again, by request or code. The dock never pins a
  new Den key by itself: a `den_sig`, a `console.auth` signature or a
  `console_public_key` that is not the pinned key's is "Den's key
  changed", shown to you, and only your choice to pair again replaces it.

### Powers

A console that has its powers may answer cards and run commands. One that
does not gets `powers_locked` (`press Ctrl+Y in Den to let the console
answer cards and run commands`) from `card.answer`, `cmd.run` and
`mode.set`; it can still read, chat (`orch.say`) and follow events.

- A pairing by code has them. A request or reconnect has them when the
  program, unit and pins are what you last gave them to in this Den's
  run, as Den read them itself; else, as after every Den start, it waits
  for your `Ctrl+Y` on Den's card, and that `Ctrl+Y` counts for the same
  build's later re-pairs. A console restored after Den restarted waits for
  it too. With `api.console_powers = "always"`, every request and
  reconnect has them, and a restored console once its process reads clean
  as `drey-host.service`.
- `/api lock` (from Den or the dock) takes them, and every console paired
  after, by any way and across Den's restarts, comes without them, until
  your `Ctrl+Y` on Den's powers card. While God mode is on, a console
  paired by itself comes without them too. Another program's try for the
  console, a wrong proof, or God mode turning on take them.
- `console.auth`'s reply carries `powers`, and a `console` event of kind
  `powers` says each change, `{on: true}` or `{on: false}`.

### Proving a connection

Each connection proves once, at its start:

- `console.challenge {}` gives `nonce` (Den's, *nd*) and `conn`.
- `console.auth {pairing, nonce, sig}`: `nonce` is the app's own 64 hex
  digits (*na*), `sig` the hex Ed25519 signature (128 digits) of these
  lines joined by `\n`: `den-console-v1`, `app-to-den`, `conn`, `pairing`,
  the app's public key, Den's public key, *nd*, *na*, and the app's
  identity.
- It answers `{paired: true, sig, god, view?}`, `sig` Den's signature
  of the same lines with `den-to-app` and Den's identity as the last line.
  Check it with the pinned `den_public_key`. `god` is God mode as it stands
  (see God mode). `view` is the view Den is in (`agent` or `user`, see
  "Switching views"), absent when this Den cannot switch views.
- The identity is the process as Den reads it in this boot:
  `exe:<path as hex>:<boot_id>:<pid>:<start>`, or
  `unreadable:<boot_id>:<pid>:<start>` when the process is not dumpable;
  `boot_id` from `/proc/sys/kernel/random/boot_id`, `start` field 22 of
  `/proc/<pid>/stat`. Den's own is `den:<boot_id>:<pid>:<start>`.
- A proof from another process answers `forbidden` (`another program holds
  this console`) and tells the console's connections (a `refused` event);
  a wrong signature answers `forbidden` and closes. Three of either in ten
  minutes unpair the console. A seventh connection gets
  `console_in_use`; a pairing that is gone, `not_found`.

### Console verbs

| Op | Params | Result |
|---|---|---|
| `console.challenge` | | `{nonce, conn, claim_salt}` |
| `console.claim` | `{public_key, proof, pages_sha256?}` | `{pairing, den_public_key, bind, console}`, once you `Ctrl+Y` in Den |
| `console.request` | `{public_key, pages_sha256?}` | `{pairing, den_public_key, console, powers}`, at once |
| `console.reconnect` | `{public_key, nonce, pages_sha256?}`, before `hello` too | `{pairing, den_sig, powers}`, at once |
| `console.auth` | `{pairing, nonce, sig}` | `{paired, sig, god, view?, powers}` |
| `console.events` | | `{ok, cards}`, `cards` the ids of every card waiting; then the snapshot (see Events: the console, a `card` event for each waiting card, its powers, God mode, the mode, the view, the desktop, the mirror), then `console`, `god`, `mode`, `card`, `view` and `desktop` events as they change, `mirror` events, and `conversation` entries while the conversation is shown here |
| `console.unpair` | `{}` or `{all: true}` | `{ok, unpaired}` |
| `conversation.history` | `{limit?}`, at most 200 | `{entries}`, newest first: the conversation entries since it was shown in this console (see Conversation mirror); `forbidden` while it is not |
| `card.get` | `{id}` | the card and a `nonce` |
| `card.answer` | `{id, text_sha256, nonce, verb}` | `{ok, result, note}` |
| `cmd.list` | | `[{name, usage, help, dock}]` |
| `cmd.complete` | `{prefix}` | the commands that start with it, at most 64 |
| `cmd.run` | `{text}` | `{ok, note}` |
| `mode.set` | `{mode}` | `{ok, applied, note}` for a lowering, or `{ok, card, note}` for a raise that waits on that card |
| `god.off` | | `{ok, was_on}`: Den turns God mode off |
| `overlay.linked` | `{mission}` | `{linked: true}`: your glow for that mission is drawn (see Desktop overlay) |
| `overlay.unlinked` | `{mission}` | `{linked: false}`: you hid them |

All but `console.challenge`, `console.claim`, `console.auth` and
`console.unpair {all: true}` need a proved connection, else `forbidden`
(`prove first`). `console.unpair {}` ends the console from its own
connection; `{all: true}` ends every console and app pairing from any
connection, at most once every ten seconds for everyone, as `den unpair
all` does in a terminal.

### Events

On subscribe, Den sends the current value of each stateful kind before
any change events, so that no client ever has to infer a state from the
order of events. `console.events` starts with this snapshot, in this
order: a `console` event (`connected`, or `restored` after Den
restarted), a `card` event, `new`, for each card waiting (the ones the
reply lists), a `console` event of kind `powers` with `on`, a `god`
event, a `mode` event (once Den knows the mode), a `view` event (once
Den has a view), a `desktop` event, and a `mirror` event. Everything after it is a change. `fleet.events` starts with the whole fleet in the
same way. A later kind that carries state joins the snapshot.

- `console {kind, pid, start_ticks, started?, program?, reason?, on?, cause?, unit?, program_sha256?, changes?}`: `kind`
  is `connected`, `restored` (the first `console.events` after Den
  restarted with this console: check `pid` and `start_ticks` are yours),
  `paired` or `reconnected` (after a `console.request` or a
  `console.reconnect`, to the first connection that subscribes, after the
  replay: `reason` `paired automatically`, with `unit`, `program` as the
  manager runs it, `program_sha256`, and `changes`, what changed since
  Den last read the service, as Den's note says), `powers` (`on` says
  whether the console has them now), `refused` (another program tried
  this console) or `unpaired`. Ignore a kind you do not know.
  An `unpaired` event carries `cause`, why the console is no longer
  paired, one of these nine values:

  | `cause` | The console was unpaired because |
  |---|---|
  | `burst` | it answered cards faster than anyone could read them |
  | `proofs` | three proofs for it were wrong in ten minutes |
  | `intruders` | three tries from other programs in ten minutes, however many programs made them (a `console.auth` or a `console.reconnect` from another program, or one that failed Den's checks, counts as a try) |
  | `self` | the console asked, with `console.unpair` (or `{all: true}` on its own proved connection) |
  | `den` | unpaired in Den (`/api unpair`) or with `den unpair` in a terminal. Den's own unpair, which a program the human or a script runs on this machine can issue too, so word it as "unpaired in Den", never "you unpaired it" |
  | `other` | another program on this machine unpaired every pairing with `console.unpair {all: true}` from a connection that is not the console's own. Not the human's act: never say they did it |
  | `replaced` | another pairing took its place: another console was paired, or this console reconnected after a restart with a fresh key (`console.reconnect`), which ends the old pairing as `replaced` and starts a new one |
  | `record` | Den found its record of the console changed outside Den, so the pairing cannot be trusted. Tell the human that, and do not tell them they unpaired |
  | `guard` | Den's own guard stopped the orchestrator and unpaired. Not the human's act |

  `reason` stays display text, at most 40 characters ("unpaired in Den"
  when Den's own words were longer); take the cause from `cause`, never
  from `reason`. The set may grow: a `cause` you do not know is Den's own
  act, never the console's or the human's. A Den from before causes sends
  none, and an `unpaired` event without one means the same as an unknown
  one. A console whose connections simply end, an end of file with no
  `unpaired` event, was not unpaired: Den exited, or the API was turned
  off or its apps disconnected (`/api off`, `/api disconnect`), and the
  pairing stands. Connect again once Den is back or the API is on again,
  and prove as before; after a restart the first `console.events` says
  `restored`.
- `view {view, shown, total}`: how a `view.show` or `view.hide` ended, as in
  "Switching views". In the snapshot it is the view now, with the last
  switch's `shown` and `total` while that switch made it, else 0.
- `card {id, kind, agent, change, text_sha256?, needs_den_screen?, result?, by?}`: `change` is
  `new`, `changed` or `settled`. A new or changed card the console may
  not answer (`dock_answers` false) carries `needs_den_screen: true`: only
  your answer on Den's own screen settles it. A settled card has no
  `text_sha256`;
  `result` is `allowed`, `denied`, `always` or `gone` (it settled without
  an answer), and `by` is `den`, `console` or `rule` (God mode answered
  it), absent for `gone`. Take any `by` you do not know as Den's own
  answer, never the console's or yours, and never drop the event for it.
- `god {on, since?}`: God mode went on or off (see God mode), on the
  console's `console.events` connections; in the snapshot, where it is.
- `mode {global, mixed}`: the permission mode changed (see Permission
  mode); in the snapshot, the mode in force, once Den has one.
- `desktop {state, mission}`: a desktop agent's mission started or ended
  (see Desktop overlay); in the snapshot, whether one runs now.
- `conversation {id, at, turn?, origin, speaker, agent?, console?, text}`: one
  entry of the human's orchestrator conversation, as Den shows it. Den sends
  these only while the conversation is shown in this console (see Conversation
  mirror). They are not part of the snapshot: backfill with
  `conversation.history`.
- `mirror {state, since?}`: whether the conversation is shown in this
  console. `state` is `off`, `asking` (Den's card asking the human to
  show it here waits in Den's own window: say "Allow it in Den's
  window"), or `on`, with `since` (RFC3339 UTC), the moment
  `conversation.history` reaches back to. It comes on every change and
  in the snapshot. On `on`, backfill with `conversation.history`; on
  `off`, clear the conversation at once. Treat an unknown `state` as
  `off`.
- After a re-dial, `cards` in the `console.events` reply is the whole set
  of cards waiting: one you knew that is not in it is gone. The `new`
  events that follow are those cards.

### Cards

- `card.get {id}` answers `{id, kind, agent, tool, title, note, body,
  text_sha256, truncated, cut_at?, verbs, dock_answers, nonce}`: the card
  as Den shows it, and a nonce for one answer, on this connection, for ten
  minutes. `text_sha256` is the SHA-256 of `kind`, `agent`, `tool`,
  `title`, `note` and `body` joined by `\n`. Show all of it, `note` (a
  warning Den shows) too.
- `card.answer` takes `verb` `allow`, `deny` or `always`, one of the card's
  `verbs`, with the hash and nonce of a `card.get`. Den answers it as your
  `Ctrl+Y` would, and replies once it is settled. Refusals:
  - `not_found`: no such card, or it was answered;
  - `nonce_expired`: spent, too old, or from another connection;
  - `card_changed`: the card is not what you read: get it again;
  - `refused_from_dock`: `dock_answers` is false or the verb is not the
    card's;
  - `read_it_first`: a card of more than ten lines, answered within 300 ms
    of its first `card.get`;
  - `unavailable`: Den has the card open (it waits for you there), it was
    answered only in part (Den shows the rest), or more than 20 answers in
    a minute.
- Five answers in a row less than a second apart unpair the console.
- Never from the dock: a console or app pairing, turning the API on,
  orchestrator guidance, installing on a host, a message to an agent on a
  host, an app's grant, skills, an agent's question, and any card of or
  about a remote host.

### Commands

- `cmd.list` is a bare array of `{name, usage, help, dock}`, one row per
  command in the order Den's help lists them. `name` is the command with
  its `/`, lower-case letters only (`/fleet`, `/orchestrator`); aliases
  are not rows, but `cmd.run` takes them. `usage` and `help` are one line
  of plain text of at most 120 characters. `dock` is `ok` (runs), `card`
  (runs, and asks you on a card in Den), `needs_den_screen` (it acts on
  Den's own screen) or `refused`, for the command as it may run with some
  arguments: `/fleet project` runs, a bare `/fleet` needs Den's screen.
  Den judges each line.
- `cmd.complete {prefix}` is the same rows, a bare array, of the commands
  whose name starts with `prefix` (with its `/`; any case), refused ones
  left out, at most 64, at most ten calls a second per connection. Use it
  only to fill the input.
- `cmd.run {text}`: one command, at most 512 bytes, 30 a minute. Den takes
  it after Unicode NFKC: no control or format characters, no line or
  paragraph separators, no `;`, `&&`, `||`, `` ` `` or `$(`, no second
  command word. It maps it onto one of its own commands, by name or alias
  in any case, and runs it as typed in Den, with a note in Den's chat
  naming the console; anything else is `refused_from_dock` with why. A
  command's own error comes back `unavailable` with Den's words. `note`,
  and the message of a refusal, is what Den said: one line of plain text
  (not markdown), at most 200 characters, "" when Den said nothing. A card
  the command raises comes as a `card` event; `note` may mention it, but
  only the event says so for sure.
- From the dock, `/usage set` and `/usage clear` put the figure on a card
  ("A lower figure can hold off your usage stop."), and no command proposes
  a mission or runs a template: those wait for Den.

### Permission mode

Den starts every agent in one global permission mode, Claude Code's
`--permission-mode`: `auto` (the default), `acceptEdits`, `plan`,
`default` or `dontAsk`. Den ranks them by what an agent does without
asking: `dontAsk < default < plan, acceptEdits < auto`. `plan` runs any
command Claude Code's classifier allows, where `default` asks, so it is
above `default`; `plan` and `acceptEdits` are not ranked against each
other. `bypassPermissions` is never the global mode.

- `mode {global, mixed}`, in hello, the fleet and the `mode` event:
  `global` is the mode new agents start in; `mixed` is true when an agent
  of this machine runs in another mode, or in one Den does not know.
  Running agents keep their mode until they restart.
- `mode.set {mode}`, from a proved console, takes the mode exactly as
  spelled above; anything else is `bad_request`. It shares `cmd.run`'s 30
  a minute. A change to a lower mode applies at once: `{ok, applied:
  true}`. Any other change (up, or between `plan` and `acceptEdits`) puts
  a card in Den and answers `{ok, card}`, its id; the mode changes only
  once that card is allowed. A newer request settles the open card as
  `gone` and raises its own. Send a `mode.set` that is not a lowering only
  from a fresh press in the dock's own window.
- `cmd.run` takes `/mode <mode>` the same way, and a bare `/mode`.

### God mode

God mode is turned on only in Den's own window: `/god on`, then a phrase
typed there within a minute. No app, command, config key, environment
variable or socket verb turns it on, and every Den start begins with it
off. While it is on:

- `hello` and `console.auth` carry `god: {on: true, since}` (RFC 3339,
  UTC), and Den's header shows it in red. Off, they carry `{on: false}`.
- Den answers its own close, cleanup, restore, mission-update and
  mission-move cards that came after God mode went on; their settled
  events say `by: "rule"`.
- The cards that stay yours (pairings, grants, turning the API on,
  installs, guidance, skills, budgets and usage, messages to hosts, an
  app's turn, merges, the console) go out with `dock_answers` false and
  `needs_den_screen`, and `card.answer` on them answers `tty_only`, as
  does `always` on any card. Answer them in Den's own window.
- `god.off` (from a proved console connection), `/god off` from the dock,
  `den god off` in a terminal and any unpair turn it off; they only lower,
  so they are never refused for who asks.

### Desktop overlay

While a desktop agent controls the desktop, the console may draw that on
screen: an outline at the agent's pointer, a glow. Den tells it when, and
where the pointer is.

- `desktop {state, mission?}`, on the console's `console.events`
  connections:
  - `asking`: no desktop mission runs, and a desktop start card waits for
    the human in Den's own window. Den sends no card text; show a line
    such as "Den asks to control the desktop: answer in Den's window". It
    comes only from `idle`.
  - `active`, with `mission`: a desktop mission runs; `mission` is Den's id
    for it, an opaque string.
  - `idle`: neither. Sent when a mission ends, however it ends, and when a
    waiting card is answered no, dropped or expires. After a yes it is
    `idle` until the mission starts (Den may install hypruse first), then
    `active`.

  Den never sends `asking` while a mission is `active`: a running mission
  always ends with `idle` first. The snapshot of `console.events` ends with
  one desktop event, where it stands. A Den from before this event sends
  none, which means idle, and a Den from before `asking` never sends it;
  ignore a `state` you do not know.
- The pointer: `$XDG_RUNTIME_DIR/den-agent-pointer`, mode 0600, one line,
  `x y` or `x y hidden`, in global logical pixels (the space of `hyprctl
  cursorpos` and the clients' `at`). Den replaces it by an atomic rename
  after each pointer step, each step of a glide too. `hidden` comes about
  550 ms before Den presses keys, which hide the real pointer: fade the
  outline out. The file is removed when the mission ends. Read it only
  while `desktop` is `active`; a missing file means no pointer.
- In a mission with apps, Den refuses a click under any surface on the
  top or overlay layer, since the click would go to that surface. It lets
  one through: a surface on the overlay level (3), with layer namespace
  `drey-agent-pointer`, whose process is that of one of the console's
  proved connections (`console.auth`), still the process that connected
  (pid and start time). That surface must take no input (an empty input
  region), so a click lands on the window beneath it. Den cannot read a
  surface's input region from Hyprland, so it relies on you for this:
  never give an input-taking surface that namespace. No other surface of
  the console's is ever let through: a click under the dock or any other
  window of yours is refused. Drey draws the glow (the aurora) and the
  pointer outline in the `drey-agent-pointer` surface, overlay level 3,
  one per output, with an empty input region, so it shows above windows
  and never blocks a click.
- The link: send `overlay.linked {mission}`, with the `mission` of the
  `active` desktop event, once your glow for that mission is on every
  monitor, and `overlay.unlinked {mission}` when you hide it. Send
  both from a proved connection subscribed to `console.events`
  (`forbidden` otherwise); `overlay.linked` needs your powers
  (`powers_locked` without them). A `mission` that is not the one running
  is `not_found`. At most 30 overlay calls a minute (`unavailable` past
  that).

  From the first `overlay.linked` the task is overlay-backed. Its input
  (pointer, keys, binds, window actions, launches; looking at the screen
  goes on) runs only while both of these hold, with no grace period:
  - the link stands: no `overlay.unlinked` since, and the connection that
    sent it is still open, proved and subscribed. A reconnect, a re-pair
    or an unpair takes it down: send `overlay.linked` again from the new
    connection;
  - Den sees your overlay itself: on every monitor `hyprctl monitors`
    lists (enabled ones; a mirror of another output is skipped), a
    surface on the overlay level (3) with layer namespace
    `drey-agent-pointer`, of the process of one of your proved
    connections (the surface the pointer rule above lets clicks
    through). Another namespace, such as a test instance's, never counts.

  While either is down, Den refuses the agent's input with "paused: the
  dock's overlay is down; Den resumes when it is back", says so in its
  chat, and says so again when it resumes. Down for 5 minutes, the task
  ends, as when Den's own window goes. The human's stop is never paused.
  A task that starts while you have a proved connection subscribed to
  `console.events` waits for its first `overlay.linked`: its input is
  paused, with the same words, for up to 15 seconds. With no link by
  then it runs unbacked, and Den's chat says that the dock did not show
  its overlay. A task that starts with no console watching is never
  paused: Den's own window and its stop are the human's view of the task
  then. Missions with apps are not paused.

### Conversation mirror

The human's orchestrator conversation can be shown in the console, so that its
command bar reads like Den's own input. It is the human's choice, made in Den:
`[api] mirror_conversation` is `ask` (a card in Den's own window once per
pairing, the default), `always` or `off`, and `/api mirror off` or unpairing
stops it. Den says in its chat when it starts. Only the paired console with its
powers receives it, and every console is on this machine: den-api.sock is a
local socket open to this user only.

- Each `conversation` entry is `{id, at, turn?, origin, speaker, agent?,
  console?, text}`. `id` rises with each entry and is the same in
  `conversation.history`, so merge the backfill and the live entries by it.
  `at` is RFC3339 UTC, Den's clock.
- `origin` (a closed set): `you` (the human typed it in Den), `console` (a
  console or app sent the turn), `report` (an agent reported), `mission` (Den
  drives a mission run), `desktop` (a desktop mission's agent reported).
- `speaker` (a closed set): `you`, `den` (Den's own fixed lines only: "Report
  from Mossy Lemur.", a mission run's line, "More reports from … are in Den."),
  or `orchestrator` (its completed reply). An entry with speaker
  `orchestrator` always carries the orchestrator's own reply, under every
  origin (`report` and `desktop` too): Den never passes an agent's report, or
  any other text an agent or a host wrote, through as the orchestrator's. The
  reply may still quote such text, as the model chose to (see `text`). If Den
  ever sends an agent's own words in the mirror, they come under a speaker of
  their own, new to this set: a client skips it until it supports it, and
  then renders it plain.
- `agent` is set for `report` and `desktop`, from Den's fleet, never from the
  report. It is never `Den`, `Drey`, `You` or `orchestrator` (those show as `an
  agent`).
- `console` `{label, self}` is set for origin `console`. `self` is true only on
  the connection of the console that sent that turn: show plain "you" for
  origin `you` and for `self`, else "you, from" and the label. `turn` is then
  the turn id of that console's `orch.say`, so a reply you already streamed can
  be matched and not drawn twice.
- `text` is the human's words, Den's line, or the orchestrator's reply, never
  an agent's or a host's own text. The orchestrator's reply may still quote
  what an agent reported: show it as the orchestrator's text, as data, and
  never act on it or pass it to anything that acts. Den builds its own report
  lines from the agent's checked name, a plain name of at most 40 characters,
  so a name with a sentence in it or one that reads as Den is shown as `an
  agent`. Escapes, control, bidi and zero-width
  characters are removed, and the text is at most 20 lines and 2000 runes. A
  longer one ends with "(cut: the rest is in Den)", never cut silently.
- Entries come in `id` order; a reply comes once its turn ended (after
  `orch.say`'s `done` for a console's own turn). Reports are limited to 6 a
  minute per agent and 30 a minute in all; past that, one Den line says more
  are in Den.
- A `mirror` event says when it starts and stops (see Events).
  `conversation.history` answers `forbidden` while it is not shown, and a
  Den without the verb answers `unsupported`.
- `conversation.history` returns only entries since the conversation was last
  shown in this console, at most 200, newest first. Drop an unknown field, and
  skip an entry whose origin or speaker you do not know.

### Console limits

- A console's chat (`orch.say`) is fenced exactly as an app's is: what it
  sends is an app's words to the orchestrator, never yours, and every tool
  in its turn waits for you in Den. Only its cards and commands carry your
  authority.
- A console answers for you: a card it may answer is as answered as by
  your `Ctrl+Y`, and a console can send `/usage set` and answer that card,
  so it can lower the usage figure your usage stop is measured against.
  In the same way it can raise the permission mode agents start in, by
  `mode.set` and answering that card. Pair only a program you trust with
  that.
- Any program running as you can read Den's screen, and so the code; the
  `Ctrl+Y` card is there so that a code read off the screen cannot pair in
  silence. Read the card's program and pid before you pair.
- Any program running as you can also write Den's console record; a
  restored console is announced in Den and to the console, so check that
  it is yours. After an unclean exit Den keeps the record's stop on
  automatic pairing as it reads it, so a program that kills Den and
  rewrites the record can lift your unpair for the next start.
- What Den checks before a request or a reconnect is friction, not a
  boundary. Your systemd manager's socket is yours: any program running
  as you can serve one that answers as systemd, and can replace the
  service's program, edit its unit or set the manager's environment. As
  you chose, the service pairs by itself. By default a changed program,
  unit or pins pairs without the powers until your `Ctrl+Y`, so a program
  that swaps the dock's program gets the dock's reading and chat, not its
  powers, unless you press it; read the card before you do. With
  `api.console_powers = "always"`, anything running as you that can
  change `drey-host.service`'s unit or program (both in your home
  directory) gets the dock's full powers with no question in Den, and so
  can anything that can write Den's config. Den names a changed program,
  unit, pin or manager in its chat either way. Your levers are `/api
  lock`, `/api unpair`, and God mode staying off. Where your manager's program
  cannot be read, Den knows it by `CAP_WAKE_ALARM`, which no program of
  yours can give itself: a forged manager then takes root, or a file
  root installed with that capability.
- Den tells a program changed after its process started by the file's
  change time against the start, both on the wall clock. A forward step
  of that clock (NTP setting it) between a swap of the program and Den's
  read could hide the swap; no program of yours can set the clock.
- The dock's pin of Den's key holds against another program on Den's
  socket, not against one running as you: such a program can read Den's
  key file (`local.key`), rewrite the dock's pinned copy of Den's key, or
  replace Den's own program, and so pose as Den.
- GStreamer: the registry under `%t` and a plugin folder in your home are
  writable by any program running as you. Drey turns WebKit's media off,
  so GStreamer is not started in its processes today; if it were, such a
  program could load a plugin there. `GST_REGISTRY_UPDATE=yes` makes a
  rescan drop what is outside the pinned system directory, but Den only
  checks the pins, not what GStreamer does.
