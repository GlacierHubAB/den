package theme

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// agent-badge's own copy of the agent colour rule, run under node, gives
// every row of the shared table its answer: TestHuesMatchAgentBadge pins
// the rule's numbers, this its logic. Without a node that strips
// TypeScript types the test skips locally and fails on CI, so it never
// skips silently there.
func TestAgentBadgeRuleUnderNode(t *testing.T) {
	node, version, why := nodeForTypeScript()
	t.Logf("node: %q, version %q", node, version)
	if why != "" {
		if os.Getenv("CI") == "true" {
			t.Fatalf("on CI the badge's rule must run under node: %s", why)
		}
		t.Skip(why)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "mods", "agent-badge", "hooks", "register.tsx"))
	if err != nil {
		t.Fatalf("agent-badge's rule: %v (is mods/agent-badge checked out?)", err)
	}
	src := string(raw)
	_, block, ok := strings.Cut(src, "// agent colour rule: begin")
	block, _, closed := strings.Cut(block, "// agent colour rule: end")
	helpers := regexp.MustCompile(`(?s)type RGB = [^\n]*\n\nfunction hexToRgb\(.*?\n}\n`).FindString(src)
	if !ok || !closed || helpers == "" {
		t.Fatal("agent-badge has no agent colour rule block, or no hexToRgb")
	}
	var hexes []string
	for _, c := range convcases.ColorRuleCases {
		hexes = append(hexes, c.Hex)
	}
	cases, _ := json.Marshal(hexes)
	prog := helpers + block + "\nconsole.log(JSON.stringify((" + string(cases) + " as string[]).map(agentColor)))\n"
	file := filepath.Join(t.TempDir(), "rule.ts")
	if err := os.WriteFile(file, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	// The block is the submodule's code, run in Den's CI: node gets only a
	// PATH and a home of its own, nothing CI or the caller set (NODE_OPTIONS
	// and the like).
	cmd := exec.Command(node, file)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + cmd.Dir}
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("node: %v\n%s", err, stderr)
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(hexes) {
		t.Fatalf("node printed %q: %v", out, err)
	}
	for i, c := range convcases.ColorRuleCases {
		if got[i] != c.Allowed {
			t.Errorf("%q: the badge says %v, the table %v (%s)", c.Hex, got[i], c.Allowed, c.Why)
		}
	}
}

// nodeForTypeScript is the node on PATH and its version when it runs a .ts
// file as is (type stripping on by default: 22.18 and later, 23.6 and
// later), else why not.
func nodeForTypeScript() (node, version, why string) {
	node, err := exec.LookPath("node")
	if err != nil {
		return "", "", "no node on PATH"
	}
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		return node, "", "node --version: " + err.Error()
	}
	version = strings.TrimSpace(string(out))
	m := regexp.MustCompile(`^v(\d+)\.(\d+)\.`).FindStringSubmatch(version)
	if m == nil {
		return node, version, "node --version printed " + strconv.Quote(version)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major > 23 || major == 23 && minor >= 6 || major == 22 && minor >= 18 {
		return node, version, ""
	}
	return node, version, "node " + version + " does not strip TypeScript types by default"
}
