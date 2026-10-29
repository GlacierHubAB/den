package ui

import (
	"bytes"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/doctor"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mods"
	"github.com/GlacierHubAB/den/internal/remote"
)

// realClaude is the real user's Claude Code folder, from the passwd entry,
// never $HOME: what no setup test may ever reach.
func realClaude(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Fatalf("no passwd home for the real-path guard: %v", err)
	}
	return filepath.Join(u.HomeDir, ".claude")
}

func under(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// isolateSetup gives a test its own HOME, Claude Code folder and XDG
// folders, and checks they are not the real user's.
func isolateSetup(t *testing.T) (home, claude string) {
	t.Helper()
	home, claude = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_PLUGIN_DIRS", "")
	real := realClaude(t)
	for _, p := range []string{hooks.SettingsPath(), mods.SkillsDir()} {
		if under(p, real) {
			t.Fatalf("%s is under the real user's %s", p, real)
		}
	}
	return home, claude
}

// TestSetupRealPathGuard is the always-on guard (den-sec-review): under
// the setup tests' isolation, neither the settings file nor any mod target
// is the real user's, by the passwd entry rather than $HOME.
func TestSetupRealPathGuard(t *testing.T) {
	isolateSetup(t)
	real := realClaude(t)
	e := doctor.System()
	p := doctor.MakePlan(e)
	if under(hooks.SettingsPath(), real) || under(p.Settings, real) || under(doctor.Dismissed(e), real) {
		t.Errorf("settings %s, plan %s, dismissed %s: one is under %s", hooks.SettingsPath(), p.Settings, doctor.Dismissed(e), real)
	}
	ms, _ := mods.List()
	for _, m := range ms {
		if under(m.Installs, real) {
			t.Errorf("mod %s installs into %s, under %s", m.Name, m.Installs, real)
		}
	}
}

// fi is a fake Lstat result with an owner.
type fi struct {
	mode fs.FileMode
	uid  int
}

func (f fi) Name() string       { return "x" }
func (f fi) Size() int64        { return 0 }
func (f fi) Mode() fs.FileMode  { return f.mode }
func (f fi) ModTime() time.Time { return time.Time{} }
func (f fi) IsDir() bool        { return f.mode.IsDir() }
func (f fi) Sys() any           { return &syscall.Stat_t{Uid: uint32(f.uid)} }

// setupModel is a model whose setup check reads the isolated folders, with
// a den at a stable, safe path, and an installer that records its calls
// (and refuses the real user's folders, on every call).
type setupRun struct {
	m      Model
	claude string
	hooks  []string
	mods   []string
}

func newSetupRun(t *testing.T) *setupRun {
	t.Helper()
	_, claude := isolateSetup(t)
	real := realClaude(t)
	r := &setupRun{m: testModel(140, 40), claude: claude}
	r.m.tab = tabInbox
	const den = "/opt/den/bin/den"
	r.m.setup.env = func() doctor.Env {
		e := doctor.System()
		e.Exe, e.UID = den, os.Getuid()
		sys := e.Lstat
		e.Lstat = func(p string) (fs.FileInfo, error) {
			switch p {
			case "/", "/opt", "/opt/den", "/opt/den/bin":
				return fi{fs.ModeDir | 0o755, 0}, nil
			case den:
				return fi{0o755, os.Getuid()}, nil
			}
			if strings.HasPrefix(p, "/opt") {
				return nil, fs.ErrNotExist
			}
			return sys(p)
		}
		e.EvalSymlinks = func(p string) (string, error) { return p, nil }
		return e
	}
	r.m.setup.install = setupInstaller{
		hooks: func(exe string) (string, error) {
			if under(hooks.SettingsPath(), real) {
				t.Fatalf("the hooks installer was called with the real %s", hooks.SettingsPath())
			}
			r.hooks = append(r.hooks, exe)
			return hooks.Install(exe)
		},
		mod: func(name string) error {
			if under(mods.SkillsDir(), real) {
				t.Fatalf("the mods installer was called with the real %s", mods.SkillsDir())
			}
			r.mods = append(r.mods, name)
			return liveInstaller.mod(name)
		},
	}
	return r
}

func (r *setupRun) card() *inbox.Item {
	for _, it := range r.m.store.Items() {
		if it.Kind == inbox.Setup && it.State == inbox.Waiting {
			return it
		}
	}
	return nil
}

func (r *setupRun) settings() []byte {
	raw, _ := os.ReadFile(filepath.Join(r.claude, "settings.json"))
	return raw
}

func TestSetupCardAtStart(t *testing.T) {
	r := newSetupRun(t)
	r.m.startSetup()
	it := r.card()
	if it == nil {
		t.Fatal("no setup card with no hooks installed")
	}
	body := strings.Join(it.Body, "\n")
	// D6: Den's window shows the real target and the den.
	for _, want := range []string{filepath.Join(r.claude, "settings.json"), "/opt/den/bin/den hook", "den uninstall-hooks"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card lacks %q:\n%s", want, body)
		}
	}
	// The start-up check raises it once.
	r.m.startSetup()
	n := 0
	for _, x := range r.m.store.Items() {
		if x.Kind == inbox.Setup && x.State == inbox.Waiting {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d open setup cards after a second check", n)
	}

	// Hidden for this version by ^N: no card; another version asks again.
	r.m.dropSetupCard("test")
	e := r.m.setupEnv()
	os.MkdirAll(filepath.Dir(doctor.Dismissed(e)), 0o700)
	os.WriteFile(doctor.Dismissed(e), []byte(remote.LocalVersion+"\n"), 0o600)
	r.m.startSetup()
	if r.card() != nil {
		t.Error("a card after ^N for this Den version")
	}
	os.WriteFile(doctor.Dismissed(e), []byte("v0.0.1\n"), 0o600)
	r.m.startSetup()
	if r.card() == nil {
		t.Error("no card for a newer Den after ^N on an older one")
	}
}

func TestSetupYesInstalls(t *testing.T) {
	r := newSetupRun(t)
	r.m.startSetup()
	it := r.card()
	if it == nil {
		t.Fatal("no card")
	}
	mm, _ := r.m.answerItem(it, "allow")
	r.m = mm.(Model)
	if it.State != inbox.Resolved || len(r.hooks) != 1 || r.hooks[0] != "/opt/den/bin/den" || len(r.mods) == 0 {
		t.Fatalf("after ^Y: state %v, hooks %v, mods %v, flash %q", it.State, r.hooks, r.mods, r.m.flash)
	}
	if st, err := hooks.CheckStatus("/opt/den/bin/den"); err != nil || len(st.Missing) != 0 || st.Other {
		t.Errorf("hooks after ^Y: %+v %v", st, err)
	}
	if p := doctor.MakePlan(r.m.setupEnv()); p.Work() {
		t.Errorf("work left after ^Y: %+v", p)
	}
	if !strings.Contains(r.m.flash, "Installed") {
		t.Errorf("flash %q", r.m.flash)
	}
}

// D5: anything changed between the card and ^Y writes nothing, and the
// new reading goes on a card of its own.
func TestSetupYesRechecks(t *testing.T) {
	r := newSetupRun(t)
	r.m.startSetup()
	it := r.card()
	// Another den's hooks went in since the card was shown: written into
	// this test's own folder only.
	if p := hooks.SettingsPath(); !under(p, r.claude) || under(p, realClaude(t)) {
		t.Fatalf("hooks.Install would write %s, not this test's %s", p, r.claude)
	}
	if _, err := hooks.Install("/srv/other/den"); err != nil {
		t.Fatal(err)
	}
	before := r.settings()
	mm, _ := r.m.answerItem(it, "allow")
	r.m = mm.(Model)
	if len(r.hooks) != 0 || len(r.mods) != 0 {
		t.Fatalf("installed after a change: hooks %v, mods %v", r.hooks, r.mods)
	}
	if !bytes.Equal(r.settings(), before) {
		t.Error("settings.json was written")
	}
	if it.State != inbox.Resolved || r.card() == nil || r.card() == it || !strings.Contains(r.m.flash, "nothing was written") {
		t.Errorf("old card %v, new card %v, flash %q", it.State, r.card(), r.m.flash)
	}
	// A settings.json that no longer parses: a new card with no ^Y.
	os.WriteFile(filepath.Join(r.claude, "settings.json"), []byte(`{"hooks":`), 0o600)
	it = r.card()
	mm, _ = r.m.answerItem(it, "allow")
	r.m = mm.(Model)
	if len(r.hooks) != 0 || r.card() == nil || r.m.setup.plan.NoY == "" {
		t.Errorf("unparseable: hooks %v, card %v, NoY %q", r.hooks, r.card(), r.m.setup.plan.NoY)
	}
	mm, _ = r.m.answerItem(r.card(), "allow")
	r.m = mm.(Model)
	if len(r.hooks) != 0 || r.card() == nil || !strings.Contains(r.m.flash, "offers no ^Y") {
		t.Errorf("^Y on a card with no y: hooks %v, flash %q", r.hooks, r.m.flash)
	}
}

func TestSetupNoHides(t *testing.T) {
	r := newSetupRun(t)
	r.m.startSetup()
	it := r.card()
	mm, _ := r.m.answerItem(it, "deny")
	r.m = mm.(Model)
	if it.State != inbox.Resolved || len(r.hooks)+len(r.mods) != 0 {
		t.Fatalf("after ^N: state %v, hooks %v, mods %v", it.State, r.hooks, r.mods)
	}
	raw, err := os.ReadFile(doctor.Dismissed(r.m.setupEnv()))
	if err != nil || strings.TrimSpace(string(raw)) != remote.LocalVersion {
		t.Errorf("dismissed file %q, %v", raw, err)
	}
	if r.settings() != nil {
		t.Error("^N wrote settings.json")
	}
	// den doctor still reports it: ^N only hides the card (D8).
	e := r.m.setupEnv()
	e.Version = func(string) (string, error) { return "", doctor.ErrTimeout } // never run a real claude or git
	if res := doctor.Run(e); res[2].ID != "hooks" || res[2].Status != doctor.Fail {
		t.Errorf("doctor after ^N: %+v", res[2])
	}
}

// D4: no ^Y for a den in a temporary build folder, and the keys say so.
func TestSetupNoYForATempBuild(t *testing.T) {
	r := newSetupRun(t)
	env := r.m.setup.env
	r.m.setup.env = func() doctor.Env {
		e := env()
		e.Exe = filepath.Join(os.TempDir(), "go-build1", "den")
		return e
	}
	r.m.startSetup()
	it := r.card()
	if it == nil || r.m.setup.plan.NoY == "" {
		t.Fatalf("card %v, NoY %q", it, r.m.setup.plan.NoY)
	}
	key := func(k, label string) string { return k + " " + label }
	if acts := r.m.setupActs(it, key, " "); strings.Contains(acts, "^Y") {
		t.Errorf("keys offer ^Y: %q", acts)
	}
	mm, _ := r.m.answerItem(it, "allow")
	r.m = mm.(Model)
	if len(r.hooks)+len(r.mods) != 0 || it.State == inbox.Resolved {
		t.Errorf("^Y on a temp build: hooks %v, mods %v, state %v", r.hooks, r.mods, it.State)
	}
}

// D8: the settle guard holds a ^Y pressed as the card first comes up at
// start; the next one acts.
func TestSetupCardSettlesFirst(t *testing.T) {
	answerSettle = 700 * time.Millisecond
	t.Cleanup(func() { answerSettle = 0 })
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	answerNow = func() time.Time { return now }
	t.Cleanup(func() { answerNow = time.Now })
	r := newSetupRun(t)
	r.m.startSetup()
	r.m.refresh()
	got := ctrl(r.m, 'y')
	if len(r.hooks) != 0 || !strings.Contains(got.flash, "A new request just came up") {
		t.Fatalf("a ^Y held through start answered the card: hooks %v, flash %q", r.hooks, got.flash)
	}
	now = now.Add(time.Second)
	if ctrl(got, 'y'); len(r.hooks) != 1 {
		t.Errorf("the second ^Y did not install: hooks %v", r.hooks)
	}
}

// den-sec-review's API conditions: the card is listed den_only, needing
// Den's screen, with its paste-safe body; a console with its powers,
// answering with the right hash, is refused for the kind alone, with God
// mode off (refused_from_dock) and on (tty_only), for every verb, and
// nothing is written, installed or hidden.
func TestSetupCardFromTheConsole(t *testing.T) {
	for _, god := range []bool{false, true} {
		r := newSetupRun(t)
		r.m.tab = tabFleet
		r.m.god.on = god
		r.m.startSetup()
		it := r.card()
		if dockMayAnswer(it) || !it.Answerable() {
			t.Fatalf("dockMayAnswer %v, Answerable %v", dockMayAnswer(it), it.Answerable())
		}
		c := api.ProjectCard(r.m.dockCard(it))
		if c.DockAnswers || c.TTYOnly != god || c.Class != string(inbox.DenOnly) || c.Step != 0 {
			t.Errorf("god %v: card dock %v tty %v class %s %d", god, c.DockAnswers, c.TTYOnly, c.Class, c.Step)
		}
		for _, leak := range []string{r.claude, os.Getenv("HOME"), "/opt/den"} {
			if strings.Contains(c.Body, leak) {
				t.Errorf("the listed card holds %q:\n%s", leak, c.Body)
			}
		}
		if !strings.Contains(c.Body, "<CLAUDE_CONFIG_DIR>/settings.json") {
			t.Errorf("the listed card:\n%s", c.Body)
		}
		want := api.CodeRefusedFromDock
		if god {
			want = api.CodeTTYOnly
		}
		for _, v := range []int{1, 2} {
			for _, verb := range []string{"allow", "deny", "always"} {
				a := &api.CardAnswer{ID: it.ID, Hash: c.HashIn(v), Version: v, Verb: verb, Console: "app-host", Reply: make(chan api.CardResult, 1)}
				r.m.answerFromDock(a)
				if res := <-a.Reply; res.Code != want {
					t.Errorf("god %v, v%d %s with the right hash: %+v, want %s", god, v, verb, res, want)
				}
			}
		}
		if it.State != inbox.Waiting || len(r.hooks)+len(r.mods) != 0 || r.settings() != nil {
			t.Errorf("god %v: after the console: state %v, hooks %v, mods %v, settings %q", god, it.State, r.hooks, r.mods, r.settings())
		}
		if _, err := os.Stat(doctor.Dismissed(r.m.setupEnv())); err == nil {
			t.Errorf("god %v: the console hid the card", god)
		}
	}
}

// The gate test's row: a setup card is never one the dock may answer.
func TestSetupNeverInTheDock(t *testing.T) {
	it := &inbox.Item{SessionID: inbox.SetupSession, Kind: inbox.Setup, State: inbox.Waiting}
	if todayDock[inbox.Setup] || dockMayAnswer(it) && it.Answerable() {
		t.Error("the setup card may be answered from the dock")
	}
	if !godTTYKinds[inbox.Setup] {
		t.Error("Setup is not in godTTYKinds")
	}
}
