package ui

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/procs"
)

// The server's child is recorded only once /proc shows it running the
// interpreter Den resolved itself: read before its exec (Den's binary, or
// the proxy's), unreadable, or with the interpreter unresolvable, the
// report is refused and nothing is recorded.
func TestDesktopChildRecordedOnlyAsHypruse(t *testing.T) {
	for _, c := range []struct {
		name   string
		exe    string
		pyErr  error
		allow  bool
		record string
	}{
		{"the interpreter", rigPython, nil, true, rigPython},
		{"Den's binary, before its exec", "/usr/bin/den", nil, false, ""},
		{"the proxy's binary", "/home/u/.local/bin/den", nil, false, ""},
		{"unreadable", "", nil, false, ""},
		{"the interpreter unresolvable", rigPython, errors.New("no venv"), false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := deskRig(t)
			r := deskReq(600, bridge.DesktopInput{Kind: "start"})
			deskDo(&m, r)
			if d, _ := r.Decided(); d.Behavior != "allow" {
				t.Fatalf("start: %+v", d)
			}
			deskExeOf = func(pid int) string {
				if pid == 700 {
					return c.exe
				}
				return ""
			}
			deskPython = func() (string, error) {
				if c.pyErr != nil {
					return "", c.pyErr
				}
				return rigPython, nil
			}
			r = deskReq(600, bridge.DesktopInput{Kind: "child", ChildPID: 700, ChildStart: "7000"})
			deskDo(&m, r)
			d, _ := r.Decided()
			dm := m.deskMission("m1")
			if (d.Behavior == "allow") != c.allow || dm.ChildExe != c.record {
				t.Fatalf("decision %+v, recorded %q (pid %d)", d, dm.ChildExe, dm.ChildPID)
			}
			if !c.allow && (dm.ChildPID != 0 || d.Message != "Den: that is not the desktop server's child") {
				t.Fatalf("a refused child left pid %d, message %q", dm.ChildPID, d.Message)
			}
		})
	}
}

// A child recorded through this path is the process Stop finds: the real
// interpreter's pid, start and executable, so it is signalled and the
// report names no failure.
func TestDesktopChildRecordedIsStopped(t *testing.T) {
	bin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	child := exec.Command(bin, "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	pid := child.Process.Pid
	m := deskRig(t)
	r := deskReq(600, bridge.DesktopInput{Kind: "start"})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "allow" {
		t.Fatalf("start: %+v", d)
	}
	real := desktop.RecordProc(pid)
	for deadline := time.Now().Add(time.Second); procs.Exe(pid) != procs.Undeleted(mustReal(t, bin)) && time.Now().Before(deadline); {
		time.Sleep(2 * time.Millisecond)
		real = desktop.RecordProc(pid)
	}
	oldStart, oldParent := deskStart, deskParent
	deskStart = func(p int) string {
		if p == pid {
			return real.Start
		}
		return oldStart(p)
	}
	deskParent = func(p int) (int, string, bool) {
		if p == pid {
			return 600, "python", true
		}
		return oldParent(p)
	}
	deskExeOf = procs.Exe
	deskPython = func() (string, error) { return procs.Undeleted(mustReal(t, bin)), nil }
	r = deskReq(600, bridge.DesktopInput{Kind: "child", ChildPID: pid, ChildStart: real.Start})
	deskDo(&m, r)
	if d, _ := r.Decided(); d.Behavior != "allow" {
		t.Fatalf("child: %+v", d)
	}
	dm := m.deskMission("m1")
	rep := desktop.Stop(desktop.Targets{Child: dm.targets().Child}, filepath.Join(t.TempDir(), "none.json"))
	if len(rep.Failed) != 0 || len(rep.Stopped) != 1 || rep.Stopped[0] != "hypruse" {
		t.Fatalf("stop: %+v", rep)
	}
	done := make(chan struct{})
	go func() { child.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the recorded child survived the stop")
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
