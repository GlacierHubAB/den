package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLastActiveComesFromTheStatusChange(t *testing.T) {
	a := Agent{Name: "X", Status: Idle}
	a.LastActive = time.Date(2026, 10, 11, 19, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"LastActive":"2026-10-11T19:00:00Z"`) || !strings.Contains(string(raw), `"StatusName":"idle"`) {
		t.Errorf("json = %s", raw)
	}
	if raw, _ = json.Marshal(Agent{}); strings.Contains(string(raw), "LastActive") {
		t.Errorf("an unknown time should be left out: %s", raw)
	}
}
