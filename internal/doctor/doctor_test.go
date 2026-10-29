package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/hooks"
)

const home = "/home/alice"

// settings is a settings.json with Den's hooks for every event, running den.
func settings(den string, skip ...string) string {
	h := map[string]any{}
	for _, ev := range hooks.Events() {
		if len(skip) > 0 && ev == skip[0] {
			continue
		}
		e := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": den + " hook"}}}
		if ev == "PreToolUse" {
			e["matcher"] = "AskUserQuestion"
		}
		h[ev] = []any{e}
	}
	raw, _ := json.Marshal(map[string]any{"hooks": h})
	return string(raw)
}

// healthy is an Env in which every check passes.
func healthy() Env {
	files := map[string]string{
		home + "/.claude/settings.json":      settings(home + "/.local/bin/den"),
		home + "/.local/state/den/hooks.log": "2026-10-28 18:40:00 Stop               0123abcd delivered\n",
	}
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	cfg := config.Defaults()
	return Env{
		Home:   home,
		Getenv: func(k string) string { return map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "sig"}[k] },
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Version: func(p string) (string, error) {
			if strings.HasSuffix(p, "/git") {
				return "git version 2.47.1\n", nil
			}
			return "2.1.309 (Claude Code)\n", nil
		},
		ReadFile: read,
		Tail:     func(p string, n int64) ([]byte, error) { return read(p) },
		Exe:      home + "/.local/bin/den",
		Running:  func() bool { return true },
		Sessions: func() (int, error) { return 3, nil },
		Mods:     func() ([]Mod, error) { return []Mod{{Name: "agent-badge", Bundled: "1.2.0", Installed: "1.2.0"}}, nil },
		Remotes:  func() ([]Remote, error) { return nil, nil },
		Config:   cfg,
		Issues:   func() ([]config.Issue, error) { return nil, nil },
		Now:      time.Date(2026, 10, 28, 19, 0, 0, 0, time.Local),
	}
}

func byID(rs []Result, id string) Result {
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	return Result{}
}

func TestHealthyRunFailsNothing(t *testing.T) {
	rs := Run(healthy())
	if Failed(rs) {
		t.Errorf("a healthy setup failed:\n%s", Text(rs))
	}
	for _, r := range rs {
		if r.Status != OK && r.Status != Skip {
			t.Errorf("%s: %s %s", r.ID, r.Status, r.Summary)
		}
		if r.Fix != "" {
			t.Errorf("%s: an ok result has a fix: %s", r.ID, r.Fix)
		}
	}
	if len(rs) != 13 {
		t.Errorf("%d checks, want 13", len(rs))
	}
}

