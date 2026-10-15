package sysd

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testUnit = ConsoleUnit

// fixture is a fake manager serving a unit that passes every check, a
// binary and a scanner of the test's own, and the system read through
// hooks: the manager is pid 4000 (a child of init, in its scope), the
// unit's main process 4242, its child.
type fixture struct {
	m       *fakeManager
	bin     string
	peer    ProcID
	ppid    map[int]int
	cgroups map[int]string
	tracer  int
	gtk     string // the folder the fixture's GTK settings live in
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{m: newFakeManager(t, testUnit)}
	dir := t.TempDir()
	f.bin = filepath.Join(dir, "console-host")
	os.WriteFile(f.bin, []byte("console build 1"), 0o755)
	scanner := filepath.Join(dir, "gst-plugin-scanner")
	os.WriteFile(scanner, []byte("scanner"), 0o755)
	unitFile := filepath.Join(dir, testUnit)
	os.WriteFile(unitFile, []byte("[Service]\nExecStart="+f.bin+"\n"), 0o644)
	run := os.Getenv("XDG_RUNTIME_DIR")

	boot := time.Now().Add(-time.Hour).Truncate(time.Second)
	// The main process started a minute from now, after the binary
	// changed (ctime is now), 100 ticks a second.
	startTicks := uint64((time.Hour + time.Minute).Seconds() * 100)
	startedUs := uint64(boot.Add(time.Hour + time.Minute).UnixMicro())
	f.peer = ProcID{PID: 4242, Start: startTicks}
	self := os.Getpid()
	uid := strconv.Itoa(os.Getuid())
	f.ppid = map[int]int{self: 1, 4242: self}
	f.cgroups = map[int]string{
		self: "/user.slice/user-" + uid + ".slice/user@" + uid + ".service/init.scope",
		4242: "/user.slice/user-" + uid + ".slice/user@" + uid + ".service/app.slice/" + testUnit,
	}
	starts := map[int]uint64{self: 777, 4242: startTicks}
	saved := []any{procStat, procCgroup, procTracer, procExe, ptraceScope, bootID, bootTime, rootUID, systemPathFn, clkTck, gtkSystemDirs}
	gtkSystemDirs = []string{filepath.Join(dir, "sysgtk")}
	clkTck = 100
	procStat = func(pid int) (int, uint64, error) {
		pp, ok := f.ppid[pid]
		if !ok {
			return 0, 0, errors.New("no such process")
		}
		return pp, starts[pid], nil
	}
	procCgroup = func(pid int) (string, error) { return f.cgroups[pid], nil }
	procTracer = func(int) (int, error) { return f.tracer, nil }
	procExe = func(int) (os.FileInfo, error) { return os.Stat(f.bin) } // a file the test owns stands for systemd
	ptraceScope = func() (string, error) { return "1", nil }
	bootID = func() (string, error) { return "0f2f0c3e-5d4b-4b8e-9a37-2a1d5c6e7f80", nil }
	bootTime = func() (time.Time, error) { return boot, nil }
	rootUID = os.Getuid()
	systemPathFn = func(p string, dir bool) bool {
		fi, err := os.Lstat(p)
		return err == nil && filepath.IsAbs(p) && fi.IsDir() == dir
	}
	t.Cleanup(func() {
		procStat = saved[0].(func(int) (int, uint64, error))
		procCgroup = saved[1].(func(int) (string, error))
		procTracer = saved[2].(func(int) (int, error))
		procExe = saved[3].(func(int) (os.FileInfo, error))
		ptraceScope = saved[4].(func() (string, error))
		bootID = saved[5].(func() (string, error))
		bootTime = saved[6].(func() (time.Time, error))
		rootUID = saved[7].(int)
		systemPathFn = saved[8].(func(string, bool) bool)
		clkTck = saved[9].(float64)
		gtkSystemDirs = saved[10].([]string)
	})

	cmd := func(path string) []any {
		return []any{[]any{path, []string{path}, false, uint64(0), uint64(0), uint64(0), uint64(0), uint32(0), int32(0), int32(0)}}
	}
	none := []any{}
	m := f.m
	m.set("ExecStart", "a(sasbttttuii)", cmd(f.bin))
	for _, n := range []string{"ExecStartPre", "ExecStartPost", "ExecCondition", "ExecReload", "ExecStop", "ExecStopPost"} {
		m.set(n, "a(sasbttttuii)", none)
	}
	m.set("Environment", "as", []string{
		"GST_PLUGIN_PATH=", "GTK_MODULES=", "VK_LOADER_LAYERS_DISABLE=~implicit~", "GST_REGISTRY_UPDATE=yes",
		"GST_PLUGIN_SYSTEM_PATH=" + dir, "GST_PLUGIN_SCANNER=" + scanner,
		"GST_REGISTRY=" + RegistryPath(run), "HOME_SOMETHING=fine",
	})
	m.set("EnvironmentFiles", "a(sb)", none)
	for _, n := range []string{"UnsetEnvironment", "PassEnvironment", "ReadWritePaths", "ExtensionDirectories"} {
		m.set(n, "as", []string{})
	}
	for _, n := range []string{"WorkingDirectory", "RootDirectory", "RootImage", "PIDFile"} {
		m.set(n, "s", "")
	}
	m.set("BindPaths", "a(ssbt)", none)
	m.set("BindReadOnlyPaths", "a(ssbt)", none)
	m.set("TemporaryFileSystem", "a(ss)", none)
	m.set("MountImages", "a(ssba(ss))", none)
	m.set("ExtensionImages", "a(sba(ss))", none)
	m.set("PrivateMounts", "b", false)
	m.set("ProtectHome", "s", "no")
	m.set("ProtectSystem", "s", "no")
	m.set("Type", "s", "simple")
	m.set("NotifyAccess", "s", "none")
	m.set("Transient", "b", false)
	m.set("FragmentPath", "s", unitFile)
	m.set("DropInPaths", "as", []string{})
	m.set("ControlGroup", "s", f.cgroups[4242])
	m.set("MainPID", "u", uint32(4242))
	m.set("ExecMainStartTimestamp", "t", startedUs)
	f.gtk = t.TempDir()
	m.env = []string{"PATH=/usr/bin", "HOME=/home/u", "XDG_CONFIG_HOME=" + filepath.Join(f.gtk, "home"), "XDG_CONFIG_DIRS=" + filepath.Join(f.gtk, "xdg")}
	return f
}

