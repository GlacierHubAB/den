package doctor

import (
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fileInfo is a fake Lstat result: a mode and an owner.
type fileInfo struct {
	name string
	mode fs.FileMode
	uid  uint32
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return 0 }
func (f fileInfo) Mode() fs.FileMode  { return f.mode }
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

const me = 1000

// setupEnv is a machine with a den at /home/alice/.local/bin/den, no hooks
// and agent-badge not installed; tree is every path Lstat finds.
func setupEnv() (Env, map[string]fileInfo) {
	e := healthy()
	e.Exe = home + "/.local/bin/den"
	e.UID = me
	tree := map[string]fileInfo{
		"/":                          {"/", fs.ModeDir | 0o755, 0},
		"/home":                      {"home", fs.ModeDir | 0o755, 0},
		home:                         {"alice", fs.ModeDir | 0o700, me},
		home + "/.local":             {".local", fs.ModeDir | 0o755, me},
		home + "/.local/bin":         {"bin", fs.ModeDir | 0o755, me},
		home + "/.local/bin/den":     {"den", 0o755, me},
		home + "/.claude":            {".claude", fs.ModeDir | 0o700, me},
		home + "/.claude/skills":     {"skills", fs.ModeDir | 0o755, me},
		home + "/.claude/skills/old": {"old", fs.ModeDir | 0o755, me},
	}
	e.Lstat = func(p string) (fs.FileInfo, error) {
		if fi, ok := tree[p]; ok {
			return fi, nil
		}
		return nil, fs.ErrNotExist
	}
	e.EvalSymlinks = func(p string) (string, error) { return p, nil }
	files := map[string]string{}
	e.ReadFile = func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
	e.Mods = func() ([]Mod, error) {
		return []Mod{{Name: "agent-badge", Bundled: "1.3.0", Target: home + "/.claude/skills/agent-badge"}}, nil
	}
	return e, tree
}

func TestPlanOffersWhatIsMissing(t *testing.T) {
	e, _ := setupEnv()
	p := MakePlan(e)
	if !p.Work() || !p.Hooks() || len(p.Missing) != 10 || p.Other || len(p.Mods) != 1 || p.NoY != "" {
		t.Fatalf("plan %+v", p)
	}
	if p.Settings != home+"/.claude/settings.json" || p.Mods[0].Target != home+"/.claude/skills/agent-badge" {
		t.Errorf("targets %q %q", p.Settings, p.Mods[0].Target)
	}

	// Everything in place: no card.
	e.ReadFile = func(string) ([]byte, error) { return []byte(settings(e.Exe)), nil }
	e.Mods = func() ([]Mod, error) {
		return []Mod{{Name: "agent-badge", Bundled: "1.3.0", Installed: "1.3.0", Target: home + "/.claude/skills/agent-badge"}}, nil
	}
	if p := MakePlan(e); p.Work() {
		t.Errorf("a setup with nothing missing has work: %+v", p)
	}
	// A mod loaded from a plugin folder is not copied again.
	e.Mods = func() ([]Mod, error) { return []Mod{{Name: "agent-badge", Bundled: "1.3.0", Loaded: true}}, nil }
	if p := MakePlan(e); p.Work() {
		t.Errorf("a loaded mod: %+v", p)
	}
	// Hooks that run another den.
	e.ReadFile = func(string) ([]byte, error) { return []byte(settings("/opt/old/den")), nil }
	if p := MakePlan(e); !p.Other || len(p.Missing) != 0 || !p.Work() {
		t.Errorf("another den: %+v", p)
	}
}

func TestPlanRefusesYWhenSettingsDoNotRead(t *testing.T) {
	for name, read := range map[string]func(string) ([]byte, error){
		"does not parse": func(string) ([]byte, error) { return []byte(`{"hooks":`), nil },
		"unreadable":     func(string) ([]byte, error) { return nil, fs.ErrPermission },
	} {
		e, _ := setupEnv()
		e.ReadFile = read
		p := MakePlan(e)
		if !p.Work() || p.NoY == "" || p.Hooks() {
			t.Errorf("%s: plan %+v", name, p)
		}
		body := strings.Join(p.Lines(e, true), "\n")
		if !strings.Contains(body, "No ^Y on this card") || strings.Contains(body, "^Y does exactly this") {
			t.Errorf("%s: card body offers ^Y:\n%s", name, body)
		}
	}
}

// D4 (den-sec-review): no y for a den others could change.
func TestPlanRefusesYForAnUnsafeDen(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(Env, map[string]fileInfo) Env
		want string // in NoY, "" for y offered
	}{
		{"safe", func(e Env, _ map[string]fileInfo) Env { return e }, ""},
		{"group-writable folder", func(e Env, t map[string]fileInfo) Env {
			t[home+"/.local/bin"] = fileInfo{"bin", fs.ModeDir | 0o775, me}
			return e
		}, "writable by others"},
		{"world-writable den", func(e Env, t map[string]fileInfo) Env {
			t[home+"/.local/bin/den"] = fileInfo{"den", 0o757, me}
			return e
		}, "writable by others"},
		{"someone else's folder", func(e Env, t map[string]fileInfo) Env {
			t[home+"/.local"] = fileInfo{".local", fs.ModeDir | 0o755, 1001}
			return e
		}, "is not yours"},
		{"root's folder outside home", func(e Env, t map[string]fileInfo) Env {
			t["/usr"], t["/usr/local"], t["/usr/local/bin"] = fileInfo{"usr", fs.ModeDir | 0o755, 0}, fileInfo{"local", fs.ModeDir | 0o755, 0}, fileInfo{"bin", fs.ModeDir | 0o755, 0}
			t["/usr/local/bin/den"] = fileInfo{"den", 0o755, 0}
			e.Exe = "/usr/local/bin/den"
			return e
		}, ""},
		{"in a git checkout", func(e Env, t map[string]fileInfo) Env {
			t[home+"/.local/.git"] = fileInfo{".git", fs.ModeDir | 0o755, me}
			return e
		}, "inside a git checkout"},
		{"a symlinked /home", func(e Env, t map[string]fileInfo) Env {
			t["/home"] = fileInfo{"home", fs.ModeSymlink | 0o777, 0}
			t["/var"], t["/var/home"] = fileInfo{"var", fs.ModeDir | 0o755, 0}, fileInfo{"home", fs.ModeDir | 0o755, 0}
			for _, p := range []string{home, home + "/.local", home + "/.local/bin", home + "/.local/bin/den"} {
				t["/var"+p] = t[p]
			}
			e.EvalSymlinks = func(p string) (string, error) { return "/var" + p, nil }
			return e
		}, ""},
		{"resolves into a writable folder", func(e Env, t map[string]fileInfo) Env {
			t["/srv"], t["/srv/den"] = fileInfo{"srv", fs.ModeDir | 0o755, 0}, fileInfo{"den", 0o755, me}
			t["/srv"] = fileInfo{"srv", fs.ModeDir | 0o777, 0}
			e.EvalSymlinks = func(string) (string, error) { return "/srv/den", nil }
			return e
		}, "/srv is writable by others"},
		{"temp build", func(e Env, _ map[string]fileInfo) Env {
			e.Exe = filepath.Join("/tmp", "go-build1", "den")
			return e
		}, "temporary build folder"},
	} {
		e, tree := setupEnv()
		e = c.edit(e, tree)
		p := MakePlan(e)
		if c.want == "" && p.NoY != "" || c.want != "" && !strings.Contains(p.NoY, c.want) {
			t.Errorf("%s: NoY %q, want %q", c.name, p.NoY, c.want)
		}
	}
}

