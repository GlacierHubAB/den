# Code review view

Design pass, 2026-10-08. Build steps 1 to 4 are shipped: the Review tab shows each agent's work, its files and a diff with author stripes, and takes comments on lines (keyboard or mouse), saved to ~/.local/state/den/reviews.json; sending is next. Mockup: Screen 3
and 3b in `docs/mockup/den-mockup.html`.

## Goal

Review what the agents changed without leaving Den, leave comments on lines,
and send each agent the comments on its own work, into the session that
still holds the context for it.

## The view

A fourth tab, `4 Review`, with three panes over the command line.

| Pane | Shows |
| --- | --- |
| Recent work (left, top) | One row per agent with changes you have not reviewed: avatar colour, name, role, task, repo · branch, `+added −removed`, draft comment count, age. Reviewed work drops to the bottom with a ✓. Gone sessions stay, marked ✕. |
| Files (left, bottom) | The tree of changed files for the selected work, each with a square in the colour of the agent that wrote it (grey for lines no agent wrote), `+ −` counts and comment count. |
| Diff (centre) | The selected file, working tree against the base branch, in hunks. A 1-cell stripe in the gutter carries the author's colour per line. One line has the cursor; a range can be selected. Comments render inline under the line they belong to. |
| Review (right) | Draft comments grouped by the agent that will receive them, with that agent's state (live and idle, live and working, or gone), and a verdict per agent: approve, request changes, comment. |

## Keys and mouse

The command line stays open, so letters stay text, as everywhere in Den.

- `← →` move between panes. `↑ ↓` move the line cursor in the diff, or the
  selection in the lists.
- `Shift+↑ ↓` extends the line range. `Ctrl+↑ ↓` jumps between hunks,
  `PgUp PgDn` between files.
- Mouse: click a line to put the cursor on it, drag to select a range, click
  a file or a work row to open it, wheel to scroll. bubbletea v2 reports
  cell-level mouse events, and each rendered diff row maps back to
  `(file, newLine)`.
- With lines selected, typing writes a comment; the command line shows a chip
  with the range (`list.go L49–51`). `Enter` adds it. `Enter` on an empty
  line with the cursor on a comment edits it. `/delete` removes it.
- `@Name` on the command line still messages an agent directly.
- `Ctrl+S` opens the send sheet.

## Where the data comes from

| Need | Source | Exists |
| --- | --- | --- |
| Which files each agent touched, with hunks | `transcript.Tail` already turns `Edit`, `Write` and Bash file changes into `FileChange{Path, Hunks}` per session | yes, but only the recent tail |
| What is really on disk | `git diff --no-color -U3 <base>...` in the agent's cwd (base: upstream of the branch, else `main`), plus untracked files | new, small |
| Line authorship | Match each `+` line of the git diff against the transcript hunks of every session whose cwd is in that repo, newest edit wins. Unmatched lines are grey ("not an agent"). | new |
| Agent liveness and how to reach it | `discover` (pid alive, socket path) | yes |
| Delivery | `peer` inbox socket, as `@Name` uses today | yes |

Two gaps:

1. **Full transcript scan.** `Tail` reads the last N entries. Review needs
   every file change since the work started. Add `transcript.Changes(path,
   since)` that scans the whole file once, caches by size and mtime, and
   tails new bytes afterwards.
2. **Where "the work" starts.** Default: since the branch left its base
   (`git merge-base`), or since the session started when the cwd is not a
   git repo. Reviewed state is stored per (session, file, content hash) in
   `~/.local/state/den/reviews.json`, so a file that changes after review
   shows as unreviewed again.

## Sending

`Ctrl+S` opens a sheet with one message per agent. Each message is plain
text, so any agent understands it without a tool:

```
Review from you via Den · request changes
tasklog · feat-pending-filter · 3 comments

cmd/list.go:49-51
  > if pending { tasks = store.Pending(tasks) }
  Pending should also exclude archived tasks, see filter.go:31.

Fix these, then say which comment each change addresses.
```

- **Live agent:** posted to its own session socket, the same path as `@Name`,
  with the same typing guard. Idle agents start a turn; working agents get it
  between tool calls.
- **Gone agent:** the sheet asks. `r` resumes it in a new terminal with
  `claude --resume <sessionId>` and sends once the new session appears in
  discovery; `o` hands the comments to the orchestrator, which picks a live
  agent; `k` keeps them as notes.
- **Lines no agent wrote** cannot be sent to an agent. They stay as notes, or
  go to the orchestrator if you say so.
- After sending, comments turn green ("sent") and stay attached to their
  lines. When that agent's next `Stop` arrives, the work row shows "changed
  since review" and the diff marks which commented lines changed, so a
  second pass only needs to look at those.

## Build order

1. `gitinfo.Diff(dir, base)` and a parser for unified diffs into the existing
   `transcript.Hunk` shape. Tests with fixture repos.
2. `transcript.Changes` (full scan, cached) and the authorship matcher.
   Tests: two sessions editing the same file, a Bash `sed` edit, a file
   edited and then reverted.
3. Review tab with Recent work and Files panes, read-only diff with author
   stripes.
4. Line cursor, range selection, mouse mapping, inline comments, drafts kept
   in `~/.local/state/den/reviews.json` so a restart loses nothing.
5. Send sheet, delivery through `peer`, gone-agent choices, re-review marks.

## Open questions

1. Should approve also do something (for example tell the agent "approved,
   go ahead and open the PR"), or only record the verdict?
2. Side-by-side diff when the pane is wide enough, or unified only?
3. Should comments on one agent's work be visible to the orchestrator, so
   "who has open review comments?" works?
