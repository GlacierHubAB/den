// Package desksand builds the sandbox a desktop mission's launched program
// runs in: pasta gives it a network namespace with outbound access only,
// Den's own wrapper closes the host's addresses in it, and bwrap gives it
// a read-only view of the machine with nothing of the session's sockets
// but Wayland's and sound's.
//
// Everything here is a pure function of a Spec, so a test pins the argv
// and the environment exactly.
package desksand

import (
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The namespace's own addresses are in the documentation ranges, which
// nothing routes: the namespace's connected route is never the host's LAN,
// so the prohibit routes over the private ranges hold, and the gateway
// leads nowhere (pasta maps no gateway to the host, --no-map-gw).
var (
	NSAddr4 = netip.MustParseAddr("192.0.2.2")
	NSGate4 = netip.MustParseAddr("192.0.2.1")
	NSAddr6 = netip.MustParseAddr("2001:db8::2")
	NSGate6 = netip.MustParseAddr("2001:db8::1")
	// DNS is pasta's forwarder: queries to it go to the host's resolver
	// on port 53, and it is the one host mapping the namespace has.
	DNS = netip.MustParseAddr("192.0.2.53")
)

// Closed are the ranges the namespace never reaches, on top of every one
// of the host's own addresses: loopback, the private ranges and the local
// ones, so a host address that changes within them stays closed.
var Closed = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// Spec is one launch's sandbox.
type Spec struct {
	// The tools, absolute: bwrap and pasta from root's dirs, Den the
	// running executable.
	Bwrap, Pasta, Den string
	UID, GID          int

	Home       string
	RuntimeDir string // XDG_RUNTIME_DIR
	Wayland    string // WAYLAND_DISPLAY, a name in RuntimeDir or absolute
	// WaylandListener, when set, is the mission's sandboxed listener
	// (wlsec): it is bound at the WAYLAND_DISPLAY path inside, in place of
	// the compositor's own socket, which then appears nowhere.
	WaylandListener string
	DenState        string // Den's own state dir ($XDG_STATE_HOME/den)
	DenConfig       string // Den's own config dir

	Mission string // the mission's folder, read-only inside
	App     string // the app's profile name, escaped by the caller
	Resolv  string // where the system's resolv.conf really is (symlinks followed)

	// X11 is the start card's switch: Display (":<n>") and the socket
	// are passed only with it on. XAuth is a cookie file Den wrote for
	// this mission alone, or "".
	X11   bool
	XAuth string

	Hosts    []netip.Prefix // the host's own addresses with their on-link prefixes, read at launch
	V6       bool           // the host has an IPv6 default route; without one the namespace is IPv4 only
	Writable []string       // desktop.task_writable, each existing
	Hide     []string       // desktop.task_hide, each existing

	// Exists says whether a path is there; a tmpfs or bind over a
	// missing one is left out (home is read-only, so bwrap could not
	// make its mount point).
	Exists func(string) bool

	Env map[string]string // Den's values for EnvNames

	// Render are the GPU's render nodes (/dev/dri/renderD*), never its
	// primary card nodes.
	Render []string
}

// EnvNames are the only variables inside, with Den's values. DBus is set
// to "disabled:", and DISPLAY and XAUTHORITY come only with X11 on.
var EnvNames = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE",
	"XDG_RUNTIME_DIR", "WAYLAND_DISPLAY", "XDG_CURRENT_DESKTOP", "XDG_DATA_DIRS", "XDG_CONFIG_DIRS"}

