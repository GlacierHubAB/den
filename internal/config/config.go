// Package config reads ~/.config/den/config.toml. Every key has a default
// and an environment override, so the file is optional.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is everything Den lets you tune.
type Config struct {
	Orchestrator struct {
		Model string // claude --model; "off" keeps Claude Code's default
		Name  string // the orchestrator session's display name
		Spawn bool   // let it open terminals and start agents on your workspace
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

// parse reads a flat TOML subset: [section] headers and key = value lines
// with string, integer or boolean values. Keys come back as section.key.
func parse(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
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
	return out
}

// Example is the documented starting point written by `den config init`.
const Example = `# Den configuration. Every key is optional; these are the defaults.

[orchestrator]
model = "sonnet"            # any claude --model value, or "off" for Claude Code's default
name = "den-orchestrator"   # how the orchestrator session names itself to peers
spawn = true                # may open terminals and start agents on the workspace you are on

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
