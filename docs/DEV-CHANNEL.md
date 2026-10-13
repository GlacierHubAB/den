# The dev channel

Releases come from `main`. Everything merged since the last release is on
`dev`, and `scripts/dev-update.sh` builds that and installs it as your `den`.
Use it to try changes before they are released. `dev` passes CI, but it has
not been through a release's checks.

The first time, clone `dev` into the build checkout the script uses, then run
the script from there:

```
git clone -b dev https://github.com/GlacierHubAB/den.git ~/.local/share/den-dev/src && ~/.local/share/den-dev/src/scripts/dev-update.sh
```

After that, `~/.local/share/den-dev/src/scripts/dev-update.sh` updates to the
latest `dev`. Running it from another checkout of this repository works too.
You need `git`, `make` and Go. The script builds with the Go version that
`go.mod` pins, and `go` downloads that version if you have another.

## What it does

1. Refuses to run while Den is running (the TUI, `den events`, `den remote
   serve`). The `den hook` and `den mcp` processes that Claude Code starts
   don't count: a hook starts the new binary on its next event, and a
   session's `den mcp` when the session restarts. Quit Den first, or pass
   `--force`. A running Den keeps the binary it started with until you
   restart it.
2. Clones `dev` from GitHub into `~/.local/share/den-dev/src`, or fetches it
   there on later runs, and initialises the submodules the build embeds. This
   is a build checkout of its own, not your working copy. It refuses to run if
   that checkout has local changes or a different origin.
3. Stops with "already up to date" if `~/.local/bin/den` already is that
   commit.
4. Runs `go vet` and the fast tests (`go test -short`) as a smoke check, with
   a scratch home and scratch Den state, then runs `make build`. If anything
   fails, nothing is installed and your `den` is left as it was.
5. Keeps the binary it replaces as `~/.local/bin/den.previous`, then installs
   the new one in a single step: it copies the binary to a temporary file in
   `~/.local/bin` and renames that over `den`.
6. Prints the old and new versions.

It never touches `~/.claude`, Claude Code's hooks or Den's state and config.
Running it again on the same commit changes nothing.

A dev build calls itself `den v0.4.0-12-dev+<sha>`: 12 commits after
v0.4.0, at commit `<sha>` of `dev`. Den compares versions (for instance, `den
remote install` replaces only an older den on a host). With the commit count
in the version, a dev build sorts after the release it comes from and after
earlier dev builds.

## Going back

```
~/.local/share/den-dev/src/scripts/dev-update.sh --rollback
```

puts `den.previous` back in place, in the same single step. Restart Den
afterwards. To return to a release, install it as the README describes;
that replaces a dev build like any other upgrade.

## Why not `go run`, or a symlink

Claude Code's hooks call the installed binary by its absolute path,
`~/.local/bin/den hook`, on every event in every session. `go run` compiles
each time it runs. A hook would then wait for a compile on every event. Hooks
that fire at the same moment would compile against one another. And if a
commit on `dev` failed to build, every hook in every session would fail until
someone fixed it.

A symlink from `~/.local/bin/den` into the build checkout has the same
weakness: the next build changes the binary under the hooks, half-written or
broken. It is not recommended.

So the script builds first, checks the build, and only then swaps the binary,
in a single step that keeps the previous one. A hook that starts at any
moment runs either the old binary or the new one, and a bad build never
reaches the hooks.

## Settings

The script reads these environment variables. You rarely need them.

| Variable | Default |
| --- | --- |
| `DEN_DEV_SRC` | `${XDG_DATA_HOME:-~/.local/share}/den-dev/src` |
| `DEN_DEV_BIN` | `~/.local/bin/den` |
| `DEN_DEV_REPO` | `https://github.com/GlacierHubAB/den.git` |
| `DEN_DEV_BRANCH` | `dev` |
