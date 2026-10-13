package remote_test

import (
	"strings"
	"testing"
	"time"
)

// A huge command: the host-side filter refuses anything over 16 KB so the
// human is never asked to approve text the inbox shows cut. The local side
// must hold the same line, since a hostile host skips the filter.
func TestAdvHugeBodyIsNotAnswerable(t *testing.T) {
	huge := strings.Repeat("a", 200<<10) // under the 256 KB line cap, over 16 KB
	g := stubApproveRig(t, held(id1, idA, "Bash", map[string]any{"command": huge}))
	g.start()
	g.live()
	time.Sleep(time.Second)
	if ids := g.heldIDs(); len(ids) != 0 {
		t.Errorf("a %d KB command became an answerable request: the human cannot see it in full", len(huge)>>10)
	}
}
