package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/gitsafe"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// serve's half of remote spawn (docs/REMOTE.md). A spawn starts Claude Code
// in a detached session of a tmux server of Den's own (`tmux -L den`), by
// argument list and never through a shell:
//
//	tmux -L den new-session -d -s <name> -c <folder> -x 200 -y 50
//	     -e DEN_PEER=<peer> -e DEN_SPAWN=<id> -e DEN_SPAWN_DIR=<staging>
//	     -- <this den> remote agent <id>
//
// `den remote agent` then reads the staged spec and execs Claude Code by its
// absolute path. The brief is never in an argument list, the environment or
// a log: serve writes it to brief.md (0600) in the private staging folder,
// and Claude Code is told, as its first prompt, to read that file. serve
// removes the file once the agent's first turn is over, its session ended,
// or after an hour.

// MinClaudeSpawn is the oldest Claude Code a host may start agents with:
// the first known to take --session-id with --name and --worktree.
var MinClaudeSpawn = "2.1.289"

// minTmux is the oldest tmux whose new-session takes -e and runs a command
// of several arguments without a shell.
const minTmux = "3.0"

// spawnFirstPrompt is the agent's first prompt: a path Den made, no user text.
func spawnFirstPrompt(brief string) string {
	return "Your brief from Den is in " + brief + ". Read it and follow it."
}

// briefTTL is how long a brief may wait on the host for its agent.
const briefTTL = time.Hour

// tmuxServerRe is the name of Den's tmux server.
var tmuxServerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// TmuxServer is Den's own tmux server on a host: `den`, or DEN_TMUX_SERVER
// (tests use a private name so they never touch a real one).
func TmuxServer() string {
	if s := os.Getenv("DEN_TMUX_SERVER"); tmuxServerRe.MatchString(s) {
		return s
	}
	return "den"
}

// OwnerSpawn is what the host's own config.toml says of spawn here.
type OwnerSpawn struct {
	Spawn bool
	Dirs  []string // nil: no limit of the owner's own
}

// hostSpawn starts, tracks and cleans up the agents a Den asks serve for.
type hostSpawn struct {
	policy     *SpawnPolicy // Den's remotes.toml for this host; nil allows none
	controller string
	full       bool
	owner      OwnerSpawn
	server     string
	den        string
	wait       time.Duration
	sessions   func() ([]discover.Session, error)
	stateDir   string
	log        func(string)

	busy sync.Mutex // one spawn at a time on a host

	capOnce sync.Once
	cap     SpawnCap
	tmuxBin string
	claude  string
}

func newHostSpawn(req Request, full bool, o ServeOptions) *hostSpawn {
	hs := &hostSpawn{policy: req.Spawn, controller: req.Controller, full: full, server: o.TmuxServer,
		den: o.Executable, wait: o.SpawnWait, sessions: o.Sessions, stateDir: filepath.Dir(o.EventsPath), log: o.Log}
	switch {
	case o.Owner != nil:
		hs.owner = *o.Owner
	case req.Spawn != nil:
		// Read only when Den asks for spawn at all.
		hs.owner = *ownerSpawn()
	}
	return hs
}

// active says whether Den asked for spawn at all; only then does serve
// probe tmux and claude, and say anything of spawn on the wire.
func (hs *hostSpawn) active() bool { return hs != nil && hs.policy != nil }

// capability probes tmux and claude once.
func (hs *hostSpawn) capability() SpawnCap {
	hs.capOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := SpawnCap{Window: hs.den}
		if !hs.owner.Spawn {
			c.Veto = "this host's own config refuses spawn (remote.spawn = false)"
		}
		c.Limited = hs.owner.Dirs != nil
		hs.tmuxBin = findBin("tmux")
		if hs.tmuxBin == "" {
			c.NoTmux = "tmux is not installed"
		} else if v, err := tmuxVersion(ctx, hs.tmuxBin); err != nil {
			c.NoTmux = err.Error()
		} else {
			c.Tmux = v
		}
		hs.claude = findBin("claude")
		if hs.claude != "" {
			c.Claude = claudeBinVersion(ctx, hs.claude)
		}
		hs.cap = c
	})
	c := hs.cap
	c.Running = 0
	for _, s := range hs.spawned(context.Background()) {
		if s.running {
			c.Running++
		}
	}
	return c
}

