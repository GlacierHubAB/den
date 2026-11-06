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

// With no theme, every curated hue is drawn, and each passes the rules a
// theme's own hue must pass, the agent colour rule among them.
func TestFallbackIsTheCuratedHues(t *testing.T) {
	if !slices.Equal(Deepharness.Bodies, Hues) || len(Hues) != MinHues {
		t.Errorf("fallback bodies %v, curated %v", Deepharness.Bodies, Hues)
	}
	for _, h := range Hues {
		if d := luminance(h) - luminance("#000000"); d <= 60 || saturation(h) < 40 {
			t.Errorf("%s: luminance gap %v, spread %v", h, d, saturation(h))
		}
		if ok, why := AgentColor(h); !ok {
			t.Errorf("%s fails the agent colour rule: %s", h, why)
		}
	}
}

// A theme's hue is a body colour only when the agent colour rule takes it:
// of these, only sky's own hex does, upper case or not.
func TestThemeHuesFollowTheRule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "state", "omarchy", "current", "theme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "background = \"#000000\"\nred = \"#ea9e9e\"\ngreen = \"#66cb8e\"\nmagenta = \"#b9a3f0\"\ncyan = \"#86C6E8\"\nblue = \"#7aa2f7\"\n"
	if err := os.WriteFile(filepath.Join(dir, "colors.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load().Bodies
	want := append([]string{"#86c6e8"}, slices.DeleteFunc(slices.Clone(Hues), func(h string) bool { return h == "#86c6e8" })...)
	if !slices.Equal(got, want) {
		t.Errorf("bodies %v, want %v", got, want)
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
	// Its copy of the agent colour rule carries the same thresholds,
	// coefficients and colours as rule.go.
	_, block, ok := strings.Cut(src, "// agent colour rule: begin")
	block, _, closed = strings.Cut(block, "// agent colour rule: end")
	if !ok || !closed {
		t.Fatal("agent-badge has no agent colour rule block")
	}
	ours, err := os.ReadFile("rule.go")
	if err != nil {
		t.Fatal(err)
	}
	if b, d := ruleLiterals(block), ruleLiterals(string(ours)); !slices.Equal(b, d) || len(d) < 40 {
		t.Errorf("agent-badge's rule literals %v, Den's %v", b, d)
	}
}

// ruleLiterals is every colour, decimal fraction and number of 50 or more
// in src outside its line comments, sorted: the rule's thresholds,
// coefficients and colours.
func ruleLiterals(src string) []string {
	var out []string
	lit := regexp.MustCompile(`#[0-9a-f]{6}|\b\d+\.\d+\b|\b\d+\b`)
	for _, line := range strings.Split(src, "\n") {
		code, _, _ := strings.Cut(line, "//")
		for _, l := range lit.FindAllString(code, -1) {
			if n, err := strconv.Atoi(l); err == nil && n < 50 {
				continue
			}
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}
