package sysd

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Den reads, of the console's service, what it can check itself: that
// the process is the unit's main one, run by the user's own manager; the
// binary the manager runs, by its file; how the manager runs it, by the
// loaded unit's properties; and the environment it runs in, against a
// closed list. A Verifier is that, taken when the human pairs; a later
// read that differs in anything is a change the human sees on a card.
//
// The manager's socket is the user's own: any program of the user can
// serve one that answers as systemd. The checks here are friction, not a
// boundary, and Den says so.

// ProcID is a process: its pid and its start, in clock ticks since boot.
type ProcID struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start_ticks"`
}

// FileID is a file as Den found it.
type FileID struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Dev    uint64 `json:"dev"`
	Ino    uint64 `json:"ino"`
}

// Verifier is what Den records of the console's service at pairing.
type Verifier struct {
	Unit     string            `json:"unit"`
	Boot     string            `json:"boot_id"`
	UID      int               `json:"uid"`
	Manager  ProcID            `json:"manager"`
	Main     ProcID            `json:"main"`
	Exe      FileID            `json:"exe"`
	Props    string            `json:"props_sha256"`
	Files    map[string]string `json:"files"` // the unit's file, by path, to its SHA-256
	Pins     map[string]string `json:"pins"`
	Scanner  FileID            `json:"gst_plugin_scanner"`
	Fragment string            `json:"fragment"`
}

// The pins drey-host.service sets in its own Environment=, as DREY-API.md
// states them. GST_PLUGIN_SYSTEM_PATH and GST_PLUGIN_SCANNER are checked
// by form (pinChecked); GST_REGISTRY is under $XDG_RUNTIME_DIR.
var pinExact = map[string]string{
	"GST_PLUGIN_PATH":          "",
	"GTK_MODULES":              "",
	"VK_LOADER_LAYERS_DISABLE": "~implicit~",
	// A rescan drops entries for plugins outside the scanned path: "no"
	// would let a planted registry stick.
	"GST_REGISTRY_UPDATE": "yes",
}

const (
	pinPluginDir = "GST_PLUGIN_SYSTEM_PATH"
	pinScanner   = "GST_PLUGIN_SCANNER"
	pinRegistry  = "GST_REGISTRY"
)

