package ui

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/inbox"
)

// Every kind the dock may never answer is refused, each on its own: a
// merge that splits the case in dockMayAnswer and strands one kind with
// no return fails here, not only in review.
func TestDockNeverAnswersTheHumansKinds(t *testing.T) {
	never := []inbox.Kind{
		inbox.Console, inbox.Pair, inbox.APIOn, inbox.Guidance, inbox.Install, inbox.Question, inbox.Grant, inbox.Skills, inbox.Message,
		inbox.DesktopInstall, inbox.DesktopMission, inbox.DesktopAct, inbox.YourTurn,
		inbox.MissionCriteria, inbox.MissionDone,
	}
	for _, k := range never {
		if dockMayAnswer(&inbox.Item{Kind: k, SessionID: "0123456789abcdef"}) {
			t.Errorf("the dock may answer %s", k)
		}
	}
	// And a kind it may answer still passes, so the test is not vacuous.
	if !dockMayAnswer(&inbox.Item{Kind: inbox.Permission, SessionID: "0123456789abcdef"}) {
		t.Error("the dock may not answer a permission: the check refuses everything")
	}
}
