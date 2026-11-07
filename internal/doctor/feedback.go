package doctor

// den feedback: a report for a bug report, built only from what Den already
// records, for the human to read before pasting it into a public issue.
//
// The rules are the doctor's and stricter (feedback-design.md, with
// den-sec-review's F1 to F8): every printed string is a fixed phrase, a
// number, a relative time, a version rebuilt from its parsed numbers or a
// value from a closed list in this file. Text from outside Den never reaches
// the output, not even sanitized. A last scan of the rendered text and JSON
// against the machine's identifying values is a backstop, not the guarantee.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/permmode"
)

// Bounds on what feedback reads (F4).
const (
	MaxDays    = 30
	hooksTailN = 1 << 20                 // hooks.log is never rotated: its newest MiB
	eventsN    = events.MaxSize + 64<<10 // each of events.jsonl and .1: Den rotates past MaxSize
	lineMax    = 64 << 10                // a longer line is skipped, not cut
	osRelMax   = 16 << 10                // /etc/os-release
)

// Notice is what den feedback says on stderr, never in the report.
const Notice = "Read this before you paste it: it goes into a public issue. Den wrote nothing,\n" +
	"copied nothing and sent nothing. Remove anything you do not want public.\n"

// Withheld replaces a line, or a JSON value, that held one of the machine's
// identifying values. It never says which.
const Withheld = "(a line was withheld: it held something that looked identifying)"

// Section is one part of the report.
type Section struct {
	ID    string   `json:"id"`
	Lines []string `json:"lines"`
}

// Report is den feedback's output before the backstop: the doctor's
// checks (but remotes) and the sections around them.
type Report struct {
	Days     int       `json:"days"`
	Sections []Section `json:"sections"`
	Checks   []Result  `json:"checks"`
	Withheld int       `json:"withheld"`
}

// Feedback builds the report over the last days days up to e.Now.
func Feedback(e Env, days int) Report {
	to := e.Now
	from := to.Add(-time.Duration(days) * 24 * time.Hour)
	var checks []Result
	for _, r := range Run(e) {
		if r.ID != "remotes" { // its host numbers stay out; counts are below
			checks = append(checks, r)
		}
	}
	return Report{Days: days, Checks: checks, Sections: []Section{
		{"den", []string{denLine(e)}},
		{"system", []string{systemLine(e)}},
		{"remotes", []string{remoteCounts(e)}},
		{"config", []string{configLine(e)}},
		{"activity", activity(e, from, to)},
		{"trouble", append(hookTrouble(e, from, to), eventTrouble(e, from, to)...)},
	}}
}

func (r Report) section(id string) Section {
	for _, s := range r.Sections {
		if s.ID == id {
			return s
		}
	}
	return Section{ID: id}
}

// body is the report as text, before the backstop.
func (r Report) body() string {
	var b strings.Builder
	write := func(id string) {
		for i, l := range r.section(id).Lines {
			label := id
			if i > 0 {
				label = ""
			}
			fmt.Fprintf(&b, "%-10s %s\n", label, l)
		}
	}
	fmt.Fprintf(&b, "den feedback, last %s\n", plural(r.Days, "day"))
	write("den")
	write("system")
	b.WriteString("\nchecks\n")
	b.WriteString(Text(r.Checks))
	write("remotes")
	write("config")
	b.WriteString("\n")
	write("activity")
	write("trouble")
	return b.String()
}

// Text is the report as a fenced block, ready to paste, with every line
// that holds one of names withheld and counted at its foot.
func (r Report) Text(names []string) string {
	deny := newDenylist(names)
	lines := strings.Split(strings.TrimSuffix(r.body(), "\n"), "\n")
	n := 0
	for i, l := range lines {
		if deny.hit(l) {
			lines[i] = Withheld
			n++
		}
	}
	return "```text\n" + strings.Join(lines, "\n") + "\n" + fmt.Sprintf("%-10s %s\n", "withheld", plural(n, "line")) + "```\n"
}