// findBin is name's absolute path in PATH, then ~/.local/bin, /usr/local/bin
// and /usr/bin, which a PATH of a non-login ssh command often lacks.
func findBin(name string) string {
	home, _ := os.UserHomeDir()
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, filepath.Join(home, ".local", "bin"), "/usr/local/bin", "/usr/bin")
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// tmuxVersion is tmux -V's version when it is minTmux or newer.
func tmuxVersion(ctx context.Context, bin string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "-V").Output()
	if err != nil {
		return "", errors.New("tmux -V failed")
	}
	f := strings.Fields(string(out)) // "tmux 3.4", "tmux next-3.6", "tmux 3.3a"
	if len(f) < 2 {
		return "", errors.New("tmux gave no version")
	}
	v := strings.TrimPrefix(f[1], "next-")
	num := strings.TrimRightFunc(v, func(r rune) bool { return r >= 'a' && r <= 'z' })
	v = sanitize.Label(v, 20)
	if _, ok := versionParts(num); !ok {
		return "", fmt.Errorf("tmux version %q is not known", v)
	}
	if versionLess(num, minTmux) {
		return "", fmt.Errorf("tmux %s is older than %s", v, minTmux)
	}
	return v, nil
}

// claudeBinVersion is `claude --version`'s version, "" when it says none.
func claudeBinVersion(ctx context.Context, bin string) string {
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = spawn.Environ(os.Environ(), nil)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return ""
	}
	return sanitize.Label(f[0], 40)
}

// tmuxEnv is the environment tmux runs with: the server Den starts keeps
// it, and a new session takes the client's update-environment variables,
// so only what an agent needs goes in, never ssh's or a desktop's.
func tmuxEnv() []string {
	home, _ := os.UserHomeDir()
	keep := func(k string) bool {
		switch k {
		case "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TZ", "TMPDIR", "TMUX_TMPDIR", "CLAUDE_CONFIG_DIR",
			"DEN_CONFIG", "DEN_TMUX_SERVER", "DEN_SSHTEST", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR", "NO_PROXY", "no_proxy":
			return true
		}
		for _, p := range []string{"LC_", "XDG_", "ANTHROPIC_", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
	var env []string
	path := os.Getenv("PATH")
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if keep(k) {
			env = append(env, kv)
		}
	}
	if local := filepath.Join(home, ".local", "bin"); !strings.Contains(":"+path+":", ":"+local+":") {
		path += ":" + local
	}
	return append(env, "PATH="+path, "TERM=xterm-256color")
}

func (hs *hostSpawn) tmux(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, hs.tmuxBin, append([]string{"-L", hs.server}, args...)...)
	cmd.Env = tmuxEnv()
	home, _ := os.UserHomeDir()
	cmd.Dir = home
	return cmd
}

// spawnedSession is one tmux session of Den's server that Den started.
type spawnedSession struct {
	Spawned
	running bool // its pane's process still runs
}

// spawned lists the sessions on Den's tmux server that carry a valid
// @den_spawn tag matching their name. Untagged sessions are never listed.
func (hs *hostSpawn) spawned(ctx context.Context) []spawnedSession {
	if hs.tmuxBin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := hs.tmux(ctx, "list-panes", "-a", "-F", "#{session_name}\t#{@den_spawn}\t#{pane_dead}").Output()
	if err != nil {
		return nil // no server yet
	}
	live := map[string]bool{}
	if ss, err := hs.sessions(); err == nil {
		for _, s := range ss {
			if s.Alive {
				live[s.SessionID] = true
			}
		}
	}
	seen := map[string]int{}
	var res []spawnedSession
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || CheckSessionID(f[1]) != nil || CheckTmuxName(f[0], f[1]) != nil {
			continue
		}
		if i, ok := seen[f[0]]; ok {
			res[i].running = res[i].running || f[2] == "0"
			continue
		}
		seen[f[0]] = len(res)
		res = append(res, spawnedSession{Spawned: Spawned{SessionID: f[1], TmuxName: f[0], Live: live[f[1]]}, running: f[2] == "0"})
	}
	return res
}

// list is the spawned sessions as a snapshot carries them.
func (hs *hostSpawn) list() []Spawned {
	var out []Spawned
	for _, s := range hs.spawned(context.Background()) {
		out = append(out, s.Spawned)
	}
	return out
}

