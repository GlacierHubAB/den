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

CI runs `make test` on every push and pull request. Please run it before you
open one.

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

## Pull requests

- One topic per pull request, branched from `main`, with a description that
  says what and why.
- `make test` passes.
- Be ready to split or rework a large change; small, reviewable pull requests
  land faster.
- Do not include generated binaries (`den`, `dist/`) or personal paths.

## License

Den is released under the MIT license (see [LICENSE](LICENSE)). By submitting a
contribution you agree that it is licensed under the same terms. Third-party
code keeps its own notice: the agent-badge mod in `mods/agent-badge` is MIT,
and the name generator in `internal/badge` is ported from it. Keep those
attributions intact.
