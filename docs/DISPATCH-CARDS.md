# Dispatch cards and a persistent orchestrator chat

Design pass, 2026-10-08. Nothing here is implemented yet.

## Goal

You stay in the Orchestrator view. When the orchestrator hands work to an
agent, that agent's own work shows up in the chat, done by the agent and
shown as the agent's, not retold by the orchestrator. You leave the chat only
to inspect an agent in depth or to answer the inbox. The chat survives a
restart.

## What a dispatch is, in Den's terms

The orchestrator runs as `claude -p` with two tools, `ListAgents` and
`SendMessage`. A dispatch is one `SendMessage` tool call whose `to` is a fleet
agent. Den already sees that call as a stream event (today it renders a short
"→ Mossy Lemur: …" line). From the receiving agent, Den already gets:

| Signal | Source | Exists |
| --- | --- | --- |
| the message landed and a turn started | `UserPromptSubmit` hook, prompt text carries the orchestrator's name | yes |
| every tool call as it happens | `PostToolUse` hook (tool name and input) | yes |
| permission, plan, question | `PermissionRequest`, `PreToolUse(AskUserQuestion)`, `Notification` hooks, already inbox items | yes |
| the turn ended and what the agent said last | `Stop` hook, `last_assistant_message` | yes |
| the full turn: prose, calls, diffs, output | transcript tail (`internal/transcript`) | yes, but not yet filtered to one turn |

So a card is a view over data Den mostly has. The one new need is to cut the
transcript at turn boundaries (see "Data we need").

## The card

A card is one entry in the chat stream, in chat order, owned by the agent it
was sent to. It carries the agent's small avatar (the 8x4 octant one from the
inbox), the name in the agent's colour, the role chip, the brief, a status
chip, and a body that depends on the state.

```
 you › get the three idle agents to review the release notes

 orchestrator
 Sending the notes to Mossy Lemur and Fuzzy Sprite; Plucky is on a task.

 ┌ 𜴀𜴿𜷥𜵥𜴚𜴊  Mossy Lemur  CODING AGENT                   ● working · 42s ┐
 │ ▸ Review docs/RELEASE.md for the 0.2 notes; flag anything misleading.    │
 │ ⠸ Read docs/RELEASE.md                                       3 calls · ctx 18% │
 └──────────────────────────────────────────────────────────── → open  ⏎ expand ┘

 ┌ 𜶤█▆▆█𜵥  Fuzzy Sprite  CODING ASSISTANT                 ◐ needs you ┐
 │ ▸ Same review, with an eye on the install section.                     │
 │ Wants to run  $ gh release view v0.1.0        answer in Inbox · ^Y ^N here │
 └──────────────────────────────────────────────────────────────────────────┘
```

### States

| State | Chip | Body while in it |
| --- | --- | --- |
| queued | `· sending` (muted) | the brief; "waiting: you are at that terminal" when the typing guard holds it |
| working | `● working · 42s` (agent colour) | a one-line ticker: spinner, latest tool call (`Read docs/RELEASE.md`, `Bash go test ./...`), call count, context % |
| needs you | `◐ needs you` (amber) | the inbox item's headline, and the keys: `^Y ^N` act on it from here when it is the top waiting item, `/inbox` jumps to it |
| done | `✓ done · 1m 12s` (ok) | collapsed: the agent's last message as a one-sentence gist, plus `5 calls · 2 files changed` |
| failed | `✕ denied` / `✕ gone` / `✕ no reply` (err) | the reason, one line |

A "needs you" card is answered in the inbox or, for a permission, with the
same Ctrl+Y / Ctrl+N that already work everywhere: the card is a mirror of the
inbox item, not a second place where state lives.

### Expanded