// spawnPlan is a spawn checked and resolved.
type spawnPlan struct {
	cmd   SpawnCmd
	dir   string // the real folder
	route string
	warn  string
	untr  bool
}

// precheck refuses, without touching the host, what the message alone
// shows to be wrong. Both sides run the same checks.
func (hs *hostSpawn) precheck(c SpawnCmd, allowed bool) (code, why string) {
	switch {
	case !allowed:
		return "", "this host's policy is observe; it takes no actions"
	case !hs.active():
		return "", "Den's remotes.toml allows no spawn on this host"
	case !hs.owner.Spawn:
		return "", "this host's own config refuses spawn (remote.spawn = false)"
	case c.Route != string(SpawnTmux):
		return "", fmt.Sprintf("route %q is not one this den can start", sanitize.Label(c.Route, 20))
	case hs.policy.Route != SpawnTmux && hs.policy.Route != SpawnAuto:
		return "", "Den's remotes.toml does not allow the tmux route on this host"
	case len(c.Skills) > 0 && string(c.Skills) != "null" && string(c.Skills) != "[]":
		return "", "role skills are not shipped to hosts yet"
	case c.Resume || c.Fork:
		return "", "resuming a remote agent is not supported by this den"
	}
	for _, err := range []error{CheckSessionID(c.SessionID), CheckPeer(c.Peer), CheckTmuxName(c.TmuxName, c.SessionID),
		CheckSpawnModel(c.Model, c.Resume), CheckWorktreeName(c.Worktree), CheckSpawnDir(c.Dir)} {
		if err != nil {
			return "", err.Error()
		}
	}
	if c.TmuxName != TmuxName(c.Peer, c.SessionID) {
		return "", "the tmux session name does not match the peer name"
	}
	if !c.Check || c.Brief != "" {
		if err := CheckBrief(c.Brief); err != nil {
			return "", err.Error()
		}
	}
	return "", ""
}

// resolve checks the spawn against the host: the folder, the binaries,
// the worktree, the agents already running. It starts nothing.
func (hs *hostSpawn) resolve(ctx context.Context, c SpawnCmd) (spawnPlan, string, string) {
	p := spawnPlan{cmd: c, route: string(SpawnTmux)}
	dir, err := resolveSpawnDir(c.Dir, hs.policy.Dirs, hs.owner.Dirs)
	if err != nil {
		return p, "", err.Error()
	}
	p.dir = dir
	cap := hs.capability()
	switch {
	case cap.Veto != "":
		return p, "", cap.Veto
	case hs.claude == "":
		return p, "", "claude is not installed on this host"
	case cap.Claude == "" || versionLess(cap.Claude, MinClaudeSpawn):
		v := cap.Claude
		if v == "" {
			v = "of an unknown version"
		}
		return p, "", fmt.Sprintf("Claude Code on this host is %s; remote spawn needs %s or newer", v, MinClaudeSpawn)
	case cap.Tmux == "":
		return p, "no_tmux", "no usable tmux on this host: " + cap.NoTmux
	case hs.den == "" || !filepath.IsAbs(hs.den):
		return p, "", "serve cannot tell where its own den binary is"
	}
	if c.Worktree != "" {
		if why := worktreeFree(ctx, dir, c.Worktree); why != "" {
			return p, "", why
		}
	}
	var running []string
	for _, s := range hs.spawned(ctx) {
		if s.TmuxName == c.TmuxName {
			return p, "duplicate", "a tmux session with that name exists"
		}
		if s.running {
			running = append(running, s.TmuxName)
		}
	}
	if max := hs.policy.Max; max > 0 && len(running) >= max {
		return p, "", fmt.Sprintf("max_agents reached: %d agents Den started run here (%s)", len(running), strings.Join(running, ", "))
	}
	if ss, err := hs.sessions(); err == nil {
		for _, s := range ss {
			if s.SessionID == c.SessionID {
				return p, "duplicate", "a session with that id exists"
			}
		}
	}
	if !spawn.Trusted(dir) {
		p.untr = true
		p.warn = "Claude Code has not been told to trust this folder here; it asks in the agent's window first"
	}
	return p, "", ""
}

