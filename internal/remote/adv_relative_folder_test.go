package remote_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sshtest"
)

// A session file whose folder is a relative path ending in "." or "..":
// gitinfo.Lookup walks up with filepath.Dir, which never reaches "" or "/"
// for "..", so it loops forever. `den list -json` hangs and so does serve's
// snapshot, which is the host's whole view of itself.
func TestAdvRelativeFolderDoesNotWedgeTheHost(t *testing.T) {
	g := fullRig(t)
	g.opts = func(o *remote.Options) { o.DiffWait = 8 * time.Second }
	g.r.SeedSession(sshtest.SessionOpts{ID: idA, CWD: "../.."})
	c := exec.Command(g.l.Den, "list", "-json")
	c.Dir, c.Env = g.r.Home, g.r.Env()
	done := make(chan struct{})
	go func() { c.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.Process.Kill()
		<-done
		t.Error("den list -json hangs on a session whose folder is \"../..\"")
	}
}