// denied reports whether an environment name may load code into the
// service: LD_*, GTK_*, GIO_*, GDK_PIXBUF_*, GST_*, WEBKIT_*, LIBGL_*,
// GBM_BACKENDS_PATH, __EGL_* and VK_*. In the unit only the pins may be
// set; in the manager's own environment, none.
func denied(name string) bool {
	for _, p := range []string{"LD_", "GTK_", "GIO_", "GDK_PIXBUF_", "GST_", "WEBKIT_", "LIBGL_", "__EGL_", "VK_"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return name == "GBM_BACKENDS_PATH"
}

// The checks' hooks: what the running system says, replaced in tests.
var (
	procStat    = readProcStat
	procCgroup  = readProcCgroup
	procTracer  = readProcTracer
	procExe     = func(pid int) (os.FileInfo, error) { return os.Stat("/proc/" + strconv.Itoa(pid) + "/exe") }
	ptraceScope = func() (string, error) { return readTrim("/proc/sys/kernel/yama/ptrace_scope") }
	bootID      = func() (string, error) { return readTrim("/proc/sys/kernel/random/boot_id") }
	bootTime    = readBootTime
	// rootUID is whom system files must belong to; a test may make its
	// own files count as the system's.
	rootUID = 0
	// systemPathFn checks a pinned path (systemPath); tests replace it,
	// since no test can make a root-owned file.
	systemPathFn = systemPath
	// clkTck is the clock ticks in a second (USER_HZ), as the kernel
	// gave this process (AT_CLKTCK); 100 where it says nothing.
	clkTck = readClkTck()
)

// Problem is one thing about the service that fails a check, or differs
// from what Den recorded, in words for the human's card.
type Problem string

// Problems is every Problem a read found.
type Problems []Problem

func (p Problems) Error() string {
	var s []string
	for _, x := range p {
		s = append(s, string(x))
	}
	return strings.Join(s, "; ")
}

// Read reads the unit as the manager runs it now and checks it, for the
// process peer (the console's): the verifier and every problem found. An
// error that is not Problems means Den could not read the manager at all.
func Read(unit string, peer ProcID) (Verifier, error) {
	path, err := managerSocket()
	if err != nil {
		return Verifier{}, err
	}
	return readFrom(path, unit, peer)
}

func readFrom(socket, unit string, peer ProcID) (Verifier, error) {
	c, err := dial(socket)
	if err != nil {
		return Verifier{}, fmt.Errorf("the systemd user manager: %w", err)
	}
	defer c.close()
	v := Verifier{Unit: unit, UID: os.Getuid(), Files: map[string]string{}, Pins: map[string]string{}}
	var probs Problems
	fail := func(f string, a ...any) { probs = append(probs, Problem(fmt.Sprintf(f, a...))) }
	if v.Boot, err = bootID(); err != nil {
		return v, err
	}
	// The manager: the user's, started by init, in its own scope.
	mgr, err := checkManager(c.peer)
	if err != nil {
		fail("%v", err)
	}
	v.Manager = mgr
	obj, err := c.getUnit(unit)
	if err != nil {
		return v, fmt.Errorf("%s: %w", unit, err)
	}
	props, err := readProps(c, obj)
	if err != nil {
		return v, err
	}
	// The loaded unit's shape (condition 3's pins).
	if t := props.str("Type"); t != "simple" && t != "exec" {
		fail("its Type is %q, not simple or exec", t)
	}
	if n := props.str("NotifyAccess"); n != "none" {
		fail("its NotifyAccess is %q, not none", n)
	}
	if p := props.str("PIDFile"); p != "" {
		fail("it names a PIDFile")
	}
	if props.b("Transient") {
		fail("it is a transient unit")
	}
	if d := props.strs("DropInPaths"); len(d) > 0 {
		fail("it has drop-ins: %s", strings.Join(d, ", "))
	}
	if ef := props.raw["EnvironmentFiles"].([]any); len(ef) > 0 {
		fail("it reads an EnvironmentFile")
	}
	if pe := props.strs("PassEnvironment"); len(pe) > 0 {
		fail("it passes the manager's %s", strings.Join(pe, ", "))
	}
	// The main process: the peer, the manager's child, in the unit's
	// cgroup, started when the manager says.
	v.Main = ProcID{PID: int(props.u32("MainPID"))}
	if v.Main.PID <= 1 {
		fail("it has no main process")
	} else {
		v.Main.Start, err = checkMain(v.Main.PID, mgr.PID, props.str("ControlGroup"), props.u64("ExecMainStartTimestamp"))
		if err != nil {
			fail("%v", err)
		}
	}
	if v.Main != peer {
		fail("the connection is not the unit's main process (pid %d, the main one is %d)", peer.PID, v.Main.PID)
	}
	if scope, err := ptraceScope(); err != nil || scope == "0" {
		fail("any process of the user may trace another (kernel.yama.ptrace_scope is 0)")
	}
	if v.Main.PID > 1 {
		if tp, err := procTracer(v.Main.PID); err != nil || tp != 0 {
			fail("its main process is being traced")
		}
	}
	// The binary the manager runs.
	argv, err := execStart(props.raw["ExecStart"])
	if err != nil {
		fail("%v", err)
	} else {
		v.Exe, err = checkBinary(argv, v.Main.Start)
		if err != nil {
			fail("%v", err)
		}
	}
	// The unit's own file.
	v.Fragment = props.str("FragmentPath")
	if sum, err := hashFile(v.Fragment); err != nil {
		fail("its unit file does not read: %v", err)
	} else {
		v.Files[v.Fragment] = sum
	}
	// The environment.
	for _, p := range checkEnv(props.strs("Environment"), &v) {
		probs = append(probs, p)
	}
	menv, err := c.call(managerPath, propsIface, "Get", "ss", "v", managerIface, "Environment")
	if err != nil {
		fail("the manager's environment does not read: %v", err)
	} else if mv := menv[0].(Variant); mv.Sig != "as" {
		fail("the manager's environment is %q, not as", mv.Sig)
	} else {
		menv := strsOf(mv.Value)
		for _, kv := range menv {
			name, _, _ := strings.Cut(kv, "=")
			if denied(name) {
				fail("the manager's own environment sets %s", name)
			}
		}
		// GTK loads the modules its settings name, as GTK_MODULES would.
		for _, file := range gtkModulesSet(menv) {
			fail("gtk-modules is set in %s", file)
		}
	}
	v.Props = props.digest()
	if len(probs) > 0 {
		return v, probs
	}
	return v, nil
}

// gtkSystemDirs are where GTK 3 reads its system-wide settings.ini, after
// the user's and $XDG_CONFIG_DIRS'; tests replace them.
var gtkSystemDirs = []string{"/etc/gtk-3.0", "/usr/share/gtk-3.0"}

// gtkModulesSet is every GTK 3 settings.ini the service would read that
// sets gtk-modules, by the manager's environment (the service's, since
// its unit sets no XDG_CONFIG_*): $XDG_CONFIG_HOME or ~/.config, each of
// $XDG_CONFIG_DIRS or /etc/xdg, then the system's.
func gtkModulesSet(menv []string) []string {
	get := func(name string) string {
		for _, kv := range menv {
			if v, ok := strings.CutPrefix(kv, name+"="); ok {
				return v
			}
		}
		return ""
	}
	home := get("XDG_CONFIG_HOME")
	if home == "" {
		h := get("HOME")
		if h == "" {
			h, _ = os.UserHomeDir()
		}
		home = filepath.Join(h, ".config")
	}
	dirs := []string{filepath.Join(home, "gtk-3.0")}
	cfgDirs := get("XDG_CONFIG_DIRS")
	if cfgDirs == "" {
		cfgDirs = "/etc/xdg"
	}
	for _, d := range strings.Split(cfgDirs, ":") {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "gtk-3.0"))
		}
	}
	dirs = append(dirs, gtkSystemDirs...)
	var out []string
	for _, d := range dirs {
		file := filepath.Join(d, "settings.ini")
		if setsGtkModules(file) {
			out = append(out, file)
		}
	}
	return out
}

