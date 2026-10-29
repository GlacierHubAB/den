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
release Go and the newest Go (with the full history checked out and
`openssh-client`, `openssh-server` and `tmux` installed for the remote
acceptance tests), `govulncheck` and a `gitleaks` scan. The tests
must pass without a git identity and without network, as they do there: a test
that runs `git commit` or `git merge` sets `user.name` and `user.email` in its
own repository. Please run `make test` before you open a pull request.

To use what is on `dev` as your own `den`, see
[docs/DEV-CHANNEL.md](docs/DEV-CHANNEL.md).

## Dependency updates

Dependabot opens a weekly pull request into `dev` for Go modules and one for
the GitHub Actions. A Go modules pull request fails CI's
`THIRD_PARTY_LICENSES` check until the file is regenerated, which Dependabot
cannot do. Check out its branch, regenerate the file and push the result to
the same branch (Dependabot then stops rebasing it; merge `dev` into it if
`dev` moves):

```
git submodule update --init
go run ./internal/tools/thirdparty
git commit -m "THIRD_PARTY_LICENSES for the module updates" THIRD_PARTY_LICENSES
```

Read what changed in each module before you merge (the release notes, or the
commits between the two versions), and check that `govulncheck ./...` is clean.

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

Start with `den doctor`: it checks your setup and prints one fix per
problem, and the README's [Troubleshooting](README.md#troubleshooting) covers
the common cases. If that does not help, open an issue with what you did,
what you expected and what happened; for a question, use the Question form.
It helps to include:

- the output of `den doctor`, which is written to name no user, host,
  project, session or agent; read it before you paste (releases up to v0.6.1
  do not have it yet);
- the output of `den version`, and `claude --version`;
- your compositor and terminal (Den's focus and spawn features depend on
  Hyprland and the terminal launcher);
- `den list` and `den explain <name>` for the agent in question;
- the relevant lines of `~/.local/state/den/hooks.log` if the inbox is
  involved.

Remove anything private from pasted output (paths, prompts, transcripts).
Security problems: please do not file a public issue. Report vulnerabilities
through GitHub's private vulnerability reporting (the repository's Security
tab, "Report a vulnerability"); [SECURITY.md](SECURITY.md) says what counts and
how quickly we answer.

## Branches and pull requests

Work happens on short-lived branches. `dev` is where changes land; `main` holds
what has been released, and every release is a tag on `main`.

```
feature/<topic>  ──PR, squash──▶  dev  ──release/vX.Y.Z, PR, merge commit──▶  main  ──tag vX.Y.Z──▶  release
fix/<topic>      ──PR, squash──▶  dev
hotfix/<topic>   ──PR, merge commit──▶  main, then the same fix into dev by cherry-pick
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

Only maintainers merge. A pull request from a `release/…` branch (cut from
`dev`) into `main` is a release and follows
[docs/RELEASING.md](docs/RELEASING.md); a hotfix is the one other way into
`main`. Nobody pushes to `dev` or `main` directly, force-pushes them or deletes
them, and only maintainers create `v*` tags. The one exception is the last
step of a release, where a maintainer fast-forwards `dev` to `main`; a branch
made from the old `dev` needs no rebase for it.

Agents work the same way: an agent's worktree branch goes into `dev` through
a pull request, never straight into `main`.

## License

Den is released under the MIT license (see [LICENSE](LICENSE)). By submitting a
contribution you agree that it is licensed under the same terms. Third-party
code keeps its own notice: the agent-badge mod in `mods/agent-badge` is MIT,
and the name generator in `internal/badge` is ported from it. Keep those
attributions intact.
