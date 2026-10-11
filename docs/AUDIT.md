# Open-source release audit: den

Audit date 2026-10-11, mission "Open source Den". Covers the working tree and the
full history of every ref (`main`, `spawn`, `review-highlight`,
`merge-chat-mission`, `origin/main`, tag `v0.1.0`): 75 commits on `main` at
audit time, every blob ever committed (deleted files included), and the
`mods/agent-badge` submodule. Nothing was rewritten, pushed or removed. This
file only finds problems and proposes fixes.

**Verdict:** no secrets anywhere. Five commits carry AI `Co-Authored-By`
trailers. Removing them needs a history rewrite, which the human has to
approve. Everything else is low severity or a recommendation. Every dependency
is MIT or BSD-3-Clause.

## Method

- **Secrets:** gitleaks v8 (built from source into a temporary GOBIN), in
  `git --log-opts=--all` mode and in `dir` mode over the working tree, for den
  and for the agent-badge submodule (its tree and its full history). Then
  `git grep -I -E` over `$(git rev-list --all)` for `sk-ant-`, `ghp_`,
  `github_pat_`, `gho_`, `AKIA…`, `PRIVATE KEY`, Slack and Google key shapes,
  and `password|secret|api_key|token` assignments.
- **Personal data:** `git log --all --format='%an <%ae> | %cn <%ce>'`, tagger of
  `v0.1.0`, and `git grep` over every revision for the OS username, `/home/`,
  `/Users/`, real names, email shapes, `claude.ai/` links, hostnames and UUIDs
  (session ids). Every commit message was scanned too. The uncommitted
  working-tree files listed under "Uncommitted work" below were scanned
  as well.
- **AI attribution:** every commit message on every ref was scanned for
  `Co-Authored-By`, `Generated with`, `🤖`, `noreply@anthropic.com`,
  `anthropic.com` and `claude.ai`/`claude.com`.
- **Size and file types:** `git rev-list --objects --all | git cat-file
  --batch-check`, sorted by size. All paths ever added, and all paths ever
  deleted.
- **Licenses:** `go list -m all` and `go list -deps ./...` (to find what is
  actually linked into the binary), then the LICENSE/COPYING file of each
  module in the Go module cache. For agent-badge: its LICENSE and its imports.

## Findings

Severity: **High** means it blocks release. **Medium** means fix before
release. **Low** means fix if convenient. **Info** needs no action.

| # | Kind | Where | Severity | Fix type |
|---|------|-------|----------|----------|
| 1 | AI attribution trailer | 5 commits (listed below) | Medium | History rewrite |
| 2 | Private claude.ai links | 2 commit messages, `docs/DISPATCH-CARDS.md:5` | Low | Working tree + optional rewrite |
| 3 | OS username, hostname, home path | `docs/mockup/den-mockup.html`, `docs/DEMO-WEBSITE.md:63`, test fixtures | Low | Working tree (history optional) |
| 4 | Real Claude Code session ids | `internal/badge/badge_test.go:8-11`, `internal/critter/critter_test.go:12` | Low / Info | Leave, or working tree |
| 5 | Orphan gitlink in history | `.claude/worktrees/spawn` (added `f6a7bde`, removed `6ea8693`) | Low | Optional rewrite |
| 6 | Internal planning docs | `docs/PLAN.md`, `docs/DEMO-WEBSITE.md`, `docs/CODE-REVIEW.md`, `docs/DISPATCH-CARDS.md` | Recommendation | Working tree |
| 7 | Local branches and worktrees | `spawn`, `review-highlight`, `merge-chat-mission` | Info | Push `main` only |
| 8 | License holder differs between repos | `LICENSE` vs `mods/agent-badge/LICENSE` | Info | Owner decision |
| 9 | Third-party notices missing from release binaries | `make release`, `.github/workflows/release.yml` | Low | Working tree |
| — | Secrets | none found | — | — |
| — | Author/committer identity | single identity on every commit | Info | none |
| — | Large files | none over 50 KB | — | — |

### 1. AI attribution trailers (Medium, history rewrite)

Each of these commit messages contains exactly the line
`Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`:

