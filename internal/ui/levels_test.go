package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	perm "github.com/GlacierHubAB/den/internal/grants"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// testLevels stands in for den-api's answer check in a test with no
// den-api: perm.Allow and perm.Listed on st, as AnswerAllowed and
// AnswerListed decide them, without the class limit.
type testLevels struct{ st perm.State }

func (l *testLevels) AnswerAllowed(a perm.Answer) (string, string) {
	if code := perm.Allow(l.st, a); code != "" {
		if code == perm.BelowLevel {
			return code, a.Class
		}
		return code, ""
	}
	return "", ""
}

func (l *testLevels) AnswerListed(a perm.Answer) bool { return perm.Listed(l.st, a) }

// atDefaults gives m a console with its powers at Den's default levels,
// confirmed, with nothing kept: a new pairing's.
func atDefaults(m *Model) { m.api.levels = &testLevels{st: perm.State{Powers: true, Confirmed: true}} }

// withLevels gives m the levels st.
func withLevels(m *Model, st perm.State) { m.api.levels = &testLevels{st: st} }

// The kept ids God mode pauses are exactly the kinds only Den's window
// answers in God mode, and "always": grants keeps its own copy for the
// read, checked here against godTTYKinds.
func TestKeptGodTTYMatchesGodKinds(t *testing.T) {
	for kind, id := range keptIDs {
		if perm.KeptGodTTY(id) != godTTYKinds[kind] {
			t.Errorf("%s (%s): grants says %v, godTTYKinds %v", kind, id, perm.KeptGodTTY(id), godTTYKinds[kind])
		}
	}
	if !perm.KeptGodTTY(perm.KeptAlways) {
		t.Error(`God mode does not pause a kept "always"`)
	}
	for _, id := range perm.KeptAll() {
		found := id == perm.KeptAlways
		for _, k := range keptIDs {
			found = found || k == id
		}
		if !found {
			t.Errorf("kept id %q covers no kind", id)
		}
	}
}

// No self-raised kind is kept, and every kept kind is one a console could
// answer before (todayDock): kept never widens the dock.
func TestKeptNeverWidensTheDock(t *testing.T) {
	for kind := range keptIDs {
		if selfRaised[kind] || !todayDock[kind] {
			t.Errorf("%s: kept, self-raised %v, in todayDock %v", kind, selfRaised[kind], todayDock[kind])
		}
	}
	for kind := range selfRaised {
		if !todayDock[kind] {
			t.Errorf("%s: self-raised but not in todayDock", kind)
		}
	}
}

// A console is offered only what it may give: at the defaults a card
// above its class's level, and every self-raised kind, is listed deny
// only; a card within its level keeps allow.
func TestListingFollowsTheLevels(t *testing.T) {
	m := testModel(140, 40)
	for _, c := range []struct {
		kind  inbox.Kind
		verbs []string
	}{
		{inbox.Close, []string{"allow", "deny"}},
		{inbox.Merge, []string{"deny"}},
		{inbox.ModeSet, []string{"deny"}},
		{inbox.UsageSet, []string{"deny"}},
		{inbox.UsageResume, []string{"deny"}},
		{inbox.ControlAsk, []string{"deny"}},
		{inbox.MissionMove, []string{"deny"}},
		{inbox.Budget, []string{"deny"}},
	} {
		it := &inbox.Item{ID: "x", Kind: c.kind, Title: "t"}
		card := api.ProjectCard(m.dockCard(it))
		if !card.DockAnswers || len(card.Verbs) != len(c.verbs) || card.Verbs[0] != c.verbs[0] {
			t.Errorf("%s: dock_answers %v, verbs %v, want %v", c.kind, card.DockAnswers, card.Verbs, c.verbs)
		}
	}
	// Kept covers a kind's allow while active, never a self-raised one.
	withLevels(&m, perm.State{Powers: true, Confirmed: true, Kept: perm.KeptAll()})
	for kind, want := range map[inbox.Kind]int{inbox.Merge: 2, inbox.Budget: 2, inbox.ModeSet: 1, inbox.MissionMove: 1} {
		card := api.ProjectCard(m.dockCard(&inbox.Item{ID: "x", Kind: kind, Title: "t"}))
		if len(card.Verbs) != want {
			t.Errorf("kept, %s: verbs %v", kind, card.Verbs)
		}
	}
	// Before the start confirm, kept covers nothing.
	withLevels(&m, perm.State{Powers: true, Kept: perm.KeptAll()})
	if card := api.ProjectCard(m.dockCard(&inbox.Item{ID: "x", Kind: inbox.Merge, Title: "t"})); len(card.Verbs) != 1 {
		t.Errorf("kept before the confirm: verbs %v", card.Verbs)
	}
}