`Enter` on an empty command line while a card is focused toggles it. Expanded,
the body is the agent's turn, rendered with the detail pane's renderers (code
in the agent's colour, diffs with line numbers, command output), newest last
so it reads like a log:

```
 ┌ 𜴀𜴿𜷥𜵥𜴚𜴊  Mossy Lemur  CODING AGENT                    ✓ done · 1m 12s ┐
 │ ▸ Review docs/RELEASE.md for the 0.2 notes; flag anything misleading.    │
 │                                                                          │
 │ ● Read  docs/RELEASE.md                                                  │
 │ ● Bash  Check the install command works                                  │
 │   ┃ den install-mods --help                                              │
 │   ⎿ usage: den install-mods [--force]                                    │
 │ ● Edit                                                                   │
 │   ⎿ Updated docs/RELEASE.md (+2 -1)                                      │
 │     14 -  `den install-mods` installs the badge into ~/.claude/mods      │
 │     14 +  `den install-mods` installs the badge into ~/.claude/skills,   │
 │     15 +  which Claude Code loads without flags.                         │
 │                                                                          │
 │ Two things were misleading: the install path (fixed above) and …         │
 └─────────────────────────────────────────── 3 calls · 1 file · → open ┘
```

The last paragraph is the agent's own `last_assistant_message`, verbatim,
rendered with the same light markdown as the detail pane. The orchestrator's
own reaction, if it says anything afterwards, follows the card as a normal
orchestrator message, visibly separate, so you always know who said what.

### Three or more in flight

Cards stay in the stream, in the order they were sent, each foldable on its
own. What keeps parallel work legible is a sticky strip at the top of the
Orchestrator pane that lists every card that is not done, one row each, in
agent colours, with the ticker. It appears when two or more cards are live
and goes away when they settle, so a long chat never hides a running agent:

```
 ┌─ Orchestrator ───────────────────────────────────────────────────────────┐
 │ in flight  ● Mossy Lemur  Bash go test ./...   ◐ Fuzzy Sprite  needs you │
 │            ● Plucky Capybara  Read internal/hooks/install.go  · 2 of 3   │
 │ ─────────────────────────────────────────────────────────────────────────│
 │ you › …                                                                  │
 │ …chat, with the three cards in it…                                       │
```

Each row is one line and the strip is at most four rows (it says "+2 more"
after that). Up and down move between the cards in the stream, and the strip
highlights the focused one. Side by side columns were considered and
rejected: the transcript bodies need width, and reading three narrow columns
of diffs is worse than folding.

### Keys

Letters still belong to the command line, so card actions use what the rest
of Den uses:

| Key | On a focused card |
| --- | --- |
| `↑ ↓` | move focus between cards (and scroll the chat when at the ends) |
| `Enter` (empty line) | expand or collapse |
| `→` | open the agent in the Fleet view, detail pane, with that agent selected; Den remembers the card |
| `←` (in Fleet, when you came from a card) | back to the Orchestrator view at that card |
| `/open` | focus the agent's terminal |
| `^Y ^N` | answer the card's permission, exactly as in the inbox |
| `/inbox` | jump to the inbox item behind a "needs you" |

`@Name` on the command line while a card is focused still messages that agent
directly; the reply shows as a new card attributed to you, not the
orchestrator, so the thread stays honest about who asked.

## Persistence

Two things must survive a restart: the chat you see, and what the
orchestrator remembers.

**The chat.** Den appends every entry to `~/.local/state/den/orchestrator.jsonl`
as it happens: your messages, orchestrator text, and for each card a record
with the dispatch id, the agent's session id, the brief, the send time, the
turn's start and stop times once known, the final state and the gist. On
launch Den reloads the file, rebuilds the cards, and refills each expanded
body lazily from the agent's transcript using the stored time window, so a
card expanded a day later still shows the actual diffs. Cards for sessions
whose transcript is gone show the stored gist and "transcript no longer
available". A card left "working" when Den quit is reconciled on restore: if
the agent's transcript has a Stop after the start time, it becomes done; if
the session is gone, it becomes "gone".

**The orchestrator's memory.** Today the orchestrator runs with
`--no-session-persistence`, so a restart gives it amnesia even if the chat on
screen is restored. The clean fix is to drop that flag, keep the orchestrator's
session id in the same state file, and start it with `--resume <id>` next
time. Claude Code then restores its own context from its own transcript, and
Den never has to replay the conversation into the prompt. `/orch new` starts
a fresh session when the context has grown stale; the old chat stays on disk
and on screen, marked with a rule "new orchestrator session". If the resume
fails (session deleted, Claude Code upgraded), Den falls back to a fresh
session and prepends a short "previous conversation" block built from the
last 20 entries of the jsonl, so continuity degrades instead of breaking.

Retention: the file keeps the last 500 entries; `den orchestrator-log` can
dump it for scripts.

## Data we need, and where it comes from

1. **Turn boundaries per dispatch.** Match the orchestrator's `SendMessage`
   call to the agent's next `UserPromptSubmit` whose prompt carries the
   orchestrator's name (Den already names it in the message header), then
   that session's next `Stop`. Both hooks exist; Den needs to keep the two
   timestamps on the card. When the agent is busy and the message queues
   inside Claude Code, the turn starts later; the card stays "sending" until
   the hook arrives, which is the truth.
2. **Transcript records with timestamps.** Every transcript line has a
   `timestamp`; `transcript.Tail` must parse it so a card can ask for "the
   entries between start and stop". Small change to the parser.
3. **Live ticker without polling.** `PostToolUse` already arrives per call
   with tool name and input; the card's ticker comes from it, and the call
   count with it. No transcript tailing while working, only on expand.
4. **The agent's final words.** `Stop` carries `last_assistant_message`. That
   is the card's result, verbatim.
5. **Orchestrator session id.** Already in the stream's init event; it has to
   be stored, and `--no-session-persistence` dropped.
6. **Agents replying to the orchestrator.** An agent may call its own
   `SendMessage` back to `den-orchestrator`. That reply reaches the
   orchestrator process as a user turn; whether it surfaces as a stream event
   Den can see needs a check. If it does not, the Stop message is still the
   agent's result, so the card is complete either way; the orchestrator's
   follow-up text would be the only thing missing, and it shows up as soon as
   the orchestrator speaks.
7. **Nothing new from the badge.** Avatar, colour and role come from what Den
   already has.

## What stays out

- No paraphrase. The card never shows text the orchestrator wrote about the
  agent as if the agent said it.
- No answering questions in the chat. `AskUserQuestion` cannot be answered
  remotely; the card says "needs you" and points at the terminal.
- No embedded terminals. `→` and `/open` are the deep links.

## Build order, once approved

1. Transcript timestamps and a `Turn(path, from, to)` reader. (small)
2. Dispatch tracking: match `SendMessage` → `UserPromptSubmit` → `Stop`, feed
   `PostToolUse` into a ticker. (medium)
3. Cards in the chat stream: states, fold, focus, the in-flight strip, keys.
   (large, reuses the detail pane renderers)
4. Chat persistence to jsonl, restore and reconcile. (medium)
5. Orchestrator resume instead of amnesia, `/orch new`. (small, needs a live
   check of `claude -p --resume` with stream-json)
