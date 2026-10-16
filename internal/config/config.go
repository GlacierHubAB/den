// Package config reads ~/.config/den/config.toml. Every key has a default
// and an environment override, so the file is optional.
package config

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/GlacierHubAB/den/internal/permmode"
)

// Config is everything Den lets you tune.
type Config struct {
	Orchestrator struct {
		Model string // claude --model; "off" keeps Claude Code's default
		Name  string // the orchestrator session's display name
		Spawn bool   // let it open terminals and start agents on your workspace
		// Guidance is text that shapes the orchestrator's answers: "i-have-adhd"
		// (shipped with Den) or the absolute path of a file of your own. Off
		// when empty. It loads only once you approve its SHA-256 in Den.
		Guidance string
	}
	// Agents is how Den starts agents.
	Agents struct {
		// PermissionMode is the global mode new agents start in
		// (claude --permission-mode): one of permmode.Modes(), never
		// bypassPermissions; permmode.Shipped (auto) when unset. A value
		// Den refuses keeps that, and ModeRefused holds it, for Den's
		// note. Den takes it only up to permmode.Shipped: the file asks.
		PermissionMode string
		ModeRefused    string
	}
	Inbox struct {
		History int // finished turns kept as history
	}
	Detail struct {
		Recent int // activity entries in the details pane
	}
	Hooks struct {
		PermissionWait  int  // seconds a held permission waits for your answer in Den; Claude Code's own dialog is up all along
		Log             bool // append every hook run to hooks.log
		AnswerQuestions bool // answer an agent's AskUserQuestion from the inbox; undocumented Claude Code behaviour
	}
	Restore struct {
		Ask bool // after a restart, ask once whether to reopen the agents that were live
	}
	Notify struct {
		NeedsYou   bool // a permission, plan or question waits on you
		Remind     int  // minutes before one more notification for something still waiting; 0 never
		Done       bool // the top agent of a branch finished or reported back
		Failed     bool // a turn ended on an API error, or tool calls keep failing
		Stalled    bool // an agent working with no hook event for a while
		WithoutDen bool // den hook notifies needs-you itself when Den is not running
	}
	Stuck struct {
		Stalled      int // minutes working with no hook event before an agent counts as stalled; 0 off
		ToolFailures int // tool calls failing in a row before the inbox says so; 0 off
	}
	Events struct {
		Log bool // den hook appends metadata-only events to events.jsonl
	}
	Usage struct {
		// StopPercent stops the fleet at that share of the weekly limit:
		// "USAGE STOP" to every agent, and no new agents or mission runs
		// until you resume. 0 (the default) never stops.
		StopPercent int
		// WeeklyUSD is a weekly $ budget Den estimates usage against when
		// Claude Code reports none; 0 for none.
		WeeklyUSD float64
	}
	// API is den-api.sock, Den's local API for apps on this machine. Off
	// unless the human turns it on (/api on in Den writes enabled = true).
	API struct {
		Enabled      bool
		AskBudgetUSD float64 // what apps' questions may spend a day
		// ConsolePowers is when the dock, pairing by itself, comes with
		// its powers: "unchanged" (the program, unit and pins Den last gave
		// them to in this run) or "always".
		ConsolePowers string
	}
	// On holds a shell command per event kind, run by Den with the event's
	// metadata. Empty runs nothing.
	On struct {
		SessionStart, SessionEnd, TurnStart, Done, NeedsYou, Idle, TurnFailed, ToolFailed, Stalled, StillWaiting string
	}
}

// OnCommand is the [on] command for an event kind, "" for none.
func (c Config) OnCommand(kind string) string {
	switch kind {
	case "session_start":
		return c.On.SessionStart
	case "session_end":
		return c.On.SessionEnd
	case "turn_start":
		return c.On.TurnStart
	case "done":
		return c.On.Done
	case "needs_you":
		return c.On.NeedsYou
	case "idle":
		return c.On.Idle
	case "turn_failed":
		return c.On.TurnFailed
	case "tool_failed":
		return c.On.ToolFailed
	case "stalled":
		return c.On.Stalled
	case "still_waiting":
		return c.On.StillWaiting
	}
	return ""
}

