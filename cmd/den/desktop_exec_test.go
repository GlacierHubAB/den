package main

import (
	"testing"
	"time"
)

func execRig(t *testing.T, of func(int) string) {
	t.Helper()
	oldOf, oldBound, oldPoll := execOf, execBound, execPoll
	t.Cleanup(func() { execOf, execBound, execPoll = oldOf, oldBound, oldPoll })
	execOf, execBound, execPoll = of, 100*time.Millisecond, time.Millisecond
}

// The fork shows the proxy's own executable for a few reads, then the
// interpreter: the wait ends there and nothing is stopped.
func TestAwaitExecAfterFork(t *testing.T) {
	reads := 0
	execRig(t, func(int) string {
		reads++
		if reads <= 5 {
			return "/usr/bin/den"
		}
		return "/usr/lib/den/venv/bin/python3.13"
	})
	stopped := false
	if !awaitExec(700, "/usr/bin/den", func() { stopped = true }) || stopped || reads != 6 {
		t.Fatalf("reads %d, stopped %v", reads, stopped)
	}
}

// A child that never execs (or can't be read) is stopped within the bound
// and the wait fails, so the proxy tells Den nothing.
func TestAwaitExecNeverExecs(t *testing.T) {
	for _, exe := range []string{"/usr/bin/den", ""} {
		execRig(t, func(int) string { return exe })
		stopped := 0
		began := time.Now()
		if awaitExec(700, "/usr/bin/den", func() { stopped++ }) || stopped != 1 {
			t.Fatalf("%q: stopped %d", exe, stopped)
		}
		if took := time.Since(began); took < execBound || took > execBound+200*time.Millisecond {
			t.Fatalf("%q: took %v for a %v bound", exe, took, execBound)
		}
	}
}

// The real bound and poll are sec's: 500 ms at most, about every 2 ms.
func TestAwaitExecBound(t *testing.T) {
	if execBound != 500*time.Millisecond || execPoll != 2*time.Millisecond {
		t.Fatalf("bound %v, poll %v", execBound, execPoll)
	}
}
