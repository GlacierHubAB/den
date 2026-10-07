package procs

import (
	"os"
	"testing"
)

func TestAncestorsIncludesSelf(t *testing.T) {
	a := Ancestors(os.Getpid())
	if len(a) == 0 || a[0].PID != os.Getpid() {
		t.Fatalf("ancestors = %+v", a)
	}
}
