package api

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestBindModeAndExit(t *testing.T) {
	path := filepath.Join(privateDir(t), "den-api.sock")
	b, err := bind(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", fi, err)
	}
	if !b.ours() {
		t.Error("not ours")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".den-api.*")); len(left) != 0 {
		t.Errorf("temporary names left: %v", left)
	}
	b.close()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the path was not removed on close")
	}
}

func TestBindLeavesWhatIsNotADeadSocketOfOurs(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "den-api.sock")

	// A live socket: another Den.
	other, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bind(path); !errors.Is(err, ErrOtherDen) {
		t.Errorf("live socket: %v", err)
	}
	other.Close()

	// A dead socket of ours is replaced.
	l, _ := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	l.SetUnlinkOnClose(false)
	l.Close()
	b, err := bind(path)
	if err != nil {
		t.Fatalf("dead socket: %v", err)
	}
	b.close()

	for name, make := range map[string]func() error{
		"regular file": func() error { return os.WriteFile(path, []byte("x"), 0o600) },
		"directory":    func() error { return os.Mkdir(path, 0o700) },
		"symlink":      func() error { return os.Symlink("/etc/passwd", path) },
	} {
		os.RemoveAll(path)
		if err := make(); err != nil {
			t.Fatal(err)
		}
		if _, err := bind(path); err == nil || !strings.Contains(err.Error(), "leaves it alone") {
			t.Errorf("%s: %v", name, err)
		}
		if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSocket != 0 {
			t.Errorf("%s was touched", name)
		}
	}
}

func TestBindRefusesAnOpenFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	os.Mkdir(dir, 0o755)
	os.Chmod(dir, 0o755)
	if _, err := bind(filepath.Join(dir, "den-api.sock")); err == nil {
		t.Error("an open folder was used")
	}
}

// Den never sets a umask: a static check that the package does not call it.
func TestNoUmask(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), "Umask") {
			t.Errorf("%s calls Umask", f)
		}
	}
	_ = syscall.Getuid
}

// A replaced socket is noticed.
func TestHijackIsNoticed(t *testing.T) {
	path := filepath.Join(privateDir(t), "den-api.sock")
	b, err := bind(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.ln.Close()
	os.Remove(path)
	l, _ := net.Listen("unix", path)
	defer l.Close()
	if b.ours() {
		t.Error("a replaced socket still counts as Den's")
	}
	b.close()
	if _, err := os.Lstat(path); err != nil {
		t.Error("Den removed a socket that is not its own")
	}
}