// Defaults are what you get with no file and no environment.
func Defaults() Config {
	var c Config
	c.Orchestrator.Model = "sonnet"
	c.Orchestrator.Name = "den-orchestrator"
	c.Orchestrator.Spawn = true
	c.Agents.PermissionMode = permmode.Shipped
	c.Inbox.History = 100
	c.Detail.Recent = 20
	c.Hooks.PermissionWait = 580
	c.Hooks.Log = true
	c.Restore.Ask = true
	c.Notify.NeedsYou = true
	c.Notify.Remind = 10
	c.Stuck.Stalled = 20
	c.Stuck.ToolFailures = 3
	c.Events.Log = true
	c.API.AskBudgetUSD = 2
	c.API.ConsolePowers = "unchanged"
	return c
}

// Path is the config file: $DEN_CONFIG, else $XDG_CONFIG_HOME/den/config.toml.
func Path() string {
	if p := os.Getenv("DEN_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "den", "config.toml")
}

// Load reads Path, then applies environment overrides. A missing file is
// fine; a malformed line is skipped.
func Load() Config {
	c := Defaults()
	c.apply(parse(Path()))
	c.env()
	return c
}

// GodKeys are the config keys that look like God mode settings: a [god]
// section, or a key with "god" in its name. Den reads none of them (God
// mode is turned on only in Den's own window); it names them loudly.
func GodKeys() []string {
	var out []string
	for k := range parse(Path()) {
		if strings.HasPrefix(k, "god.") || strings.Contains(strings.ToLower(k), "god") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Prices is the [prices] section: a model-id prefix to "input, output,
// cache write, cache read" in dollars per million tokens, overriding the
// cost estimate's built-in table.
func Prices() map[string]string {
	out := map[string]string{}
	for k, v := range parse(Path()) {
		if model, ok := strings.CutPrefix(k, "prices."); ok && v != "" {
			out[model] = v
		}
	}
	return out
}

// DesktopApps is the [desktop.apps.<name>] sections: the apps the human
// allows desktop missions to use, as raw values by app name and key
// (command = ["zen-browser", "--new-instance"], class = "zen", ...).
// internal/desktop reads them; nothing here interprets them.
func DesktopApps() map[string]map[string]string {
	out := map[string]map[string]string{}
	for k, v := range parse(Path()) {
		rest, ok := strings.CutPrefix(k, "desktop.apps.")
		if !ok {
			continue
		}
		name, key, ok := strings.Cut(rest, ".")
		if !ok || name == "" || key == "" {
			continue
		}
		if out[name] == nil {
			out[name] = map[string]string{}
		}
		out[name][key] = v
	}
	return out
}

// Task mission limits when the [desktop] section sets none.
const (
	DefaultTaskMinutes = 20
	DefaultTaskActions = 300
)

// DesktopTask is a task mission's limits from the [desktop] section:
// task_minutes of wall time and task_actions forwarded desktop calls. They
// are the human's alone: read from this file when a task starts, with no
// environment override and nothing an agent or tool can set. A value that
// is not a positive whole number keeps the default.
func DesktopTask() (minutes, actions int) {
	minutes, actions = DefaultTaskMinutes, DefaultTaskActions
	kv := parse(Path())
	if n, err := strconv.Atoi(kv["desktop.task_minutes"]); err == nil && n > 0 {
		minutes = n
	}
	if n, err := strconv.Atoi(kv["desktop.task_actions"]); err == nil && n > 0 {
		actions = n
	}
	return minutes, actions
}

func (c *Config) apply(kv map[string]string) {
	str := func(dst *string, key string) {
		if v, ok := kv[key]; ok && v != "" {
			*dst = v
		}
	}
	num := func(dst *int, key string) {
		if v, ok := kv[key]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				*dst = n
			}
		}
	}
	// count is num where 0 is a value: it turns the setting off.
	count := func(dst *int, key string) {
		if v, ok := kv[key]; ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				*dst = n
			}
		}
	}
	boolean := func(dst *bool, key string) {
		if v, ok := kv[key]; ok {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	str(&c.Orchestrator.Model, "orchestrator.model")
	str(&c.Orchestrator.Name, "orchestrator.name")
	boolean(&c.Orchestrator.Spawn, "orchestrator.spawn")
	str(&c.Orchestrator.Guidance, "orchestrator.guidance")
	if v, ok := kv["agents.permission_mode"]; ok {
		if mode, ok := permmode.Parse(v); ok {
			c.Agents.PermissionMode = mode
		} else {
			c.Agents.ModeRefused = v
		}
	}
	num(&c.Inbox.History, "inbox.history")
	num(&c.Detail.Recent, "detail.recent")
	num(&c.Hooks.PermissionWait, "hooks.permission_wait")
	boolean(&c.Hooks.Log, "hooks.log")
	boolean(&c.Hooks.AnswerQuestions, "hooks.answer_questions")
	boolean(&c.Restore.Ask, "restore.ask")
	boolean(&c.Notify.NeedsYou, "notify.needs_you")
	count(&c.Notify.Remind, "notify.remind")
	boolean(&c.Notify.Done, "notify.done")
	boolean(&c.Notify.Failed, "notify.failed")
	boolean(&c.Notify.Stalled, "notify.stalled")
	boolean(&c.Notify.WithoutDen, "notify.without_den")
	count(&c.Stuck.Stalled, "stuck.stalled")
	count(&c.Stuck.ToolFailures, "stuck.tool_failures")
	boolean(&c.Events.Log, "events.log")
	if v, ok := kv["usage.stop_percent"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			c.Usage.StopPercent = n
		}
	}
	if v, ok := kv["usage.weekly_usd"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f < 1e9 {
			c.Usage.WeeklyUSD = f
		}
	}
	boolean(&c.API.Enabled, "api.enabled")
	if v, ok := kv["api.ask_budget_usd"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			c.API.AskBudgetUSD = f
		}
	}
	if v := kv["api.console_powers"]; v == "always" || v == "unchanged" {
		c.API.ConsolePowers = v
	}
	for key, dst := range map[string]*string{
		"session_start": &c.On.SessionStart, "session_end": &c.On.SessionEnd, "turn_start": &c.On.TurnStart,
		"done": &c.On.Done, "needs_you": &c.On.NeedsYou, "idle": &c.On.Idle, "turn_failed": &c.On.TurnFailed,
		"tool_failed": &c.On.ToolFailed, "stalled": &c.On.Stalled, "still_waiting": &c.On.StillWaiting,
	} {
		str(dst, "on."+key)
	}
}

