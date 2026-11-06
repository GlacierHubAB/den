package sysd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// The units Den drives itself: the transient services that hold what a
// desktop mission launches, in a slice of the mission's own, which Den
// freezes when the mission pauses and stops when it ends. Den drives the
// user manager with systemd's own tools (systemd-run, systemctl --user),
// found in the system's own directories, never on PATH, and checks every
// call's exit status: anything it cannot read as success fails, and the
// caller keeps Den's window holding.

// Service is a transient service to start: argv run in dir, with env (on
// top of nothing else: every other variable of the manager's is unset).
type Service struct {
	Name, Slice, Description string
	Argv, Env                []string
	Dir                      string
}

// StopTimeoutUs is how long a unit has, after SIGTERM, before SIGKILL.
const StopTimeoutUs = 5_000_000

// toolWait bounds every call of a tool; a start waits for its job.
const (
	toolWait  = 5 * time.Second
	startWait = 15 * time.Second
)

// toolDirs are where systemd's tools are taken from, in order.
var toolDirs = []string{"/usr/bin", "/bin"}

// tool is the absolute path of a systemd tool, or why it is refused: only
// from toolDirs, and only a file that is root's, in a directory that is
// root's, neither writable by group or others.
func tool(name string) (string, error) {
	for _, dir := range toolDirs {
		p := filepath.Join(dir, name)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if err := rootOwned(real); err != nil {
			return "", fmt.Errorf("%s: %v", p, err)
		}
		if err := rootOwned(filepath.Dir(real)); err != nil {
			return "", fmt.Errorf("%s: %v", filepath.Dir(real), err)
		}
		return real, nil
	}
	return "", fmt.Errorf("no %s in %s", name, strings.Join(toolDirs, " or "))
}

// rootOwned checks a path is root's and not writable by group or others.
func rootOwned(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return errors.New("not owned by root")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return errors.New("writable by group or others")
	}
	return nil
}

// toolEnv is the environment every tool runs with: only what it needs to
// find the user manager, plain output, no pager and no colours.
func toolEnv(extra []string) []string {
	env := []string{"LC_ALL=C", "SYSTEMD_PAGER=cat", "SYSTEMD_COLORS=0"}
	for _, k := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, kv := range extra {
		if strings.HasPrefix(kv, "LC_ALL=") {
			env[0] = kv // the launched program's own, set by name below
			continue
		}
		env = append(env, kv)
	}
	return env
}

// runTool runs a tool with a deadline, killed when it passes; tests
// replace it. stdout comes back; a failure carries stderr.
var runTool = func(name string, wait time.Duration, args, env []string) (string, error) {
	path, err := tool(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s took over %s", name, wait)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("%s: %v: %s", name, err, msg)
	}
	return out.String(), nil
}

// systemctl runs systemctl --user with the arguments.
func systemctl(args ...string) (string, error) {
	return runTool("systemctl", toolWait, append([]string{"--user", "--no-pager", "--no-ask-password"}, args...), toolEnv(nil))
}

// unitName is a unit name Den makes: systemd's characters only, no
// leading '-' (never read as a flag), within the length limit.
var unitName = regexp.MustCompile(`^[A-Za-z0-9:_.\\][A-Za-z0-9:_.\\-]*\.(service|slice)$`)

func checkUnit(name string) error {
	if !unitName.MatchString(name) || len(name) > MaxUnitName {
		return fmt.Errorf("%q is not a unit name Den makes", name)
	}
	return nil
}

// envName is a variable's name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ManagerEnvNames are the names of the user manager's own environment,
// which every unit it starts inherits.
func ManagerEnvNames() ([]string, error) {
	out, err := systemctl("show-environment")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		k, _, ok := strings.Cut(l, "=")
		if !ok || !envName.MatchString(k) {
			return nil, fmt.Errorf("show-environment line %q does not read", l)
		}
		names = append(names, k)
	}
	return names, nil
}

