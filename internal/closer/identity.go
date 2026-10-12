package closer

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
)

// A pid in a sessions file is only a hint: Claude Code may have crashed
// and left the file, and the pid may since belong to anything else of this
// user's (the tmux server holding every agent, a database). Before it
// signals, System checks the process is Claude Code and no younger than the
// session, and holds it by a pidfd, so every signal goes to that process
// and never to whatever takes its pid next. When it cannot be sure, it
// refuses rather than guess.

// Verifier is implemented by Ops that can hold the process of a target
// after checking it is that agent's Claude Code. Run uses it when Ops have
// it; System does.
type Verifier interface {
	Hold(t Target) (Held, error)
}

// Held is a process Den checked and holds: signals go to it alone.
type Held interface {
	Signal(sig syscall.Signal) error
	Alive() bool
	Release()
}

// ErrGone is a target whose process had already ended.
var ErrGone = errors.New("its process has already ended")

// ErrNotKnown is a close whose wait was cut short (the link dropped, Den
// stopped): it was asked to stop, and whether it did is not known. It was
// not killed.
var ErrNotKnown = errors.New("the wait was cut short; it was asked to stop, and whether it did is not known")

// startSlack is how much later than the session's recorded start its
// process may have started: Claude Code writes startedAt after it starts,
// and the boot time behind a process's start is kept in whole seconds.
const startSlack = 10 * time.Second

// isClaudeProc reports whether a process looks like Claude Code: the
// native binary (comm or executable "claude"), or Node or Bun running its
// npm package (cli.js under @anthropic-ai/claude-code).
func isClaudeProc(comm, exe string, argv []string) bool {
	if comm == "claude" || filepath.Base(exe) == "claude" {
		return true
	}
	if len(argv) == 0 {
		return false
	}
	switch strings.TrimSuffix(filepath.Base(argv[0]), ".exe") {
	case "node", "nodejs", "bun":
		// The script it runs: its first argument that is not a flag.
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			return strings.Contains(a, "/claude-code/") || filepath.Base(a) == "claude"
		}
	}
	return false
}

// sessionStart is when the session with id started, from this machine's
// sessions files; zero when not found.
func sessionStart(id string) time.Time {
	if id == "" {
		return time.Time{}
	}
	sessions, _ := discover.Scan()
	for _, s := range sessions {
		if s.SessionID == id && s.StartedAt > 0 {
			return s.Started()
		}
	}
	return time.Time{}
}
