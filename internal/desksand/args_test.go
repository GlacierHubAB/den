package desksand

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testSpec() Spec {
	return Spec{
		Bwrap: "/usr/bin/bwrap", Pasta: "/usr/bin/pasta", Den: "/home/u/bin/den",
		UID: 1000, GID: 1000,
		Home: "/home/u", RuntimeDir: "/run/user/1000", Wayland: "wayland-1",
		DenState: "/home/u/.local/state/den", DenConfig: "/home/u/.config/den",
		Mission: "/home/u/.local/state/den/desktop/m1", App: "firefox",
		Resolv: "/run/systemd/resolve/stub-resolv.conf",
		Hosts:  []netip.Prefix{netip.MustParsePrefix("192.168.1.10/24"), netip.MustParsePrefix("2a01:1:2:3::5/64")}, V6: true,
		Render: []string{"/dev/dri/renderD128"},
		Hide:   []string{"/home/u/secret"}, Writable: []string{"/home/u/Documents/out"},
		Exists: func(p string) bool { return p != "/home/u/.gnupg" },
		Env: map[string]string{"PATH": "/usr/bin", "HOME": "/home/u", "USER": "u", "LANG": "C.UTF-8",
			"XDG_RUNTIME_DIR": "/run/user/1000", "WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0",
			"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus", "HYPRLAND_INSTANCE_SIGNATURE": "sig",
			"SSH_AUTH_SOCK": "/run/user/1000/ssh", "DEN_SOCKET": "/x", "TMUX": "/tmp/t"},
	}
}

