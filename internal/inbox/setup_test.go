package inbox

import "testing"

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
