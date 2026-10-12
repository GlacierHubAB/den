package remote

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
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

// A held id is single use: once answered, the same id offered again (on
// the same session or a later one) is refused, flagged and sent back to its
// terminal with no decision. A request that was never answered, let go when
// its link was lost, is answerable again when the next session re-holds it.
func TestHeldIDsAreSingleUse(t *testing.T) {
	answered := newAnsweredIDs()
	var mu sync.Mutex
	var wrote []Decision
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		wrote = append(wrote, v.(Decision))
		return nil
	}
	session := func() (*heldSet, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		return newHeldSet(ctx, "devbox", answered, write), cancel
	}
	id1, id2 := strings.Repeat("a", 32), strings.Repeat("b", 32)
	hm := func(id, cmd string) Held {
		return Held{Type: TypeHeld, ID: id, Session: sidA, Tool: "Bash", Body: json.RawMessage(`{"command":"` + cmd + `"}`)}
	}

	s1, end1 := session()
	r, why := s1.add(hm(id1, "ls"), Approve, Approve)
	if r == nil {
		t.Fatalf("a fresh request was refused: %s", why)
	}
	if r2, _ := s1.add(hm(id2, "ls"), Approve, Approve); r2 == nil {
		t.Fatal("a second fresh request was refused")
	}
	r.Reply(bridge.Decision{Behavior: "allow"})
	if r, why := s1.add(hm(id1, "rm -rf ~"), Approve, Approve); r != nil || !strings.Contains(why, "after it was answered") {
		t.Errorf("an answered id offered again on the same session: request %v, flag %q", r != nil, why)
	}
	end1()
	s1.releaseAll() // id2 was never answered: let go as link lost

	s2, end2 := session()
	defer end2()
	if r, why := s2.add(hm(id1, "ls"), Approve, Approve); r != nil || !strings.Contains(why, "after it was answered") {
		t.Errorf("an answered id offered again on a later session: request %v, flag %q", r != nil, why)
	}
	r2, why := s2.add(hm(id2, "ls"), Approve, Approve)
	if r2 == nil {
		t.Fatalf("a request still open on the host, re-held after a reconnect, was refused: %s", why)
	}
	r2.Reply(bridge.Decision{Behavior: "deny"})

	mu.Lock()
	defer mu.Unlock()
	var behaviors []string
	for _, d := range wrote {
		behaviors = append(behaviors, d.ID[:1]+":"+d.Behavior)
	}
	// id1 allowed once, then each re-offer sent back with no decision; id2 denied.
	if got, want := strings.Join(behaviors, " "), "a:allow a: a: b:deny"; got != want {
		t.Errorf("decisions written = %q, want %q", got, want)
	}
}