func TestChecks(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("exec: not found") }
	for _, tc := range []struct {
		name   string
		id     string
		edit   func(*Env)
		status string
		want   string // in the summary
	}{
		// claude: the version edges
		{"claude missing", "claude", func(e *Env) { e.LookPath = notFound }, Fail, "not on PATH"},
		{"claude 2.1.288", "claude", claudeVersion("2.1.288 (Claude Code)"), Fail, "2.1.288 is older than 2.1.289"},
		{"claude 2.1.289", "claude", claudeVersion("2.1.289 (Claude Code)"), OK, "2.1.289 on PATH"},
		{"claude 2.1.309", "claude", claudeVersion("2.1.309 (Claude Code)"), OK, "2.1.309 on PATH"},
		{"claude 2.1.310", "claude", claudeVersion("2.1.310 (Claude Code)"), Warn, "2.1.310 is newer than 2.1.309"},
		{"claude 2.2.0", "claude", claudeVersion("2.2.0 (Claude Code)"), Warn, "newer"},
		{"claude unparseable", "claude", claudeVersion("Claude Code, nightly\n"), Warn, "unparseable"},
		{"claude timed out", "claude", func(e *Env) { e.Version = func(string) (string, error) { return "", ErrTimeout } }, Warn, "timed out"},

		// sessions
		{"sessions none", "sessions", func(e *Env) { e.Sessions = func() (int, error) { return 0, nil } }, Warn, "no live sessions"},
		{"sessions one", "sessions", func(e *Env) { e.Sessions = func() (int, error) { return 1, nil } }, OK, "1 live session"},
		{"sessions folder missing", "sessions", func(e *Env) { e.Sessions = func() (int, error) { return 0, fs.ErrNotExist } }, Warn, "~/.claude/sessions is missing"},
		{"sessions unreadable", "sessions", func(e *Env) { e.Sessions = func() (int, error) { return 0, fs.ErrPermission } }, Fail, "is unreadable"},

		// hooks
		{"hooks absent", "hooks", setFile(home+"/.claude/settings.json", ""), Fail, "not in ~/.claude/settings.json"},
		{"hooks none", "hooks", setFile(home+"/.claude/settings.json", `{"model":"x"}`), Fail, "not in"},
		{"hooks one missing", "hooks", setFile(home+"/.claude/settings.json", settings(home+"/.local/bin/den", "Stop")), Fail, "miss 1 of 10 events: Stop"},
		{"hooks another den", "hooks", setFile(home+"/.claude/settings.json", settings(home+"/old/den")), Warn, "another den than this one (~/…/den)"},
		{"hooks den outside home", "hooks", func(e *Env) {
			setFile(home+"/.claude/settings.json", settings("/opt/den"))(e)
			e.Exe = "/srv/build/den"
		}, Warn, "(a den outside your home)"},
		{"hooks unparseable", "hooks", setFile(home+"/.claude/settings.json", `{"hooks":`), Fail, "~/.claude/settings.json does not parse"},
		{"hooks unreadable", "hooks", func(e *Env) {
			e.ReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
		}, Fail, "settings.json is unreadable"},
		{"hooks moved config dir", "hooks", func(e *Env) {
			e.Getenv = func(k string) string { return map[string]string{"CLAUDE_CONFIG_DIR": "/cfg"}[k] }
		}, Fail, "not in <CLAUDE_CONFIG_DIR>/settings.json"},

		// den
		{"den stopped", "den", func(e *Env) { e.Running = func() bool { return false } }, Warn, "not running"},

		// hooks.log
		{"hooks.log missed", "hooks.log", setFile(home+"/.local/state/den/hooks.log",
			"2026-10-28 18:30:00 Stop               0123abcd delivered\n"+
				"2026-10-28 18:39:00 Stop               0123abcd no den listening\n"+
				"2026-10-28 18:40:00 SessionStart       0123abcd no den listening\n"), Warn, "2 of the last 3 hook runs found no Den listening (last 20 min ago)"},
		{"hooks.log fine", "hooks.log", nil, OK, "the last hook run reached Den"},
		{"hooks.log only the last 50", "hooks.log", setFile(home+"/.local/state/den/hooks.log",
			strings.Repeat("2026-10-28 18:00:00 Stop               0123abcd no den listening\n", 10)+
				strings.Repeat("2026-10-28 18:30:00 Stop               0123abcd delivered\n", 50)), OK, "the last 50 hook runs reached Den"},
		{"hooks.log none yet", "hooks.log", setFile(home+"/.local/state/den/hooks.log", ""), OK, "no hook has run yet"},
		{"hooks.log off", "hooks.log", func(e *Env) { e.Config.Hooks.Log = false }, Skip, "off in config.toml"},

		// focus
		{"focus not hyprland", "focus", func(e *Env) {
			e.Getenv = func(string) string { return "" }
			e.LookPath = lookExcept("hyprctl")
		}, Skip, "not on Hyprland"},
		{"focus no hyprctl", "focus", func(e *Env) { e.LookPath = lookExcept("hyprctl") }, Warn, "hyprctl is not on PATH"},
		{"focus no signature", "focus", func(e *Env) { e.Getenv = func(string) string { return "" } }, Warn, "HYPRLAND_INSTANCE_SIGNATURE is not set"},

		// terminal: spawn.launch's order
		{"terminal xdg", "terminal", nil, OK, "xdg-terminal-exec"},
		{"terminal $TERMINAL", "terminal", func(e *Env) {
			e.LookPath = lookExcept("xdg-terminal-exec")
			e.Getenv = func(k string) string { return map[string]string{"TERMINAL": "foot"}[k] }
		}, OK, "$TERMINAL"},
		{"terminal TERMINAL=xdg-terminal-exec", "terminal", func(e *Env) {
			e.LookPath = lookExcept("xdg-terminal-exec")
			e.Getenv = func(k string) string { return map[string]string{"TERMINAL": "xdg-terminal-exec"}[k] }
		}, Fail, "no terminal launcher"},
		{"terminal none", "terminal", func(e *Env) { e.LookPath = lookExcept("xdg-terminal-exec") }, Fail, "no terminal launcher"},

		// notify
		{"notify missing", "notify", func(e *Env) { e.LookPath = lookExcept("notify-send") }, Warn, "notify-send is not on PATH"},

		// git
		{"git missing", "git", func(e *Env) { e.LookPath = lookExcept("git") }, Fail, "git is not on PATH"},
		{"git 2.30", "git", gitVersion("git version 2.30.9\n"), Fail, "2.30.9 is older than 2.31"},
		{"git 2.31", "git", gitVersion("git version 2.31.0\n"), Warn, "older than 2.38: Review's merge check is off"},
		{"git 2.38", "git", gitVersion("git version 2.38.0\n"), OK, "git 2.38.0"},
		{"git apple", "git", gitVersion("git version 2.39.5 (Apple Git-154)\n"), OK, "git 2.39.5"},
		{"git unparseable", "git", gitVersion("hub version 3\n"), Warn, "unparseable"},

		// mods
		{"mods not installed", "mods", withMods(Mod{Name: "agent-badge", Bundled: "1.2.0"}), Warn, "agent-badge 1.2.0 is not installed"},
		{"mods old", "mods", withMods(Mod{Name: "agent-badge", Bundled: "1.2.0", Installed: "1.1.0"}), Warn, "agent-badge 1.1.0 is installed, this den has 1.2.0"},
		{"mods current", "mods", nil, OK, "agent-badge 1.2.0 installed"},
		{"mods loaded from a plugin dir", "mods", withMods(Mod{Name: "agent-badge", Bundled: "1.2.0", Loaded: true}), OK, "loaded through CLAUDE_CODE_PLUGIN_DIRS"},
		{"mods odd version", "mods", withMods(Mod{Name: "agent-badge", Bundled: "1.2.0", Installed: "1.2.0 /home/alice"}), Warn, "(unparseable version) is installed"},

		// remotes: saved state only
		{"remotes none", "remotes", nil, Skip, "no remote hosts"},
		{"remotes fine", "remotes", withRemotes(Remote{Enabled: true, Saved: true, State: "live", Heartbeat: time.Date(2026, 10, 28, 18, 59, 50, 0, time.Local)}), OK, "1 of 1 hosts enabled"},
		{"remotes stale", "remotes", withRemotes(Remote{Enabled: true, Saved: true, State: "live", Heartbeat: time.Date(2026, 10, 28, 18, 50, 0, 0, time.Local)}), Warn, "host 1 has not been confirmed"},
		{"remotes stale but Den stopped", "remotes", func(e *Env) {
			withRemotes(Remote{Enabled: true, Saved: true, State: "live", Heartbeat: time.Date(2026, 10, 28, 18, 50, 0, 0, time.Local)})(e)
			e.Running = func() bool { return false }
		}, OK, "none needs you"},
		{"remotes mismatch", "remotes", withRemotes(Remote{Enabled: true}, Remote{Enabled: true, Saved: true, State: "version_mismatch"}), Warn, "host 2 has a version mismatch"},
		{"remotes needs you", "remotes", withRemotes(Remote{Enabled: true, Saved: true, State: "needs_attention"}), Warn, "host 1 needs you"},
		{"remotes disabled", "remotes", withRemotes(Remote{Enabled: false, Saved: true, State: "needs_attention"}), OK, "0 of 1 hosts enabled"},
		{"remotes unparseable", "remotes", func(e *Env) { e.Remotes = func() ([]Remote, error) { return nil, ErrParse } }, Warn, "remotes.toml does not parse"},

		// orchestrator
		{"orchestrator", "orchestrator", func(e *Env) { e.Config.Orchestrator.Spawn = false }, OK, "model sonnet, spawning off"},

		// config
		{"config issues", "config", func(e *Env) {
			e.Issues = func() ([]config.Issue, error) {
				return config.CheckKeys(map[string]string{"notify.remind": "soon", "notify.x": "1", "god.mode": "on"}), nil
			}
		}, Warn, "a God mode key, which Den never reads"},
		{"config unreadable", "config", func(e *Env) { e.Issues = func() ([]config.Issue, error) { return nil, fs.ErrPermission } }, Warn, "config.toml is unreadable"},
	} {
		e := healthy()
		if tc.edit != nil {
			tc.edit(&e)
		}
		r := byID(Run(e), tc.id)
		if r.Status != tc.status || !strings.Contains(r.Summary, tc.want) {
			t.Errorf("%s: %s %q, want %s containing %q", tc.name, r.Status, r.Summary, tc.status, tc.want)
		}
		if (r.Status == Warn || r.Status == Fail) != (r.Fix != "") {
			t.Errorf("%s: status %s with fix %q", tc.name, r.Status, r.Fix)
		}
	}
}