| Commit | Subject | Refs containing it |
|--------|---------|--------------------|
| `ebf8792adb77bef9fb5f25084f96f5c1431351d2` | agent-badge 1.2.1: directory validation fix | main, origin/main, spawn, review-highlight, merge-chat-mission |
| `99de247990dac864e1648dab62f94f7599c7e26c` | Orchestrator can open terminals and start agents on your workspace | same |
| `76ad26d5041a5aec7f183de42907ba252521dfee` | Spawn: notice Claude Code's trust dialog instead of waiting on it | same |
| `cb2da6fa3f7ac80ebcc50c65fa3bec3c3dd80f1e` | Merge main into spawn: dispatch cards, watchdog and resume meet the spawn tools | same |
| `342a144d34edc220af15dca639d2fd305f8ebf2b` | Merge branch 'spawn': orchestrator spawns terminals and agents | all except spawn |

None of these commits has a `Generated with` line, `🤖`, or any other
attribution. None of them is in tag `v0.1.0`. They are already on the private
`origin/main` (github.com/GlacierHubAB/den, private).

There is nothing to fix in the working tree. The fix is the rewrite in "History
plan" below. The rewrite changes the message of these 5 commits, and so the
hash of `ebf8792` and of every commit after it: 39 commits on `main` at audit
time (`ebf8792^..main`), plus whatever lands later. `v0.1.0` and its 24
ancestors keep their hashes.

### 2. Private claude.ai artifact links (Low)

- Commit `c2706cfcbabdfe1e67faa98b039cfd9eadd9c219` ("Mockup: Overview tab…")
  ends with "Published at" followed by a claude.ai artifact link.
