package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/desksand"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// What a mission launches runs in a sandbox (internal/desksand): pasta's
// network namespace with outbound access only, and bwrap's read-only view
// of the machine with only Wayland's and sound's sockets, Xwayland's with
// the start card's X11 switch on. Without pasta or bwrap nothing launches:
// Den never falls back to the host's network or to no sandbox.

// deskSandTools are the sandbox's tools, taken once when Den starts.
type deskSandTools struct {
	bwrap, pasta string
	self         desksand.Self
	err          error // why there are none
}

var (
	deskSandOnce  deskSandTools
	deskSandTaken sync.Once
	// The seams: what a launch's sandbox is, and its socket walk.
	deskSandSpec = deskSpecFor
	deskSandWalk = deskWalk
	// deskSandErr is why the sandbox has no tools here, for the start card.
	deskSandErr = func() error { TakeDesktopSandbox(); return deskSandOnce.err }
)

// deskNeedsPasta is the refusal when pasta is missing.
const deskNeedsPasta = "needs pasta (passt) to give opened apps the network without your machine's local services"

// TakeDesktopSandbox takes the sandbox's tools once, at Den's start: bwrap
// and pasta from root's dirs, and Den's own binary as it is now.
func TakeDesktopSandbox() { deskSandTaken.Do(takeDesktopSandbox) }

func takeDesktopSandbox() {
	t := &deskSandOnce
	var err error
	if t.bwrap, err = sysd.RootTool("bwrap"); err != nil {
		t.err = fmt.Errorf("needs bwrap (bubblewrap) to keep opened apps away from your session: %v", err)
		return
	}
	if t.pasta, err = sysd.RootToolLink("pasta"); err != nil {
		t.err = errors.New(deskNeedsPasta)
		return
	}
	if t.self, err = desksand.TakeSelf(); err != nil {
		t.err = fmt.Errorf("Den's own binary: %v", err)
	}
}

// deskSandLines are the start card's lines on what opened apps get.
func deskSandLines() []string {
	lines := []string{"Apps it opens run in a sandbox: they see your files read-only, write only in the mission's own folder, reach the internet but not your machine's local services or your network, and stop with the mission."}
	if w, ok := config.DesktopPaths("task_writable"); !ok {
		lines = append(lines, "Your config's desktop.task_writable doesn't read as a list of folders: apps it opens won't start.")
	} else if len(w) > 0 {
		lines = append(lines, "They may also write in (desktop.task_writable): "+strings.Join(w, ", ")+".")
	}
	if err := deskSandErr(); err != nil {
		lines = append(lines, "It can't open apps on this machine yet: Den "+err.Error()+".")
	}
	return lines
}

