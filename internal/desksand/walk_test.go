package desksand

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestNoValuesOnArgv: bwrap gets no --setenv (the values reach it by name
// from the service), the bus is disabled, and XDG_RUNTIME_DIR is the
// folder bwrap makes a fresh tmpfs.
func TestNoValuesOnArgv(t *testing.T) {
	s := testSpec()
	for _, v := range Argv(s, "x") {
		if strings.HasPrefix(v, "--setenv") || strings.HasPrefix(v, "--clearenv") {
			t.Fatalf("argv carries %s", v)
		}
	}
	env := Env(s)
	if !slices.Contains(env, "DBUS_SESSION_BUS_ADDRESS=disabled:") {
		t.Fatalf("bus not disabled: %q", env)
	}
	b := Bwrap(s)
	i := slices.Index(b, s.RuntimeDir)
	if !slices.Contains(env, "XDG_RUNTIME_DIR="+s.RuntimeDir) || i < 1 || b[i-1] != "--tmpfs" {
		t.Fatalf("XDG_RUNTIME_DIR is not the tmpfs'd folder: %q", b)
	}
}

func TestRenderNodesOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "renderD128"), nil, 0o600) // a plain file, not a device
	if got := RenderNodes(dir); len(got) != 0 {
		t.Fatalf("a plain file taken: %v", got)
	}
	for _, p := range RenderNodes("/dev/dri") {
		if !strings.HasPrefix(filepath.Base(p), "renderD") {
			t.Fatalf("took %s", p)
		}
	}
}

func listen(t *testing.T, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o700)
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

// TestWalkNamesEverySocket: planted sockets outside the allowed list are
// named, the allowed ones are not, and a folder that can be entered but
// not listed is named too.
func TestWalkNamesEverySocket(t *testing.T) {
	root := t.TempDir()
	run := filepath.Join(root, "run")
	allow := []string{filepath.Join(run, "wayland-1"), filepath.Join(run, "pulse/native")}
	for _, p := range allow {
		listen(t, p)
	}
	bad := []string{filepath.Join(root, "home/.tmux-s"), filepath.Join(root, "srv/editor.sock"), filepath.Join(run, "bus")}
	for _, p := range bad {
		listen(t, p)
	}
	hidden := filepath.Join(root, "home/xonly")
	os.MkdirAll(hidden, 0o700)
	os.Chmod(hidden, 0o100)
	t.Cleanup(func() { os.Chmod(hidden, 0o700) })

	got := Walk([]string{root}, allow, time.Now().Add(WalkBound))
	want := []string{"unlisted " + strconv.Quote(hidden)}
	for _, p := range bad {
		want = append(want, "socket "+strconv.Quote(p))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("walk:\n got %q\nwant %q", got, want)
	}
}

// TestWalkSkipsWhatItCantConnect: a socket the sandbox's user can't
// connect to (someone else's 0600, faked) isn't reported; one it can
// (0666) still is. The real access call is checked on a socket of the
// test's own.
func TestWalkSkipsWhatItCantConnect(t *testing.T) {
	root := t.TempDir()
	theirs, open := filepath.Join(root, "etc/S.gpg-agent"), filepath.Join(root, "srv/open.sock")
	listen(t, theirs)
	listen(t, open)
	os.Chmod(open, 0o666)
	if !connectable(open) {
		t.Fatal("the real access call says a 0666 socket of the test's own can't be connected to")
	}
	old := connectable
	t.Cleanup(func() { connectable = old })
	connectable = func(p string) bool { return p != theirs }
	got := Walk([]string{root}, nil, time.Now().Add(WalkBound))
	if !slices.Equal(got, []string{"socket " + strconv.Quote(open)}) {
		t.Fatalf("walk %q, want only the connectable socket", got)
	}
}

func TestWalkBounded(t *testing.T) {
	got := Walk([]string{t.TempDir()}, nil, time.Now().Add(-time.Second))
	if !slices.Equal(got, []string{"timeout"}) {
		t.Fatalf("past the bound: %q", got)
	}
}

func TestMountsAndAbstract(t *testing.T) {
	mi := `22 1 0:21 / / ro,relatime - btrfs /dev/x rw
23 22 0:22 / /proc rw - proc proc rw
24 22 0:23 / /my\040disk rw - ext4 /dev/y rw
25 22 0:24 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw
26 22 0:25 / /run/user/1000 rw - tmpfs tmpfs rw
`
	if got := Mounts(mi); !slices.Equal(got, []string{"/", "/my disk", "/run/user/1000"}) {
		t.Fatalf("mounts %q", got)
	}
	nu := `Num       RefCount Protocol Flags    Type St Inode Path
0000000000000000: 00000002 00000000 00010000 0001 01 1234 @/tmp/.X11-unix/X0
0000000000000000: 00000002 00000000 00010000 0001 01 1235 /run/user/1000/wayland-1
0000000000000000: 00000003 00000000 00000000 0001 03 1236
`
	if got := Abstract(nu); !slices.Equal(got, []string{`abstract "@/tmp/.X11-unix/X0"`}) {
		t.Fatalf("abstract %q", got)
	}
}
