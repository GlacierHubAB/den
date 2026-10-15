package sysd

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestRealUserManager reads the console's service from this machine's
// own systemd user manager, read only, as console.request would: what
// the fake manager cannot show (the kernel's view of the real manager,
// the real unit's properties) shows here. It runs only with
// DEN_REAL_SYSTEMD=1, never in CI, and only where the unit is running.
// It checks nothing about a unit's own pins, which a machine may lack:
// it fails on any problem with the manager, or on a manager that does
// not read at all.
func TestRealUserManager(t *testing.T) {
	if os.Getenv("DEN_REAL_SYSTEMD") != "1" {
		t.Skip("set DEN_REAL_SYSTEMD=1 to read this machine's systemd user manager")
	}
	sock, err := managerSocket()
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(sock)
	if err != nil {
		t.Fatalf("the user manager: %v", err)
	}
	if _, err := checkManager(c.peer); err != nil {
		c.close()
		t.Fatalf("the user manager fails Den's check: %v", err)
	}
	obj, err := c.getUnit(ConsoleUnit)
	if err != nil {
		c.close()
		t.Skipf("%s is not running here: %v", ConsoleUnit, err)
	}
	pid, err := c.get(obj, serviceIface, "MainPID", "u")
	c.close()
	if err != nil || pid.(uint32) == 0 {
		t.Skipf("%s has no main process: %v", ConsoleUnit, err)
	}
	_, start, err := procStat(int(pid.(uint32)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := Read(ConsoleUnit, ProcID{PID: int(pid.(uint32)), Start: start})
	var probs Problems
	switch {
	case err == nil:
		t.Logf("%s reads clean: pid %d, %s", ConsoleUnit, v.Main.PID, v.Exe.Path)
	case errors.As(err, &probs):
		for _, p := range probs {
			if strings.Contains(string(p), "manager") {
				t.Errorf("a manager problem: %s", p)
			}
		}
		t.Logf("%s's own problems (its pins may not be set here): %v", ConsoleUnit, err)
	default:
		t.Fatalf("does not read: %v", err)
	}
}