// deskSpecFor is the sandbox of one launch of a mission: app names its
// profile, x11 is the mission's switch. It refuses rather than loosen:
// a tool missing or changed, a config list that doesn't read, a writable
// folder that is missing or overlaps one the sandbox hides.
func deskSpecFor(dir, app string, x11 bool) (desksand.Spec, error) {
	TakeDesktopSandbox()
	t := deskSandOnce
	if t.err != nil {
		return desksand.Spec{}, t.err
	}
	if err := t.self.Check(); err != nil {
		return desksand.Spec{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return desksand.Spec{}, err
	}
	writable, ok := config.DesktopPaths("task_writable")
	if !ok {
		return desksand.Spec{}, errors.New("your config's desktop.task_writable doesn't read as a list of absolute folders")
	}
	hide, ok := config.DesktopPaths("task_hide")
	if !ok {
		return desksand.Spec{}, errors.New("your config's desktop.task_hide doesn't read as a list of absolute folders")
	}
	s := desksand.Spec{
		Bwrap: t.bwrap, Pasta: t.pasta, Den: t.self.Path, UID: os.Getuid(), GID: os.Getgid(),
		Home: home, RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"), Wayland: os.Getenv("WAYLAND_DISPLAY"),
		DenState: stateDir(), DenConfig: filepath.Dir(config.Path()),
		Mission: dir, App: app, X11: x11,
		Render: desksand.RenderNodes("/dev/dri"),
		Exists: func(p string) bool { _, err := os.Lstat(p); return err == nil },
		Env:    map[string]string{},
	}
	if s.RuntimeDir == "" || s.Wayland == "" {
		return desksand.Spec{}, errors.New("no Wayland session to open apps in")
	}
	for _, p := range hide {
		if s.Exists(p) {
			s.Hide = append(s.Hide, resolved(p))
		}
	}
	hidden := append([]string{filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg"), s.DenState, s.DenConfig}, hide...)
	if s.Writable, err = deskWritable(writable, hidden, home, dir, filepath.Dir(t.self.Path)); err != nil {
		return desksand.Spec{}, err
	}
	for _, k := range desksand.EnvNames {
		if v, ok := os.LookupEnv(k); ok {
			s.Env[k] = v
		}
	}
	if x11 {
		d := os.Getenv("DISPLAY")
		if desksand.XSocket(d) == "" {
			return desksand.Spec{}, errors.New("X11 apps are on, but there is no local X display (DISPLAY)")
		}
		s.Env["DISPLAY"] = d
		if xa := os.Getenv("XAUTHORITY"); xa != "" {
			file, err := os.ReadFile(xa)
			if err != nil {
				return desksand.Spec{}, fmt.Errorf("X11 apps are on, but the X cookie can't be read: %v", err)
			}
			n := strings.TrimPrefix(strings.SplitN(d, ".", 2)[0], ":")
			cookie, err := desksand.XAuthFor(file, n)
			if err != nil {
				return desksand.Spec{}, fmt.Errorf("X11 apps are on, but %v", err)
			}
			s.XAuth = filepath.Join(dir, "xauth")
			if err := os.WriteFile(s.XAuth, cookie, 0o600); err != nil {
				return desksand.Spec{}, err
			}
		}
	}
	if s.Resolv, err = filepath.EvalSymlinks("/etc/resolv.conf"); err != nil {
		return desksand.Spec{}, fmt.Errorf("the system's resolv.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resolv.conf"), []byte("nameserver "+desksand.DNS.String()+"\n"), 0o644); err != nil {
		return desksand.Spec{}, err
	}
	if raw, err := os.ReadFile("/proc/net/ipv6_route"); err == nil {
		s.V6 = desksand.HasV6Default(string(raw))
	}
	if s.Hosts, err = deskHostAddrs(); err != nil {
		return desksand.Spec{}, fmt.Errorf("the machine's own addresses: %v", err)
	}
	// One level at a time, never following a link: an app may have left
	// one in its profile, and Den must not make a folder where it points.
	for _, d := range append([]string{desksand.Work(dir)}, profileDirs(dir, app)...) {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return desksand.Spec{}, err
		}
	}
	return s, nil
}

func profileDirs(dir, app string) []string {
	out := []string{desksand.Profile(dir, app)}
	for _, d := range desksand.ProfileDirs {
		out = append(out, filepath.Join(desksand.Profile(dir, app), d.Dir))
	}
	return out
}

// deskWritable checks desktop.task_writable and gives the folders to bind,
// links resolved, as bwrap would follow them: each must be a folder that
// is not the whole home, and must not overlap a hidden folder, the
// mission's own, or the one holding Den's binary.
func deskWritable(writable, hidden []string, home, mission, denDir string) ([]string, error) {
	type fence struct{ path, why string }
	var fences []fence
	for _, h := range hidden {
		fences = append(fences, fence{resolved(h), "a folder the sandbox hides"})
	}
	fences = append(fences, fence{resolved(mission), "the mission's own folder"}, fence{resolved(denDir), "the folder of Den's own binary"})
	var out []string
	for _, w := range writable {
		r := resolved(w)
		if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("desktop.task_writable names %s, which isn't a folder", w)
		}
		if r == resolved(home) || r == "/" {
			return nil, fmt.Errorf("desktop.task_writable names %s: a writable folder can't be your whole home", w)
		}
		for _, f := range fences {
			if within(r, f.path) || within(f.path, r) {
				return nil, fmt.Errorf("desktop.task_writable names %s, which overlaps %s, %s", w, f.path, f.why)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// resolved is a path with its links followed, or as it is when it can't
// be (a folder that isn't there yet).
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// within reports whether p is q or under it.
func within(p, q string) bool {
	return p == q || strings.HasPrefix(p, strings.TrimSuffix(q, "/")+"/")
}

// deskHostAddrs are the machine's own addresses with their on-link
// prefixes, every interface's, read at each launch.
var deskHostAddrs = func() ([]netip.Prefix, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		bits, _ := n.Mask.Size()
		ip = ip.Unmap()
		if ip.Is4() && bits > 32 {
			bits -= 96
		}
		out = append(out, netip.PrefixFrom(ip, bits))
	}
	return out, nil
}

// deskWalkOK is what Den writes once a mission's sandbox walked clean.
type deskWalkOK struct {
	Mission string `json:"mission"`
	X11     bool   `json:"x11"`
}

// deskWalked reports whether the mission's sandbox already walked clean,
// with this X11 switch, by Den's own record in the mission's folder.
func deskWalked(dir, id string, x11 bool) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "walk.ok"))
	var ok deskWalkOK
	return err == nil && json.Unmarshal(raw, &ok) == nil && ok.Mission == id && ok.X11 == x11
}

// deskSandCheck walks a mission's sandbox at its first launch, and
// records a clean walk: a socket the sandbox should not see refuses the
// launch, naming it.
func deskSandCheck(s desksand.Spec, id string) error {
	if !filepath.IsAbs(s.Mission) {
		return errors.New("the mission has no folder of its own")
	}
	if deskWalked(s.Mission, id, s.X11) {
		return nil
	}
	findings, err := deskSandWalk(s)
	if err != nil {
		return fmt.Errorf("the sandbox's socket walk failed (%v)", err)
	}
	if len(findings) > 0 {
		return fmt.Errorf("the sandbox can reach %s; hide it with desktop.task_hide in your config", strings.Join(findings, ", "))
	}
	raw, _ := json.Marshal(deskWalkOK{Mission: id, X11: s.X11})
	return os.WriteFile(filepath.Join(s.Mission, "walk.ok"), raw, 0o600)
}

// deskWalk runs the walk in the very sandbox a launch gets, as Den's own
// process (no app in it), and reads what it says over a pipe.
func deskWalk(s desksand.Spec) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), desksand.WalkBound+5*time.Second)
	defer cancel()
	argv := append(desksand.Outer(s), "--", s.Den, "desk-wrap", "walk")
	for _, a := range desksand.Allowed(s) {
		argv = append(argv, "--allow", a)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = desksand.Env(s)
	cmd.Dir = desksand.Work(s.Mission)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var out limitBuf
	out.max = desksand.WalkMax + 1
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return []string{"the walk took over " + desksand.WalkBound.String()}, nil
		}
		return nil, err
	}
	return desksand.ParseWalk(out.Bytes())
}

// limitBuf keeps at most max bytes, so a walk that says too much fails
// its parse rather than filling Den's memory.
type limitBuf struct {
	bytes.Buffer
	max int
}

func (b *limitBuf) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// deskProfileName is the profile a launched command gets: its program's
// name, escaped, so a browser opened twice keeps one profile.
func deskProfileName(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return "app"
	}
	if n := sysd.EscapePart(filepath.Base(f[0])); n != "" && !slices.Contains([]string{".", ".."}, n) {
		return n
	}
	return "app"
}