// D7: no y when the skills folder or a mod's folder is a symbolic link.
func TestPlanRefusesYThroughASymlink(t *testing.T) {
	for _, p := range []string{home + "/.claude/skills", home + "/.claude/skills/agent-badge"} {
		e, tree := setupEnv()
		e.ReadFile = func(string) ([]byte, error) { return []byte(settings(e.Exe)), nil } // mods only
		tree[p] = fileInfo{filepath.Base(p), fs.ModeSymlink | 0o777, me}
		if plan := MakePlan(e); !strings.Contains(plan.NoY, p+" is a symbolic link") {
			t.Errorf("%s a symlink: NoY %q", p, plan.NoY)
		}
	}
}

// D5: Same tells a changed reading from the one shown.
func TestPlanSame(t *testing.T) {
	e, _ := setupEnv()
	a, b := MakePlan(e), MakePlan(e)
	if !a.Same(b) {
		t.Error("the same reading differs")
	}
	e.ReadFile = func(string) ([]byte, error) { return []byte(settings(e.Exe, "Stop")), nil }
	if a.Same(MakePlan(e)) {
		t.Error("a settings.json that changed reads the same")
	}
	e2, _ := setupEnv()
	e2.Exe = home + "/bin/den"
	if a.Same(MakePlan(e2)) {
		t.Error("another den reads the same")
	}
}

// D6: Den's window shows the real targets and the den; the copy
// den-api.sock lists shows neither (the paste-safe rules of den doctor).
func TestPlanLinesFullAndListed(t *testing.T) {
	e, _ := setupEnv()
	e.Getenv = func(k string) string { return map[string]string{"CLAUDE_CONFIG_DIR": "/srv/acme-project/cc"}[k] }
	e.Exe = home + "/acme-project/bin/den"
	p := MakePlan(e)
	full := strings.Join(p.Lines(e, true), "\n")
	for _, want := range []string{"/srv/acme-project/cc/settings.json", home + "/acme-project/bin/den hook", home + "/.claude/skills/agent-badge", "den uninstall-hooks"} {
		if !strings.Contains(full, want) {
			t.Errorf("Den's window lacks %q:\n%s", want, full)
		}
	}
	listed := strings.Join(p.Lines(e, false), "\n")
	for _, leak := range []string{"alice", "acme-project", "/srv", "/home"} {
		if strings.Contains(listed, leak) {
			t.Errorf("the listed copy holds %q:\n%s", leak, listed)
		}
	}
	for _, want := range []string{"<CLAUDE_CONFIG_DIR>/settings.json", "~/…/den hook", "~/.claude/skills/agent-badge"} {
		if !strings.Contains(listed, want) {
			t.Errorf("the listed copy lacks %q:\n%s", want, listed)
		}
	}
	// A reason for no y names real paths: the listed copy says only that
	// there is one.
	e.Exe = "/srv/acme-project/den"
	p = MakePlan(e)
	if p.NoY == "" {
		t.Fatal("a den in an unknown folder got y")
	}
	if listed := strings.Join(p.Lines(e, false), "\n"); strings.Contains(listed, "acme-project") || !strings.Contains(listed, "No ^Y") {
		t.Errorf("listed with no y:\n%s", listed)
	}
}

func TestDismissedLivesInDensState(t *testing.T) {
	e, _ := setupEnv()
	if got := Dismissed(e); got != home+"/.local/state/den/setup-dismissed" {
		t.Errorf("Dismissed = %q", got)
	}
	e.Getenv = func(k string) string { return map[string]string{"XDG_STATE_HOME": "/st"}[k] }
	if got := Dismissed(e); got != "/st/den/setup-dismissed" {
		t.Errorf("Dismissed with XDG_STATE_HOME = %q", got)
	}
}
