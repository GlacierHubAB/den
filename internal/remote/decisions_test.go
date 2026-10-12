package remote

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// A host that stops reading its stdin (a stuck pipe) must not hold up
// Reply, which Den calls from its update loop; once it reads again the
// decisions arrive, in the order they were made.
func TestReplyNeverWaitsOnThePipe(t *testing.T) {
	s := newScratch(t)
	ids := []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)}
	readAgain := make(chan struct{})
	got := make(chan Decision, 8)
	d := &pipeDialer{serve: func(ctx context.Context, in io.Reader, out io.Writer) error {
		lr := newLineReader(in, MaxLine)
		lr.next() // Den's request
		writeMsg(out, Hello{Type: TypeHello, Proto: 1, Policy: Approve, RemoteNow: time.Now()})
		for _, id := range ids {
			writeMsg(out, Held{Type: TypeHeld, ID: id, Session: "s1", Tool: "Bash", Body: json.RawMessage(`{"command":"ls"}`)})
		}
		select { // stuck: nothing more is read from the pipe for now
		case <-readAgain:
		case <-ctx.Done():
			return nil
		}
		for {
			line, err := lr.next()
			if err != nil {
				return nil
			}
			var dm Decision
			if json.Unmarshal(line, &dm) == nil && dm.Type == TypeDecision {
				got <- dm
			}
		}
	}}
	h := hostB()
	h.Policy = Approve
	m := s.manager(d, h)
	m.Start()
	defer m.Close()

	var reqs []*bridge.Request
	deadline := time.After(5 * time.Second)
	for len(reqs) < len(ids) {
		select {
		case u := <-m.Updates():
			if u.Kind == HeldArrived {
				reqs = append(reqs, u.Request)
			}
		case <-deadline:
			t.Fatalf("got %d held requests", len(reqs))
		}
	}
	go func() { // keep draining, as the UI does
		for range m.Updates() {
		}
	}()
	answered := make(chan struct{})
	go func() {
		for i := len(reqs) - 1; i >= 0; i-- { // answered newest first
			reqs[i].Reply(bridge.Decision{Behavior: "allow"})
		}
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(time.Second):
		t.Fatal("Reply waited on a pipe the host does not read")
	}
	close(readAgain)
	for i := len(ids) - 1; i >= 0; i-- {
		select {
		case dm := <-got:
			if dm.ID != ids[i] || dm.Behavior != "allow" || dm.Hash != BodyHash([]byte(`{"command":"ls"}`)) {
				t.Errorf("decision %d = %+v, want id %s", len(ids)-1-i, dm, ids[i][:4])
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("decision for %s never came", ids[i][:4])
		}
	}
}
