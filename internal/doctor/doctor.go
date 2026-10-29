// Package doctor is `den doctor`: read-only checks of everything README's
// Troubleshooting names, each with a one-line fix.
//
// The output is meant to be pasted into an issue, so it names no person,
// machine, project or host: only Den's own fixed paths, written from ~ or
// from the variable that moved them, parsed version numbers, counts, and
// fixed phrases in place of error text (which carries paths). Every seam
// to the outside is in Env, so tests drive each check with fakes.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/stats"
)

// Statuses.
const (
	OK   = "ok"
	Warn = "warn" // works, but something will not
	Fail = "fail" // a feature README names will not work
	Skip = "skip" // the check does not apply here
)

// Result is one check's outcome.
type Result struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Fix     string `json:"fix,omitempty"`
}

// Mod is one bundled mod as the mods check sees it.
type Mod struct {
	Name      string // from the manifest built into den
	Bundled   string // its version in this den
	Installed string // the version in ~/.claude/skills/<name>, "" when none
	Loaded    bool   // Claude Code loads a copy through CLAUDE_CODE_PLUGIN_DIRS
	Target    string // where den install-mods puts it: ~/.claude/skills/<name>, real path
}

// Remote is what Den saved about one host of remotes.toml, in file order.
// It carries no label, target or hostname, so none can be printed.
type Remote struct {
	Enabled   bool
	Saved     bool      // Den has saved state for it
	State     string    // remote.State as saved
	Heartbeat time.Time // when a running Den last confirmed it
}

// ErrTimeout is what Env.Version returns for a program that did not answer
// in time.
var ErrTimeout = errors.New("timed out")

// Env holds every seam to the outside world.
type Env struct {
	Home     string                              // the user's home directory
	Getenv   func(string) string                 // the environment
	LookPath func(string) (string, error)        // exec.LookPath
	Version  func(path string) (string, error)   // the output of `path --version`, bounded
	ReadFile func(string) ([]byte, error)        // os.ReadFile
	Tail     func(string, int64) ([]byte, error) // at most the last n bytes of a file
	Exe      string                              // this den, resolved as install-hooks resolves it
	Running  func() bool                         // a Den of this user listens on den.sock
	Sessions func() (live int, err error)        // live Claude Code sessions; fs.ErrNotExist when the folder is missing
	Mods     func() ([]Mod, error)               // the bundled mods
	Remotes  func() ([]Remote, error)            // remotes.toml and the saved state
	Config   config.Config                       // config.Load()
	Issues   func() ([]config.Issue, error)      // config.Check()
	Now      time.Time

	// For den feedback only.
	DenVersion string                                // den's version as the Makefile set it from git describe
	GoVersion  string                                // runtime.Version()
	Platform   string                                // GOOS/GOARCH
	Kernel     func() (string, error)                // the kernel's release string
	OSRelease  func() ([]byte, error)                // os-release, bounded
	Stats      func(from, to time.Time) stats.Report // den stats over [from, to)
	Names      func() []string                       // the machine's identifying values, for the backstop only; never printed

	// For the setup card's plan only; den doctor does not use them.
	Lstat        func(string) (fs.FileInfo, error) // os.Lstat
	EvalSymlinks func(string) (string, error)      // filepath.EvalSymlinks
	UID          int                               // os.Getuid()
}

// Versions Den is checked against.
const (
	ClaudeMin     = "2.1.289" // the oldest Claude Code that writes ~/.claude/sessions
	ClaudeChecked = "2.1.309" // the newest Claude Code Den's compatibility probes passed on
	GitMin        = "2.31"    // gitsafe's config overrides travel in GIT_CONFIG_COUNT, new in 2.31
	GitMerge      = "2.38"    // Review's merge check runs git merge-tree --write-tree, new in 2.38
)

// logLines is how many of hooks.log's last lines the hooks.log check reads.
const (
	logLines = 50
	logTail  = 64 << 10
)

