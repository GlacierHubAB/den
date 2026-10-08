package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommentsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "den", "reviews.json")
	if got := LoadComments(p); got != nil {
		t.Fatalf("missing file = %+v", got)
	}
	in := []Comment{
		{ID: "c1", Root: "/r", Path: "cmd/list.go", Side: "new", Start: 49, End: 51, Quote: []string{"if pending {"}, Body: "exclude archived", SessionID: "s1", Created: time.Unix(100, 0).UTC()},
		{ID: "c2", Root: "/r", Path: "a.go", Side: "old", Start: 3, End: 3, Body: "why remove this?", SessionID: "s2", Created: time.Unix(200, 0).UTC(), Sent: time.Unix(300, 0).UTC()},
	}
	if err := SaveComments(p, in); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	out := LoadComments(p)
	if len(out) != 2 || out[0].End != 51 || out[0].Quote[0] != "if pending {" || !out[0].Draft() || out[1].Draft() || !out[1].Sent.Equal(in[1].Sent) {
		t.Errorf("round trip = %+v", out)
	}
	os.WriteFile(p, []byte("{not json"), 0o600)
	if got := LoadComments(p); got != nil {
		t.Errorf("corrupt file = %+v", got)
	}
}
