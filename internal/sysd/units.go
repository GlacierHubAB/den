package sysd

import (
	"errors"
	"fmt"
	"strings"
)

// The units Den drives itself: the transient services that hold what a
// desktop mission launches, in a slice of the mission's own, which Den
// freezes when the mission pauses and stops when it ends. Every call
// first checks that the socket is the user's own manager (checkManager),
// as Read does.

// managerConn dials the user manager and checks who answers.
func managerConn() (*conn, error) {
	socket, err := managerSocket()
	if err != nil {
		return nil, err
	}
	c, err := dial(socket)
	if err != nil {
		return nil, fmt.Errorf("the systemd user manager: %w", err)
	}
	if _, err := checkManager(c.peer); err != nil {
		c.close()
		return nil, fmt.Errorf("the systemd user manager: %w", err)
	}
	return c, nil
}

// Service is a transient service to start: argv run in dir, with env as
// its whole environment, in slice.
type Service struct {
	Name, Slice, Description string
	Argv, Env                []string
	Dir                      string
}

// StopTimeoutUs is how long a unit has, after SIGTERM, before SIGKILL.
const StopTimeoutUs = 5_000_000

// StartService starts s. It is collected by the manager once it is
// inactive or failed, so a unit Den launched never outlives what it ran.
func StartService(s Service) error {
	if len(s.Argv) == 0 || !strings.HasSuffix(s.Name, ".service") || !strings.HasSuffix(s.Slice, ".slice") {
		return errors.New("a service needs a command, a .service name and a .slice")
	}
	c, err := managerConn()
	if err != nil {
		return err
	}
	defer c.close()
	props := []any{
		[]any{"Description", Variant{Sig: "s", Value: s.Description}},
		[]any{"Slice", Variant{Sig: "s", Value: s.Slice}},
		[]any{"ExecStart", Variant{Sig: "a(sasb)", Value: []any{[]any{s.Argv[0], s.Argv, false}}}},
		[]any{"Environment", Variant{Sig: "as", Value: s.Env}},
		[]any{"TimeoutStopUSec", Variant{Sig: "t", Value: uint64(StopTimeoutUs)}},
		[]any{"CollectMode", Variant{Sig: "s", Value: "inactive-or-failed"}},
	}
	if s.Dir != "" {
		props = append(props, []any{"WorkingDirectory", Variant{Sig: "s", Value: s.Dir}})
	}
	_, err = c.call(managerPath, managerIface, "StartTransientUnit", "ssa(sv)a(sa(sv))", "o", s.Name, "fail", props, []any{})
	return err
}

// Freeze freezes a unit and everything in it (cgroup freeze).
func Freeze(unit string) error { return unitCall("FreezeUnit", unit) }

// Thaw thaws a frozen unit.
func Thaw(unit string) error { return unitCall("ThawUnit", unit) }

func unitCall(member, unit string) error {
	c, err := managerConn()
	if err != nil {
		return err
	}
	defer c.close()
	_, err = c.call(managerPath, managerIface, member, "s", "", unit)
	return err
}

// Stop stops a unit: SIGTERM to all of it, SIGKILL after its timeout.
func Stop(unit string) error {
	c, err := managerConn()
	if err != nil {
		return err
	}
	defer c.close()
	_, err = c.call(managerPath, managerIface, "StopUnit", "ss", "o", unit, "replace")
	if noUnit(err) {
		return nil // nothing of it is loaded: stopped already
	}
	return err
}

// UnitState is a unit as the manager has it now: Loaded false when the
// manager has no such unit (never started, or collected).
type UnitState struct {
	Loaded         bool
	Active, Freeze string // ActiveState, FreezerState
}

// Gone reports whether nothing of the unit runs: not loaded, or inactive.
func (u UnitState) Gone() bool { return !u.Loaded || u.Active == "inactive" }

// State reads a unit's ActiveState and FreezerState.
func State(unit string) (UnitState, error) {
	c, err := managerConn()
	if err != nil {
		return UnitState{}, err
	}
	defer c.close()
	obj, err := c.getUnit(unit)
	if noUnit(err) {
		return UnitState{}, nil
	}
	if err != nil {
		return UnitState{}, err
	}
	u := UnitState{Loaded: true}
	active, err := c.get(obj, unitIface, "ActiveState", "s")
	if err != nil {
		return UnitState{}, err
	}
	freeze, err := c.get(obj, unitIface, "FreezerState", "s")
	if err != nil {
		return UnitState{}, err
	}
	u.Active, _ = active.(string)
	u.Freeze, _ = freeze.(string)
	return u, nil
}

// List is the names of the loaded units matching a pattern (systemd's
// glob, as ListUnitsByPatterns takes it).
func List(pattern string) ([]string, error) {
	c, err := managerConn()
	if err != nil {
		return nil, err
	}
	defer c.close()
	body, err := c.call(managerPath, managerIface, "ListUnitsByPatterns", "asas", "a(ssssssouso)", []string{}, []string{pattern})
	if err != nil {
		return nil, err
	}
	rows, _ := body[0].([]any)
	var out []string
	for _, r := range rows {
		f, ok := r.([]any)
		if !ok || len(f) == 0 {
			continue
		}
		if name, ok := f[0].(string); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// noUnit reports the manager's "no such unit" answer.
func noUnit(err error) bool {
	var ce *CallError
	return errors.As(err, &ce) && ce.Name == "org.freedesktop.systemd1.NoSuchUnit"
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
