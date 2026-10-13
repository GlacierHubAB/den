#!/usr/bin/env bash
# dev-update.sh: build Den from the dev branch on GitHub and install it in
# place of ~/.local/bin/den, keeping the binary it replaces for a rollback.
# See docs/DEV-CHANNEL.md.
#
#   dev-update.sh              update to the latest dev
#   dev-update.sh --rollback   put the previous binary back
#   dev-update.sh --force      update even while Den is running
#
# It reads and writes only its own source checkout and the binary (plus the
# .previous copy next to it). It never touches ~/.claude, Claude Code's
# hooks or Den's state and config.
set -euo pipefail

# The whole script is one function, read before any of it runs: a run from
# the build checkout updates this file, and bash must not read on into the
# new version.
main() {
REPO="${DEN_DEV_REPO:-https://github.com/GlacierHubAB/den.git}"
BRANCH="${DEN_DEV_BRANCH:-dev}"
SRC="${DEN_DEV_SRC:-${XDG_DATA_HOME:-$HOME/.local/share}/den-dev/src}"
BIN="${DEN_DEV_BIN:-$HOME/.local/bin/den}"
PREV="$BIN.previous"
# The process name to look for; tests change it.
NAME="${DEN_DEV_PGREP_NAME:-den}"

force=0
rollback=0
for arg in "$@"; do
  case "$arg" in
    --force) force=1 ;;
    --rollback) rollback=1 ;;
    -h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "dev-update: unknown argument $arg (try --help)" >&2; exit 2 ;;
  esac
done

say() { printf 'dev-update: %s\n' "$*"; }
die() { printf 'dev-update: %s\n' "$*" >&2; exit 1; }
version_of() { [ -x "$1" ] && "$1" version 2>/dev/null || echo "none"; }

# Den's TUI (or den events, den remote serve) runs the binary it started
# with. den hook and den mcp, which Claude Code starts, are fine: they pick
# up the new binary on their next start.
running="$(pgrep -u "$(id -u)" -a -x "$NAME" 2>/dev/null | awk '$3 != "mcp" && $3 != "hook"' || true)"
if [ -n "$running" ] && [ "$force" -eq 0 ]; then
  echo "dev-update: refusing: Den is running:" >&2
  echo "$running" >&2
  die "quit Den first, or pass --force (Den then keeps the old binary until you restart it)"
fi

# Scratch files and directories, removed on exit.
scratch=()
# shellcheck disable=SC2329 # run by the trap below
cleanup() { [ "${#scratch[@]}" -eq 0 ] || rm -rf "${scratch[@]}"; }
trap cleanup EXIT

# place copies src to a temporary file in dst's directory and renames it over
# dst: a hook that starts at any moment runs either the old binary or the new
# one, never half of either.
place() {
  local src="$1" dst="$2" tmp
  mkdir -p "$(dirname "$dst")"
  tmp="$(mktemp "$(dirname "$dst")/.den-new.XXXXXX")"
  scratch+=("$tmp")
  cp "$src" "$tmp"
  chmod 755 "$tmp"
  mv -f "$tmp" "$dst"
}

if [ "$rollback" -eq 1 ]; then
  [ -x "$PREV" ] || die "no previous binary at $PREV"
  old="$(version_of "$BIN")"
  place "$PREV" "$BIN"
  say "rolled back: $old -> $(version_of "$BIN")"
  say "restart Den to use it"
  exit 0
fi

command -v git >/dev/null || die "git is not installed"
command -v go >/dev/null || die "go is not installed"

# The source checkout: the public repository's dev branch, never a working
# copy of anyone's. A checkout with local changes or another origin is
# refused, not reset.
if [ -d "$SRC/.git" ]; then
  origin="$(git -C "$SRC" remote get-url origin)"
  [ "$origin" = "$REPO" ] || die "$SRC has origin $origin, not $REPO; remove it or set DEN_DEV_SRC"
  [ -z "$(git -C "$SRC" status --porcelain --untracked-files=no)" ] || die "$SRC has local changes; it is a build checkout, leave it unedited"
  git -C "$SRC" fetch --quiet --tags origin "$BRANCH"
  git -C "$SRC" checkout --quiet --detach FETCH_HEAD
else
  mkdir -p "$(dirname "$SRC")"
  git clone --quiet --branch "$BRANCH" "$REPO" "$SRC"
fi
git -C "$SRC" submodule update --init --recursive --quiet

# v0.4.0-12-dev+<sha>: 12 commits after the last release on dev. Den compares
# versions (den remote install replaces only an older den), and it reads the
# count, so a dev build sorts after the release and after earlier dev builds.
sha="$(git -C "$SRC" rev-parse --short=12 HEAD)"
base="$(git -C "$SRC" describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || echo v0.0.0)"
if [ "$base" = v0.0.0 ]; then
  count="$(git -C "$SRC" rev-list --count HEAD)"
else
  count="$(git -C "$SRC" rev-list --count "$base..HEAD")"
fi
version="$base-$count-dev+$sha"
old="$(version_of "$BIN")"
if [ "$old" = "den $version" ]; then
  say "already up to date: $old"
  exit 0
fi

# The Go the repository pins (go.mod's toolchain line); go fetches it if
# this machine has another.
toolchain="$(sed -n 's/^toolchain //p' "$SRC/go.mod")"
export GOTOOLCHAIN="${toolchain:-auto}"

say "building $version with ${toolchain:-the installed go}"
log="$(mktemp "${TMPDIR:-/tmp}/den-dev-update.XXXXXX")"
scratch+=("$log")
check() {
  local what="$1"; shift
  if ! "$@" > "$log" 2>&1; then
    tail -n 40 "$log" >&2
    die "$what failed on dev at $sha; nothing was installed"
  fi
}
cd "$SRC"
check "go vet" go vet ./...
# The fast tests run as CI runs them: a scratch home, scratch Den state and
# no git config of yours, so they cannot read or write ~/.claude or your
# state. The build and module caches stay where they are.
home="$(mktemp -d)"
scratch+=("$home")
check "go test -short" env GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)" \
  HOME="$home" XDG_STATE_HOME="$home/state" XDG_CONFIG_HOME="$home/config" XDG_DATA_HOME="$home/data" \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 \
  go test -short -count=1 -timeout 15m ./...
check "make build" make build VERSION="$version"
new="$("$SRC/den" version)"
[ "$new" = "den $version" ] || die "the build reports '$new', not 'den $version'; nothing was installed"

[ -x "$BIN" ] && place "$BIN" "$PREV"
place "$SRC/den" "$BIN"
say "installed $BIN: $old -> $(version_of "$BIN")"
[ -x "$PREV" ] && say "previous binary kept at $PREV ($0 --rollback)"
say "restart Den to use it"
}

main "$@"; exit
