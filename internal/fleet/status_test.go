package fleet

import (
	"encoding/json"
	"testing"
)

func TestAgentJSONCarriesStatusName(t *testing.T) {
	for _, tc := range []struct {
		status Status
		name   string
	}{
		{Unknown, "unknown"},
		{NeedsYou, "needs_you"},
		{Working, "working"},
		{Idle, "idle"},
		{Gone, "gone"},
	} {
		raw, err := json.Marshal(Agent{Name: "a", Status: tc.status})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Name       string
			Status     int
			StatusName string
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != int(tc.status) || got.StatusName != tc.name || got.Name != "a" {
			t.Errorf("%v: got %+v from %s", tc.status, got, raw)
		}
	}
}
