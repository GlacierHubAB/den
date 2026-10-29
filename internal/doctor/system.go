package doctor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/mods"
	"github.com/GlacierHubAB/den/internal/remote"
)

// Bounds on running `<program> --version`.
const (
	versionTimeout = 5 * time.Second
	versionMax     = 4 << 10
)

// System is the Env of this machine. Nothing in it writes.
func System() Env {
	home, _ := os.UserHomeDir()
	// As install-hooks resolves it, so the hooks check compares like with like.
	exe, _ := hooks.Executable()
	return Env{
		Lstat:        os.Lstat,
		EvalSymlinks: filepath.EvalSymlinks,
		UID:          os.Getuid(),
		Home:         home,
		Getenv:       os.Getenv,
		LookPath:     exec.LookPath,
		Version:      runVersion,
		ReadFile:     os.ReadFile,
		Tail:         tail,
		Exe:          exe,
		Running:      func() bool { _, ok := bridge.Running(bridge.SocketPath()); return ok },
		Sessions:     liveSessions,
		Mods:         bundledMods,
		Remotes:      savedRemotes,
		Config:       config.Load(),
		Issues:       config.Check,
		Now:          time.Now(),
	}
}

// runVersion runs `path --version` with no stdin, a timeout and capped
// output; stderr is dropped.
func runVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	var out capped
	cmd.Stdout = &out
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ErrTimeout
	}
	if err != nil {
		return "", err
	}
	return out.b.String(), nil
}

// capped keeps the first versionMax bytes written to it and drops the rest.
type capped struct{ b bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := versionMax - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

// tail reads at most the last n bytes of path.
func tail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if off := fi.Size() - n; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, n))
}

func liveSessions() (int, error) {
	if _, err := os.Stat(discover.Dir()); err != nil {
		return 0, err
	}
	ss, err := discover.Scan()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range ss {
		if s.Alive {
			n++
		}
	}
	return n, nil
}

func bundledMods() ([]Mod, error) {
	list, err := mods.List()
	if err != nil {
		return nil, err
	}
	out := make([]Mod, 0, len(list))
	for _, m := range list {
		out = append(out, Mod{Name: m.Name, Bundled: m.Version, Installed: mods.InstalledVersion(m), Loaded: m.Loaded != "", Target: m.Installs})
	}
	return out, nil
}

func savedRemotes() ([]Remote, error) {
	if _, err := os.Stat(remote.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cfg, err := remote.Load()
	if err != nil {
		// Load reads the file, which Stat just found, so this is the parse.
		return nil, ErrParse
	}
	dir := remote.StateDir()
	out := make([]Remote, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		r := Remote{Enabled: h.Enabled}
		if k, ok := remote.LoadKnown(dir, h.Label); ok {
			r.Saved, r.State, r.Heartbeat = true, string(k.State), k.Heartbeat
		}
		out = append(out, r)
	}
	return out, nil
}