// setsGtkModules reports whether a settings.ini gives gtk-modules a value.
// A file that is there but does not read counts as setting it.
func setsGtkModules(file string) bool {
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return true
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok || strings.HasPrefix(k, "#") || strings.HasPrefix(k, ";") {
			continue
		}
		if strings.TrimSpace(k) == "gtk-modules" && strings.Trim(strings.TrimSpace(v), `"`) != "" {
			return true
		}
	}
	return false
}

// checkManager checks the manager by its process: the user's, a child of
// init, running a root-owned systemd from the user's manager scope.
func checkManager(p Peer) (ProcID, error) {
	id := ProcID{PID: p.PID}
	if p.UID != os.Getuid() {
		return id, errors.New("the manager's socket is another user's")
	}
	ppid, start, err := procStat(p.PID)
	if err != nil {
		return id, fmt.Errorf("the manager's process does not read: %v", err)
	}
	id.Start = start
	if ppid != 1 {
		return id, errors.New("the manager was not started by init")
	}
	// The image it runs, as the kernel holds it: not whatever a path
	// names now.
	fi, err := procExe(p.PID)
	if err != nil {
		return id, fmt.Errorf("the manager's program does not read: %v", err)
	}
	if !fi.Mode().IsRegular() || !ownedBy(fi, rootUID) || fi.Mode().Perm()&0o022 != 0 {
		return id, errors.New("the manager's program is not a root-owned file")
	}
	uid := strconv.Itoa(os.Getuid())
	want := "/user.slice/user-" + uid + ".slice/user@" + uid + ".service/init.scope"
	if cg, err := procCgroup(p.PID); err != nil || cg != want {
		return id, errors.New("the manager is not in the user manager's own scope")
	}
	return id, nil
}

