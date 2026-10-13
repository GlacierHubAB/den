package ui

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the testdata/*.golden frames")

// TestNoRemotesViewUnchanged is remote acceptance test 8:
// with no remote host configured, the frames are what they were before
// remote support. The goldens were rendered by this same test on the code
// before it; only the clock and ages are masked, since they move.
func TestNoRemotesViewUnchanged(t *testing.T) {
	// Clock times and ages vary from run to run. An age can follow an SGR
	// sequence directly ("…124m20739d"), with no word boundary before it, so
	// the "m" that ends the sequence counts as one and is kept.
	volatile := regexp.MustCompile(`\d{2}:\d{2}|(\b|m)\d+[smhd]\b`)
	for _, c := range []struct {
		name string
		tab  tab
		w, h int
	}{
		{"fleet", tabFleet, 120, 40},
		{"fleet_narrow", tabFleet, 70, 30},
		{"inbox", tabInbox, 120, 40},
		{"overview", tabOverview, 120, 30},
	} {
		m := Model{st: newStyles(theme.Deepharness), base: sample(), store: inbox.New(), width: c.w, height: c.h, tab: c.tab, hooksReady: true}
		m.input = newInput(m.st)
		m.store.Apply(bridge.TestRequest(bridge.Envelope{Event: "Notification", SessionID: "0123456789abcdef", Payload: []byte(`{"session_id":"0123456789abcdef","hook_event_name":"Notification","notification_type":"idle_prompt","message":"Waiting for your input"}`)}))
		m.store.Apply(bridge.TestRequest(bridge.Envelope{Event: "Stop", SessionID: "0123456789abcdef", Payload: []byte(`{"session_id":"0123456789abcdef","hook_event_name":"Stop","last_assistant_message":"All tests pass."}`)}))
		m.colors = map[string]string{} // no colors.json: hues come from the session id
		m.refresh()
		// Styled, so colours count too; the mask also hides some SGR digits, alike on both sides.
		got := volatile.ReplaceAllStringFunc(m.View().Content, func(v string) string {
			if strings.HasPrefix(v, "m") {
				return "m#"
			}
			return "#"
		})
		path := filepath.Join("testdata", "noremote_"+c.name+".golden")
		if *updateGolden {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: the frame changed with no remotes configured\n--- got\n%s\n--- want\n%s", c.name, got, want)
		}
	}
}
