package sysd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Escape is reversible, so two different ids never make one unit name,
// and its output is a valid unit-name part.
func TestEscapeRoundTrip(t *testing.T) {
	for _, s := range []string{"", "m1", "d-abc", "a/b", ".lead", "x.y", "a b", "\\x2d", "ünï", "--", "a\x00b"} {
		e := Escape(s)
		back, ok := Unescape(e)
		if !ok || back != s {
			t.Errorf("Escape(%q) = %q, back %q %v", s, e, back, ok)
		}
		for i := 0; i < len(e); i++ {
			c := e[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.' || c == '\\' || c == '-') {
				t.Errorf("Escape(%q) = %q has %q", s, e, c)
			}
		}
	}
	// EscapePart: the same round trip, and never a '_' or a '-'.
	for _, s := range []string{"", "x_app", "a-b", "a/b", "_", ".x", "ünï"} {
		e := EscapePart(s)
		if back, ok := Unescape(e); !ok || back != s || strings.ContainsAny(e, "_-/") {
			t.Errorf("EscapePart(%q) = %q, back %q %v", s, e, back, ok)
		}
	}
	if Escape("d-1") == Escape("d/1") {
		t.Error("'-' and '/' escape alike")
	}
	if Escape("d-1") != `d\x2d1` {
		t.Errorf("Escape(d-1) = %q", Escape("d-1"))
	}
	for _, bad := range []string{`\`, `\x`, `\x4`, `\y41`, `\xzz`} {
		if _, ok := Unescape(bad); ok {
			t.Errorf("Unescape(%q) took it", bad)
		}
	}
}

// A service's shape is checked before any tool runs: a command, a
// .service and a .slice of Den's characters, an absolute directory.
func TestStartServiceRefusesShapes(t *testing.T) {
	ran := false
	old := runTool
	t.Cleanup(func() { runTool = old })
	runTool = func(string, time.Duration, []string, []string) (string, error) { ran = true; return "", nil }
	for _, s := range []Service{
		{Name: "a.service", Slice: "b.slice"},
		{Name: "a", Slice: "b.slice", Argv: []string{"true"}},
		{Name: "a.service", Slice: "b", Argv: []string{"true"}},
		{Name: "-a.service", Slice: "b.slice", Argv: []string{"true"}},
		{Name: "a b.service", Slice: "b.slice", Argv: []string{"true"}},
		{Name: "a.service", Slice: "b.slice", Argv: []string{"true"}, Dir: "rel"},
		{Name: "a.service", Slice: "b.slice", Argv: []string{"true"}, Description: "two\nlines"},
		{Name: "a.service", Slice: "b.slice", Argv: []string{"true"}, Env: []string{"A=1", "A=2"}},
		{Name: "a.service", Slice: "b.slice", Argv: []string{"true"}, Env: []string{"no-good=1"}},
	} {
		if err := StartService(s); err == nil {
			t.Errorf("%+v was taken", s)
		}
	}
	if ran {
		t.Error("a tool ran for a refused service")
	}
}

// systemd-run's argv: one token per flag, names only for the
// environment (no value ever on a command line, which every user can
// read), every manager variable the service does not set unset, then --
// and the argv as given.
func TestStartArgs(t *testing.T) {
	s := Service{Name: `den-desk-ab-m\x2d1-1.service`, Slice: `den-desk-ab-m\x2d1.slice`, Description: "Den mission", Argv: []string{"/bin/sh", "-c", "foot -e sh"},
		Env: []string{"PATH=/usr/bin", "DEN_TEST_VALUE_NAME=plain-value-not-in-argv"}, Dir: "/home/u"}
	args, values, err := StartArgs(s, []string{"PATH", "HYPRLAND_INSTANCE_SIGNATURE", "DISPLAY", "DEN_SOCKET"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "plain-value-not-in-argv") || strings.Contains(joined, "/usr/bin") {
		t.Errorf("a value is on the command line: %q", args)
	}
	for _, want := range []string{"--setenv=PATH", "--setenv=DEN_TEST_VALUE_NAME", "--property=UnsetEnvironment=HYPRLAND_INSTANCE_SIGNATURE", "--property=UnsetEnvironment=DISPLAY", "--property=UnsetEnvironment=DEN_SOCKET",
		"--unit=" + s.Name, "--slice=" + s.Slice, "--working-directory=/home/u", "--collect", "--no-ask-password"} {
		if !strings.Contains("\x00"+joined+"\x00", "\x00"+want+"\x00") {
			t.Errorf("no %q in %q", want, args)
		}
	}
	if strings.Contains(joined, "UnsetEnvironment=PATH") {
		t.Error("a name the service sets is unset")
	}
	dd := -1
	for i, a := range args {
		if a == "--" {
			dd = i
			break
		}
	}
	if dd < 0 || strings.Join(args[dd+1:], " ") != "/bin/sh -c foot -e sh" {
		t.Errorf("the argv after --: %q", args)
	}
	if strings.Join(values, " ") != "PATH=/usr/bin DEN_TEST_VALUE_NAME=plain-value-not-in-argv" {
		t.Errorf("values %q", values)
	}
	env := strings.Join(toolEnv(values), " ")
	if !strings.Contains(env, "DEN_TEST_VALUE_NAME=plain-value-not-in-argv") || !strings.Contains(env, "SYSTEMD_PAGER=cat") {
		t.Errorf("the tool's environment: %q", env)
	}
}

// show is read strictly: the four keys once each, known values only.
func TestParseState(t *testing.T) {
	u, err := parseState("LoadState=loaded\nActiveState=active\nFreezerState=frozen\nControlGroup=/user.slice/x.slice\n")
	if err != nil || !u.Loaded || u.Freeze != "frozen" || u.Cgroup != "/user.slice/x.slice" {
		t.Errorf("%+v %v", u, err)
	}
	if u, err := parseState("LoadState=not-found\nActiveState=inactive\nFreezerState=running\nControlGroup=\n"); err != nil || u.Loaded || !u.Gone() {
		t.Errorf("not-found: %+v %v", u, err)
	}
	for _, bad := range []string{
		"", "LoadState=loaded\nActiveState=active\nFreezerState=frozen\n",
		"LoadState=loaded\nActiveState=active\nFreezerState=frozen\nControlGroup=\nExtra=1\n",
		"LoadState=loaded\nActiveState=active\nFreezerState=iced\nControlGroup=\n",
		"LoadState=loaded\nLoadState=loaded\nFreezerState=frozen\nControlGroup=\n",
		"LoadState=loaded\nActiveState=active\nFreezerState\nControlGroup=\n",
		"LoadState=loaded\nActiveState=active\nFreezerState=frozen\nControlGroup=\nControlGroup=\n",
	} {
		if _, err := parseState(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

// Stopped is not-found, or loaded and inactive with the kernel saying no
// process is left under its cgroup; every other state holds.
func TestGoneNeedsAnEmptyCgroup(t *testing.T) {
	old := cgroupRoot
	t.Cleanup(func() { cgroupRoot = old })
	cgroupRoot = t.TempDir()
	mk := func(cg, events string) {
		os.MkdirAll(filepath.Join(cgroupRoot, cg), 0o755)
		if events != "" {
			os.WriteFile(filepath.Join(cgroupRoot, cg, "cgroup.events"), []byte(events), 0o644)
		}
	}
	mk("/empty.slice", "populated 0\nfrozen 0\n")
	mk("/busy.slice", "populated 1\nfrozen 0\n")
	mk("/noevents.slice", "")
	state := func(load, active, cg string) UnitState {
		u, err := parseState("LoadState=" + load + "\nActiveState=" + active + "\nFreezerState=running\nControlGroup=" + cg + "\n")
		if err != nil {
			t.Fatal(err)
		}
		u.Empty = cgroupEmpty(u.Cgroup)
		return u
	}
	for _, c := range []struct {
		load, active, cg string
		gone             bool
	}{
		{"not-found", "inactive", "", true},
		{"loaded", "inactive", "", true},
		{"loaded", "inactive", "/empty.slice", true},
		{"loaded", "inactive", "/gone.slice", true}, // its directory is gone
		{"loaded", "inactive", "/busy.slice", false},
		{"loaded", "inactive", "/noevents.slice", false},
		{"loaded", "inactive", "../etc", false},
		{"loaded", "inactive", "/a/../../etc", false},
		{"loaded", "active", "", false},
		{"loaded", "activating", "", false},
		{"loaded", "deactivating", "/empty.slice", false},
		{"loaded", "failed", "", false},
	} {
		if g := state(c.load, c.active, c.cg).Gone(); g != c.gone {
			t.Errorf("%s %s %q: gone %v, want %v", c.load, c.active, c.cg, g, c.gone)
		}
	}
}

// A tool is taken only from the system's own directories, and only a
// root's file in a root's directory, neither writable by others.
// RootToolLink keeps a tool's own name where RootTool resolves it: pasta
// is a link to passt, and passt reads its mode from its name. sh is a
// link on most systems, so it stands in here.
func TestRootToolLinkKeepsTheName(t *testing.T) {
	fi, err := os.Lstat("/usr/bin/sh")
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skip("/usr/bin/sh is not a link here")
	}
	real, err := RootTool("sh")
	if err != nil {
		t.Fatal(err)
	}
	link, err := RootToolLink("sh")
	if err != nil || filepath.Base(link) != "sh" || link == real {
		t.Fatalf("RootToolLink(sh) = %q, %v; RootTool = %q", link, err, real)
	}
	if _, err := RootToolLink("den-no-such-tool"); err == nil {
		t.Fatal("a missing tool was found")
	}
}

func TestToolOnlyFromRootsDirs(t *testing.T) {
	old := toolDirs
	t.Cleanup(func() { toolDirs = old })
	mine := t.TempDir()
	os.WriteFile(filepath.Join(mine, "systemctl"), []byte("#!/bin/sh\necho frozen\n"), 0o755)
	toolDirs = []string{mine}
	if _, err := tool("systemctl"); err == nil {
		t.Error("a user's own systemctl was taken")
	}
	toolDirs = []string{t.TempDir()}
	if _, err := tool("systemctl"); err == nil {
		t.Error("a missing tool was found")
	}
	t.Setenv("PATH", mine)
	toolDirs = old
	if p, err := tool("systemctl"); err == nil && strings.HasPrefix(p, mine) {
		t.Errorf("PATH was read: %s", p)
	}
}

// The listing takes only whole names with this Den's prefix, and the
// prefix holds no character the manager's matching reads as special.
func TestListTakesOnlyItsOwn(t *testing.T) {
	old := runTool
	t.Cleanup(func() { runTool = old })
	runTool = func(_ string, _ time.Duration, args, _ []string) (string, error) {
		return "den_desk_ab_m1.slice loaded active active Den\n● den_desk_ab_m2.slice loaded failed failed x\nden_desk_abc_m3.slice loaded active active y\nden_desk_ab_.slice x\nnot a line\n", nil
	}
	got, err := List("den_desk_ab_")
	if err != nil || strings.Join(got, " ") != "den_desk_ab_m1.slice den_desk_ab_m2.slice" {
		t.Errorf("List: %v %v", got, err)
	}
	for _, bad := range []string{`den\-`, "den*", "den-[a]", ""} {
		if _, err := List(bad); err == nil {
			t.Errorf("prefix %q taken", bad)
		}
	}
}

// With DEN_REAL_SYSTEMD=1: a real transient service in a slice of its
// own is started, frozen, thawed and stopped on this machine's user
// manager, holds only the environment it was given, and is gone after.
// Never in CI.
func TestRealUnits(t *testing.T) {
	if os.Getenv("DEN_REAL_SYSTEMD") != "1" {
		t.Skip("set DEN_REAL_SYSTEMD=1 to start a unit on this machine's systemd user manager")
	}
	// Names exactly as Den's ui makes them (deskSliceOf): den_desk_, an
	// instance of 12 hex, '_', the id through EscapePart, from an id with
	// both a '_' and a '-' (so \x5f and \x2d meet the real manager). No
	// '-' in the name: each would be a level of slices systemd makes.
	const inst = "00000000beef"
	id := "dentest_a-" + strconv.Itoa(os.Getpid())
	prefix := "den_desk_" + inst + "_"
	slice := prefix + EscapePart(id) + ".slice"
	svc := strings.TrimSuffix(slice, ".slice") + "_1.service"
	if strings.Contains(slice, "-") || !strings.Contains(slice, `\x5f`) || !strings.Contains(slice, `\x2d`) {
		t.Fatalf("the test's name %q is not the shape Den makes", slice)
	}
	t.Cleanup(func() { Stop(slice) })
	// The unit writes its own environment to a file the test names (as
	// an argument, never in the shell's text): another process's environ
	// in /proc may be closed to the test even for the same user.
	envFile := filepath.Join(t.TempDir(), "env")
	argv := []string{"/bin/sh", "-c", `/usr/bin/env > "$1"; exec /bin/sleep 60`, "sh", envFile}
	if err := StartService(Service{Name: svc, Slice: slice, Description: "Den test", Argv: argv, Env: []string{"PATH=/usr/bin:/bin", "DEN_TEST_ONLY=1"}}); err != nil {
		t.Fatal(err)
	}
	wait := func(want func(UnitState) bool, unit string) UnitState {
		var u UnitState
		for i := 0; i < 50; i++ {
			var err error
			if u, err = State(unit); err != nil {
				t.Fatal(err)
			}
			if want(u) {
				return u
			}
			time.Sleep(100 * time.Millisecond)
		}
		// The test's own unit's last log lines, read only, so a failure
		// explains itself.
		log, _ := exec.Command("journalctl", "--user", "-u", svc, "-n", "20", "--no-pager", "-o", "short-iso").CombinedOutput()
		t.Fatalf("%s stayed %+v\n%s's log:\n%s", unit, u, svc, log)
		return u
	}
	wait(func(u UnitState) bool { return u.Active == "active" }, slice)
	// E1: the program holds only what it was given, nothing of the
	// manager's own (no Hyprland instance among it).
	var raw []byte
	for i := 0; i < 50 && len(raw) == 0; i++ {
		raw, _ = os.ReadFile(envFile)
		time.Sleep(100 * time.Millisecond)
	}
	if len(raw) == 0 {
		t.Fatal("the unit wrote no environment")
	}
	for _, kv := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "DEN_TEST_ONLY":
		case "PWD", "SHLVL", "_":
			// the shell's own
		case "INVOCATION_ID", "JOURNAL_STREAM", "SYSTEMD_EXEC_PID", "MANAGERPID", "MEMORY_PRESSURE_WATCH", "MEMORY_PRESSURE_WRITE", "HOME", "LANG", "LANGUAGE", "USER", "LOGNAME", "SHELL", "XDG_RUNTIME_DIR":
			// set by the manager for every service it runs (not inherited)
		default:
			t.Errorf("the unit holds %s", k)
		}
	}
	if err := Freeze(slice); err != nil {
		t.Fatal(err)
	}
	wait(func(u UnitState) bool { return u.Freeze == "frozen" }, slice)
	if err := Thaw(slice); err != nil {
		t.Fatal(err)
	}
	wait(func(u UnitState) bool { return u.Freeze == "running" }, slice)
	names, err := List(prefix)
	if err != nil || len(names) != 1 || names[0] != slice {
		t.Errorf("List: %v %v", names, err)
	}
	if err := Stop(slice); err != nil {
		t.Fatal(err)
	}
	u := wait(UnitState.Gone, slice)
	// Which branch the real manager takes after a stop (sec asked).
	events, _ := os.ReadFile(filepath.Join(cgroupRoot, u.Cgroup, "cgroup.events"))
	t.Logf("after the stop: %+v; cgroup.events %q", u, events)
	wait(UnitState.Gone, svc)
	if err := Stop(slice); err != nil {
		t.Errorf("a second stop of a gone slice: %v", err)
	}
	// Nothing of the test's is left, and no level of slices above it was
	// made: every den* slice listed is named.
	left, err := systemctl("list-units", "--all", "--plain", "--no-legend", "--", "den*")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("den* after: %q", left)
	for _, l := range strings.Split(left, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == "●" {
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		switch n := f[0]; {
		case strings.HasPrefix(n, prefix):
			t.Errorf("the test's unit %s is left", n)
		case n == "den.slice", n == "den-desk.slice", strings.HasPrefix(n, "den_desk."), strings.HasPrefix(n, "den_desk_"+inst+"."):
			t.Errorf("a parent slice %s was made", n)
		}
	}
}