// checkMain checks the unit's main process and returns its start ticks:
// the manager's child, in the unit's cgroup, started within a second of
// when the manager says it started it.
func checkMain(pid, manager int, cgroup string, startedUs uint64) (uint64, error) {
	ppid, start, err := procStat(pid)
	if err != nil {
		return 0, fmt.Errorf("its main process does not read: %v", err)
	}
	if ppid != manager {
		return start, errors.New("its main process is not the manager's child")
	}
	if cg, err := procCgroup(pid); err != nil || cg != cgroup {
		return start, errors.New("its main process is not in the unit's cgroup")
	}
	bt, err := bootTime()
	if err != nil {
		return start, err
	}
	started := bt.Add(time.Duration(float64(start) / clkTck * float64(time.Second)))
	// btime is in whole seconds and ticks are hundredths: two seconds.
	said := time.UnixMicro(int64(startedUs))
	if d := started.Sub(said); d > 2*time.Second || d < -2*time.Second {
		return start, errors.New("its main process did not start when the manager started it")
	}
	return start, nil
}

// execStart is ExecStart's one command line.
func execStart(raw any) ([]string, error) {
	cmds, _ := raw.([]any)
	if len(cmds) != 1 {
		return nil, errors.New("it does not run exactly one ExecStart command")
	}
	c := cmds[0].([]any)
	path := c[0].(string)
	argv := strsOf(c[1])
	if !filepath.IsAbs(path) || len(argv) == 0 {
		return nil, errors.New("its ExecStart is not an absolute path")
	}
	return append([]string{path}, argv[1:]...), nil
}

// checkBinary checks the program ExecStart runs: a regular file of the
// user's, one link, not a symlink, changed before the main process
// started, and hashes it.
func checkBinary(argv []string, mainStart uint64) (FileID, error) {
	id := FileID{Path: argv[0]}
	fi, err := os.Lstat(id.Path)
	if err != nil {
		return id, fmt.Errorf("its program does not read: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() {
		return id, errors.New("its program is not a regular file")
	}
	if int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return id, errors.New("its program is not the user's own file with one link")
	}
	id.Dev, id.Ino = st.Dev, st.Ino
	if bt, err := bootTime(); err == nil && mainStart > 0 {
		started := bt.Add(time.Duration(float64(mainStart) / clkTck * float64(time.Second)))
		if time.Unix(st.Ctim.Sec, st.Ctim.Nsec).After(started) {
			return id, errors.New("its program changed after the process started")
		}
	}
	id.SHA256, err = hashFile(id.Path)
	if err != nil {
		return id, fmt.Errorf("its program does not read: %v", err)
	}
	// The file hashed is the one checked: same device, same inode.
	if fi2, err := os.Lstat(id.Path); err != nil || !os.SameFile(fi, fi2) {
		return id, errors.New("its program changed while Den read it")
	}
	return id, nil
}

// checkEnv checks the unit's Environment: every name that may load code
// is a pin with its value, and every pin is set. It records the pins.
func checkEnv(env []string, v *Verifier) Problems {
	var probs Problems
	fail := func(f string, a ...any) { probs = append(probs, Problem(fmt.Sprintf(f, a...))) }
	set := map[string]string{}
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		set[name] = val
	}
	for name := range set {
		if !denied(name) {
			continue
		}
		_, exact := pinExact[name]
		if !exact && name != pinPluginDir && name != pinScanner && name != pinRegistry {
			fail("its environment sets %s", name)
		}
	}
	for name, want := range pinExact {
		got, ok := set[name]
		if !ok || got != want {
			fail("%s is not pinned to %q", name, want)
		}
		v.Pins[name] = want
	}
	dir, ok := set[pinPluginDir]
	if !ok || !systemPathFn(dir, true) {
		fail("%s is not one root-owned system directory", pinPluginDir)
	}
	v.Pins[pinPluginDir] = dir
	scanner, ok := set[pinScanner]
	if !ok || !systemPathFn(scanner, false) {
		fail("%s is not a root-owned system program", pinScanner)
	} else if sum, err := hashFile(scanner); err == nil {
		fi, _ := os.Lstat(scanner)
		st := fi.Sys().(*syscall.Stat_t)
		v.Scanner = FileID{Path: scanner, SHA256: sum, Dev: st.Dev, Ino: st.Ino}
	}
	v.Pins[pinScanner] = scanner
	reg, ok := set[pinRegistry]
	run := os.Getenv("XDG_RUNTIME_DIR")
	if !ok || run == "" || reg != filepath.Join(run, "drey-host", "gst-registry.bin") {
		fail("%s is not pinned under the user's runtime directory", pinRegistry)
	}
	v.Pins[pinRegistry] = reg
	return probs
}