// StartArgs is systemd-run's argv for s, the manager's environment being
// managerEnv: every flag one token, values only by name (--setenv=NAME,
// taken from systemd-run's own environment, so no value is ever on a
// command line), every variable of the manager's that s does not set
// unset, then -- and s's argv.
func StartArgs(s Service, managerEnv []string) ([]string, []string, error) {
	if len(s.Argv) == 0 {
		return nil, nil, errors.New("a service needs a command, a .service name and a .slice")
	}
	if !strings.HasSuffix(s.Name, ".service") || !strings.HasSuffix(s.Slice, ".slice") {
		return nil, nil, errors.New("a service needs a command, a .service name and a .slice")
	}
	if err := checkUnit(s.Name); err != nil {
		return nil, nil, err
	}
	if err := checkUnit(s.Slice); err != nil {
		return nil, nil, err
	}
	if s.Dir != "" && !filepath.IsAbs(s.Dir) {
		return nil, nil, errors.New("a service's working directory must be absolute")
	}
	if strings.ContainsAny(s.Description, "\n\x00") {
		return nil, nil, errors.New("a description is one line")
	}
	args := []string{"--user", "--quiet", "--no-ask-password", "--collect",
		"--unit=" + s.Name, "--slice=" + s.Slice, "--description=" + s.Description,
		fmt.Sprintf("--property=TimeoutStopSec=%dus", StopTimeoutUs)}
	set := map[string]bool{}
	var values []string
	for _, kv := range s.Env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || !envName.MatchString(k) || set[k] {
			return nil, nil, fmt.Errorf("environment entry %q", k)
		}
		set[k] = true
		args = append(args, "--setenv="+k)
		values = append(values, kv)
	}
	for _, k := range managerEnv {
		if !set[k] {
			args = append(args, "--property=UnsetEnvironment="+k)
		}
	}
	if s.Dir != "" {
		args = append(args, "--working-directory="+s.Dir)
	}
	args = append(append(args, "--"), s.Argv...)
	return args, values, nil
}

// StartService starts s and waits for its start job.
func StartService(s Service) error {
	if _, _, err := StartArgs(s, nil); err != nil {
		return err // refused before the manager is asked
	}
	menv, err := ManagerEnvNames()
	if err != nil {
		return err
	}
	args, values, err := StartArgs(s, menv)
	if err != nil {
		return err
	}
	_, err = runTool("systemd-run", startWait, args, toolEnv(values))
	return err
}

// Freeze freezes a unit and everything in it (cgroup freeze). Its exit
// status alone frees nothing: only State's FreezerState=frozen does.
func Freeze(unit string) error { return unitCall("freeze", unit) }

// Thaw thaws a frozen unit.
func Thaw(unit string) error { return unitCall("thaw", unit) }

// Stop stops a unit: SIGTERM to all of it, SIGKILL after its timeout. A
// unit that is not loaded is stopped already.
func Stop(unit string) error {
	if err := checkUnit(unit); err != nil {
		return err
	}
	if u, err := State(unit); err == nil && !u.Loaded {
		return nil
	}
	return unitCall("stop", unit)
}

func unitCall(verb, unit string) error {
	if err := checkUnit(unit); err != nil {
		return err
	}
	_, err := systemctl("--quiet", verb, "--", unit)
	return err
}

// UnitState is a unit as the manager has it now: Loaded false when the
// manager has no such unit (LoadState not-found: never started, or
// collected). Empty says the kernel holds no process anywhere under the
// unit's cgroup (cgroupEmpty).
type UnitState struct {
	Loaded         bool
	Active, Freeze string // ActiveState, FreezerState
	Cgroup         string // ControlGroup, "" for none
	Empty          bool
}

// Gone reports whether nothing of the unit runs: the manager has no such
// unit, or keeps it loaded and inactive (a stopped slice stays so a
// while) with its cgroup empty. Anything else, failed included, runs.
func (u UnitState) Gone() bool { return !u.Loaded || u.Active == "inactive" && u.Empty }

// cgroupRoot is where the cgroup v2 tree is mounted; tests replace it.
var cgroupRoot = "/sys/fs/cgroup"