// JSON is the report as JSON, with every string value that holds one of
// names replaced by Withheld, so the JSON stays valid, and the count in
// withheld.
func (r Report) JSON(names []string) ([]byte, error) {
	r.Withheld = 0
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	deny := newDenylist(names)
	n := 0
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			if deny.hit(t) {
				n++
				return Withheld
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			for k := range t {
				t[k] = walk(t[k])
			}
		}
		return x
	}
	walk(v)
	v["withheld"] = n
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <CLAUDE_CONFIG_DIR> reads as written
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// denylist is the backstop's list: lower case, at least 4 characters (F3).
type denylist []string

func newDenylist(names []string) denylist {
	var d denylist
	for _, s := range names {
		s = strings.ToLower(strings.TrimSpace(s))
		if len([]rune(s)) >= 4 && !slices.Contains(d, s) {
			d = append(d, s)
		}
	}
	return d
}

// hit reports whether s holds an entry. An entry of digits only (a uid)
// matches only as a whole number: for uid 1000, "21000 prompts" is kept
// but "user/1000/", and a count of exactly 1000, are withheld.
func (d denylist) hit(s string) bool {
	s = strings.ToLower(s)
	for _, w := range d {
		if !allDigits(w) {
			if strings.Contains(s, w) {
				return true
			}
			continue
		}
		for i := 0; ; {
			j := strings.Index(s[i:], w)
			if j < 0 {
				break
			}
			j += i
			end := j + len(w)
			if (j == 0 || !isAlnum(s[j-1])) && (end == len(s) || !isAlnum(s[end])) {
				return true
			}
			i = j + 1
		}
	}
	return false
}

func allDigits(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// The den version from git describe, anchored, and rebuilt from its
// captures (F2).
var denVer = regexp.MustCompile(`^v?(\d{1,4})\.(\d{1,4})\.(\d{1,4})(?:-rc\.(\d{1,4}))?(?:-(\d{1,6})-g([0-9a-f]{7,12}))?(-dirty)?$`)

// shownDen is den's version as printed.
func shownDen(s string) string {
	if s == "dev" {
		return "a development build"
	}
	m := denVer.FindStringSubmatch(s)
	if m == nil {
		return "a custom build"
	}
	v := "v" + num(m[1]) + "." + num(m[2]) + "." + num(m[3])
	if m[4] != "" {
		v += "-rc." + num(m[4])
	}
	if m[5] != "" {
		v += "-" + num(m[5]) + "-g" + m[6] // m[6] is [0-9a-f] only
	}
	if m[7] != "" {
		v += "-dirty"
	}
	return v
}

// num is a run of digits rebuilt as a number.
func num(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return "0"
	}
	return strconv.Itoa(n)
}

var (
	goVer    = regexp.MustCompile(`^go(\d{1,3})\.(\d{1,3})(?:\.(\d{1,3}))?`)
	platform = regexp.MustCompile(`^[a-z0-9]{1,16}/[a-z0-9]{1,16}$`)
	kernel   = regexp.MustCompile(`^(\d{1,4})(?:\.(\d{1,4}))?(?:\.(\d{1,4}))?`)
)

func denLine(e Env) string {
	g := "Go of an unknown version"
	if v := parseVer(goVer, e.GoVersion); v != nil {
		g = "go" + v.String()
	}
	p := "an unknown platform"
	if platform.MatchString(e.Platform) {
		p = e.Platform
	}
	return shownDen(e.DenVersion) + " (" + g + ", " + p + ")"
}

// distros are the os-release IDs printed by name (F1); any other is "other".
var distros = []string{
	"omarchy", "arch", "debian", "ubuntu", "fedora", "nixos", "opensuse-tumbleweed", "opensuse-leap", "gentoo",
	"void", "alpine", "endeavouros", "manjaro", "cachyos", "pop", "linuxmint", "rhel", "rocky",
	"almalinux", "centos",
}