// systemPath reports whether p is an absolute path to a directory (dir)
// or a regular file, owned by root and not writable by the user or its
// group, with no symlink at any step and every step root-owned.
func systemPath(p string, dir bool) bool {
	if !filepath.IsAbs(p) || strings.ContainsRune(p, ':') || filepath.Clean(p) != p {
		return false
	}
	cur := "/"
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !ownedBy(fi, rootUID) || fi.Mode().Perm()&0o022 != 0 {
			return false
		}
	}
	fi, _ := os.Lstat(p)
	if dir {
		return fi.IsDir()
	}
	return fi.Mode().IsRegular()
}

func ownedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// maxHashed caps a file Den hashes.
const maxHashed = 512 << 20

func hashFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("no path")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxHashed+1))
	if err != nil {
		return "", err
	}
	if n > maxHashed {
		return "", errors.New("too large to check")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Compare lists how now differs from what Den recorded, in words for the
// card; none means the same service, run the same way, on the same build.
// The main process is the one thing that may differ: a restart.
func Compare(was, now Verifier) Problems {
	var d Problems
	diff := func(f string, a ...any) { d = append(d, Problem(fmt.Sprintf(f, a...))) }
	if was.Unit != now.Unit {
		diff("another unit (%s)", now.Unit)
	}
	if was.Boot != now.Boot {
		diff("another boot")
	}
	if was.UID != now.UID {
		diff("another user")
	}
	if was.Manager != now.Manager {
		diff("the systemd user manager is another process")
	}
	if was.Exe != now.Exe {
		diff("the program changed (%s)", now.Exe.Path)
	}
	if was.Props != now.Props {
		diff("how systemd runs it changed (its unit's settings)")
	}
	if was.Fragment != now.Fragment || !sameMap(was.Files, now.Files) {
		diff("its unit file changed")
	}
	if !sameMap(was.Pins, now.Pins) {
		var names []string
		for k := range now.Pins {
			if was.Pins[k] != now.Pins[k] {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		diff("its pinned environment changed (%s)", strings.Join(names, ", "))
	}
	if was.Scanner != now.Scanner {
		diff("GStreamer's plugin scanner changed")
	}
	return d
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// props are the unit's properties Den reads, each of one exact type.
type props struct {
	raw map[string]any
}

// propSpec is a property Den reads: its interface, its type, and whether
// it is part of how the manager runs the service (the digest).
type propSpec struct {
	iface  string
	sig    Signature
	digest bool
}

// unitProps are condition 3's properties, and those Den checks the main
// process with.
var unitProps = map[string]propSpec{
	"ExecStart":              {serviceIface, "a(sasbttttuii)", true},
	"ExecStartPre":           {serviceIface, "a(sasbttttuii)", true},
	"ExecStartPost":          {serviceIface, "a(sasbttttuii)", true},
	"ExecCondition":          {serviceIface, "a(sasbttttuii)", true},
	"ExecReload":             {serviceIface, "a(sasbttttuii)", true},
	"ExecStop":               {serviceIface, "a(sasbttttuii)", true},
	"ExecStopPost":           {serviceIface, "a(sasbttttuii)", true},
	"Environment":            {serviceIface, "as", true},
	"EnvironmentFiles":       {serviceIface, "a(sb)", true},
	"UnsetEnvironment":       {serviceIface, "as", true},
	"PassEnvironment":        {serviceIface, "as", true},
	"WorkingDirectory":       {serviceIface, "s", true},
	"RootDirectory":          {serviceIface, "s", true},
	"RootImage":              {serviceIface, "s", true},
	"BindPaths":              {serviceIface, "a(ssbt)", true},
	"BindReadOnlyPaths":      {serviceIface, "a(ssbt)", true},
	"ReadWritePaths":         {serviceIface, "as", true},
	"TemporaryFileSystem":    {serviceIface, "a(ss)", true},
	"MountImages":            {serviceIface, "a(ssba(ss))", true},
	"ExtensionImages":        {serviceIface, "a(sba(ss))", true},
	"ExtensionDirectories":   {serviceIface, "as", true},
	"PrivateMounts":          {serviceIface, "b", true},
	"ProtectHome":            {serviceIface, "s", true},
	"ProtectSystem":          {serviceIface, "s", true},
	"Type":                   {serviceIface, "s", true},
	"NotifyAccess":           {serviceIface, "s", true},
	"PIDFile":                {serviceIface, "s", true},
	"Transient":              {unitIface, "b", true},
	"FragmentPath":           {unitIface, "s", true},
	"DropInPaths":            {unitIface, "as", true},
	"ControlGroup":           {serviceIface, "s", false},
	"MainPID":                {serviceIface, "u", false},
	"ExecMainStartTimestamp": {serviceIface, "t", false},
}

func readProps(c *conn, obj ObjectPath) (props, error) {
	p := props{raw: map[string]any{}}
	for name, spec := range unitProps {
		v, err := c.get(obj, spec.iface, name, spec.sig)
		if err != nil {
			return p, fmt.Errorf("%s: %w", name, err)
		}
		p.raw[name] = v
	}
	return p, nil
}

func (p props) str(name string) string { s, _ := p.raw[name].(string); return s }
func (p props) b(name string) bool     { b, _ := p.raw[name].(bool); return b }
func (p props) u32(name string) uint32 { u, _ := p.raw[name].(uint32); return u }
func (p props) u64(name string) uint64 { u, _ := p.raw[name].(uint64); return u }
func (p props) strs(name string) []string {
	return strsOf(p.raw[name])
}

func strsOf(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// digest is the SHA-256 of the properties that say how the manager runs
// the service, in a canonical form: by name, and for a command only its
// path, argv and whether a failure is ignored (not when it last ran).
func (p props) digest() string {
	var names []string
	for name, spec := range unitProps {
		if spec.digest {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, name := range names {
		v := p.raw[name]
		if strings.HasPrefix(name, "Exec") {
			var cmds []any
			items, _ := v.([]any)
			for _, it := range items {
				c := it.([]any)
				cmds = append(cmds, []any{c[0], c[1], c[2]})
			}
			v = cmds
		}
		j, _ := json.Marshal([]any{name, v})
		b.Write(j)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

// readProcStat is a process's parent and start ticks (fields 4 and 22).
func readProcStat(pid int) (int, uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, 0, errors.New("bad stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return 0, 0, errors.New("bad stat")
	}
	ppid, err1 := strconv.Atoi(f[1])
	start, err2 := strconv.ParseUint(f[19], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, errors.New("bad stat")
	}
	return ppid, start, nil
}

// readProcCgroup is a process's cgroup (v2).
func readProcCgroup(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(l, "0::"); ok {
			return p, nil
		}
	}
	return "", errors.New("no cgroup v2")
}

// readProcTracer is a process's TracerPid.
func readProcTracer(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "TracerPid:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, errors.New("no TracerPid")
}

// readClkTck is AT_CLKTCK from this process's auxiliary vector: pairs of
// native words, type then value, to a zero type.
func readClkTck() float64 {
	b, err := os.ReadFile("/proc/self/auxv")
	if err != nil || strconv.IntSize != 64 {
		return 100
	}
	const atClkTck = 17
	for i := 0; i+16 <= len(b); i += 16 {
		typ := binary.NativeEndian.Uint64(b[i:])
		if typ == 0 {
			break
		}
		if typ == atClkTck {
			if v := binary.NativeEndian.Uint64(b[i+8:]); v > 0 && v < 100000 {
				return float64(v)
			}
		}
	}
	return 100
}

// readBootTime is when the machine booted (btime in /proc/stat).
func readBootTime() (time.Time, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Unix(n, 0), err
		}
	}
	return time.Time{}, errors.New("no btime")
}
