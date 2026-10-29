package doctor

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/config"
)

// Identifying values a fake machine is full of: none may reach the output.
var secrets = []string{
	"alice",          // the user, in every home path
	"devbox-secret",  // a hostname
	"acme-project",   // a project folder
	"0123abcd",       // a session id
	"4242",           // a pid
	"secret-host",    // a remote host's label
	"203.0.113.7",    // an address
	"\u202e",         // a bidi override
	"\x1b[31m",       // a terminal escape
	"/srv/cc-config", // a moved CLAUDE_CONFIG_DIR
	"/var/xdg-state", // a moved XDG_STATE_HOME
	"opus-of-alice",  // a model name of the human's own
}

// leaky is an Env whose every string from outside Den carries identifying
// values, and whose every error does too (D1, den-sec-review).
func leaky(variant int) Env {
	home := "/home/alice"
	errWith := func(base error) error {
		return &fs.PathError{Op: "open", Path: "/home/alice/acme-project/devbox-secret 0123abcd 4242 secret-host 203.0.113.7", Err: base}
	}
	env := map[string]string{
		"CLAUDE_CONFIG_DIR":           "/srv/cc-config/alice",
		"XDG_STATE_HOME":              "/var/xdg-state/alice",
		"HYPRLAND_INSTANCE_SIGNATURE": "devbox-secret",
		"TERMINAL":                    "/home/alice/acme-project/term",
		"HOSTNAME":                    "devbox-secret",
		"USER":                        "alice",
	}
	logLine := "2026-10-28 18:40:00 Stop\u202e               0123abcd no den listening\n"
	e := Env{
		Home:     home,
		Getenv:   func(k string) string { return env[k] },
		LookPath: func(n string) (string, error) { return "/home/alice/acme-project/bin/" + n, nil },
		Version: func(p string) (string, error) {
			return "2.1.309 (Claude Code) on devbox-secret for alice \u202e\x1b[31m 0123abcd\n", nil
		},
		ReadFile: func(p string) ([]byte, error) {
			return []byte(settings("/home/alice/acme-project/old/den")), nil
		},
		Tail:     func(string, int64) ([]byte, error) { return []byte(strings.Repeat(logLine, 3)), nil },
		Exe:      "/home/alice/acme-project/devbox-secret/den",
		Running:  func() bool { return true },
		Sessions: func() (int, error) { return 7, nil }, // a count is printed; 4242 is a pid in the errors
		Mods: func() ([]Mod, error) {
			return []Mod{{Name: "agent-badge", Bundled: "1.2.0", Installed: "1.1.0 alice@devbox-secret\u202e"}}, nil
		},
		Remotes: func() ([]Remote, error) {
			return []Remote{{Enabled: true, Saved: true, State: "needs_attention"}, {Enabled: true, Saved: true, State: "version_mismatch"}}, nil
		},
		Config: config.Defaults(),
		Issues: func() ([]config.Issue, error) {
			return config.CheckKeys(map[string]string{
				"hosts.secret-host.target":              "alice@203.0.113.7",
				"notify.acme-project":                   "1",
				"god.devbox-secret":                     "on",
				"desktop.apps.acme-project.secret-host": "1",
				"prices.alice":                          "1, 2",
				"notify.remind":                         "devbox-secret",
			}), nil
		},
		Now: time.Date(2026, 10, 28, 19, 0, 0, 0, time.Local),
	}
	e.Config.Orchestrator.Model = "opus-of-alice"
	switch variant {
	case 1: // every seam fails with an error that names things
		e.LookPath = func(n string) (string, error) {
			if n == "claude" || n == "git" {
				return "/home/alice/acme-project/bin/" + n, nil
			}
			return "", errWith(errors.New("not found"))
		}
		e.Version = func(string) (string, error) { return "", errWith(errors.New("exit status 1: alice devbox-secret")) }
		e.ReadFile = func(string) ([]byte, error) { return nil, errWith(fs.ErrPermission) }
		e.Tail = func(string, int64) ([]byte, error) { return nil, errWith(fs.ErrPermission) }
		e.Sessions = func() (int, error) { return 0, errWith(fs.ErrPermission) }
		e.Mods = func() ([]Mod, error) { return nil, errWith(fs.ErrInvalid) }
		e.Remotes = func() ([]Remote, error) { return nil, errWith(ErrParse) }
		e.Issues = func() ([]config.Issue, error) { return nil, errWith(fs.ErrPermission) }
		e.Exe = "/srv/acme-project/den"
	case 2: // a settings.json that does not parse, with names in it; a timed-out version
		e.ReadFile = func(string) ([]byte, error) { return []byte(`{"alice": "devbox-secret` + "\u202e"), nil }
		e.Version = func(string) (string, error) { return "", errWith(ErrTimeout) }
		e.Sessions = func() (int, error) { return 0, errWith(fs.ErrNotExist) }
	case 3: // versions that parse to nothing, with names in them
		e.Version = func(p string) (string, error) {
			return "alice@devbox-secret: 0123abcd \u202e 203.0.113.7\n2.1.309\n", nil
		}
	}
	return e
}

func TestOutputCarriesNoIdentifyingValues(t *testing.T) {
	for variant := 0; variant <= 3; variant++ {
		rs := Run(leaky(variant))
		raw, err := json.Marshal(rs)
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{Text(rs), string(raw)} {
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Errorf("variant %d: the output holds %q:\n%s", variant, s, out)
				}
			}
		}
	}
}

// L1 (den-sec-review): the orchestrator's model is printed only when it is
// a Claude Code alias or a claude-\u2026 id; anything else can name an account
// or a company.
func TestModelShownOnlyWhenKnown(t *testing.T) {
	for model, want := range map[string]string{
		"haiku":               "haiku",
		"opus[1m]":            "opus[1m]",
		"off":                 "off",
		"claude-sonnet-5-5":   "claude-sonnet-5-5",
		"claude-opus-5-5[1m]": "claude-opus-5-5[1m]",
		"arn:aws:bedrock:eu-north-1:123456789012:inference-profile/eu.anthropic.claude-opus": "a custom model",
		"acme-internal-opus":                "a custom model",
		"opus\u202e\x1b[31m":                "a custom model",
		"claude-" + strings.Repeat("x", 41): "a custom model",
		"":                                  "a custom model",
	} {
		e := leaky(0)
		e.Config.Orchestrator.Model = model
		if r := byID(Run(e), "orchestrator"); !strings.HasPrefix(r.Summary, "model "+want+", ") {
			t.Errorf("model %q: %q, want it shown as %q", model, r.Summary, want)
		}
	}
}

// The paths that are printed are Den's own, from ~ or the variable that
// moved them.
func TestPathsAreShownFromTheirVariable(t *testing.T) {
	out := Text(Run(leaky(0)))
	for _, want := range []string{"<CLAUDE_CONFIG_DIR>/settings.json", "<CLAUDE_CONFIG_DIR>/sessions", "~/…/den"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	e := leaky(2)
	e.Config.Hooks.Log = true
	e.Tail = func(string, int64) ([]byte, error) { return nil, fs.ErrPermission }
	if r := byID(Run(e), "hooks.log"); !strings.Contains(r.Summary, "<XDG_STATE_HOME>/den/hooks.log is unreadable") {
		t.Errorf("hooks.log: %q", r.Summary)
	}
}