func claudeVersion(out string) func(*Env) {
	return func(e *Env) {
		e.Version = func(p string) (string, error) {
			if strings.HasSuffix(p, "/git") {
				return "git version 2.47.1\n", nil
			}
			return out, nil
		}
	}
}

func gitVersion(out string) func(*Env) {
	return func(e *Env) {
		e.Version = func(p string) (string, error) {
			if strings.HasSuffix(p, "/git") {
				return out, nil
			}
			return "2.1.309 (Claude Code)\n", nil
		}
	}
}

func setFile(path, body string) func(*Env) {
	return func(e *Env) {
		prev := e.ReadFile
		e.ReadFile = func(p string) ([]byte, error) {
			if p == path {
				if body == "" && strings.HasSuffix(p, ".json") {
					return nil, fs.ErrNotExist
				}
				return []byte(body), nil
			}
			return prev(p)
		}
		e.Tail = func(p string, n int64) ([]byte, error) { return e.ReadFile(p) }
	}
}

func lookExcept(missing string) func(string) (string, error) {
	return func(name string) (string, error) {
		if name == missing {
			return "", errors.New("exec: " + name + ": not found")
		}
		return "/usr/bin/" + name, nil
	}
}

func withMods(ms ...Mod) func(*Env) {
	return func(e *Env) { e.Mods = func() ([]Mod, error) { return ms, nil } }
}