// TestArgvExact pins the whole ExecStart, X11 off: the order of bwrap's
// mounts matters, later ones on top.
func TestArgvExact(t *testing.T) {
	got := Argv(testSpec(), "firefox --new-window")
	want := []string{"/usr/bin/pasta", "--config-net", "--foreground", "--quiet", "--no-map-gw",
		"-a", "192.0.2.2", "-n", "24", "-g", "192.0.2.1", "-a", "2001:db8::2", "-g", "2001:db8::1",
		"--dns-forward", "192.0.2.53", "-t", "none", "-u", "none", "-T", "none", "-U", "none",
		"--", "/home/u/bin/den", "desk-wrap", "net", "--host", "192.168.1.10/24", "--host", "2a01:1:2:3::5/64", "--",
		"/usr/bin/bwrap", "--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-cgroup", "--disable-userns", "--new-session", "--die-with-parent", "--cap-drop", "ALL",
		"--ro-bind", "/", "/", "--dev", "/dev", "--dev-bind", "/dev/dri/renderD128", "/dev/dri/renderD128", "--proc", "/proc",
		"--ro-bind", "/sys", "/sys", "--tmpfs", "/sys/fs/cgroup", "--remount-ro", "/sys/fs/cgroup",
		"--tmpfs", "/tmp", "--tmpfs", "/run",
		"--ro-bind", "/home/u/.local/state/den/desktop/m1/resolv.conf", "/run/systemd/resolve/stub-resolv.conf",
		"--tmpfs", "/run/user/1000", "--ro-bind", "/run/user/1000/wayland-1", "/run/user/1000/wayland-1",
		"--ro-bind-try", "/run/user/1000/pipewire-0", "/run/user/1000/pipewire-0",
		"--ro-bind-try", "/run/user/1000/pulse/native", "/run/user/1000/pulse/native",
		"--tmpfs", "/home/u/.ssh", "--tmpfs", "/home/u/.local/state/den", "--tmpfs", "/home/u/.config/den", "--tmpfs", "/home/u/secret",
		"--ro-bind", "/home/u/.local/state/den/desktop/m1", "/home/u/.local/state/den/desktop/m1",
		"--bind", "/home/u/.local/state/den/desktop/m1/work", "/home/u/.local/state/den/desktop/m1/work",
		"--bind", "/home/u/.local/state/den/desktop/m1/profile-firefox", "/home/u/.local/state/den/desktop/m1/profile-firefox",
		"--bind", "/home/u/Documents/out", "/home/u/Documents/out",
		"--chdir", "/home/u/.local/state/den/desktop/m1/work",
		"--", "/bin/sh", "-c", "firefox --new-window"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// TestCgroupHiddenAfterSys: the tmpfs over /sys/fs/cgroup comes after
// every mount that shows /sys, so it sits on top.
func TestCgroupHiddenAfterSys(t *testing.T) {
	a := Bwrap(testSpec())
	cg := slices.Index(a, "/sys/fs/cgroup")
	for i, v := range a {
		if (v == "/" || v == "/sys") && i > cg {
			t.Fatalf("a mount of %s at %d comes after the cgroup tmpfs at %d", v, i, cg)
		}
	}
	if a[cg-1] != "--tmpfs" {
		t.Fatalf("/sys/fs/cgroup is not a tmpfs: %q", a[cg-1])
	}
}

// TestX11OnlyWithTheSwitch: off, no X socket and no DISPLAY; on, both,
// and the cookie file.
func TestX11OnlyWithTheSwitch(t *testing.T) {
	s := testSpec()
	off := strings.Join(Argv(s, "x"), " ") + " " + strings.Join(Env(s), " ")
	if strings.Contains(off, "X11") || strings.Contains(off, " DISPLAY=") || strings.Contains(off, "XAUTHORITY") {
		t.Fatalf("X11 off still has X: %s", off)
	}
	s.X11, s.XAuth = true, s.Mission+"/xauth"
	a := Bwrap(s)
	if i := slices.Index(a, "/tmp/.X11-unix/X0"); i < 0 || a[i-1] != "--ro-bind" || slices.Index(a, "/tmp") > i {
		t.Fatalf("X11 on: no bind of X0 over the fresh /tmp: %q", a)
	}
	if !slices.Contains(a, s.XAuth) {
		t.Fatal("X11 on: no cookie bind")
	}
	env := Env(s)
	if !slices.Contains(env, "DISPLAY=:0") || !slices.Contains(env, "XAUTHORITY="+s.XAuth) {
		t.Fatalf("X11 on: env %q", env)
	}
}

// TestEnvClosed: only the fixed names, with the bus disabled; nothing of
// Hyprland's, ssh's, Den's or tmux's.
func TestEnvClosed(t *testing.T) {
	got := Env(testSpec())
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "USER=u", "LANG=C.UTF-8",
		"XDG_RUNTIME_DIR=/run/user/1000", "WAYLAND_DISPLAY=wayland-1",
		"XDG_SESSION_TYPE=wayland", "DBUS_SESSION_BUS_ADDRESS=disabled:",
		"XDG_CONFIG_HOME=/home/u/.local/state/den/desktop/m1/profile-firefox/config",
		"XDG_DATA_HOME=/home/u/.local/state/den/desktop/m1/profile-firefox/data",
		"XDG_CACHE_HOME=/home/u/.local/state/den/desktop/m1/profile-firefox/cache",
		"XDG_STATE_HOME=/home/u/.local/state/den/desktop/m1/profile-firefox/state"}
	if !slices.Equal(got, want) {
		t.Fatalf("env:\n got %q\nwant %q", got, want)
	}
}

// TestV4OnlyWithoutAV6Route: with no IPv6 way out, pasta gets -4 and no
// v6 address or gateway.
func TestV4OnlyWithoutAV6Route(t *testing.T) {
	s := testSpec()
	s.V6 = false
	a := strings.Join(Outer(s), " ")
	if !strings.Contains(a, " -g 192.0.2.1 -4 --dns-forward ") || strings.Contains(a, "2001:db8") {
		t.Fatalf("v4 only: %s", a)
	}
	lo := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo\n"
	real := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00450003 enp4s0\n"
	if HasV6Default(lo) || !HasV6Default(lo+real) || HasV6Default("") {
		t.Fatal("HasV6Default reads a reject default as a way out, or misses a real one")
	}
}

func TestXSocket(t *testing.T) {
	for in, want := range map[string]string{":0": "/tmp/.X11-unix/X0", ":12.0": "/tmp/.X11-unix/X12",
		"": "", "host:0": "", ":../../x": "", ":": "", ":99999999": ""} {
		if got := XSocket(in); got != want {
			t.Errorf("XSocket(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRoutesCoverPrefixes: each host address, its on-link prefix and
// every closed range, once each.
func TestRoutesCoverPrefixes(t *testing.T) {
	got := Routes(testSpec().Hosts)
	for _, p := range append(slices.Clone(Closed),
		netip.MustParsePrefix("192.168.1.10/32"), netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("2a01:1:2:3::5/128"), netip.MustParsePrefix("2a01:1:2:3::/64")) {
		if !slices.Contains(got, p) {
			t.Errorf("no route for %s", p)
		}
	}
	if len(got) != len(Closed)+4 {
		t.Errorf("%d routes, want %d: %v", len(got), len(Closed)+4, got)
	}
	for _, p := range got {
		for _, ns := range []netip.Addr{NSAddr4, NSGate4, NSAddr6, NSGate6, DNS} {
			if p.Contains(ns) {
				t.Errorf("%s closes the namespace's own %s", p, ns)
			}
		}
	}
}

// TestRouteMsgRoundTrip: what Close sends parses back as the same
// prohibit route, and a dump missing one is caught.
func TestRouteMsgRoundTrip(t *testing.T) {
	want := Routes(testSpec().Hosts)
	var all []byte
	for i, p := range want {
		all = append(all, routeMsg(p, uint32(i))...)
	}
	done := make([]byte, unix.SizeofNlMsghdr+4)
	done[0] = byte(len(done))
	done[4] = unix.NLMSG_DONE
	got, ok, err := parseRoutes(append(all, done...))
	if err != nil || !ok || !slices.Equal(got, want) {
		t.Fatalf("parsed %v %v %v, want %v", got, ok, err, want)
	}
	if miss := Missing(want, got[1:]); !slices.Equal(miss, want[:1]) {
		t.Fatalf("missing %v, want %v", miss, want[:1])
	}
}

func TestSelfChangeRefuses(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	p := filepath.Join(dir, "den")
	os.WriteFile(p, []byte("one"), 0o700)
	s, err := takeSelf(p, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	os.WriteFile(p, []byte("two!"), 0o700)
	if err := s.Check(); err == nil {
		t.Fatal("a changed binary was accepted")
	}
	os.Chmod(p, 0o722)
	if _, err := takeSelf(p, os.Getuid()); err == nil {
		t.Fatal("a group-writable binary was taken")
	}
	os.Chmod(p, 0o700)
	os.Chmod(dir, 0o777)
	if _, err := takeSelf(p, os.Getuid()); err == nil {
		t.Fatal("a binary in a world-writable dir was taken")
	}
}