- Commit `f3af5076100f28216a2fc2c3b3240a3044fa61c7` ("Mockup: agent
  hierarchies…") ends with "Published at" followed by a claude.ai artifact
  link.
- `docs/DISPATCH-CARDS.md:5` has the same kind of link, in the working tree and
  in history.

These artifacts are private by default. The links are dead ends for the public
and reveal internal tooling. They are not secrets.

- **Working tree:** in `docs/DISPATCH-CARDS.md`, delete the parenthesised
  `(published at https://claude.ai/artifact/…)` text on lines 4–5 and keep
  the mockup path.
- **History (optional):** the message callback in "History plan" strips
  "Published at <link>" from both commits. Both are after `ebf8792`, so this
  adds no extra rewritten commits.

### 3. OS username, hostname and home paths (Low)

No email addresses or real names other than the author identity. The local
username `grvlle` and the hostname `omarchy` do appear:

- `docs/mockup/den-mockup.html:260,440,487,636`: status-bar text `<user>@<host> · hh:mm`.
  Older revisions also had it at lines 177 and 356.
- `docs/DEMO-WEBSITE.md:63`: an absolute path under the author's home
  directory (`/home/<user>/code/den-site`).
- Test fixtures:
  - `internal/inbox/describe_test.go:10` and `internal/ui/tree_test.go:138,140`
    hold a Claude Code scratchpad path that contains the home directory and a
    session UUID.
  - `internal/orch/orch_test.go:16,18,62-70`, `internal/ui/orch_view_test.go`
    and `internal/ui/view_test.go:20-22` use peer names of the form
    `<user>-45`, `<user>-46` and `<user>-aa`.

The username is not a credential. It identifies the author's account name on
their machine, and the GitHub org already identifies the company.

- **Working tree (recommended):** replace `grvlle@omarchy` with `you@host` in
  the mockup. Replace the absolute path in `docs/DEMO-WEBSITE.md:63` with
  `$HOME/code/den-site`. In the test fixtures, replace `grvlle` with `user`
  (for example `user-45`, `-home-user`). The tests compare these strings to
  themselves, so a consistent replace keeps them passing.
  `internal/ui/tree_test.go` currently has another session's uncommitted
  edits. Make that change only after that work lands.
- **History (optional, only if the owner wants the name gone from history):**
  add `--replace-text` to the rewrite with an expressions file containing
  `literal:grvlle==>user`. Every occurrence of the string is in a test or a
  doc. No non-test code contains it, so the build is unaffected.

### 4. Real Claude Code session ids in tests (Low / Info)

`internal/badge/badge_test.go:8-11` and `internal/critter/critter_test.go:12`
use four real session UUIDs from the author's machine. The tests use them to
pin the UUID → critter-name mapping. A session id is a random local
identifier, not a credential, and it can't be used remotely. Leaving them is
fine. If they should go, swap in freshly generated UUIDs and update the
expected names. The names are computed, so recompute them with the hash under
test.

### 5. Orphan gitlink in history (Low, optional rewrite)

Commit `f6a7bde` ("Dispatch cards and a persistent orchestrator chat") added
`.claude/worktrees/spawn` as a gitlink (mode 160000, pointing at den's own
`ebf8792`) without a `.gitmodules` entry. `6ea8693` removed it and ignored
`.claude/`. It holds no content and no data. A checkout of that one commit may
print "no submodule mapping found". Optionally remove it in the rewrite with
`--path .claude/worktrees/spawn --invert-paths`. It is after `ebf8792`, so no
extra commits are rewritten.

### 6. Internal docs that probably should not ship as-is (recommendation)

| File | What it is | Recommendation |
|------|------------|----------------|
| `docs/PLAN.md` | the original scoped plan and milestones | remove, or move to a `docs/design/` folder marked historical |
| `docs/DEMO-WEBSITE.md` | runbook for recording the website demo; contains `git reset --hard` / `clean -fdq` on a sibling checkout and a home path | remove from the public tree |
| `docs/CODE-REVIEW.md`, `docs/DISPATCH-CARDS.md` | dated design passes ("Nothing here is implemented yet") | keep as design notes or remove; fix item 2 if kept |
| `docs/mockup/*.html` | HTML design mockups of the TUI | fine to ship; fix item 3 |

There is no `CLAUDE.md` in den. `.claude/` (holding local worktrees) is
ignored and was never committed apart from item 5. No build outputs (`den`,
`dist/`) were ever committed.

None of this is sensitive, so history can keep these files. If the owner wants
them gone from history too, add `--path docs/PLAN.md --path
docs/DEMO-WEBSITE.md --invert-paths` to the rewrite. That rewrites from
`f337466`, the first commit, so every hash changes, `v0.1.0` included.

### 7. Branches and worktrees (Info)

The local branches `spawn`, `review-highlight` and `merge-chat-mission` are
checked out in worktrees under `.claude/worktrees/`. One more detached worktree
lives in a temporary directory. At audit time none of these branches had
commits that are not on `main` (`git log main..<branch>` is empty). When publishing, push `main` and the release tag only, never `--all` or
`--mirror`.

### 8. Copyright holder (Info)

`LICENSE` (den, added `36fc579`) names "Martin Granström". The bundled
`mods/agent-badge/LICENSE` and its `.claude-plugin/marketplace.json` name
"GlacierHub AB". Both are valid MIT. The owner should decide whether den's
copyright belongs to the person or the company and make the two consistent.

### 9. Third-party notices in release binaries (Low)

Every linked dependency is MIT or BSD-3-Clause (see Licenses). Both licenses
require the copyright notice and license text to accompany redistributed
binaries. `make release` and the release workflow ship bare binaries plus
`SHA256SUMS`. Proposed fix: generate a `THIRD_PARTY_LICENSES` file and attach
it to each release. One way is `go-licenses report ./cmd/den` or `go-licenses
save ./cmd/den --save_path dist/licenses`, run from a temporary GOBIN. The other
is a small script that concatenates each linked module's LICENSE from `go list
-deps -f '{{.Module.Dir}}' ./cmd/den`. Den's own LICENSE and agent-badge's are
already in the tree (agent-badge's is embedded in the binary).

### Secrets: none

- gitleaks found nothing in the den history (all refs), the den working tree,
  or the agent-badge tree and history.
- The targeted grep found only `internal/spawn/spawn_test.go:141,143`:
  `ANTHROPIC_API_KEY=k`, a one-letter placeholder in an environment-filtering
  test. It is not a secret.
- No `.env`, key, certificate or credential file was ever committed. The CI
  workflows use no repository secrets.

### Identity: one address (Info)

All commits on every ref, author and committer, use the single identity
`Martin Granström <martin@glacierhub.io>`. The `v0.1.0` tagger uses it too. It
is a company address, it is consistent, and it is the one the author intends
to publish under. No `.mailmap` is needed. If a no-reply address is preferred,
a mailmap rewrite would change every hash.

### Large or binary files: none

The largest blob ever committed is `docs/mockup/den-mockup.html` (46 KB). No
binaries, images or videos are in den's history. The submodule's
`.claude-plugin/icon.png` lives in the agent-badge repo.

## The agent-badge submodule

`mods/agent-badge` points at https://github.com/GlacierHubAB/agent-badge.git.
That repo is **public** and MIT-licensed (GitHub API: `private: false`,
`license: MIT`). Den's history pins three commits: `ac369b5` (in `af0ca69`),
`ec26dfe` (in `ebf8792`) and `c779c78` (in `d0bfd3a`, current). All three are
on the public remote (`git ls-remote` shows `main` at `c779c78`). A public
`git clone --recurse-submodules` of den therefore works for every revision. The
README, CONTRIBUTING.md and docs/DEVELOPMENT.md already say to clone with
`--recurse-submodules` and that `go install …@latest` cannot work. The reason
is that `den.go` embeds submodule files and Go module zips omit submodules.
Both workflows check out submodules.

agent-badge's own history carries AI trailers, already public:
`Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` in `12a494d`,
`5384b1f`, `db668fd`, `7c0d378` and `62b4068`, and `Co-Authored-By: Claude
Fable 5.1 <noreply@anthropic.com>` in `ec26dfe`. Rewriting that repo is a
separate decision. If it is rewritten, `ac369b5`, `ec26dfe` and `c779c78`
change hash. Den's gitlinks would then point at commits that no longer exist,
and old den revisions would fail to clone recursively. The rewritten hashes
would also have to be mapped into den's gitlinks during den's rewrite. The
recommendation is to leave agent-badge's history alone, since it is already
public, and stop the practice going forward.

agent-badge has no third-party dependencies. Its only imports are `'claude-code'`
and `'claude-code/testing'`, which are module types provided by the Claude Code
host at runtime, plus its own `../types`.

## Licenses

Linked into the den binary (from `go list -deps ./...`):

| Module | Version | License |
|--------|---------|---------|
| charm.land/bubbles/v2 | v2.2.1 | MIT (Charmbracelet) |
| charm.land/bubbletea/v2 | v2.0.10 | MIT (Charmbracelet) |
| charm.land/lipgloss/v2 | v2.0.6 | MIT (Charmbracelet) |
| github.com/alecthomas/chroma/v2 | v2.27.0 | MIT (COPYING, Alec Thomas) |
| github.com/charmbracelet/ultraviolet | v0.0.0-20260811164956-006e29f97886 | MIT |
| github.com/charmbracelet/x/ansi | v0.11.8 | MIT |
| github.com/charmbracelet/x/term | v0.2.2 | MIT |
| github.com/charmbracelet/x/termios | v0.1.1 | MIT |
| github.com/charmbracelet/x/windows | v0.2.2 | MIT |
| github.com/charmbracelet/colorprofile | v0.4.3 | MIT |
| github.com/fsnotify/fsnotify | v1.10.1 | BSD-3-Clause |
| github.com/atotto/clipboard | v0.1.4 | BSD-3-Clause |
| github.com/clipperhouse/displaywidth | v0.11.0 | MIT |
| github.com/clipperhouse/uax29/v2 | v2.7.0 | MIT |
| github.com/dlclark/regexp2/v2 | v2.2.1 | MIT |
| github.com/lucasb-eyer/go-colorful | v1.4.1 | MIT |
| github.com/mattn/go-runewidth | v0.0.27 | MIT |
| github.com/muesli/cancelreader | v0.2.2 | MIT |
| github.com/rivo/uniseg | v0.4.7 | MIT (LICENSE.txt) |
| github.com/xo/terminfo | v0.0.0-20220910002029-abceb7e1c41e | MIT |
| golang.org/x/sync | v0.22.0 | BSD-3-Clause |
| golang.org/x/sys | v0.47.0 | BSD-3-Clause |
| mods/agent-badge (embedded) | c779c78 | MIT (GlacierHub AB) |

Other modules appear in `go list -m all` only through test and tooling
requirements of dependencies and are not linked: MakeNowJust/heredoc,
alecthomas/assert, alecthomas/repr, aymanbagabas/go-udiff,
bits-and-blooms/bitset, charmbracelet/harmonica, charmbracelet/x/exp/golden,
clipperhouse/stringish, dustin/go-humanize, hexops/gotextdiff, sahilm/fuzzy,
golang.org/x/exp.

**Compatibility:** MIT and BSD-3-Clause are permissive and compatible with
releasing den under MIT. No Apache-2.0, MPL, LGPL or GPL code is linked. No
dependency ships a NOTICE file, so there are no Apache §4(d) obligations. The
only obligation is attribution in binary distributions (item 9). BSD-3's
no-endorsement clause means the README must not imply endorsement by the Go
Authors or the fsnotify authors. It currently doesn't.

## History plan (requires human approval)

The proposal: publish a rewritten copy to the new public repo, and leave the
private `GlacierHubAB/den` and the shared checkout as they are. Don't rewrite
in place. Nothing needs to be force-pushed anywhere, the private repo stays as
the archive of record, and the agents working in `/home/grvlle/code/den` are
undisturbed.

These commands were not executed during the audit. Run them only after
approval, in a fresh temporary directory, never in the shared checkout.
`git-filter-repo` is a single Python script from
https://github.com/newren/git-filter-repo.

```sh
# 1. Fresh copy. The shared checkout is never touched.
cd "$(mktemp -d)"
git clone --no-local --single-branch --branch main /home/grvlle/code/den den-public
cd den-public
git fetch origin 'refs/tags/v0.1.0:refs/tags/v0.1.0'
git remote remove origin

# 2. Rewrite: drop the AI trailers (item 1), the claude.ai links in messages
#    (item 2) and the orphan gitlink (item 5).
git filter-repo --force \
  --path .claude/worktrees/spawn --invert-paths \
  --message-callback '
import re
message = re.sub(rb"(?m)^Co-Authored-By: Claude [^\n]*<noreply@anthropic\.com>[ \t]*\n?", b"", message)
message = re.sub(rb"\s*Published at\s+https://claude\.ai/artifact/\S+", b"", message)
return message.rstrip(b"\n") + b"\n"
'
#    Optional (item 3): add  --replace-text ../expressions.txt
#    where expressions.txt contains:  literal:grvlle==>user

# 3. Verify before anything leaves the machine.
git log --all --format=%B | grep -ciE 'co-authored-by|noreply@anthropic|claude\.ai/artifact'   # expect 0
git log --all --format='%an <%ae> | %cn <%ce>' | sort -u                                    # one identity
git submodule update --init && go build ./... && go test ./...
git rev-parse v0.1.0^{commit}    # unchanged: b27851f478d30cae7c369ef6dc74f47a6820d165

# 4. Only with the human's go-ahead: add the NEW public repo as the remote and
#    push main plus the chosen tags. Never --all or --mirror.
```

What changes:

- The 5 commits in item 1 lose their trailer line. `c2706cf` and `f3af507` lose
  "Published at <link>". `f6a7bde` no longer adds the gitlink, and `6ea8693`
  becomes a `.gitignore`-only commit.
- Every commit from `ebf8792` onward gets a new hash (39 at audit time).
- The first 24 commits, through `b27851f` / tag `v0.1.0`, are unchanged.
- File contents at the tip are identical, except for the optional replace-text.
- Do the working-tree fixes (items 2, 3, 6, 9) as ordinary commits on `main`
  before cutting the public copy, so they are part of it.

Alternative if history is not wanted at all: publish a single squashed initial
commit (`git checkout --orphan public && git commit`) made from the cleaned
tree. It is simpler, but it loses authorship history and `v0.1.0`.

## Uncommitted work at audit time

The working tree had another session's uncommitted edits in
`internal/ui/project.go`, `internal/ui/project_test.go`, `internal/ui/tree.go`,
`internal/ui/tree_test.go` and `internal/ui/view.go`. They were scanned and not
touched. The only finding in them is the scratchpad path in
`internal/ui/tree_test.go:140` (item 3), which mirrors the committed line 138.

## Fix in the working tree (no history change)

1. `docs/DISPATCH-CARDS.md:4-5`: delete the claude.ai link (item 2).
2. `docs/mockup/den-mockup.html`: replace `grvlle@omarchy` with `you@host`
   (item 3).
3. `docs/DEMO-WEBSITE.md`: remove the file (item 6). If it stays, replace the
   absolute path on line 63 with `$HOME/code/den-site`.
4. Test fixtures: replace `grvlle` with `user` in
   `internal/inbox/describe_test.go`, `internal/orch/orch_test.go`,
   `internal/ui/orch_view_test.go` and `internal/ui/view_test.go`, and in
   `internal/ui/tree_test.go` after the pending work there lands (item 3).
5. `docs/PLAN.md`: remove or move to `docs/design/` (item 6).
6. Release tooling: ship a `THIRD_PARTY_LICENSES` file with every release
   (item 9).
7. Owner decision: the copyright holder in `LICENSE` (item 8).

## Needs a history rewrite (requires human approval)

1. Strip `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>` from
   `ebf8792`, `99de247`, `76ad26d`, `cb2da6f` and `342a144` (item 1).
2. Optional: strip the claude.ai links from the messages of `c2706cf` and
   `f3af507` (item 2).
3. Optional: drop the gitlink `.claude/worktrees/spawn` from `f6a7bde` and
   `6ea8693` (item 5).
4. Optional: replace `grvlle` with `user` throughout history (item 3).

All four run in a single `git filter-repo` pass, shown under "History plan".