// resolveSpawnDir expands ~/ to this host's home, resolves symlinks, and
// accepts a folder of this user's that lies, by its real path, under an
// entry of den's list and, when the owner set one, of the owner's too.
func resolveSpawnDir(d string, den, owner []string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("this host's home folder is unknown")
	}
	expand := func(p string) string {
		if p == "~" {
			return home
		}
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			return filepath.Join(home, rest)
		}
		return p
	}
	if CheckSpawnDir(d) != nil {
		return "", errors.New("the folder is not absolute or ~/, or holds '..' or control characters")
	}
	real, err := filepath.EvalSymlinks(expand(d))
	if err != nil {
		return "", errors.New("no such folder on this host")
	}
	st, err := os.Stat(real)
	switch {
	case err != nil || !st.IsDir():
		return "", errors.New("not a folder on this host")
	case st.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()):
		return "", errors.New("the folder belongs to another user")
	}
	under := func(roots []string) bool {
		for _, r := range roots {
			if CheckSpawnDir(r) != nil {
				continue
			}
			rr, err := filepath.EvalSymlinks(expand(r))
			if err != nil {
				continue
			}
			if rel, err := filepath.Rel(rr, real); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return true
			}
		}
		return false
	}
	if !under(den) {
		return "", errors.New("the folder is not under the host's spawn_dirs, by its real path")
	}
	if owner != nil && !under(owner) {
		return "", errors.New("the folder is outside what this host's own config allows (remote.spawn_dirs)")
	}
	return real, nil
}

// worktreeFree checks, read-only, that dir is in a git repository with a
// commit and that the worktree name is not taken there.
func worktreeFree(ctx context.Context, dir, name string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := gitsafe.Command(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "worktree: the folder is not inside a git repository"
	}
	root := strings.TrimSpace(string(out))
	if filepath.Base(root) == ".git" {
		root = filepath.Dir(root)
	}
	if gitsafe.Command(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}").Run() != nil {
		return "worktree: the repository has no commit yet to branch from"
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", "worktrees", name)); err == nil {
		return "worktree: " + name + " exists in that repository"
	}
	return ""
}

// spawnBase is the private folder spawns are staged in: den/spawn under
// $XDG_RUNTIME_DIR, else spawn in the private fallback folder. Each level
// is made 0700 and must be this user's own.
func spawnBase() (string, error) {
	dir := bridge.FallbackDir()
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		dir = filepath.Join(d, "den")
	}
	if err := bridge.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	base := filepath.Join(dir, "spawn")
	return base, bridge.EnsurePrivateDir(base)
}