// Run runs every check, in README's order.
func Run(e Env) []Result {
	return []Result{
		claude(e), sessions(e), hooksCheck(e), den(e), hookLog(e), focus(e), terminal(e),
		notify(e), git(e), modsCheck(e), remotes(e), orchestrator(e), configCheck(e),
	}
}

// Failed reports whether any result failed.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

// Text renders results one line each, with the fix under a warn or fail.
func Text(rs []Result) string {
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "%-5s %-12s %s\n", r.Status, r.ID, r.Summary)
		if r.Fix != "" {
			fmt.Fprintf(&b, "%-5s %-12s fix: %s\n", "", "", r.Fix)
		}
	}
	return b.String()
}

// phrase is an error as a fixed phrase that follows its subject ("is
// missing", "is unreadable", "timed out", "does not parse"): os and exec
// errors carry full paths, so they are never printed (D1, den-sec-review).
func phrase(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "is missing"
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, ErrParse):
		return "does not parse"
	}
	return "is unreadable"
}

func result(id, status, summary, fix string) Result {
	if status == OK || status == Skip {
		fix = ""
	}
	return Result{ID: id, Status: status, Summary: summary, Fix: fix}
}

// ver is a parsed version number.
type ver []int

var (
	claudeVer = regexp.MustCompile(`^\s*v?(\d+)\.(\d+)\.(\d+)(?:\s|$)`)
	gitVer    = regexp.MustCompile(`^\s*git version (\d+)\.(\d+)(?:\.(\d+))?`)
	modVer    = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?$`)
)

// parseVer reads the numbers re captures from the first line of s; nil
// when it does not match.
func parseVer(re *regexp.Regexp, s string) ver {
	line, _, _ := strings.Cut(s, "\n")
	m := re.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	var v ver
	for _, p := range m[1:] {
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		v = append(v, n)
	}
	return v
}

func mustVer(s string) ver {
	v := parseVer(modVer, s)
	if v == nil {
		panic("doctor: bad version " + s)
	}
	return v
}

// less reports whether a is older than b; missing parts count as 0.
func (a ver) less(b ver) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func (a ver) String() string {
	parts := make([]string, len(a))
	for i, n := range a {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// claudeDir is Claude Code's config folder as printed: from ~ or from the
// variable that moved it, never its value.
func claudeDir(e Env) (real, shown string) {
	if d := e.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, "<CLAUDE_CONFIG_DIR>"
	}
	return filepath.Join(e.Home, ".claude"), "~/.claude"
}

// stateDir is Den's state folder, real and as printed.
func stateDir(e Env) (real, shown string) {
	if d := e.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "den"), "<XDG_STATE_HOME>/den"
	}
	return filepath.Join(e.Home, ".local", "state", "den"), "~/.local/state/den"
}

// denShown is a den executable as printed: ~/…/den under home, else only
// that it is outside home.
func denShown(e Env, p string) string {
	if e.Home != "" && strings.HasPrefix(p, strings.TrimSuffix(e.Home, "/")+"/") {
		return "~/…/den"
	}
	return "a den outside your home"
}

func claude(e Env) Result {
	const fix = "Install or update Claude Code (" + ClaudeMin + " or newer); `claude` must be on PATH"
	p, err := e.LookPath("claude")
	if err != nil {
		return result("claude", Fail, "Claude Code is not on PATH", fix)
	}
	out, err := e.Version(p)
	if err != nil {
		return result("claude", Warn, "Claude Code is on PATH, but its version "+phrase(err), fix)
	}
	v := parseVer(claudeVer, out)
	switch {
	case v == nil:
		return result("claude", Warn, "Claude Code is on PATH, but its version is unparseable", fix)
	case v.less(mustVer(ClaudeMin)):
		return result("claude", Fail, "Claude Code "+v.String()+" is older than "+ClaudeMin+", which Den needs", fix)
	case mustVer(ClaudeChecked).less(v):
		return result("claude", Warn, "Claude Code "+v.String()+" is newer than "+ClaudeChecked+", the newest Den was checked against",
			"Den should work; if something breaks, say so in an issue with this output")
	}
	return result("claude", OK, "Claude Code "+v.String()+" on PATH (Den needs "+ClaudeMin+" or newer)", "")
}

func sessions(e Env) Result {
	_, shown := claudeDir(e)
	const fix = "Start a Claude Code session; `den list` shows what Den sees, `den explain <name>` why"
	n, err := e.Sessions()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return result("sessions", Warn, shown+"/sessions is missing: no Claude Code "+ClaudeMin+" or newer has run", fix)
	case err != nil:
		return result("sessions", Fail, shown+"/sessions "+phrase(err), "Make "+shown+"/sessions readable by you")
	case n == 0:
		return result("sessions", Warn, shown+"/sessions: no live sessions", fix)
	case n == 1:
		return result("sessions", OK, shown+"/sessions: 1 live session", "")
	}
	return result("sessions", OK, shown+"/sessions: "+strconv.Itoa(n)+" live sessions", "")
}

func hooksCheck(e Env) Result {
	dir, shown := claudeDir(e)
	file := shown + "/settings.json"
	const fix = "den install-hooks, or ^Y on Den's setup card"
	raw, err := e.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return result("hooks", Fail, file+" "+phrase(err), "Make "+file+" readable by you, then: "+fix)
	}
	st, err := hooks.StatusOf(raw, e.Exe)
	if err != nil {
		return result("hooks", Fail, file+" does not parse", "Fix the file by hand (den install-hooks will not rewrite a file it cannot read)")
	}
	all := len(hooks.Events())
	switch {
	case len(st.Missing) == all:
		return result("hooks", Fail, "Den's hooks are not in "+file, fix)
	case len(st.Missing) > 0:
		return result("hooks", Fail, fmt.Sprintf("Den's hooks in %s miss %d of %d events: %s", file, len(st.Missing), all, strings.Join(st.Missing, ", ")), fix)
	case st.Other:
		return result("hooks", Warn, "Den's hooks in "+file+" run another den than this one ("+denShown(e, e.Exe)+")",
			fix+" with the den you use, so hooks run that one")
	}
	return result("hooks", OK, "Den's hooks are in "+file+" for every event", "")
}

func den(e Env) Result {
	if e.Running() {
		return result("den", OK, "Den is running", "")
	}
	return result("den", Warn, "Den is not running (den doctor works without it)", "Start den: hooks only reach a running Den")
}

func hookLog(e Env) Result {
	dir, shown := stateDir(e)
	file := shown + "/hooks.log"
	if !e.Config.Hooks.Log {
		return result("hooks.log", Skip, "hooks.log is off in config.toml ([hooks] log)", "")
	}
	raw, err := e.Tail(filepath.Join(dir, "hooks.log"), logTail)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return result("hooks.log", OK, file+": no hook has run yet", "")
	case err != nil:
		return result("hooks.log", Warn, file+" "+phrase(err), "Make "+file+" readable by you")
	}
	if len(raw) >= logTail {
		if i := strings.IndexByte(string(raw), '\n'); i >= 0 {
			raw = raw[i+1:] // the first line is cut
		}
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > logLines {
		lines = lines[len(lines)-logLines:]
	}
	runs, missed := 0, 0
	var last time.Time
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		runs++
		if strings.HasSuffix(l, " no den listening") {
			missed++
			if len(l) >= 19 {
				if at, err := time.ParseInLocation("2006-01-02 15:04:05", l[:19], time.Local); err == nil && at.After(last) {
					last = at
				}
			}
		}
	}
	switch {
	case runs == 0:
		return result("hooks.log", OK, file+": no hook has run yet", "")
	case missed == 0 && runs == 1:
		return result("hooks.log", OK, "the last hook run reached Den", "")
	case missed == 0:
		return result("hooks.log", OK, fmt.Sprintf("the last %d hook runs reached Den", runs), "")
	}
	when := ""
	if !last.IsZero() {
		when = " (last " + ago(e.Now, last) + ")"
	}
	return result("hooks.log", Warn, fmt.Sprintf("%d of the last %d hook runs found no Den listening%s", missed, runs, when),
		"Start den; sessions started before den install-hooks need a restart")
}

// ago is t as printed: relative to now, never a clock time or a zone, which
// hint where someone lives (den-sec-review, on den feedback).
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "min") + " ago"
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "h") + " ago"
	}
	return plural(int(d/(24*time.Hour)), "day") + " ago"
}

// plural is n and its unit: "1 day", "2 days"; "min" and "h" stay as they
// are.
func plural(n int, unit string) string {
	s := strconv.Itoa(n) + " " + unit
	if n != 1 && unit != "min" && unit != "h" {
		s += "s"
	}
	return s
}

func focus(e Env) Result {
	_, err := e.LookPath("hyprctl")
	sig := e.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != ""
	switch {
	case err != nil && !sig:
		return result("focus", Skip, "not on Hyprland: Enter does not focus terminals, nothing else is affected", "")
	case err != nil:
		return result("focus", Warn, "Hyprland is running but hyprctl is not on PATH, so focus is off", "Put hyprctl on PATH")
	case !sig:
		return result("focus", Warn, "hyprctl is on PATH but HYPRLAND_INSTANCE_SIGNATURE is not set, so focus is off",
			"Run den from inside your Hyprland session; `den explain <name>` says how a window matched")
	}
	return result("focus", OK, "Hyprland and hyprctl found: Enter focuses an agent's terminal", "")
}

// terminal follows spawn.launch: xdg-terminal-exec (which may choose
// Ghostty), else $TERMINAL.
func terminal(e Env) Result {
	const fix = "Install xdg-terminal-exec or set $TERMINAL"
	if _, err := e.LookPath("xdg-terminal-exec"); err == nil {
		return result("terminal", OK, "new agents open through xdg-terminal-exec", "")
	}
	if t := e.Getenv("TERMINAL"); t != "" && t != "xdg-terminal-exec" {
		return result("terminal", OK, "new agents open through $TERMINAL", "")
	}
	return result("terminal", Fail, "no terminal launcher: Den cannot start agents in new terminals", fix)
}

func notify(e Env) Result {
	if _, err := e.LookPath("notify-send"); err != nil {
		return result("notify", Warn, "notify-send is not on PATH: Den's desktop notifications are lost", "Install libnotify (notify-send)")
	}
	return result("notify", OK, "notify-send found", "")
}

func git(e Env) Result {
	const fix = "Install or update git (" + GitMerge + " or newer)"
	p, err := e.LookPath("git")
	if err != nil {
		return result("git", Fail, "git is not on PATH: worktrees and Review's merge check do not work", fix)
	}
	out, err := e.Version(p)
	if err != nil {
		return result("git", Warn, "git is on PATH, but its version "+phrase(err), fix)
	}
	v := parseVer(gitVer, out)
	switch {
	case v == nil:
		return result("git", Warn, "git is on PATH, but its version is unparseable", fix)
	case v.less(mustVer(GitMin)):
		return result("git", Fail, "git "+v.String()+" is older than "+GitMin+", which Den needs to read agents' checkouts safely", fix)
	case v.less(mustVer(GitMerge)):
		return result("git", Warn, "git "+v.String()+" is older than "+GitMerge+": Review's merge check is off", fix)
	}
	return result("git", OK, "git "+v.String(), "")
}

func modsCheck(e Env) Result {
	const fix = "den install-mods, or ^Y on Den's setup card"
	ms, err := e.Mods()
	if err != nil {
		return result("mods", Warn, "the list of mods built into this den "+phrase(err), "Rebuild den with its submodule (see README, Install)")
	}
	if len(ms) == 0 {
		return result("mods", Skip, "this den has no mods built in", "")
	}
	status := OK
	var parts []string
	for _, m := range ms {
		name, bundled := sanitize.Label(m.Name, 40), shownVer(m.Bundled)
		switch {
		case m.Loaded:
			parts = append(parts, name+" loaded through CLAUDE_CODE_PLUGIN_DIRS")
		case m.Installed == "":
			status = Warn
			parts = append(parts, name+" "+bundled+" is not installed")
		case m.Installed != m.Bundled:
			status = Warn
			parts = append(parts, name+" "+shownVer(m.Installed)+" is installed, this den has "+bundled)
		default:
			parts = append(parts, name+" "+bundled+" installed")
		}
	}
	return result("mods", status, strings.Join(parts, "; "), fix)
}

// shownVer is a mod version as printed: only a parsed number.
func shownVer(s string) string {
	if v := parseVer(modVer, s); v != nil {
		return v.String()
	}
	return "(unparseable version)"
}

// stale is how old a running Den's heartbeat for a live host may be: three
// of remote.HeartbeatEvery.
const stale = 45 * time.Second

func remotes(e Env) Result {
	const fix = "Run `den remote list` to match host numbers to labels, then `den remote status <label>`"
	rs, err := e.Remotes()
	if err != nil {
		return result("remotes", Warn, "remotes.toml "+phrase(err)+": Den connects to no host", "Run `den remote list`; it says what is wrong")
	}
	if len(rs) == 0 {
		return result("remotes", Skip, "no remote hosts", "")
	}
	running := e.Running()
	var bad []string
	enabled := 0
	for i, r := range rs {
		if !r.Enabled {
			continue
		}
		enabled++
		host := "host " + strconv.Itoa(i+1)
		switch {
		case r.State == "needs_attention":
			bad = append(bad, host+" needs you")
		case r.State == "version_mismatch":
			bad = append(bad, host+" has a version mismatch")
		case running && !r.Saved:
			bad = append(bad, host+" has no saved state")
		case running && (r.State == "live" || r.State == "attached_elsewhere") && e.Now.Sub(r.Heartbeat) >= stale:
			bad = append(bad, host+" has not been confirmed for over 45 s")
		}
	}
	if len(bad) > 0 {
		return result("remotes", Warn, strings.Join(bad, "; ")+" (saved state; not dialled)", fix)
	}
	return result("remotes", OK, fmt.Sprintf("%d of %d hosts enabled, none needs you (saved state; not dialled)", enabled, len(rs)), "")
}

// ErrParse marks a file that was read but does not parse.
var ErrParse = errors.New("does not parse")

func orchestrator(e Env) Result {
	o := e.Config.Orchestrator
	spawn := "spawning off"
	if o.Spawn {
		spawn = "spawning on"
	}
	return result("orchestrator", OK, "model "+shownModel(o.Model)+", "+spawn+
		" (a real Claude Code session: lower the cost with model = \"haiku\" or spawn = false)", "")
}

var modelID = regexp.MustCompile(`^claude-[a-z0-9.-]{1,40}(\[1m\])?$`)

// shownModel is the orchestrator's model as printed: a Claude Code alias
// or a claude-… id as written, anything else (a Bedrock ARN, a gateway's
// own name) as "a custom model", since it can name an account or a company.
func shownModel(m string) string {
	switch strings.TrimSuffix(m, "[1m]") {
	case "opus", "sonnet", "haiku", "default", "opusplan", "best", "off":
		return m
	}
	if modelID.MatchString(m) {
		return m
	}
	return "a custom model"
}

func configCheck(e Env) Result {
	const fix = "Fix the named keys in config.toml (`den config` shows what Den uses)"
	is, err := e.Issues()
	if err != nil {
		return result("config", Warn, "config.toml "+phrase(err)+": Den uses its defaults", fix)
	}
	if len(is) == 0 {
		return result("config", OK, "config.toml: every key is one Den uses", "")
	}
	var parts []string
	for _, i := range is {
		where := ""
		if i.Section != "" {
			where = " in [" + i.Section + "]"
		}
		switch i.Kind {
		case config.IssueUnread:
			parts = append(parts, i.Key+" has a value Den cannot use, so it uses its default")
		case config.IssueGod:
			parts = append(parts, "a God mode key"+where+", which Den never reads (God mode is turned on only in Den's window)")
		default:
			parts = append(parts, "an unknown key"+where)
		}
	}
	return result("config", Warn, "config.toml: "+strings.Join(parts, "; "), fix)
}