func (f *fixture) read(t *testing.T) (Verifier, error) {
	t.Helper()
	return readFrom(f.m.socket(), testUnit, f.peer)
}

func TestReadPasses(t *testing.T) {
	f := newFixture(t)
	v, err := f.read(t)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if v.Main != f.peer || v.Exe.Path != f.bin || len(v.Exe.SHA256) != 64 || len(v.Props) != 64 || v.Manager.PID != os.Getpid() {
		t.Errorf("verifier %+v", v)
	}
	if v.Pins["VK_LOADER_LAYERS_DISABLE"] != "~implicit~" || v.Scanner.SHA256 == "" || len(v.Files) != 1 {
		t.Errorf("pins %v scanner %+v files %v", v.Pins, v.Scanner, v.Files)
	}
	// Read again, the same: no difference.
	again, err := f.read(t)
	if err != nil || len(Compare(v, again)) != 0 {
		t.Errorf("a second read differs: %v %v", err, Compare(v, again))
	}
}

// Each failed check is named, and fails the read.
func TestReadNamesEveryProblem(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(f *fixture)
		want string
	}{
		{"forking", func(f *fixture) { f.m.set("Type", "s", "forking") }, "Type"},
		{"notify", func(f *fixture) { f.m.set("NotifyAccess", "s", "all") }, "NotifyAccess"},
		{"pidfile", func(f *fixture) { f.m.set("PIDFile", "s", "/run/x.pid") }, "PIDFile"},
		{"transient", func(f *fixture) { f.m.set("Transient", "b", true) }, "transient"},
		{"drop-in", func(f *fixture) { f.m.set("DropInPaths", "as", []string{"/x/override.conf"}) }, "drop-ins"},
		{"env file", func(f *fixture) {
			f.m.set("EnvironmentFiles", "a(sb)", []any{[]any{"/x/env", false}})
		}, "EnvironmentFile"},
		{"pass env", func(f *fixture) { f.m.set("PassEnvironment", "as", []string{"LD_PRELOAD"}) }, "passes"},
		{"ld_preload", func(f *fixture) {
			f.m.set("Environment", "as", []string{"LD_PRELOAD=/x.so"})
		}, "LD_PRELOAD"},
		{"manager env", func(f *fixture) { f.m.env = append(f.m.env, "GTK_PATH=/x") }, "manager's own environment"},
		{"not main", func(f *fixture) { f.peer.PID = 9999 }, "not the unit's main process"},
		{"traced", func(f *fixture) { f.tracer = 77 }, "traced"},
		{"manager not init's", func(f *fixture) { f.ppid[os.Getpid()] = 50 }, "not started by init"},
		{"main not manager's", func(f *fixture) { f.ppid[4242] = 50 }, "not the manager's child"},
		{"main elsewhere", func(f *fixture) { f.cgroups[4242] = "/elsewhere" }, "not in the unit's cgroup"},
		{"two commands", func(f *fixture) {
			c := []any{f.bin, []string{f.bin}, false, uint64(0), uint64(0), uint64(0), uint64(0), uint32(0), int32(0), int32(0)}
			f.m.set("ExecStart", "a(sasbttttuii)", []any{c, c})
		}, "exactly one"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.edit(f)
			_, err := f.read(t)
			var probs Problems
			if !errors.As(err, &probs) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %v, want a problem with %q", err, c.want)
			}
		})
	}
}