// agentSpec is spec.json: what `den remote agent` starts. No brief.
type agentSpec struct {
	SessionID string    `json:"session_id"`
	Peer      string    `json:"peer"`
	Dir       string    `json:"dir"`
	Model     string    `json:"model,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	Claude    string    `json:"claude"`
	Created   time.Time `json:"created"`
}

// writePrivate creates path 0600 with data, refusing one that exists or a
// symlink in its place.
func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// start stages the spawn and starts its tmux session, then waits for the
// session file. A failure says what failed, never where.
func (hs *hostSpawn) start(ctx context.Context, p spawnPlan) Result {
	c := p.cmd
	base, err := spawnBase()
	if err != nil {
		return Result{Error: "no private folder to stage the spawn in"}
	}
	stage := filepath.Join(base, c.SessionID)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return Result{Error: "the spawn is staged already", Code: "duplicate"}
	}
	undo := func() { os.RemoveAll(stage) }
	brief := filepath.Join(stage, "brief.md")
	spec, _ := json.Marshal(agentSpec{SessionID: c.SessionID, Peer: c.Peer, Dir: p.dir, Model: c.Model, Worktree: c.Worktree, Claude: hs.claude, Created: time.Now().UTC()})
	if writePrivate(brief, []byte(c.Brief)) != nil || writePrivate(filepath.Join(stage, "spec.json"), spec) != nil {
		undo()
		return Result{Error: "could not stage the spawn"}
	}
	e := spawnEntry{SessionID: c.SessionID, TmuxName: c.TmuxName, Peer: c.Peer, Controller: hs.controller, Created: time.Now().UTC()}
	if err := hs.ledgerAdd(e); err != nil {
		undo()
		return Result{Error: "could not record the spawn"}
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := hs.tmux(tctx, "new-session", "-d", "-s", c.TmuxName, "-c", p.dir, "-x", "200", "-y", "50",
		"-e", "DEN_PEER="+c.Peer, "-e", "DEN_SPAWN="+c.SessionID, "-e", "DEN_SPAWN_DIR="+stage,
		"--", hs.den, "remote", "agent", c.SessionID)
	cmd.Stderr = &limitWriter{w: &stderr, n: 4 << 10}
	if err := cmd.Run(); err != nil {
		undo()
		hs.ledgerDrop(c.SessionID)
		if strings.Contains(stderr.String(), "duplicate session") {
			return Result{Error: "a tmux session with that name exists", Code: "duplicate"}
		}
		return Result{Error: "tmux could not start the session: " + sanitize.Label(lastLines(stderr.String(), 1), 120)}
	}
	hs.ledgerUpdate(c.SessionID, func(e *spawnEntry) { e.Started = true })
	if err := hs.tmux(tctx, "set-option", "-t", "="+c.TmuxName+":", "@den_spawn", c.SessionID).Run(); err != nil {
		// Untagged, Den would never attach to or clean it up: end it.
		hs.tmux(tctx, "kill-session", "-t", "="+c.TmuxName).Run()
		return Result{Error: "tmux could not tag the session; it was ended"}
	}
	hs.log(fmt.Sprintf("spawn %s started in tmux %s", c.SessionID[:8], c.TmuxName))
	r := Result{OK: true, Route: p.route, TmuxName: c.TmuxName, Dir: p.dir, Untrusted: p.untr, Warning: p.warn}
	deadline := time.Now().Add(hs.wait)
	for n := 0; time.Now().Before(deadline); n++ {
		if ss, err := hs.sessions(); err == nil {
			for _, s := range ss {
				if s.SessionID == c.SessionID && s.Alive {
					return r
				}
			}
		}
		if n%5 == 4 && hs.tmux(ctx, "has-session", "-t", "="+c.TmuxName).Run() != nil {
			return Result{Error: "Claude Code ended at once on the host", Code: "no_session", TmuxName: c.TmuxName}
		}
		select {
		case <-ctx.Done():
			return Result{Error: "the spawn was cut short; whether its agent started is not known", TmuxName: c.TmuxName}
		case <-time.After(200 * time.Millisecond):
		}
	}
	why := "no session registered within " + hs.wait.Round(time.Second).String() + "; open its window to finish setting Claude Code up on the host"
	if hs.full {
		if out, err := hs.tmux(ctx, "capture-pane", "-p", "-t", "="+c.TmuxName+":").Output(); err == nil {
			if l := lastLines(sanitize.Text(string(out)), 3); l != "" {
				why += "; its window shows: " + strings.ReplaceAll(l, "\n", " / ")
			}
		}
	}
	r.OK, r.Error, r.Code = false, why, "no_session"
	return r
}

// cleanup ends a leftover: a tmux session Den started (tagged with this
// session id) whose Claude Code session is not running.
func (hs *hostSpawn) cleanup(ctx context.Context, c CleanupCmd) Result {
	if CheckSessionID(c.SessionID) != nil || CheckTmuxName(c.TmuxName, c.SessionID) != nil {
		return Result{Error: "not a session Den started"}
	}
	hs.capability()
	for _, s := range hs.spawned(ctx) {
		if s.TmuxName != c.TmuxName {
			continue
		}
		if s.SessionID != c.SessionID {
			return Result{Error: "that tmux session belongs to another agent"}
		}
		if s.Live {
			return Result{Error: "its agent is running; close it instead"}
		}
		if err := hs.tmux(ctx, "kill-session", "-t", "="+c.TmuxName).Run(); err != nil {
			return Result{Error: "tmux could not end it"}
		}
		hs.sweepOne(c.SessionID, true)
		return Result{OK: true, TmuxName: c.TmuxName}
	}
	return Result{Error: "no such tmux session of Den's on the host"}
}

// spawnEntry is one spawn in the ledger, remote-spawns.json (0600) in the
// state folder: metadata only. The brief itself waits in brief.md.
type spawnEntry struct {
	SessionID  string    `json:"session_id"`
	TmuxName   string    `json:"tmux_name"`
	Peer       string    `json:"peer"`
	Controller string    `json:"controller,omitempty"`
	Created    time.Time `json:"created"`
	Started    bool      `json:"started,omitempty"`
	Busy       bool      `json:"busy,omitempty"`      // its agent's first turn began
	Delivered  bool      `json:"delivered,omitempty"` // the first turn is over and brief.md is gone
	Ended      bool      `json:"ended,omitempty"`     // its agent ended, or never started; brief.md is gone
	Expired    bool      `json:"expired,omitempty"`   // an hour passed; brief.md is gone
}

var ledgerMu sync.Mutex

func (hs *hostSpawn) ledgerPath() string { return filepath.Join(hs.stateDir, "remote-spawns.json") }

// ledger runs fn on the ledger under a lock, in this process and across
// the serves of other Dens, and saves it when fn says so.
func (hs *hostSpawn) ledger(fn func(es []spawnEntry) ([]spawnEntry, bool)) error {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if err := os.MkdirAll(hs.stateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(hs.ledgerPath()+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	var es []spawnEntry
	if f, err := os.OpenFile(hs.ledgerPath(), os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
		raw, _ := io.ReadAll(io.LimitReader(f, 4<<20))
		f.Close()
		json.Unmarshal(raw, &es)
	}
	es, save := fn(es)
	if !save {
		return nil
	}
	raw, _ := json.Marshal(es)
	return writeAtomic(hs.ledgerPath(), raw, 0o600)
}

func (hs *hostSpawn) ledgerAdd(e spawnEntry) error {
	return hs.ledger(func(es []spawnEntry) ([]spawnEntry, bool) { return append(es, e), true })
}

func (hs *hostSpawn) ledgerDrop(sid string) {
	hs.ledger(func(es []spawnEntry) ([]spawnEntry, bool) {
		out := es[:0]
		for _, e := range es {
			if e.SessionID != sid {
				out = append(out, e)
			}
		}
		return out, true
	})
}

func (hs *hostSpawn) ledgerUpdate(sid string, fn func(*spawnEntry)) {
	hs.ledger(func(es []spawnEntry) ([]spawnEntry, bool) {
		for i := range es {
			if es[i].SessionID == sid {
				fn(&es[i])
			}
		}
		return es, true
	})
}

// stageDir is a spawn's staging folder, "" when there is none to use.
func stageDir(sid string) string {
	base, err := spawnBase()
	if err != nil || CheckSessionID(sid) != nil {
		return ""
	}
	return filepath.Join(base, sid)
}

// sweep removes briefs whose agent has read them (its first turn is over),
// whose agent ended or never started, or that waited an hour, and forgets
// entries a day old. It reads the host's sessions, never the brief.
func (hs *hostSpawn) sweep() {
	ss, err := hs.sessions()
	if err != nil {
		return
	}
	byID := map[string]discover.Session{}
	for _, s := range ss {
		if s.Alive || byID[s.SessionID].SessionID == "" {
			byID[s.SessionID] = s
		}
	}
	now := time.Now()
	var gone []string
	hs.ledger(func(es []spawnEntry) ([]spawnEntry, bool) {
		out := es[:0]
		changed := false
		for _, e := range es {
			age := now.Sub(e.Created)
			if age > 24*time.Hour {
				changed = true
				gone = append(gone, e.SessionID)
				continue
			}
			if !e.Delivered && !e.Ended && !e.Expired {
				s, found := byID[e.SessionID]
				switch {
				case age > briefTTL:
					e.Expired = true
				case found && s.Alive && s.Status == "busy":
					e.Busy, e.Started = true, true
				case found && s.Alive && e.Busy:
					e.Delivered = true
				case found && !s.Alive:
					e.Ended, e.Started = true, true
				case !found && age > hs.wait+15*time.Second:
					e.Ended = true
				}
				if e.Delivered || e.Ended || e.Expired {
					gone = append(gone, e.SessionID)
				}
				changed = true
			}
			out = append(out, e)
		}
		return out, changed
	})
	for _, sid := range gone {
		if d := stageDir(sid); d != "" {
			os.RemoveAll(d)
		}
	}
	// Staged folders the ledger does not know, an hour old: a serve that
	// died between staging and recording.
	if base, err := spawnBase(); err == nil {
		ents, _ := os.ReadDir(base)
		for _, d := range ents {
			if info, err := d.Info(); err == nil && CheckSessionID(d.Name()) == nil && now.Sub(info.ModTime()) > briefTTL {
				os.RemoveAll(filepath.Join(base, d.Name()))
			}
		}
	}
}

// sweepOne marks one spawn ended and removes its staging folder.
func (hs *hostSpawn) sweepOne(sid string, ended bool) {
	hs.ledgerUpdate(sid, func(e *spawnEntry) { e.Ended = e.Ended || ended })
	if d := stageDir(sid); d != "" {
		os.RemoveAll(d)
	}
}

// report is what serve knows of this controller's spawns of the last day,
// for its hello: whether each agent started and its brief was read.
func (hs *hostSpawn) report() []SpawnState {
	if hs.controller == "" {
		return nil
	}
	tagged := map[string]bool{}
	for _, s := range hs.spawned(context.Background()) {
		tagged[s.SessionID] = true
	}
	live := map[string]bool{}
	if ss, err := hs.sessions(); err == nil {
		for _, s := range ss {
			live[s.SessionID] = true
		}
	}
	var out []SpawnState
	hs.ledger(func(es []spawnEntry) ([]spawnEntry, bool) {
		for _, e := range es {
			if e.Controller == hs.controller {
				out = append(out, SpawnState{SessionID: e.SessionID, TmuxName: e.TmuxName, Started: e.Started || tagged[e.SessionID] || live[e.SessionID],
					Delivered: e.Delivered, Expired: e.Expired})
			}
		}
		return es, false
	})
	return out
}

// handle takes a spawn or cleanup line already checked against the
// allowlist: one result each, a malformed line ends the session.
func (a *actions) spawnCmd(ctx context.Context, typ string, line []byte) error {
	var id string
	var sc SpawnCmd
	var cc CleanupCmd
	if typ == TypeSpawn {
		if json.Unmarshal(line, &sc) != nil {
			return fmt.Errorf("a malformed spawn")
		}
		id = sc.ID
	} else {
		if json.Unmarshal(line, &cc) != nil {
			return fmt.Errorf("a malformed cleanup")
		}
		id = cc.ID
	}
	if id == "" || len(id) > 64 || sanitize.Line(id) != id {
		return fmt.Errorf("a %s without a usable id", typ)
	}
	reply := func(r Result) error {
		r.Type, r.ID = TypeResult, id
		r.Error, r.Warning = sanitize.Label(r.Error, 300), sanitize.Label(r.Warning, 200)
		if r.OK {
			a.log(fmt.Sprintf("%s %s done", typ, shortID(id)))
		} else {
			a.log(fmt.Sprintf("%s %s refused: %s", typ, shortID(id), r.Error))
		}
		return a.up(r)
	}
	hs := a.spawn
	if typ == TypeCleanup {
		switch {
		case !a.allowed:
			return reply(Result{Error: "this host's policy is observe; it takes no actions"})
		case !hs.active():
			return reply(Result{Error: "Den's remotes.toml allows no spawn on this host"})
		}
	} else if code, why := hs.precheck(sc, a.allowed); why != "" {
		return reply(Result{Error: why, Code: code})
	}
	select {
	case a.sem <- struct{}{}:
	default:
		return reply(Result{Error: "too many actions at once"})
	}
	if typ == TypeSpawn && !sc.Check && !hs.busy.TryLock() {
		<-a.sem
		return reply(Result{Error: "another spawn is starting on this host; try again"})
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { <-a.sem }()
		if typ == TypeCleanup {
			reply(hs.cleanup(ctx, cc))
			return
		}
		if !sc.Check {
			defer hs.busy.Unlock()
		}
		p, code, why := hs.resolve(ctx, sc)
		if why != "" {
			reply(Result{Error: why, Code: code, Dir: p.dir})
			return
		}
		if sc.Check {
			reply(Result{OK: true, Route: p.route, TmuxName: sc.TmuxName, Dir: p.dir, Untrusted: p.untr, Warning: p.warn})
			return
		}
		reply(hs.start(ctx, p))
	}()
	return nil
}

// ownerSpawn is the host's own say over spawn, from its config.toml.
func ownerSpawn() *OwnerSpawn {
	c := config.Load()
	return &OwnerSpawn{Spawn: c.Remote.Spawn, Dirs: c.Remote.SpawnDirs}
}

// spawnWait is how long serve waits for a spawned session to register.
func defaultSpawnWait() time.Duration { return spawn.SessionWait }
