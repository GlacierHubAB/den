package remote

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// The names and values of a remote spawn, checked the same way by Den
// before it sends and by serve when the spawn arrives. Every one of them
// ends up in an argument list or a tmux target, so each has a narrow shape
// and nothing in it needs quoting.

var (
	// peerRe is a remote agent's peer name: what PeerName makes, cut to
	// 32 characters of [a-z0-9-].
	peerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// tmuxRe is a tmux session name Den makes: den-<peer>-<8 hex of the
	// session id>. No '.' or ':', which tmux reads as window and pane.
	tmuxRe = regexp.MustCompile(`^den-[a-z0-9][a-z0-9-]{0,31}-[0-9a-f]{8}$`)
	// uuidRe is a session id as Den makes them: lower-case hex UUID.
	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// worktreeRe is a worktree name as planWorktree makes them.
	worktreeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// fullModelRe is the exact model id a resumed transcript names.
	fullModelRe = regexp.MustCompile(`^claude-[a-z0-9][a-z0-9.-]{0,63}$`)
	unsafePeer  = regexp.MustCompile(`[^a-z0-9]+`)
)

// spawnModels are the models a new remote agent may be started on.
var spawnModels = []string{"haiku", "sonnet", "opus"}

// NewSessionID is a fresh random UUID (version 4) in lower-case hex.
func NewSessionID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// TmuxName is the tmux session Den starts a remote agent in.
func TmuxName(peer, sessionID string) string {
	return "den-" + peer + "-" + sessionID[:8]
}

// CheckTmuxName says whether name is one Den could have made for the
// session id (any session id when sid is "").
func CheckTmuxName(name, sid string) error {
	if !tmuxRe.MatchString(name) {
		return fmt.Errorf("%q is not a tmux session name Den makes", sanitize.Label(name, 80))
	}
	if sid != "" && !strings.HasSuffix(name, "-"+sid[:min(8, len(sid))]) {
		return errors.New("the tmux session name does not belong to that session id")
	}
	return nil
}

// CheckSessionID refuses anything but a lower-case UUID.
func CheckSessionID(sid string) error {
	if !uuidRe.MatchString(sid) {
		return errors.New("the session id is not a UUID")
	}
	return nil
}

// RemotePeer turns a wanted name, or the folder's base name when none, into
// a peer name of [a-z0-9-] no agent in taken uses: the name, else the name
// with -2, -3 …, cut so it stays within 32 characters.
func RemotePeer(want, dir string, taken map[string]bool) string {
	base := strings.Trim(unsafePeer.ReplaceAllString(strings.ToLower(want), "-"), "-")
	if base == "" {
		d := strings.TrimRight(dir, "/")
		if i := strings.LastIndexByte(d, '/'); i >= 0 {
			d = d[i+1:]
		}
		base = strings.Trim(unsafePeer.ReplaceAllString(strings.ToLower(d), "-"), "-")
	}
	if base == "" || base == "~" {
		base = "agent"
	}
	cut := func(s string, n int) string {
		if len(s) > n {
			s = strings.TrimRight(s[:n], "-")
		}
		return s
	}
	base = cut(base, 32)
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		if n := cut(base, 32-len(suffix)) + suffix; !taken[n] {
			return n
		}
	}
}

// CheckPeer refuses a peer name outside [a-z0-9-] or over 32 characters.
func CheckPeer(peer string) error {
	if !peerRe.MatchString(peer) {
		return fmt.Errorf("peer name %q must be 1 to 32 of a-z, 0-9 and '-'", sanitize.Label(peer, 40))
	}
	return nil
}

// CheckSpawnModel accepts haiku, sonnet, opus or "" (the host's default),
// and, when resuming, the exact claude-* id the transcript names.
func CheckSpawnModel(model string, resume bool) error {
	if model == "" {
		return nil
	}
	for _, m := range spawnModels {
		if m == model {
			return nil
		}
	}
	if resume && fullModelRe.MatchString(model) {
		return nil
	}
	return fmt.Errorf("unknown model %q: pick one of %s", sanitize.Label(model, 40), strings.Join(spawnModels, ", "))
}

// CheckWorktreeName accepts "" (none) or a name of [a-z0-9-].
func CheckWorktreeName(name string) error {
	if name != "" && !worktreeRe.MatchString(name) {
		return fmt.Errorf("worktree name %q must be a-z, 0-9 and '-'", sanitize.Label(name, 40))
	}
	return nil
}

// CheckBrief refuses an empty brief, one over MaxSendText, and one that
// sanitising would change (escapes, control characters): it is refused
// rather than altered, so the agent gets exactly what the human saw.
func CheckBrief(brief string) error {
	switch {
	case strings.TrimSpace(brief) == "":
		return errors.New("an agent needs a brief")
	case len(brief) > MaxSendText:
		return fmt.Errorf("the brief is %d KB, over the %d KB limit for a remote agent", (len(brief)+1023)>>10, MaxSendText>>10)
	case sanitize.Text(brief) != brief:
		return errors.New("the brief holds control characters or terminal escapes; remove them and try again")
	}
	return nil
}

// CheckSpawnDir accepts a folder as remotes.toml and a spawn name it: an
// absolute path or one under ~/, with no '..' and no control characters.
// It does not say the folder exists; only the host can.
func CheckSpawnDir(d string) error {
	if d != "~" && !strings.HasPrefix(d, "/") && !strings.HasPrefix(d, "~/") {
		return fmt.Errorf("folder %q must be absolute or start with ~/", sanitize.Label(d, 80))
	}
	if len(d) > 1024 {
		return errors.New("the folder name is over 1024 bytes")
	}
	for _, r := range d {
		if r < ' ' || r == 0x7f {
			return errors.New("the folder name holds a control character")
		}
	}
	if sanitize.Line(d) != d {
		return errors.New("the folder name holds characters Den cannot show as they are")
	}
	for _, part := range strings.Split(d, "/") {
		if part == ".." {
			return fmt.Errorf("folder %q holds '..'", sanitize.Label(d, 80))
		}
	}
	return nil
}

// UnderSpawnDirs reports whether dir is one of roots or inside one, by
// name alone: Den's check before it asks the host, which then checks the
// real path. Both must be written the same way (~/ or absolute).
func UnderSpawnDirs(dir string, roots []string) bool {
	if CheckSpawnDir(dir) != nil {
		return false
	}
	clean := func(p string) string {
		if p == "~" {
			return "~"
		}
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			return "~/" + strings.TrimPrefix(filepath.Clean("/"+rest), "/")
		}
		return filepath.Clean(p)
	}
	d := clean(dir)
	for _, r := range roots {
		if CheckSpawnDir(r) != nil {
			continue
		}
		r = clean(r)
		if d == r || strings.HasPrefix(d, strings.TrimSuffix(r, "/")+"/") || r == "~" && strings.HasPrefix(d, "~/") {
			return true
		}
	}
	return false
}

// ProtectedNote is what Den says when the orchestrator, or a lead it
// started, asks to act on a host whose orchestrator key is false.
func ProtectedNote(host string) string {
	return fmt.Sprintf("%s is a protected host (orchestrator = false in remotes.toml); only the human can act there", host)
}