// terminals are the $TERMINAL programs printed by name; any other is
// "another terminal".
var terminals = []string{"ghostty", "kitty", "alacritty", "foot", "wezterm", "konsole", "gnome-terminal", "xterm"}

var versionID = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){0,3}$`)

// distro is os-release's ID from the closed list, and its VERSION_ID as
// digits and dots, rebuilt. Another ID is "other", or "other, like <x>"
// when the first word of ID_LIKE is exactly an entry; either way with no
// version, which would fingerprint an in-house distro (den-sec-review).
func distro(raw []byte) string {
	id, version, like := "", "", ""
	for _, l := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		case "ID_LIKE":
			if f := strings.Fields(v); len(f) > 0 {
				like = f[0]
			}
		}
	}
	if !slices.Contains(distros, id) {
		if slices.Contains(distros, like) {
			return "other, like " + like
		}
		return "other"
	}
	if !versionID.MatchString(version) {
		return id
	}
	// Rebuilt from its parts, each digits only by versionID; leading
	// zeros stay, as in 24.04.
	var parts []string
	for _, p := range strings.Split(version, ".") {
		parts = append(parts, strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, p))
	}
	return id + " " + strings.Join(parts, ".")
}

func systemLine(e Env) string {
	k := "kernel unknown"
	if raw, err := e.Kernel(); err == nil {
		if v := parseVer(kernel, raw); v != nil {
			k = "kernel " + v.String()
		}
	}
	d := "distro unknown"
	if raw, err := e.OSRelease(); err == nil {
		d = distro(raw)
	}
	hypr := "Hyprland: no"
	if e.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		hypr = "Hyprland: yes"
	}
	term := "terminal: none found"
	if _, err := e.LookPath("xdg-terminal-exec"); err == nil {
		term = "terminal: through xdg-terminal-exec"
	} else if t := strings.Fields(e.Getenv("TERMINAL")); len(t) > 0 {
		term = "terminal: another terminal"
		if name := filepath.Base(t[0]); slices.Contains(terminals, name) {
			term = "terminal: " + name
		}
	}
	return k + ", " + d + ", " + hypr + ", " + term
}

// remoteCounts is the remotes check as counts: no host numbers, no labels.
func remoteCounts(e Env) string {
	rs, err := e.Remotes()
	if err != nil {
		return "remotes.toml " + phrase(err)
	}
	if len(rs) == 0 {
		return "no remote hosts"
	}
	enabled, needs, mismatch := 0, 0, 0
	for _, r := range rs {
		if !r.Enabled {
			continue
		}
		enabled++
		switch r.State {
		case "needs_attention":
			needs++
		case "version_mismatch":
			mismatch++
		}
	}
	return fmt.Sprintf("%s, %d enabled, %d need you, %d with a version mismatch (saved state; not dialled)",
		plural(len(rs), "host"), enabled, needs, mismatch)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// configLine is Den's known switches and enums; never a string value.
func configLine(e Env) string {
	c := e.Config
	mode := "a mode Den does not know"
	if permmode.Valid(c.Agents.PermissionMode) {
		mode = c.Agents.PermissionMode
	}
	stop := "usage stop off"
	if p := c.Usage.StopPercent; p > 0 && p <= 100 {
		stop = "usage stop at " + strconv.Itoa(p) + "%"
	}
	return "hooks.log " + onOff(c.Hooks.Log) + ", events.log " + onOff(c.Events.Log) + ", API " + onOff(c.API.Enabled) +
		", agents start in " + mode + ", orchestrator " + shownModel(c.Orchestrator.Model) + " with spawning " + onOff(c.Orchestrator.Spawn) +
		", " + stop
}

// seconds is a duration as printed, coarse on purpose.
func seconds(s float64) string {
	switch {
	case s < 90:
		return strconv.Itoa(int(s+0.5)) + " s"
	case s < 90*60:
		return strconv.Itoa(int(s/60+0.5)) + " min"
	}
	return strconv.Itoa(int(s/3600+0.5)) + " h"
}

// activity is stats' counts: integers and durations only (F5).
func activity(e Env, from, to time.Time) []string {
	r := e.Stats(from, to)
	var out []string
	if t := r.Turns; t != nil {
		out = append(out, fmt.Sprintf("%s; %s, %d finished, %d failed", plural(t.Agents, "agent"), plural(t.Prompts, "prompt"), t.Finished, t.Failed))
	} else {
		out = append(out, "turns not recorded (events.jsonl is missing or unreadable)")
	}
	if w := r.Waits; w != nil {
		l := fmt.Sprintf("%s (%d permission, %d plan, %d question, %d other)", plural(w.Requests, "wait"), w.Permission, w.Plan, w.Question, w.Other)
		if w.MedianSeconds != nil && w.LongestSeconds != nil {
			l += ", median " + seconds(*w.MedianSeconds) + ", longest " + seconds(*w.LongestSeconds)
		}
		out = append(out, l)
	}
	if m := r.Merges; m != nil {
		if m.Count == 1 {
			out = append(out, "1 mission branch merged")
		} else {
			out = append(out, strconv.Itoa(m.Count)+" mission branches merged")
		}
	}
	return out
}

// lines is raw split into lines, the first dropped when raw was cut at
// limit (it is part of a line), and every line longer than lineMax
// skipped (F4).
func lines(raw []byte, limit int64) []string {
	if int64(len(raw)) >= limit {
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		}
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" && len(l) <= lineMax {
			out = append(out, l)
		}
	}
	return out
}

func within(t, from, to time.Time) bool { return !t.Before(from) && t.Before(to) }

// counts is a tally by name, printed largest first.
type counts map[string]int

func (c counts) String() string {
	names := make([]string, 0, len(c))
	for k := range c {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if c[names[i]] != c[names[j]] {
			return c[names[i]] > c[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = strconv.Itoa(c[k]) + " " + k
	}
	return strings.Join(parts, ", ")
}

// hookTrouble counts hooks.log in the window: runs, runs that found no Den,
// and failed [on] commands by event kind. Never a session, never the [on]
// command or its error.
func hookTrouble(e Env, from, to time.Time) []string {
	dir, shown := stateDir(e)
	if !e.Config.Hooks.Log {
		return []string{"hooks: hooks.log is off, not counted"}
	}
	raw, err := e.Tail(filepath.Join(dir, "hooks.log"), hooksTailN)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []string{"hooks: no hook has run yet"}
	case err != nil:
		return []string{"hooks: " + shown + "/hooks.log " + phrase(err) + ", not counted"}
	}
	runs, missed := 0, 0
	var lastMiss, lastOn time.Time
	on := counts{}
	for _, l := range lines(raw, hooksTailN) {
		if len(l) < 20 {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05", l[:19], time.Local)
		if err != nil || !within(at, from, to) {
			continue
		}
		f := strings.Fields(l[19:])
		switch {
		case len(f) > 0 && strings.HasPrefix(f[0], "on.") && strings.Contains(l, " [on] command failed"):
			kind := strings.TrimPrefix(f[0], "on.")
			if !slices.Contains(events.Kinds, kind) {
				kind = "other"
			}
			on["on."+kind]++
			if at.After(lastOn) {
				lastOn = at
			}
		case strings.HasSuffix(l, " no den listening"):
			runs++
			missed++
			if at.After(lastMiss) {
				lastMiss = at
			}
		case strings.HasSuffix(l, " delivered"):
			runs++
		}
	}
	var out []string
	switch {
	case runs == 0:
		out = append(out, "hooks: no hook ran")
	case missed == 0:
		out = append(out, "hooks: "+plural(runs, "run")+", every one reached Den")
	default:
		out = append(out, fmt.Sprintf("hooks: %s, %d found no Den listening, the last %s", plural(runs, "run"), missed, ago(e.Now, lastMiss)))
	}
	if n := sum(on); n > 0 {
		out = append(out, fmt.Sprintf("[on] commands: %d failed (%s), the last %s", n, on, ago(e.Now, lastOn)))
	}
	return out
}

func sum(c counts) int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// tools are Claude Code's built-in tools, printed by name; an MCP tool is
// "an MCP tool" and any other "another tool", since their names can name a
// server, a company or a project.
var tools = []string{
	"Bash", "BashOutput", "KillShell", "Read", "Write", "Edit", "MultiEdit", "NotebookEdit", "NotebookRead",
	"Glob", "Grep", "LS", "WebFetch", "WebSearch", "Task", "Agent", "TodoWrite", "ExitPlanMode",
	"EnterPlanMode", "AskUserQuestion", "Skill", "SlashCommand", "ToolSearch", "LSP", "Monitor", "SendMessage",
}

func toolName(t string) string {
	switch {
	case slices.Contains(tools, t):
		return t
	case strings.HasPrefix(t, "mcp__"):
		return "an MCP tool"
	}
	return "another tool"
}

// event is the fields of an events.jsonl line that feedback counts; every
// other field is never decoded (F4).
type event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Error   string    `json:"error"`
	Tool    string    `json:"tool"`
	Minutes int       `json:"minutes"`
}

// maxMinutes caps a minutes field from the file, which is untrusted.
const maxMinutes = 60 * 24 * 365

// eventTrouble counts events.jsonl (and .1) in the window: failed turns by
// error type, failed tool calls by tool, stalls and long waits.
func eventTrouble(e Env, from, to time.Time) []string {
	if !e.Config.Events.Log {
		return []string{"turns: events.log is off, not counted"}
	}
	dir, shown := stateDir(e)
	var evs []event
	found := false
	for _, name := range []string{"events.jsonl.1", "events.jsonl"} {
		raw, err := e.Tail(filepath.Join(dir, name), eventsN)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return []string{"turns: " + shown + "/" + name + " " + phrase(err) + ", not counted"}
		}
		found = true
		for _, l := range lines(raw, eventsN) {
			var ev event
			if json.Unmarshal([]byte(l), &ev) != nil || !within(ev.Time, from, to) {
				continue
			}
			evs = append(evs, ev)
		}
	}
	if !found {
		return []string{"turns: no event recorded yet"}
	}
	failed, tool := counts{}, counts{}
	var lastFail time.Time
	stalls, waits, longestStall, longestWait := 0, 0, 0, 0
	for _, ev := range evs {
		switch ev.Kind {
		case events.TurnFailed:
			t := ev.Error
			if !slices.Contains(events.FailureTypes, t) {
				t = "other"
			}
			failed[t]++
			if ev.Time.After(lastFail) {
				lastFail = ev.Time
			}
		case events.ToolFailed:
			tool[toolName(ev.Tool)]++
		case events.Stalled:
			stalls++
			if m := ev.Minutes; m > longestStall && m <= maxMinutes {
				longestStall = m
			}
		case events.StillWaiting:
			waits++
			if m := ev.Minutes; m > longestWait && m <= maxMinutes {
				longestWait = m
			}
		}
	}
	var out []string
	if n := sum(failed); n > 0 {
		out = append(out, fmt.Sprintf("turns: %d failed (%s), the last %s", n, failed, ago(e.Now, lastFail)))
	} else {
		out = append(out, "turns: none failed")
	}
	if n := sum(tool); n > 0 {
		out = append(out, fmt.Sprintf("tools: %d failed (%s)", n, tool))
	} else {
		out = append(out, "tools: none failed")
	}
	if stalls > 0 {
		out = append(out, fmt.Sprintf("stalls: %d, the longest %d min", stalls, longestStall))
	}
	if waits > 0 {
		out = append(out, fmt.Sprintf("still waiting: %s, the longest %d min", plural(waits, "reminder"), longestWait))
	}
	return out
}
