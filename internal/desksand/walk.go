package desksand

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// WalkBound is how long the socket walk may take before the launch is
// refused.
const WalkBound = 10 * time.Second

// Allowed are the only sockets a launch may see: Wayland's, sound's, and
// Xwayland's with X11 on.
func Allowed(s Spec) []string {
	var out []string
	way := s.Wayland
	if way != "" && !filepath.IsAbs(way) {
		way = filepath.Join(s.RuntimeDir, way)
	}
	if way != "" {
		out = append(out, way)
	}
	if s.RuntimeDir != "" {
		out = append(out, filepath.Join(s.RuntimeDir, "pipewire-0"), filepath.Join(s.RuntimeDir, "pulse/native"))
	}
	if s.X11 {
		if x := XSocket(s.Env["DISPLAY"]); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// skipFS are the mounts the walk leaves out: the kernel's own views.
var skipFS = map[string]bool{"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "devpts": true,
	"mqueue": true, "devtmpfs": true, "debugfs": true, "tracefs": true, "securityfs": true, "bpf": true,
	"pstore": true, "configfs": true, "fusectl": true, "efivarfs": true, "binfmt_misc": true, "hugetlbfs": true}

// Mounts are the mount points of a mountinfo, without the kernel's views.
func Mounts(mountinfo string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(mountinfo))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := slices.Index(f, "-")
		if len(f) < 5 || sep < 0 || sep+1 >= len(f) || skipFS[f[sep+1]] {
			continue
		}
		out = append(out, unmangle(f[4]))
	}
	return out
}

// unmangle undoes mountinfo's octal escapes (\040 for a space).
func unmangle(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Walk looks under every mount for a socket that is not allowed, and for a
// folder that can be entered but not listed (a socket with a known name in
// it would still be reachable). Each finding is one line naming the path.
// Past the deadline it stops and says so.
func Walk(mounts, allow []string, deadline time.Time) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mounts {
		err := filepath.WalkDir(m, func(p string, d fs.DirEntry, err error) error {
			if time.Now().After(deadline) {
				return errTimeout
			}
			if err != nil {
				if d != nil && d.IsDir() || errors.Is(err, fs.ErrPermission) {
					if unix.Access(p, unix.X_OK) == nil {
						out = append(out, "unlisted "+strconv.Quote(p))
					}
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if seen[p] {
					return fs.SkipDir
				}
				seen[p] = true
				if p != m && slices.Contains(mounts, p) {
					return fs.SkipDir // walked as its own mount
				}
				return nil
			}
			if d.Type()&fs.ModeSocket != 0 && !slices.Contains(allow, p) && !seen[p] && connectable(p) {
				seen[p] = true
				out = append(out, "socket "+strconv.Quote(p))
			}
			return nil
		})
		if errors.Is(err, errTimeout) {
			return append(out, "timeout")
		}
	}
	return out
}

var errTimeout = errors.New("timeout")

// connectable is the kernel's own answer to whether the sandbox's user may
// connect to a socket: write on it and search on every folder down to it,
// ACLs and the user namespace's mapping included. A socket it may not
// connect to (a root daemon's 0600) is not reported.
var connectable = func(p string) bool {
	return unix.Faccessat(unix.AT_FDCWD, p, unix.W_OK, unix.AT_EACCESS) == nil
}

// Abstract are the abstract socket names in a /proc/net/unix.
func Abstract(netUnix string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(netUnix))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 8 && strings.HasPrefix(f[7], "@") {
			out = append(out, "abstract "+strconv.Quote(f[7]))
		}
	}
	return out
}

// SelfWalk is the walk stage: every mount in this process's own view, and
// its network namespace's abstract names.
func SelfWalk(allow []string) []string {
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return []string{"unreadable /proc/self/mountinfo"}
	}
	out := Walk(Mounts(string(mi)), allow, time.Now().Add(WalkBound))
	nu, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return append(out, "unreadable /proc/net/unix")
	}
	return append(out, Abstract(string(nu))...)
}
