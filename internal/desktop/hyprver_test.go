package desktop

import (
	"context"
	"errors"
	"testing"
)

// Only the checked commit, built clean, read without error, is a yes.
func TestScopedBuildIsOneCleanCommit(t *testing.T) {
	cases := []struct {
		out string
		ok  bool
	}{
		{`{"tag":"v0.56.2","commit":"` + ScopedCommit + `","dirty":false}`, true},
		{`{"tag":"v0.56.2","commit":"` + ScopedCommit + `","dirty":true}`, false},
		{`{"tag":"v0.56.2","commit":"` + ScopedCommit + `"}`, false},
		{`{"tag":"v0.56.2","dirty":false}`, false},
		{`{"tag":"v0.56.2","commit":"efb5099","dirty":false}`, false},
		{`{"tag":"v0.56.3","commit":"0000000000000000000000000000000000000000","dirty":false}`, false},
		{`{"tag":"v0.56.2","commit":"` + ScopedCommit + `","dirty":"false"}`, false},
		{`not json`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := scopedBuild([]byte(c.out)); (got == "") != c.ok {
			t.Errorf("%s: %q, want ok=%v", c.out, got, c.ok)
		}
	}
}

// A hyprctl that fails is a no, and the read asks for "version -j".
func TestScopedBuildReadsFromDensSide(t *testing.T) {
	old := hyprctl
	t.Cleanup(func() { hyprctl = old })
	var asked []string
	hyprctl = func(_ context.Context, args ...string) ([]byte, error) {
		asked = args
		return nil, errors.New("no instance")
	}
	if ScopedBuild(context.Background()) == "" {
		t.Error("a failed read was taken as the checked build")
	}
	if len(asked) != 2 || asked[0] != "version" || asked[1] != "-j" {
		t.Errorf("asked hyprctl %v", asked)
	}
	hyprctl = func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"tag":"v0.56.2","commit":"` + ScopedCommit + `","dirty":false}`), nil
	}
	if why := ScopedBuild(context.Background()); why != "" {
		t.Errorf("the checked build refused: %s", why)
	}
}
