package api

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api/denconformance"
)

// The suite's console.request, console.reconnect and request refusals end
// to end against this server: the dock pairs with no code and no card,
// rejoins after a restart, is refused after it unpaired itself until a
// pairing starts in Den. A test process is no systemd unit, so the read of
// the console's service is stood in for (fakeUnit); internal/ui's run of
// the suite keeps them off, since it cannot stand in for it.
//
// As with api.console_powers = "always", the console ends with its powers.
func TestConformanceRequestAndReconnect(t *testing.T) {
	if cur := runConsoleSuite(t, alwaysHarness(t), false); !cur.Powers {
		t.Errorf("at the end: %+v", cur)
	}
}

// With the default ("unchanged"), the same pairings through the suite's
// path never come with their powers: the suite wants powers false from
// every request and reconnect (PowersLocked), as no build was given them
// in this run.
func TestConformanceRequestAndReconnectUnchanged(t *testing.T) {
	h, _ := consoleHarness(t)
	if cur := runConsoleSuite(t, h, true); cur.Powers {
		t.Errorf("at the end: %+v", cur)
	}
}

// runConsoleSuite runs the suite's console.request, console.reconnect and
// "console.request after an unpairing" against h, and checks that each
// ran: no card, and the unit read for each pairing. It returns the console
// at the end.
func runConsoleSuite(t *testing.T, h *harness, powersLocked bool) Console {
	t.Helper()
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
	// The service pairs with no card: Confirm answers one if it comes,
	// and else lets the request's reply through.
	var answered []ConsoleAsk
	confirm := func(ctx context.Context) error {
		select {
		case ask := <-asks:
			answered = append(answered, *ask)
			h.s.ConsoleAnswered(ask.ID, true)
			return nil
		case <-time.After(300 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Only these: the rest of the suite runs in internal/ui.
	run := map[string]bool{"console.request": true, "console.reconnect": true, "console.request after an unpairing": true}
	var skip []string
	for _, name := range append(append([]string(nil), denconformance.Subtests...), denconformance.ConsoleSubtests...) {
		if !run[name] {
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
			Offer:           func(context.Context) (string, error) { return strings.Join(h.s.StartConsoleCode(), " "), nil },
			Confirm:         confirm,
			Request:         true,
			Reconnect:       true,
			RequestRefusals: true,
			PowersLocked:    powersLocked,
		},
	})
	// They ran, not skipped: no card, and the unit read for each pairing:
	// the request, the reconnect, and the two requests of "after an
	// unpairing" that were not refused before any read.
	if len(answered) != 0 {
		t.Errorf("cards answered %+v", answered)
	}
	if len(*asked) != 4 {
		t.Errorf("unit read %d times, want 4", len(*asked))
	}
	cur, ok := h.s.CurrentConsole()
	if !ok {
		t.Fatal("no console at the end")
	}
	return cur
}