// env applies the overrides that predate the file, so scripts keep working.
func (c *Config) env() {
	if v := os.Getenv("DEN_ORCHESTRATOR_MODEL"); v != "" {
		c.Orchestrator.Model = v
	}
	if os.Getenv("DEN_ORCHESTRATOR_SPAWN") == "off" {
		c.Orchestrator.Spawn = false
	}
	if v := os.Getenv("DEN_PERMISSION_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Hooks.PermissionWait = n
		}
	}
	if os.Getenv("DEN_HOOK_LOG") == "off" {
		c.Hooks.Log = false
	}
	if os.Getenv("DEN_RESTORE") == "off" {
		c.Restore.Ask = false
	}
	if os.Getenv("DEN_NOTIFY") == "off" {
		c.Notify.NeedsYou, c.Notify.Done, c.Notify.Failed, c.Notify.Stalled, c.Notify.WithoutDen = false, false, false, false, false
		c.Notify.Remind = 0
	}
}

// SetKey writes one key of the config file, for the settings Den changes
// on the human's own command (/api on). value is written as given (a
// boolean or number; a string must come quoted). The key's line in its
// section is replaced, or added under the section, which is added at the
// end when it is missing; every other line stays as it was. The file is
// replaced atomically and keeps its mode (0644 for a new one).
func SetKey(section, key, value string) error {
	path := Path()
	if path == "" {
		return errors.New("no config path")
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	line := key + " = " + value
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	cur, inSection, header, done := "", false, -1, false
	for i, l := range lines {
		t := strings.TrimSpace(stripComment(l))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			cur = strings.TrimSpace(t[1 : len(t)-1])
			inSection = cur == section
			if inSection && header < 0 {
				header = i
			}
			continue
		}
		if !inSection {
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			lines[i] = line
			done = true
			break
		}
	}
	switch {
	case done:
	case header >= 0:
		lines = append(lines[:header+1], append([]string{line}, lines[header+1:]...)...)
	default:
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", line)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GuidanceSetting reads orchestrator.guidance from the file as it is now,
// for Den's watch on it: "" when the key or the file is gone. A file that
// is there but does not read is an error, and the setting in use stays.
// No environment variable sets it.
func GuidanceSetting() (string, error) {
	f, err := os.Open(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	kv, err := parseFrom(f)
	if err != nil {
		return "", err
	}
	return kv["orchestrator.guidance"], nil
}

// ModeSetting reads agents.permission_mode from the file as it is now, for
// Den's watch on it: the mode (Default when the key or the file is gone),
// or the value as written when it is not a mode the global mode may take.
// A file that is there but does not read is an error. No environment
// variable sets it.
func ModeSetting() (mode, refused string, err error) {
	f, err := os.Open(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return permmode.Shipped, "", nil
	}
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	kv, err := parseFrom(f)
	if err != nil {
		return "", "", err
	}
	v, ok := kv["agents.permission_mode"]
	if !ok {
		return permmode.Shipped, "", nil
	}
	if mode, ok := permmode.Parse(v); ok {
		return mode, "", nil
	}
	return "", v, nil
}

// parse reads a flat TOML subset: [section] headers and key = value lines
// with string, integer or boolean values. Keys come back as section.key.
func parse(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	out, _ = parseFrom(f)
	return out
}

// parseFrom is parse on an open file; the error is the read's.
func parseFrom(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(stripComment(sc.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
		}
		if section != "" {
			k = section + "." + k
		}
		out[k] = v
	}
	return out, sc.Err()
}

// Example is the documented starting point written by `den config init`.
const Example = `# Den configuration. Every key is optional; these are the defaults.

[orchestrator]
model = "sonnet"            # any claude --model value, or "off" for Claude Code's default
name = "den-orchestrator"   # how the orchestrator session names itself to peers
spawn = true                # may open terminals and start agents on the workspace you are on
guidance = ""               # off; "i-have-adhd" or /abs/path/to/your.md: text that shapes its answers, at most
                            # 8192 bytes after frontmatter, loaded only once you approve its SHA-256 in the inbox

[agents]
permission_mode = "auto"    # the mode agents Den starts run in: auto, acceptEdits, plan, default or dontAsk;
                            # /mode in Den changes it, and a change to a mode that asks less waits for your ^Y

[inbox]
history = 100               # finished turns kept as history

[detail]
recent = 20                 # activity entries shown in the details pane

[hooks]
permission_wait = 580       # seconds Den may hold a permission; the terminal dialog then answers alone
log = true                  # append every hook run to ~/.local/state/den/hooks.log
answer_questions = false    # answer an agent's questions (AskUserQuestion) from the inbox; waits permission_wait

[restore]
ask = true                  # after a restart, ask once whether to reopen the agents that were live

[notify]
needs_you = true            # desktop notification when a permission, plan or question waits on you
remind = 10                 # minutes before one more for something still waiting; 0 never
done = false                # when the top agent of a branch finishes; its workers stay quiet
failed = false              # when a turn ends on an API error or tool calls keep failing
stalled = false             # when an agent has gone quiet while working
without_den = false         # let den hook notify needs-you itself while Den is not running

[stuck]
stalled = 20                # minutes working with no hook event before the inbox says stalled; 0 off
tool_failures = 3           # tool calls failing in a row before the inbox says so; 0 off

[events]
log = true                  # den hook appends metadata-only events to ~/.local/state/den/events.jsonl

[usage]
stop_percent = 0            # off; e.g. 95 sends "USAGE STOP" to every agent at 95% of the weekly limit and holds new work
weekly_usd = 0              # none; a weekly $ budget to estimate usage against when Claude Code reports none

[api]
enabled = false             # den-api.sock for apps on this machine; turn it on with /api on in Den
ask_budget_usd = 2          # what apps' questions to the read-only orchestrator may spend a day
console_powers = "unchanged" # the dock pairs by itself; "unchanged": with its powers only as Den last gave them in this run, "always": whatever changed

# A shell command per event kind, run by Den with the event's metadata as
# DEN_* variables and as JSON on stdin. Nothing runs unless you add one.
# Kinds: session_start session_end turn_start done needs_you idle
# turn_failed tool_failed stalled still_waiting. See docs/EVENTS.md.
[on]
# needs_you = "ntfy publish my-den-topic \"$DEN_AGENT needs you\""

# The cost estimate prices tokens from a built-in table (taken 2026-09-25).
# Override a model, by id prefix, in dollars per million tokens:
# input, output, cache write, cache read. The last two may be left out.
# [prices]
# claude-opus-5-5 = "4, 20, 5, 0.2"
`

// stripComment drops a trailing # comment that is not inside quotes.
func stripComment(line string) string {
	inQuote, escaped := false, false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == '#' && !inQuote:
			return line[:i]
		}
	}
	return line
}
