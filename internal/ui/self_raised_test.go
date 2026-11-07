package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	perm "github.com/GlacierHubAB/den/internal/grants"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// everyLevel are the states a self-raised card is checked in: the
// defaults, every class at its highest step and confirmed, and that with
// every kept entry.
func everyLevel() map[string]perm.State {
	top := map[string]int{}
	for _, id := range perm.Classes() {
		top[id] = perm.Steps
	}
	return map[string]perm.State{
		"the defaults":     {Powers: true, Confirmed: true},
		"every class high": {Powers: true, Confirmed: true, DenLevels: top},
		"everything kept":  {Powers: true, Confirmed: true, DenLevels: top, Kept: perm.KeptAll()},
	}
}

// dockAnswerV2 is a console's answer on it with the card's v2 hash now.
func dockAnswerV2(m *Model, it *inbox.Item, verb string) api.CardResult {
	read := api.ProjectCard(m.dockCard(it))
	a := &api.CardAnswer{ID: it.ID, Hash: read.HashV2, Version: 2, Verb: verb, Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	return <-a.Reply
}

// The kinds a console raises itself are never allowed from a console, at
// any level, kept or not: card.get lists deny only, an allow is refused (by
// the listing, and below it by the level check, below_level), and the
// card stays open for Den's window.
func TestSelfRaisedAreDenyOnly(t *testing.T) {
	for _, kind := range []inbox.Kind{inbox.ModeSet, inbox.UsageSet, inbox.UsageResume, inbox.ControlAsk, inbox.MissionMove} {
		for name, st := range everyLevel() {
			m := testModel(140, 40)
			withLevels(&m, st)
			it := m.store.AddMissionMove("api:console", "a card", []string{"a line"})
			it.Kind = kind
			read := api.ProjectCard(m.dockCard(it))
			if !read.DockAnswers || len(read.Verbs) != 1 || read.Verbs[0] != "deny" {
				t.Errorf("%s, %s: listed dock_answers %v, verbs %v", kind, name, read.DockAnswers, read.Verbs)
			}
			if r := dockAnswerV2(&m, it, "allow"); r.Code == "" || it.State == inbox.Resolved {
				t.Errorf("%s, %s: an allow was taken: %+v", kind, name, r)
			}
			class, step := inbox.Classify(it, inbox.Facts{})
			if code, _ := m.cardLevels().AnswerAllowed(permAnswer(it, class, step, "allow")); code != perm.BelowLevel {
				t.Errorf("%s, %s: the level check gives %q", kind, name, code)
			}
		}
	}
}

// A console's deny on a self-raised card is taken: saying no is always
// allowed from the console.
func TestSelfRaisedDenyIsTaken(t *testing.T) {
	m := modeModel(t, inDefault)
	r := &api.ModeSet{Mode: "auto", Console: "app-host", Reply: make(chan api.ModeSetResult, 1)}
	m.modeFromDock(r)
	<-r.Reply
	it := openModeCard(t, m)
	if r := dockAnswerV2(&m, it, "deny"); r.Code != "" || it.State != inbox.Resolved || m.agentMode() == "auto" {
		t.Errorf("deny: %+v, state %v, mode %q", r, it.State, m.agentMode())
	}
}

// A card's listing follows the levels, outside its hash: at the start
// confirm a kept Merge gains allow, with the same hash (den-api sends the
// console a changed event for the listing). Back before the confirm, an
// allow read while it was listed is refused at answer time, never taken.
func TestListingFlipFollowsTheLevels(t *testing.T) {
	m := testModel(140, 40)
	withLevels(&m, perm.State{Powers: true, Kept: perm.KeptAll()})
	it := m.store.AddMerge("api:console", "Merge a branch?", []string{"a line"})
	before := api.ProjectCard(m.dockCard(it))
	withLevels(&m, perm.State{Powers: true, Confirmed: true, Kept: perm.KeptAll()})
	after := api.ProjectCard(m.dockCard(it))
	if len(before.Verbs) != 1 || len(after.Verbs) != 2 || before.HashV2 != after.HashV2 {
		t.Fatalf("before %v %s, after %v %s", before.Verbs, before.HashV2, after.Verbs, after.HashV2)
	}
	withLevels(&m, perm.State{Powers: true, Kept: perm.KeptAll()})
	a := &api.CardAnswer{ID: it.ID, Hash: after.HashV2, Version: 2, Verb: "allow", Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	if r := <-a.Reply; !staleRefusal(r) || it.State == inbox.Resolved {
		t.Errorf("an allow read before the flip back: %+v", r)
	}
}

// MissionUpdate's way in from a console's turn is the update_mission
// ControlAsk, listed deny-only: the console's allow on it is refused, so
// no MissionUpdate card is raised before the human allows it in Den.
func TestMissionUpdateWaitsOnTheHuman(t *testing.T) {
	m := testModel(140, 40)
	withLevels(&m, everyLevel()["everything kept"])
	it := m.store.AddControlAsk("update_mission", []string{"the orchestrator asks to update a mission"})
	if r := dockAnswerV2(&m, it, "allow"); r.Code == "" {
		t.Fatalf("the console allowed its own turn's tool: %+v", r)
	}
	for _, x := range m.store.Items() {
		if x.Kind == inbox.MissionUpdate {
			t.Errorf("a MissionUpdate card before the human's answer: %+v", x)
		}
	}
	if it.State == inbox.Resolved {
		t.Error("the ControlAsk was settled by the console")
	}
}

// The level check's refusal reaches the console with its class: a powers
// lock after card.get is powers_locked, and the class limit rate_limited,
// both at answer time only.
func TestDockAnswerRefusalsCarryTheirReason(t *testing.T) {
	m := testModel(140, 40)
	it := m.store.AddMerge("api:console", "Merge a branch?", []string{"a line"})
	withLevels(&m, perm.State{Powers: true, Confirmed: true, Kept: perm.KeptAll()})
	read := api.ProjectCard(m.dockCard(it))
	withLevels(&m, perm.State{Powers: true, Locked: true, Confirmed: true, Kept: perm.KeptAll()})
	a := &api.CardAnswer{ID: it.ID, Hash: read.HashV2, Version: 2, Verb: "allow", Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	if r := <-a.Reply; r.Code != api.CodePowersLocked || it.State == inbox.Resolved {
		t.Errorf("locked after card.get: %+v", r)
	}
	if got := answerRefusal(perm.BelowLevel, perm.Outward); got != "Send out is set below this in Den: Den's window answers it" {
		t.Errorf("below_level says %q", got)
	}
}

// A level lowered after card.get refuses the console's allow on the hash
// it read, and nothing is applied: Den decides at answer time from its own
// record, whatever the console was shown.
func TestLoweredAfterCardGetIsRefused(t *testing.T) {
	m := testModel(140, 40)
	it := m.store.AddMerge("api:console", "Merge a branch?", []string{"a line"})
	withLevels(&m, perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Outward: 2}})
	read := api.ProjectCard(m.dockCard(it))
	if !containsVerb(read.Verbs, "allow") {
		t.Fatalf("listed %v", read.Verbs)
	}
	before := perm.State{Powers: true, Confirmed: true, DenLevels: map[string]int{perm.Outward: 2}}
	lowered := before
	lowered.Sessions = map[string]perm.Session{perm.Outward: {Level: 1}}
	// Once Den has published again, the listing refuses it.
	withLevels(&m, lowered)
	a := &api.CardAnswer{ID: it.ID, Hash: read.HashV2, Version: 2, Verb: "allow", Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	if r := <-a.Reply; !staleRefusal(r) || it.State == inbox.Resolved {
		t.Errorf("the allow after a lowering: %+v", r)
	}
	// Before it has (the listing still the one read), the level check at
	// answer time refuses it: below_level, and nothing is applied.
	m.api.levels = &staleListing{listed: before, now: lowered}
	a = &api.CardAnswer{ID: it.ID, Hash: read.HashV2, Version: 2, Verb: "allow", Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	if r := <-a.Reply; r.Code != perm.BelowLevel || r.Class != perm.Outward || it.State == inbox.Resolved {
		t.Errorf("the allow on a listing not yet published again: %+v, state %v", r, it.State)
	}
}

// staleListing is the window between a lowering and Den's next publish:
// the listing still the one the console read, the levels already lower.
type staleListing struct{ listed, now perm.State }

func (l *staleListing) AnswerAllowed(a perm.Answer) (string, string) {
	return (&testLevels{st: l.now}).AnswerAllowed(a)
}

func (l *staleListing) AnswerListed(a perm.Answer) bool { return perm.Listed(l.listed, a) }

// staleRefusal reports whether r refuses an allow the console read before
// a lowering: by the listing now, or by the level check.
func staleRefusal(r api.CardResult) bool {
	return r.Code == perm.BelowLevel || r.Code == api.CodeRefusedFromDock
}