func withRemotes(rs ...Remote) func(*Env) {
	return func(e *Env) { e.Remotes = func() ([]Remote, error) { return rs, nil } }
}

func TestHooksLogTailDropsTheCutLine(t *testing.T) {
	e := healthy()
	line := "2026-10-28 18:30:00 Stop               0123abcd delivered\n"
	body := strings.Repeat(line, logTail/len(line)+1)
	cut := "en listening\n" + body[len(body)-logTail+len("en listening\n"):]
	e.Tail = func(string, int64) ([]byte, error) { return []byte(cut[:logTail]), nil }
	if r := byID(Run(e), "hooks.log"); r.Status != OK {
		t.Errorf("a cut first line counted: %s %s", r.Status, r.Summary)
	}
}

func TestFailedAndText(t *testing.T) {
	rs := []Result{{ID: "a", Status: OK, Summary: "fine"}, {ID: "b", Status: Warn, Summary: "meh", Fix: "do x"}}
	if Failed(rs) {
		t.Error("warn is not a failure")
	}
	want := "ok    a            fine\nwarn  b            meh\n                   fix: do x\n"
	if got := Text(rs); got != want {
		t.Errorf("Text =\n%q\nwant\n%q", got, want)
	}
	if !Failed(append(rs, Result{Status: Fail})) {
		t.Error("fail is a failure")
	}
}

func ExampleText() {
	fmt.Print(Text([]Result{{ID: "hooks", Status: Fail, Summary: "Den's hooks are not in ~/.claude/settings.json", Fix: "den install-hooks"}}))
	// Output:
	// fail  hooks        Den's hooks are not in ~/.claude/settings.json
	//                    fix: den install-hooks
}