// A registry that is not rebuilt, or another registry setting, would let
// a planted registry stick: each is a problem.
func TestReadRegistryPins(t *testing.T) {
	for _, env := range [][]string{
		{"GST_REGISTRY_UPDATE=no"},
		{"GST_REGISTRY_FORK=no"},
		{"GST_REGISTRY_1_0=/x"},
	} {
		f := newFixture(t)
		cur := f.m.props["Environment"].Value.([]string)
		var kept []string
		for _, kv := range cur {
			if !strings.HasPrefix(kv, "GST_REGISTRY_UPDATE=") {
				kept = append(kept, kv)
			}
		}
		if env[0] != "GST_REGISTRY_UPDATE=no" {
			kept = append(kept, "GST_REGISTRY_UPDATE=yes")
		}
		f.m.set("Environment", "as", append(kept, env...))
		if _, err := f.read(t); err == nil {
			t.Errorf("%v passed", env)
		}
	}
}

// gtk-modules in any GTK 3 settings.ini the service would read is a
// problem that names the file; an empty, commented or absent one is not.
func TestReadGtkModules(t *testing.T) {
	write := func(t *testing.T, dir, body string) string {
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "settings.ini")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	f := newFixture(t)
	write(t, filepath.Join(f.gtk, "home", "gtk-3.0"), "[Settings]\ngtk-modules=\n# gtk-modules=evil\n; gtk-modules = evil\ngtk-theme-name=Adwaita\n")
	if _, err := f.read(t); err != nil {
		t.Fatalf("an empty or commented gtk-modules: %v", err)
	}
	for _, where := range []string{"home", "xdg"} {
		f := newFixture(t)
		p := write(t, filepath.Join(f.gtk, where, "gtk-3.0"), "[Settings]\ngtk-modules = \"canberra-gtk-module\"\n")
		if _, err := f.read(t); err == nil || !strings.Contains(err.Error(), "gtk-modules is set in "+p) {
			t.Errorf("%s: %v", where, err)
		}
	}
	f = newFixture(t)
	p := write(t, gtkSystemDirs[0], "[Settings]\ngtk-modules=x\n")
	if _, err := f.read(t); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("the system's: %v", err)
	}
}

