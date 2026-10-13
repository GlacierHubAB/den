package procs

import (
	"os"
	"os/exec"
	"testing"
)

func TestAncestorsIncludesSelf(t *testing.T) {
	a := Ancestors(os.Getpid())
	if len(a) == 0 || a[0].PID != os.Getpid() {
		t.Fatalf("ancestors = %+v", a)
	}
}

func TestDescends(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	me := os.Getpid()
	start := StartTicks(me)
	if start == "" {
		t.Fatal("no start time for this process")
	}
	child := cmd.Process.Pid
	if !Descends(child, me, start) {
		t.Error("a child descends from its parent")
	}
	if Descends(me, child, StartTicks(child)) {
		t.Error("a parent does not descend from its child")
	}
	if Descends(child, me, "1") {
		t.Error("another start time is another process")
	}
	if Descends(child, child, StartTicks(child)) || Descends(0, me, start) || Descends(1<<30, me, start) {
		t.Error("self, pid 0 or a gone pid descend from nothing")
	}
}
