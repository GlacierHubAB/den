package grants

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/permmode"
)

// A read names a mode only when Den has read a ranked one: none before
// that, and never bypassPermissions, a mode that is not ranked, or a name
// spelled any other way. The key is absent, not empty.
func TestReadNamesOnlyARankedMode(t *testing.T) {
	for _, mode := range []string{"", "unknown", permmode.Bypass, "Default", "dontask"} {
		g := Read(State{Powers: true, Confirmed: true, Mode: mode})
		raw, _ := json.Marshal(g)
		if g.Mode != nil || strings.Contains(string(raw), `"mode"`) {
			t.Errorf("mode %q: the read names a mode: %s", mode, raw)
		}
	}
}

// For each ranked mode the read carries Den's modes most restrictive first
// and, as lower_to, Den's own list of the modes strictly below the current
// one: never the current mode, never a mode that is only unranked against
// it, and a list ([]) even when nothing is below.
func TestReadModeIsDensOwnOrder(t *testing.T) {
	for _, mode := range permmode.Modes() {
		g := Read(State{Mode: mode})
		if g.Mode == nil || g.Mode.Current != mode {
			t.Fatalf("mode %q: the read says %+v", mode, g.Mode)
		}
		var names []string
		for _, n := range g.Mode.Modes {
			names = append(names, n.Name)
		}
		if !reflect.DeepEqual(names, permmode.Modes()) {
			t.Errorf("mode %q: modes %v, want %v", mode, names, permmode.Modes())
		}
		if !reflect.DeepEqual(g.Mode.LowerTo, permmode.LowerTo(mode)) {
			t.Errorf("mode %q: lower_to %v, want %v", mode, g.Mode.LowerTo, permmode.LowerTo(mode))
		}
		for _, to := range g.Mode.LowerTo {
			if to == mode || !permmode.Lowers(mode, to) {
				t.Errorf("mode %q: lower_to has %q, which is not a lowering", mode, to)
			}
		}
	}
	plan := Read(State{Mode: permmode.Plan}).Mode
	if !reflect.DeepEqual(plan.LowerTo, []string{permmode.DontAsk, permmode.Default}) {
		t.Errorf("plan lowers to %v: acceptEdits is not below it", plan.LowerTo)
	}
	raw, _ := json.Marshal(Read(State{Mode: permmode.DontAsk}))
	if !strings.Contains(string(raw), `"lower_to":[]`) {
		t.Errorf("nothing is below dontAsk, and the read says %s", raw)
	}
}

// The mode is shown and decides nothing: every class reads as it did.
func TestReadModeChangesNoLevel(t *testing.T) {
	base := Read(State{Powers: true, Confirmed: true})
	for _, mode := range permmode.Modes() {
		g := Read(State{Powers: true, Confirmed: true, Mode: mode})
		g.Mode = nil
		if !reflect.DeepEqual(g, base) {
			t.Errorf("mode %q changed the read beyond its own key", mode)
		}
	}
}