// Env is the launch's whole environment: the service gets exactly this,
// by name, and the wrapper passes only these names on to bwrap.
func Env(s Spec) []string {
	var out []string
	for _, k := range EnvNames {
		if v, ok := s.Env[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	out = append(out, "XDG_SESSION_TYPE=wayland", "DBUS_SESSION_BUS_ADDRESS=disabled:")
	// Home is read-only: an app keeps its settings, data, cache and state
	// in its own profile.
	for _, d := range ProfileDirs {
		out = append(out, d.Name+"="+filepath.Join(Profile(s.Mission, s.App), d.Dir))
	}
	if s.X11 {
		if d, ok := s.Env["DISPLAY"]; ok {
			out = append(out, "DISPLAY="+d)
		}
		if s.XAuth != "" {
			out = append(out, "XAUTHORITY="+s.XAuth)
		}
	}
	return out
}

// ProfileDirs are the XDG folders an app gets inside its profile, which
// Den makes before the launch.
var ProfileDirs = []struct{ Name, Dir string }{
	{"XDG_CONFIG_HOME", "config"}, {"XDG_DATA_HOME", "data"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_STATE_HOME", "state"},
}

// Work and Profile are the two folders inside the mission's that a
// launched program may write.
func Work(mission string) string         { return filepath.Join(mission, "work") }
func Profile(mission, app string) string { return filepath.Join(mission, "profile-"+app) }

// Argv is the launch's ExecStart: pasta, Den's net stage, bwrap, then the
// command through /bin/sh -c.
func Argv(s Spec, command string) []string {
	return append(Outer(s), "--", "/bin/sh", "-c", command)
}

// Outer is everything before the program bwrap runs, so the socket walk
// runs in the very same sandbox as the launch.
func Outer(s Spec) []string {
	a := []string{s.Pasta, "--config-net", "--foreground", "--quiet", "--no-map-gw",
		"-a", NSAddr4.String(), "-n", "24", "-g", NSGate4.String()}
	if s.V6 {
		a = append(a, "-a", NSAddr6.String(), "-g", NSGate6.String())
	} else {
		a = append(a, "-4") // the host has no way out over IPv6
	}
	a = append(a, "--dns-forward", DNS.String(),
		"-t", "none", "-u", "none", "-T", "none", "-U", "none",
		"--", s.Den, "desk-wrap", "net")
	for _, h := range s.Hosts {
		a = append(a, "--host", h.String())
	}
	a = append(a, "--")
	return append(a, Bwrap(s)...)
}

// Bwrap is bwrap's own argv, in mount order: later mounts sit on top of
// earlier ones.
func Bwrap(s Spec) []string {
	a := []string{s.Bwrap,
		"--unshare-user", "--uid", strconv.Itoa(s.UID), "--gid", strconv.Itoa(s.GID),
		"--unshare-pid", "--unshare-ipc", "--unshare-cgroup", "--disable-userns", "--new-session", "--die-with-parent", "--cap-drop", "ALL",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
	}
	for _, r := range s.Render {
		a = append(a, "--dev-bind", r, r)
	}
	a = append(a,
		"--proc", "/proc",
		"--ro-bind", "/sys", "/sys", "--tmpfs", "/sys/fs/cgroup", "--remount-ro", "/sys/fs/cgroup",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
	)
	if s.Resolv != "" {
		a = append(a, "--ro-bind", filepath.Join(s.Mission, "resolv.conf"), s.Resolv)
	}
	if s.RuntimeDir != "" {
		a = append(a, "--tmpfs", s.RuntimeDir)
		way := s.Wayland
		if way != "" && !filepath.IsAbs(way) {
			way = filepath.Join(s.RuntimeDir, way)
		}
		switch {
		case way != "" && s.WaylandListener != "":
			a = append(a, "--ro-bind", s.WaylandListener, way)
		case way != "":
			a = append(a, "--ro-bind", way, way)
		}
		for _, p := range []string{"pipewire-0", "pulse/native"} {
			p = filepath.Join(s.RuntimeDir, p)
			a = append(a, "--ro-bind-try", p, p)
		}
	}
	if s.X11 {
		if x := XSocket(s.Env["DISPLAY"]); x != "" {
			a = append(a, "--ro-bind", x, x)
		}
		if s.XAuth != "" {
			a = append(a, "--ro-bind", s.XAuth, s.XAuth)
		}
	}
	hide := []string{filepath.Join(s.Home, ".ssh"), filepath.Join(s.Home, ".gnupg"), s.DenState, s.DenConfig}
	for _, p := range append(hide, s.Hide...) {
		if p != "" && s.Exists(p) {
			a = append(a, "--tmpfs", p)
		}
	}
	a = append(a, "--ro-bind", s.Mission, s.Mission,
		"--bind", Work(s.Mission), Work(s.Mission),
		"--bind", Profile(s.Mission, s.App), Profile(s.Mission, s.App))
	for _, p := range s.Writable {
		a = append(a, "--bind", p, p)
	}
	return append(a, "--chdir", Work(s.Mission))
}

// RenderNodes are the GPU's render nodes in a /dev/dri: renderD<n>
// character devices only, never the primary card nodes.
func RenderNodes(dri string) []string {
	ents, err := os.ReadDir(dri)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n, ok := strings.CutPrefix(e.Name(), "renderD")
		if _, err := strconv.ParseUint(n, 10, 16); !ok || err != nil || e.Type()&os.ModeCharDevice == 0 {
			continue
		}
		out = append(out, filepath.Join(dri, e.Name()))
	}
	return out
}

// XSocket is Xwayland's path socket for a DISPLAY of ":<n>", or "".
func XSocket(display string) string {
	n, ok := strings.CutPrefix(display, ":")
	if i := strings.IndexByte(n, '.'); i >= 0 {
		n = n[:i]
	}
	if !ok || n == "" {
		return ""
	}
	if _, err := strconv.ParseUint(n, 10, 16); err != nil {
		return ""
	}
	return "/tmp/.X11-unix/X" + n
}