// A property of another type than Den reads is refused, not taken.
func TestReadRefusesAnotherType(t *testing.T) {
	f := newFixture(t)
	f.m.set("MainPID", "s", "4242")
	if _, err := f.read(t); err == nil || !strings.Contains(err.Error(), "MainPID") {
		t.Errorf("err %v", err)
	}
}

// A manager that refuses the authentication, or answers garbage, is no
// manager.
func TestReadRefusesABadManager(t *testing.T) {
	f := newFixture(t)
	f.m.auth = "REJECTED EXTERNAL\r\n"
	if _, err := f.read(t); err == nil {
		t.Error("read through a refused authentication")
	}
	f = newFixture(t)
	f.m.raw = []byte("lllllllllllllllllllllllllllll")
	CallTimeout = 500 * time.Millisecond
	t.Cleanup(func() { CallTimeout = 3 * time.Second })
	if _, err := f.read(t); err == nil {
		t.Error("read garbage as a reply")
	}
}

// A restart of the same build reads the same but for its main process; a
// new build, a changed unit or pin, or another manager differ, by name.
func TestCompare(t *testing.T) {
	f := newFixture(t)
	was, err := f.read(t)
	if err != nil {
		t.Fatal(err)
	}
	now := was
	now.Main = ProcID{PID: 5000, Start: 999}
	if d := Compare(was, now); len(d) != 0 {
		t.Errorf("a restart differs: %v", d)
	}
	for _, c := range []struct {
		edit func(v *Verifier)
		want string
	}{
		{func(v *Verifier) { v.Exe.SHA256 = strings.Repeat("0", 64) }, "program changed"},
		{func(v *Verifier) { v.Exe.Ino++ }, "program changed"},
		{func(v *Verifier) { v.Props = strings.Repeat("1", 64) }, "settings"},
		{func(v *Verifier) { v.Files = map[string]string{v.Fragment: "x"} }, "unit file"},
		{func(v *Verifier) {
			v.Pins = map[string]string{}
			for k, x := range was.Pins {
				v.Pins[k] = x
			}
			v.Pins["GST_PLUGIN_SYSTEM_PATH"] = "/elsewhere"
		}, "GST_PLUGIN_SYSTEM_PATH"},
		{func(v *Verifier) { v.Manager.Start++ }, "manager is another process"},
		{func(v *Verifier) { v.Boot = "other" }, "another boot"},
	} {
		now := was
		c.edit(&now)
		if d := Compare(was, now); len(d) == 0 || !strings.Contains(d.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, d)
		}
	}
}

// The binary must be a regular file of the user's with one link.
func TestCheckBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "b")
	os.WriteFile(bin, []byte("x"), 0o755)
	if _, err := checkBinary([]string{bin}, 0); err != nil {
		t.Errorf("a plain file: %v", err)
	}
	os.Link(bin, filepath.Join(dir, "b2"))
	if _, err := checkBinary([]string{bin}, 0); err == nil {
		t.Error("a file with two links passed")
	}
	os.Symlink(filepath.Join(dir, "b2"), filepath.Join(dir, "l"))
	if _, err := checkBinary([]string{filepath.Join(dir, "l")}, 0); err == nil {
		t.Error("a symlink passed")
	}
}

// USER_HZ comes from the kernel's auxiliary vector: 100 on every Linux
// Den runs on, never zero.
func TestClkTck(t *testing.T) {
	if got := readClkTck(); got != 100 {
		t.Errorf("AT_CLKTCK %v", got)
	}
}

// A pinned system path is root-owned at every step, with no symlink, and
// not writable by others: a test's own folder never is.
func TestSystemPath(t *testing.T) {
	if !systemPath("/usr", true) {
		t.Skip("this machine's /usr is not root-owned")
	}
	if systemPath(t.TempDir(), true) {
		t.Error("a test folder passed as a system directory")
	}
	for _, bad := range []string{"usr", "/usr/", "/usr:/opt", "/usr/../usr"} {
		if systemPath(bad, true) {
			t.Errorf("%q passed", bad)
		}
	}
}
