package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
)

// A host that churns held and gone pairs, fresh ids each, under MaxRate,
// gets no more held requests through than its bucket allows, its quick
// gones count as drops, and it is disabled for flooding: it cannot grow
// Den's inbox without bound.
func TestHeldGoneChurnIsMetered(t *testing.T) {
	s := newScratch(t)
	const pairs = 2000
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		newLineReader(in, MaxLine).next()
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Approve, RemoteNow: time.Now()})
		writeMsg(out, Snapshot{Type: TypeSnapshot, RemoteNow: time.Now(), Agents: []fleet.Meta{{Session: sidA, Status: "busy"}}})
		body, _ := json.Marshal(map[string]string{"command": "ls"})
		for i := range pairs {
			id := fmt.Sprintf("%032x", i+1)
			if writeMsg(out, Held{Type: TypeHeld, ID: id, Session: sidA, Tool: "Bash", Body: body}) != nil ||
				writeMsg(out, Gone{Type: TypeGone, ID: id}) != nil {
				return nil
			}
			if i%500 == 499 {
				time.Sleep(time.Second) // 1000 messages a second, under MaxRate
			}
		}
		<-ctx.Done()
		return nil
	}}
	h := hostB()
	h.Policy = Approve
	m := s.manager(d, h)
	var mu sync.Mutex
	arrived := 0
	go func() {
		for u := range m.Updates() {
			if u.Kind == HeldArrived {
				mu.Lock()
				arrived++
				mu.Unlock()
			}
		}
	}()
	m.Start()
	t.Cleanup(m.Close)
	waitFor(t, "the host disabled", func() bool { return state(m).State == Disabled })
	mu.Lock()
	defer mu.Unlock()
	if arrived > HeldBurst+10 {
		t.Errorf("%d held requests reached the inbox from a churning host (burst %d)", arrived, HeldBurst)
	}
	for _, f := range []string{"held requests withdrawn as soon as offered", "more held requests than Den takes from one host", "flooding: disabled"} {
		if !hasFlag(m, f) {
			t.Errorf("not flagged: %q; flags %q", f, state(m).Flags)
		}
	}
}
