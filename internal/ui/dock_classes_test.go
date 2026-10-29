package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// everyKind is every inbox Kind: from Permission up to the first one
// String does not name. It must reach RenamePick, the last kind: a kind
// added without a String case would end the walk early and hide the kinds
// after it.
func everyKind(t *testing.T) []inbox.Kind {
	t.Helper()
	var out []inbox.Kind
	for k := inbox.Permission; k.String() != "?"; k++ {
		out = append(out, k)
	}
	if len(out) == 0 || out[len(out)-1] < inbox.RenamePick {
		t.Fatalf("the kinds stop after %d, before RenamePick: give every kind its String", len(out))
	}
	return out
}

// neverBefore is dockMayAnswer's never-case as it stood before the
// permission manager, frozen here: the dock gate it pins does not move.
var neverBefore = map[inbox.Kind]bool{
	inbox.Console: true, inbox.Pair: true, inbox.APIOn: true, inbox.Guidance: true, inbox.Install: true,
	inbox.Question: true, inbox.Grant: true, inbox.Skills: true, inbox.Message: true,
	inbox.DesktopInstall: true, inbox.DesktopMission: true, inbox.DesktopAct: true, inbox.YourTurn: true,
	inbox.MissionCriteria: true, inbox.MissionDone: true, inbox.MirrorAsk: true, inbox.ProcessTuned: true,
	inbox.PromptProposal: true, inbox.RulesPick: true, inbox.RulesEdited: true, inbox.PowersSet: true,
	inbox.RenamePick: true,
}

// reachesAConsole reports whether a waiting card of kind k can be one a
// console sees as answerable: Den's own questions while they wait, and an
// agent's permission, plan or question while its hook waits. Den's
// notices (Done, Idle, Sent, Stalled, Failed, Usage, ConsoleNote,
// GuardNote) never are.
func reachesAConsole(k inbox.Kind) bool {
	it := &inbox.Item{SessionID: "s1", Kind: k, State: inbox.Waiting}
	return it.Answerable() || k == inbox.Permission || k == inbox.Plan || k == inbox.Question
}

// TestDockGateUnchanged: for every kind, what a console may answer is what
// it could before the permission manager, on a card from this machine.
// The derived gate leaves out the eight notices the old never-case let
// through; they never reach a console, so in effect nothing moves.
func TestDockGateUnchanged(t *testing.T) {
	notices := 0
	for _, k := range everyKind(t) {
		it := &inbox.Item{SessionID: "s1", Kind: k, State: inbox.Waiting}
		before, now := !neverBefore[k], dockMayAnswer(it)
		if r := reachesAConsole(k); (before && r) != (now && r) {
			t.Errorf("%v: a console could answer it before: %v, now: %v", k, before && r, now && r)
		}
		if now && !before {
			t.Errorf("%v: the dock may answer it now and could not before", k)
		}
		if before && !now {
			notices++
			if reachesAConsole(k) {
				t.Errorf("%v: left out of todayDock, but it reaches a console", k)
			}
		}
	}
	if notices != 8 {
		t.Errorf("%d kinds left out that the old never-case let through, want the 8 notices", notices)
	}
	if len(todayDock) != 13 {
		t.Errorf("todayDock has %d kinds, want 13", len(todayDock))
	}
}

// TestClassesNeverWidenTheDock: no kind outside todayDock is ever in a
// class other than den_only, whatever Den knows of it.
func TestClassesNeverWidenTheDock(t *testing.T) {
	for _, k := range everyKind(t) {
		if todayDock[k] {
			continue
		}
		for _, f := range []inbox.Facts{{}, {CleanupKnown: true}, {CleanupKnown: true, CleanupIgnored: 3}} {
			if c, step := inbox.Classify(&inbox.Item{SessionID: "s1", Kind: k, State: inbox.Waiting}, f); c != inbox.DenOnly || step != 0 {
				t.Errorf("%v (not in todayDock) is %s %d", k, c, step)
			}
		}
	}
}

// TestDockNeverAnswersAHostCard: for every kind, a card from a remote
// host, or about one, is not the console's, and it is den_only.
func TestDockNeverAnswersAHostCard(t *testing.T) {
	for _, k := range everyKind(t) {
		for _, it := range []*inbox.Item{{Host: "build", SessionID: "s1", Kind: k, State: inbox.Waiting}, {SessionID: inbox.HostPrefix + "build", Kind: k, State: inbox.Waiting}} {
			if dockMayAnswer(it) {
				t.Errorf("%v from %q %q: dock may answer", k, it.Host, it.SessionID)
			}
			if c, _ := inbox.Classify(it, inbox.Facts{CleanupKnown: true}); c != inbox.DenOnly {
				t.Errorf("%v from %q %q: class %s", k, it.Host, it.SessionID, c)
			}
		}
	}
}

// TestDockCardCarriesItsClass: card.get's class and step are Den's, and
// the v2 hash covers them.
func TestDockCardCarriesItsClass(t *testing.T) {
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	it := permissionCard(t, &m) // Bash
	card := api.ProjectCard(m.dockCard(it))
	if card.Class != "code" || card.Step != 1 {
		t.Fatalf("a Bash card is %s %d, want code 1", card.Class, card.Step)
	}
	want, err := api.CardHashV2(card.Kind, card.Agent, card.Tool, card.Title, card.Note, "code", 1, "", nil, card.Body)
	if err != nil || card.HashV2 != want || card.HashV2 == card.Hash {
		t.Errorf("v2 hash %q, want %q (err %v); v1 %q", card.HashV2, want, err, card.Hash)
	}
}

// TestCleanupFactChangeIsACardChange: a Cleanup read at step 1 (no
// ignored files) whose plan then lists one is a changed card for a v2
// answer: the step is in its hash, and Den classes it again at the answer.
func TestCleanupFactChangeIsACardChange(t *testing.T) {
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	sid := m.agents[0].Session.SessionID
	it := m.store.AddCleanup(sid, "Remove the worktree of feature?", []string{"the worktree goes"})
	plan := &worktree.CleanupPlan{Branch: "feature"}
	m.rememberWt(it, wtOffer{cleanup: plan})
	read := api.ProjectCard(m.dockCard(it))
	if read.Class != "destructive" || read.Step != 1 {
		t.Fatalf("a Cleanup with no ignored files is %s %d, want destructive 1", read.Class, read.Step)
	}
	plan.Ignored = []string{"node_modules"}
	now := api.ProjectCard(m.dockCard(it))
	if now.Step != 2 || now.HashV2 == read.HashV2 {
		t.Fatalf("after an ignored file: step %d, v2 hash moved %v", now.Step, now.HashV2 != read.HashV2)
	}
	a := &api.CardAnswer{ID: it.ID, Hash: read.HashV2, Version: 2, Verb: "allow", Console: "app-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	if r := <-a.Reply; r.Code != api.CodeCardChanged {
		t.Errorf("answer with the hash read at step 1: %+v, want card_changed", r)
	}
	if it.State == inbox.Resolved {
		t.Error("the Cleanup was answered")
	}
}
