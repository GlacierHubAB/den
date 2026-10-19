package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
)

// The mirror follows the human's answer for the console's lineage: a kept
// yes turns it on again (after a reconnect, Den's restart, or the powers
// coming back) with no new card, a kept no never asks again, and an
// unanswered lineage is asked once. Without its powers nothing is shown,
// whatever the answer.
func TestMirrorFollowsTheLineage(t *testing.T) {
	lin := "0123456789abcdef0123456789abcdef"
	con := func(id, mirror string, powers bool) api.Console {
		return api.Console{ID: id, Lineage: lin, Mirror: mirror, Powers: powers}
	}
	cases := []struct {
		name     string
		mode     string
		cur      api.Console
		ok       bool
		onID     string
		on       bool
		asked    string
		wantOff  bool
		wantStep mirrorStep
	}{
		{"no console: off", "ask", api.Console{}, false, "p1", true, "", true, mirrorStay},
		{"no powers: off, even with a yes", "ask", con("p2", api.MirrorYes, false), true, "p1", true, "", true, mirrorStay},
		{"already on for it", "ask", con("p1", api.MirrorYes, true), true, "p1", true, "", false, mirrorStay},
		{"a kept yes after a reconnect: on again, no card", "ask", con("p2", api.MirrorYes, true), true, "", false, lin, false, mirrorAgain},
		{"a kept yes while another pairing is shown", "ask", con("p2", api.MirrorYes, true), true, "p1", true, "", true, mirrorAgain},
		{"a kept no: nothing, never asked", "ask", con("p2", api.MirrorNo, true), true, "", false, "", false, mirrorStay},
		{"not answered yet: asked", "ask", con("p2", "", true), true, "", false, "", false, mirrorAsk},
		{"not answered, the card already up: once", "ask", con("p2", "", true), true, "", false, lin, false, mirrorStay},
		{"no lineage: never asked", "ask", api.Console{ID: "p2", Powers: true}, true, "", false, "", false, mirrorStay},
		{"always: on", "always", con("p2", "", true), true, "", false, "", false, mirrorFresh},
		{"off: never, even with a yes", "off", con("p2", api.MirrorYes, true), true, "", false, "", false, mirrorStay},
	}
	for _, c := range cases {
		off, step := mirrorNext(c.mode, c.cur, c.ok, c.onID, c.on, c.asked)
		if off != c.wantOff || step != c.wantStep {
			t.Errorf("%s: off %v step %v, want %v %v", c.name, off, step, c.wantOff, c.wantStep)
		}
	}
}
