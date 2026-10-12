# Contributing to Den

Thanks for helping. Den is a Go terminal UI for watching and steering Claude
Code agents; this file is the short version of how to change it.

## Build and test

```
git clone --recurse-submodules https://github.com/GlacierHubAB/den   # the badge mod is a submodule
cd den
make build        # ./den
make test         # go vet ./... and go test ./..., against a scratch XDG_STATE_HOME
```

You need Go 1.26 or newer (see `go.mod`). If you cloned without
`--recurse-submodules`, run `git submodule update --init`; the build embeds
`mods/agent-badge` and fails without it.

`make test` points `XDG_STATE_HOME` at a temporary directory so tests never
touch your own Den state. Do the same when you run `go test` by hand:

```
XDG_STATE_HOME=$(mktemp -d) go test ./internal/review -run TestAttribute
```

CI (`.github/workflows/ci.yml`) runs on every pull request into `dev` or
`main` and every push to them: `gofmt`, `go mod tidy`, the
`THIRD_PARTY_LICENSES` check, `actionlint`, `make test` with `-race` on the
release Go and the newest Go, `govulncheck` and a `gitleaks` scan. The tests
must pass without a git identity and without network, as they do there: a test
that runs `git commit` or `git merge` sets `user.name` and `user.email` in its
own repository. Please run `make test` before you open a pull request.

## Code layout

`cmd/den` holds the commands, `internal/ui` the bubbletea application, and the
rest of `internal/*` the pieces it is built from (discovery, hooks, inbox,
orchestrator, spawn, review, missions…). [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)
has the package map and how to run Den against throwaway state.

## Changes

- Keep commits small and focused: one change, one commit, tests with it.
- Write plain commit messages: a short imperative first line that says what
  changed (`Review: syntax highlighting in diffs`), and a body when the why is
  not obvious.
- Match the code around you: naming, comment density, error handling. Run
  `gofmt` and `go vet`.
- Add or update a test for behaviour you change. Tests that need a real
  Claude Code, Hyprland or a terminal do not belong in the default run.
- Update the README when you change a key, a command, a config setting or a
  file Den writes. Describe only what the code does.
- Do not edit `mods/agent-badge` from here; it is a separate repository.

## Reporting issues

Open an issue with what you did, what you expected and what happened. It
helps to include:

- the output of `den version`, and `claude --version`;
- your compositor and terminal (Den's focus and spawn features depend on
  Hyprland and the terminal launcher);
- `den list` and `den explain <name>` for the agent in question;
- the relevant lines of `~/.local/state/den/hooks.log` if the inbox is
  involved.

Remove anything private from pasted output (paths, prompts, transcripts).
Security problems: please do not file a public issue. Report vulnerabilities
through GitHub's private vulnerability reporting (the repository's Security
tab, "Report a vulnerability").

## Branches and pull requests

Work happens on short-lived branches. `dev` is where changes land; `main` holds
what has been released, and every release is a tag on `main`.

```
feature/<topic>  ──PR, squash──▶  dev  ──PR, merge commit──▶  main  ──tag vX.Y.Z──▶  release
fix/<topic>      ──PR, squash──▶  dev
hotfix/<topic>   ──PR, squash──▶  main, then the same fix into dev
```

- Branch from `dev`: `git switch -c feature/review-filter origin/dev`. Name the
  branch `feature/…`, `fix/…`, `docs/…` or `ci/…` after what it does.
- Open the pull request against `dev`. One topic per pull request, with a
  description that says what and why. The PR template has the checklist.
- `ci-passed` must be green. It is the one required check and it only passes
  when every CI job did.
- Pull requests into `dev` are squash-merged, so each one becomes one commit
  on `dev` and the pull request title becomes its first line: write the title
  as a plain commit message.
- Keep your branch up to date with `dev` by rebasing (`git rebase origin/dev`)
  rather than merging `dev` into it.
- Be ready to split or rework a large change; small, reviewable pull requests
  land faster.
- Do not include generated binaries (`den`, `dist/`) or personal paths.

Only maintainers merge. A pull request from `dev` into `main` is a release
and follows [docs/RELEASING.md](docs/RELEASING.md); a hotfix is the one other
way into `main`. Nobody pushes to `dev` or `main` directly, force-pushes them
or deletes them, and only maintainers create `v*` tags.

Agents work the same way: an agent's worktree branch goes into `dev` through
a pull request, never straight into `main`.

## License

Den is released under the MIT license (see [LICENSE](LICENSE)). By submitting a
contribution you agree that it is licensed under the same terms. Third-party
code keeps its own notice: the agent-badge mod in `mods/agent-badge` is MIT,
and the name generator in `internal/badge` is ported from it. Keep those
attributions intact.