// cgroupEmpty reports whether no process is left under a cgroup: none
// named, its directory gone, or its cgroup.events reading "populated 0".
// Anything else (a path that is not one, a read that fails) is not empty.
func cgroupEmpty(cg string) bool {
	if cg == "" {
		return true
	}
	if !strings.HasPrefix(cg, "/") || strings.Contains(cg, "/../") || strings.HasSuffix(cg, "/..") || strings.ContainsAny(cg, "\n\x00") {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(cgroupRoot, cg, "cgroup.events"))
	if errors.Is(err, os.ErrNotExist) {
		if _, derr := os.Stat(filepath.Join(cgroupRoot, cg)); errors.Is(derr, os.ErrNotExist) {
			return true // the cgroup is gone with everything in it
		}
		return false
	}
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if l == "populated 0" {
			return true
		}
	}
	return false
}

var (
	loadStates   = map[string]bool{"loaded": true, "not-found": true, "stub": true, "error": true, "merged": true, "masked": true, "bad-setting": true}
	activeStates = map[string]bool{"active": true, "reloading": true, "inactive": true, "failed": true, "activating": true, "deactivating": true, "maintenance": true, "refreshing": true}
	freezeStates = map[string]bool{"running": true, "freezing": true, "frozen": true, "thawing": true, "freezing-by-parent": true, "frozen-by-parent": true}
)

// State reads a unit's LoadState, ActiveState, FreezerState and
// ControlGroup: exactly those four, the first three each a value systemd
// has, or an error; and whether its cgroup is empty.
func State(unit string) (UnitState, error) {
	if err := checkUnit(unit); err != nil {
		return UnitState{}, err
	}
	out, err := systemctl("show", "--property=LoadState", "--property=ActiveState", "--property=FreezerState", "--property=ControlGroup", "--", unit)
	if err != nil {
		return UnitState{}, err
	}
	u, err := parseState(out)
	if err != nil {
		return UnitState{}, err
	}
	u.Empty = cgroupEmpty(u.Cgroup)
	return u, nil
}

func parseState(out string) (UnitState, error) {
	got := map[string]string{}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || seen[k] {
			return UnitState{}, fmt.Errorf("show line %q does not read", l)
		}
		seen[k], got[k] = true, v
	}
	if len(seen) != 4 || !seen["ControlGroup"] || !loadStates[got["LoadState"]] || !activeStates[got["ActiveState"]] || !freezeStates[got["FreezerState"]] {
		return UnitState{}, fmt.Errorf("show answered %q", out)
	}
	return UnitState{Loaded: got["LoadState"] != "not-found", Active: got["ActiveState"], Freeze: got["FreezerState"], Cgroup: got["ControlGroup"]}, nil
}

// prefixGlob is a listing prefix: letters, digits and '-' only, so the
// glob holds no character the manager's matching would read as special.
var prefixGlob = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// List is the names of the loaded slices whose names start with prefix,
// each matched whole against the units Den makes; any other line is left.
func List(prefix string) ([]string, error) {
	if !prefixGlob.MatchString(prefix) {
		return nil, fmt.Errorf("%q is not a prefix Den lists", prefix)
	}
	out, err := systemctl("list-units", "--all", "--plain", "--no-legend", "--type=slice", "--", prefix+"*.slice")
	if err != nil {
		return nil, err
	}
	whole := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `[A-Za-z0-9:_.\\-]+\.slice$`)
	var names []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == "●" {
			f = f[1:]
		}
		if len(f) > 0 && whole.MatchString(f[0]) && len(f[0]) <= MaxUnitName {
			names = append(names, f[0])
		}
	}
	return names, nil
}

// Escape is systemd's unit-name escaping of s: every byte outside
// [a-zA-Z0-9:_.] becomes \xNN (a leading '.' too), '/' becomes '-'.
// Unescape(Escape(s)) == s for every s, so two different strings never
// escape to one name.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}

// Unescape undoes Escape; false for a string Escape never makes.
func Unescape(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-':
			b.WriteByte('/')
		case c == '\\':
			if i+3 >= len(s) || s[i+1] != 'x' {
				return "", false
			}
			var v byte
			if _, err := fmt.Sscanf(s[i+2:i+4], "%02x", &v); err != nil {
				return "", false
			}
			b.WriteByte(v)
			i += 3
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// MaxUnitName is systemd's limit on a unit's name.
const MaxUnitName = 255
