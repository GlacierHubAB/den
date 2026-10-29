package doctor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/mods"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/stats"
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
		GoVersion:    runtime.Version(),
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		Kernel: func() (string, error) {
			b, err := tail("/proc/sys/kernel/osrelease", 256)
			return string(b), err
		},
		OSRelease: func() ([]byte, error) {
			b, err := tail("/etc/os-release", osRelMax)
			if errors.Is(err, fs.ErrNotExist) {
				return tail("/usr/lib/os-release", osRelMax)
			}
			return b, err
		},
		Stats: func(from, to time.Time) stats.Report {
			p := events.Path()
			return stats.Compute(stats.Paths{Events: p, Hooks: filepath.Join(filepath.Dir(p), "hooks.log"), Missions: mission.Path()}, from, to)
		},
		Names: func() []string { return identifying(home, exe) },
	}
}

// identifying is what could name this person, machine or their projects:
// the backstop withholds any line that holds one. Nothing here is printed.
func identifying(home, exe string) []string {
	out := []string{home, exe, filepath.Dir(exe)}
	if u, err := user.Current(); err == nil {
		out = append(out, u.Username, u.Uid, u.HomeDir, u.Name)
		out = append(out, strings.Fields(u.Name)...)
	}
	if h, err := os.Hostname(); err == nil {
		short, _, _ := strings.Cut(h, ".")
		out = append(out, h, short)
	}
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "USER", "LOGNAME", "HOSTNAME"} {
		out = append(out, os.Getenv(k))
	}
	if b, err := tail("/etc/machine-id", 256); err == nil {
		out = append(out, strings.TrimSpace(string(b)))
	}
	if cfg, err := remote.Load(); err == nil {
		for _, h := range cfg.Hosts {
			who, where, _ := strings.Cut(h.Target, "@")
			out = append(out, h.Label, h.Target, who, where, h.DenPath)
		}
	}
	if ss, err := discover.Scan(); err == nil {
		for _, s := range ss {
			id := s.SessionID
			if len(id) > 8 {
				id = id[:8]
			}
			out = append(out, id, s.Name, badge.Name(s.SessionID), s.CWD, filepath.Base(s.CWD))
		}
	}
	return out
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
