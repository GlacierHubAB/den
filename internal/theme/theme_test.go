package theme

import "testing"

func TestFillHuesSkipsGreysAndTopsUp(t *testing.T) {
	got := fillHues([]string{"#66cb8e", "#e8b84a"}, "#000000")
	if len(got) < MinHues {
		t.Fatalf("got %d hues, want at least %d", len(got), MinHues)
	}
	if got[0] != "#66cb8e" || got[1] != "#e8b84a" {
		t.Errorf("theme hues must come first: %v", got[:2])
	}
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h] {
			t.Errorf("duplicate hue %s", h)
		}
		seen[h] = true
	}
	if saturation("#cccccc") != 0 || saturation("#e8b84a") < 40 {
		t.Errorf("saturation: grey=%v amber=%v", saturation("#cccccc"), saturation("#e8b84a"))
	}
}
