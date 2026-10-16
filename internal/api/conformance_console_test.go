package api

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api/denconformance"
)

// The suite's console.request and console.reconnect, end to end against
// this server: the dock pairs with no code, Den's card answered as the
// human's ^Y would, and its restart rejoins with no card. A test process
// is no systemd unit, so the read of the console's service is stood in
// for (fakeUnit) as the same unit each time; internal/ui's run of the
// suite keeps both off, since it cannot stand in for it.
func TestConformanceRequestAndReconnect(t *testing.T) {
	h, _ := consoleHarness(t)
	asked := fakeUnit(t, testVerifier(), nil)
	// A view, as Den publishes one: a reconnect before hello needs it.
	h.s.Publish(Project(Snapshot{Taken: time.Now()}))
	// Den's acts, read here: Confirm answers the console card waiting.
	asks := make(chan *ConsoleAsk, 8)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case a := <-h.s.Acts():
				if a.Console != nil {
					asks <- a.Console
				}
			case <-stop:
				return
			}
		}
	}()
	var answered []ConsoleAsk
	confirm := func(ctx context.Context) error {
		select {
		case ask := <-asks:
			answered = append(answered, *ask)
			h.s.ConsoleAnswered(ask.ID, true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Only the two subtests: the rest of the suite runs in internal/ui.
	var skip []string
	for _, name := range append(append([]string(nil), denconformance.Subtests...), denconformance.ConsoleSubtests...) {
		if name != "console.request" && name != "console.reconnect" {
			skip = append(skip, name)
		}
	}
	denconformance.Run(t, denconformance.Options{
		Dial: func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", h.s.Path())
		},
		Skip: skip,
		Console: &denconformance.ConsoleOptions{
			Offer:     func(context.Context) (string, error) { return strings.Join(h.s.StartConsoleCode(), " "), nil },
			Confirm:   confirm,
			Request:   true,
			Reconnect: true,
		},
	})
	// Each ran, not skipped: one card for the request, none for the
	// silent reconnect, and the unit read for both.
	if len(answered) != 1 || !answered[0].Codeless || answered[0].Reconnect {
		t.Errorf("cards answered %+v", answered)
	}
	if len(*asked) != 2 {
		t.Errorf("unit read %d times, want 2", len(*asked))
	}
	if cur, ok := h.s.CurrentConsole(); !ok || cur.Powers {
		t.Errorf("after the reconnect: %+v %v", cur, ok)
	}
}
