package theme

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

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

// With no theme, every curated hue up to MinHues is drawn, olive and slate
// among them, and each passes the rules a theme's own hue must pass.
func TestFallbackOffersOliveAndSlate(t *testing.T) {
	got := map[string]bool{}
	for _, h := range Deepharness.Bodies {
		got[h] = true
	}
	if len(Deepharness.Bodies) != MinHues || !got["#a6b562"] || !got["#9fabc9"] {
		t.Errorf("fallback bodies %v", Deepharness.Bodies)
	}
	for _, h := range []string{"#a6b562", "#9fabc9"} {
		if d := luminance(h) - luminance("#000000"); d <= 60 || saturation(h) < 40 {
			t.Errorf("%s: luminance gap %v, spread %v", h, d, saturation(h))
		}
	}
}

// agent-badge picks colours from its own copy of Hues and MinHues; a
// session's colour matches in both only while the copies agree.
func TestHuesMatchAgentBadge(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mods", "agent-badge", "hooks", "register.tsx"))
	if err != nil {
		t.Fatalf("agent-badge's hues: %v (is mods/agent-badge checked out?)", err)
	}
	src := string(raw)
	_, list, ok := strings.Cut(src, "const HUES = [")
	list, _, closed := strings.Cut(list, "]")
	if !ok || !closed {
		t.Fatal("agent-badge has no HUES list")
	}
	var badge []string
	for _, m := range regexp.MustCompile(`'(#[0-9a-f]{6})'`).FindAllStringSubmatch(list, -1) {
		badge = append(badge, m[1])
	}
	if !slices.Equal(badge, Hues) {
		t.Errorf("agent-badge's HUES %v, Den's %v", badge, Hues)
	}
	if m := regexp.MustCompile(`const MIN_HUES = (\d+)`).FindStringSubmatch(src); m == nil || m[1] != strconv.Itoa(MinHues) {
		t.Errorf("agent-badge's MIN_HUES %v, Den's %d", m, MinHues)
	}
}
