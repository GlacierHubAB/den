package desksand

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// TestParseWalkOnlyOkPasses: the walk passes only on its whole output
// being one "ok" line; every other output fails, an "ok" after a finding
// and an empty output included.
func TestParseWalkOnlyOkPasses(t *testing.T) {
	if f, err := ParseWalk([]byte("ok\n")); err != nil || f != nil {
		t.Fatalf("ok: %v %v", f, err)
	}
	for _, out := range []string{"", "ok", "ok\nok\n", " ok\n", "OK\n",
		"socket \"/home/u/s\"\nok\n", "ok\nsocket \"/x\"\n", "timeout\n", "timeout\nok\n",
		"unlisted \"/a\"\n", "abstract \"@x\"\n", "socket /unquoted\n", "other \"/x\"\n",
		"socket \"/x\"", strings.Repeat("socket \"/x\"\n", WalkMax/12+1)} {
		f, err := ParseWalk([]byte(out))
		if err == nil && len(f) == 0 {
			t.Errorf("%q passed", out)
		}
	}
	f, err := ParseWalk([]byte("socket \"/home/u/a b\"\nunlisted \"/srv/x\"\ntimeout\n"))
	if err != nil || !slices.Equal(f, []string{`a socket at "/home/u/a b"`,
		`a folder that can be entered but not listed at "/srv/x"`, "the walk took over 10s"}) {
		t.Fatalf("findings %q %v", f, err)
	}
	if got := WalkOutput(nil); got != "ok\n" {
		t.Fatalf("WalkOutput(nil) = %q", got)
	}
}

func TestPassEnvClosed(t *testing.T) {
	got := PassEnv([]string{"PATH=/usr/bin", "HYPRLAND_INSTANCE_SIGNATURE=s", "DBUS_SESSION_BUS_ADDRESS=unix:path=/b",
		"SSH_AUTH_SOCK=/s", "DEN_SOCKET=/d", "DISPLAY=:0", "LD_PRELOAD=/x.so", "noequals"})
	want := []string{"PATH=/usr/bin", "DBUS_SESSION_BUS_ADDRESS=disabled:", "DISPLAY=:0"}
	if !slices.Equal(got, want) {
		t.Fatalf("PassEnv = %q, want %q", got, want)
	}
}

// TestPassEnvKeepsEnv: everything Env gives the service reaches bwrap.
func TestPassEnvKeepsEnv(t *testing.T) {
	s := testSpec()
	s.X11, s.XAuth = true, "/x/xauth"
	env := Env(s)
	if got := PassEnv(env); !slices.Equal(got, env) {
		t.Fatalf("PassEnv dropped some of Env:\n got %q\nwant %q", got, env)
	}
}

func TestXAuthFor(t *testing.T) {
	entry := func(num, cookie string) []byte {
		b := []byte{0, 1}
		for _, f := range []string{"host", num, "MIT-MAGIC-COOKIE-1", cookie} {
			b = append(b, byte(len(f)>>8), byte(len(f)))
			b = append(b, f...)
		}
		return b
	}
	file := append(append(entry("0", "zero-cookie-data"), entry("1", "one-cookie-data")...), entry("0", "zero-again")...)
	got, err := XAuthFor(file, "0")
	if want := append(entry("0", "zero-cookie-data"), entry("0", "zero-again")...); err != nil || string(got) != string(want) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := XAuthFor(file, "2"); err == nil {
		t.Fatal("a missing display was found")
	}
	if _, err := XAuthFor(file[:5], "0"); err == nil {
		t.Fatal("a short file was read")
	}
}

func TestNetArgs(t *testing.T) {
	h, rest, err := NetArgs([]string{"--host", "192.168.1.10/24", "--", "/usr/bin/bwrap", "--ro-bind"})
	if err != nil || !slices.Equal(h, []netip.Prefix{netip.MustParsePrefix("192.168.1.10/24")}) || !slices.Equal(rest, []string{"/usr/bin/bwrap", "--ro-bind"}) {
		t.Fatalf("%v %q %v", h, rest, err)
	}
	for _, bad := range [][]string{{}, {"--host"}, {"--host", "x", "--", "/b"}, {"--", "bwrap"}, {"--"}, {"--other", "--", "/b"}} {
		if _, _, err := NetArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
