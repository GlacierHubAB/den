package inbox

import (
	"testing"
	"time"
)

// The setup card is Den's own question, one at a time, and den_only: no
// console's "allow" is ever classed for it (D8, den-sec-review).
func TestSetupCard(t *testing.T) {
	s := New()
	it := s.AddSetup("Install?", []string{"line"})
	if it == nil || it.Kind != Setup || !it.DenAsks() || !it.Answerable() || it.SessionID != SetupSession {
		t.Fatalf("setup item %+v", it)
	}
	if again := s.AddSetup("Install?", nil); again != nil {
		t.Error("a second setup card while one is open")
	}
	if Setup.String() != "setup" {
		t.Errorf("String = %q", Setup.String())
	}
	if c, step := Classify(it, Facts{CleanupKnown: true}); c != DenOnly || step != 0 {
		t.Errorf("Classify(Setup) = %s %d, want den_only 0", c, step)
	}
	if Setup != RenamePick+1 {
		t.Error("Setup is not appended after RenamePick")
	}
	s.Settle(it, false, "hidden")
	if s.AddSetup("Install?", nil) == nil {
		t.Error("no new card after the first settled")
	}
}

// The setup card waits behind every other waiting item, even ones that
// came after it, and ahead of notices.
func TestSetupCardSortsLast(t *testing.T) {
	s := New()
	setup := s.AddSetup("Install?", nil)
	setup.At = setup.At.Add(-time.Hour)
	later := s.AddUsageResume("Go on?", nil)
	note := s.AddUsage("80%", nil)
	got := s.Items()
	if len(got) != 3 || got[0] != later || got[1] != setup || got[2] != note {
		var kinds []string
		for _, it := range got {
			kinds = append(kinds, it.Kind.String())
		}
		t.Errorf("order %v, want usage resume, setup, usage", kinds)
	}
}
