package tuning

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/templates"
)

var now = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

func ctx(maxAgents int) Context {
	return Context{MaxAgents: maxAgents, Template: func(name string) (templates.Template, bool) {
		return templates.Find(templates.Builtins(), name)
	}}
}

// Each team key takes a whole number within its bounds and nothing else;
// unknown keys are refused.
func TestTeamBounds(t *testing.T) {
	cases := map[string][2]int{"team.max_agents": {1, 6}, "team.branch_hours": {4, 72}, "team.behind_max": {10, 100}, "team.idle_minutes": {15, 240}}
	for key, b := range cases {
		for _, v := range []string{"", "x", "1.5", itoa(b[0] - 1), itoa(b[1] + 1)} {
			f := File{}
			if _, err := f.Apply(key, v, ctx(3), now); err == nil || len(f.Team) != 0 {
				t.Errorf("%s took %q", key, v)
			}
		}
		for _, v := range []int{b[0], b[1]} {
			f := File{}
			if ch, err := f.Apply(key, itoa(v), ctx(3), now); err != nil || f.Team[key] != v || ch.Had {
				t.Errorf("%s refused %d: %v", key, v, err)
			}
		}
	}
	for _, key := range []string{"team.max_agentz", "budget.usd", "god.on", "agents.permission_mode", "prompt.system", "template.code-review.mission", "template.code-review.remove_criterion", "template.nope.add_criterion"} {
		f := File{}
		if _, err := f.Apply(key, "1", ctx(3), now); err == nil {
			t.Errorf("took key %s", key)
		}
	}
}

// Criteria and constraints are only added, whole, one clean line at most
// MaxText characters; models come from the list; a cadence is at least
// 30 minutes apart; a role is refused past the cap.
func TestTemplateChanges(t *testing.T) {
	f := File{}
	refuse := func(key, v string) {
		t.Helper()
		if _, err := f.Apply(key, v, ctx(3), now); err == nil {
			t.Errorf("%s took %q", key, v)
		}
	}
	refuse("template.code-review.add_criterion", strings.Repeat("x", MaxText+1))
	refuse("template.code-review.add_criterion", "two\nlines")
	refuse("template.code-review.add_criterion", "bidi "+string(rune(0x202e))+"x")
	refuse("template.code-review.add_criterion", "  ")
	refuse("template.code-review.model.security auditor", "gpt-9")
	refuse("template.code-review.model.nobody", "opus")
	refuse("template.code-review.cadence", "every 10m")
	refuse("template.code-review.cadence", "*/5 * * * *")
	refuse("template.code-review.cadence", "whenever")
	refuse("template.code-review.add_role", "tester")
	if len(f.Templates) != 0 || len(f.Log) != 0 {
		t.Fatalf("refusals changed the file: %+v", f)
	}
	ch, err := f.Apply("template.code-review.add_criterion", strings.Repeat("y", MaxText), ctx(3), now)
	if err != nil || ch.New != strings.Repeat("y", MaxText) {
		t.Fatalf("whole criterion refused: %v", err)
	}
	ch, err = f.Apply("template.code-review.model.security auditor", "opus", ctx(3), now)
	if err != nil || ch.Old != "sonnet" || CostWord(ch.Old, ch.New) != "sonnet → opus, costs more" {
		t.Errorf("model %+v %v", ch, err)
	}
	if _, err := f.Apply("template.bugfix.cadence", "every 30m", ctx(3), now); err != nil {
		t.Errorf("30m refused: %v", err)
	}
	// code-review has three roles: a fourth passes a cap of 3.
	if _, err := f.Apply("template.code-review.add_role", "tester on haiku", ctx(3), now); err == nil {
		t.Error("a role past the cap")
	}
	if _, err := f.Apply("template.code-review.add_role", "tester on haiku under review lead", ctx(4), now); err != nil {
		t.Errorf("a role within the cap: %v", err)
	}
	if CostWord("opus", "haiku") != "opus → haiku, costs less" {
		t.Error(CostWord("opus", "haiku"))
	}
}

// Five changes a day per project and one per key; a day later, again.
func TestRateLimit(t *testing.T) {
	f := File{}
	if _, err := f.Apply("team.max_agents", "4", ctx(3), now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Apply("team.max_agents", "5", ctx(3), now.Add(time.Hour)); err == nil {
		t.Error("one key twice in a day")
	}
	for _, k := range []string{"team.branch_hours", "team.behind_max", "team.idle_minutes", "template.bugfix.cadence"} {
		v := map[string]string{"team.branch_hours": "12", "team.behind_max": "20", "team.idle_minutes": "60", "template.bugfix.cadence": "daily 08:30"}[k]
		if _, err := f.Apply(k, v, ctx(3), now); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if _, err := f.Apply("template.bugfix.add_constraint", "keep it small", ctx(3), now); err == nil {
		t.Error("a sixth change in a day")
	}
	if _, err := f.Apply("team.max_agents", "5", ctx(3), now.Add(25*time.Hour)); err != nil {
		t.Errorf("next day: %v", err)
	}
}

// Undo restores the exact value before, once; it never takes back a later
// change; and it does not count against the limit.
func TestUndo(t *testing.T) {
	f := File{}
	a, _ := f.Apply("team.max_agents", "4", ctx(3), now)
	if !f.Undo(a) || len(f.Team) != 0 || f.Undo(a) {
		t.Errorf("undo of a new key: %+v", f.Team)
	}
	f.Team = map[string]int{"team.branch_hours": 24}
	b, _ := f.Apply("team.branch_hours", "12", ctx(3), now)
	if !b.Had || b.Old != "24" || !f.Undo(b) || f.Team["team.branch_hours"] != 24 {
		t.Errorf("undo restores: %+v %+v", b, f.Team)
	}
	c, _ := f.Apply("template.code-review.add_constraint", "no force pushes", ctx(3), now)
	if !f.Undo(c) || len(f.Templates["code-review"].AddConstraints) != 0 || f.Undo(c) {
		t.Errorf("undo of an add: %+v", f.Templates)
	}
	d, _ := f.Apply("template.code-review.model.code reviewer", "opus", ctx(3), now)
	f.Templates["code-review"].Models["code reviewer"] = "haiku" // changed since
	if f.Undo(d) || f.Templates["code-review"].Models["code reviewer"] != "haiku" {
		t.Error("undo took back a later value")
	}
	if n := len(f.Log); n != 4 {
		t.Errorf("%d logged; undos must not count", n)
	}
}

// The file is Den's alone (0700/0600) and round-trips; at load a key out of
// bounds or unknown is left out with a note, and a file that does not
// parse is ignored whole; a link is never followed.
func TestLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, "den-abc123")
	f := File{}
	f.Apply("team.max_agents", "4", ctx(3), now)
	f.Apply("template.code-review.add_criterion", "the auditor signs off", ctx(3), now)
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	got, notes := Load(path)
	if len(notes) != 0 || got.Team["team.max_agents"] != 4 || got.Templates["code-review"].AddCriteria[0] != "the auditor signs off" || len(got.Log) != 2 {
		t.Errorf("round trip %+v %v", got, notes)
	}
	os.WriteFile(path, []byte(`{"v":1,"team":{"team.max_agents":9,"team.branch_hours":12,"team.god":1},
		"templates":{"code-review":{"add_criteria":["ok","bad\nline"],"models":{"code reviewer":"gpt"},"cadence":"every 1m"},"../x":{}}}`), 0o600)
	got, notes = Load(path)
	if len(got.Team) != 1 || got.Team["team.branch_hours"] != 12 || len(got.Templates) != 1 || len(got.Templates["code-review"].AddCriteria) != 1 ||
		got.Templates["code-review"].Models != nil || got.Templates["code-review"].Cadence != "" || len(notes) != 6 {
		t.Errorf("bounds at load: %+v\n%v", got, notes)
	}
	os.WriteFile(path, []byte(`{"v":1,"team":{"team.branch_hours":12}`), 0o600)
	if got, notes := Load(path); len(got.Team) != 0 || len(notes) != 1 {
		t.Errorf("a broken file was partly used: %+v %v", got, notes)
	}
	target := filepath.Join(dir, "elsewhere.json")
	os.WriteFile(target, []byte(`{"team":{"team.max_agents":6}}`), 0o600)
	os.Remove(path)
	os.Symlink(target, path)
	if got, notes := Load(path); len(got.Team) != 0 || len(notes) != 1 {
		t.Errorf("followed a link: %+v %v", got, notes)
	}
}

// Seeding marks what tuning added, applies models and the cadence, and
// leaves out roles past the cap in force then.
func TestSeed(t *testing.T) {
	f := File{}
	f.Apply("template.code-review.add_constraint", "no force pushes", ctx(5), now)
	f.Apply("template.code-review.add_role", "tester on haiku", ctx(5), now)
	f.Apply("template.code-review.model.code reviewer", "opus", ctx(5), now)
	base, _ := templates.Find(templates.Builtins(), "code-review")
	got, left := f.Seed(base, 4)
	if len(left) != 0 || len(got.Team) != 4 || got.Team[3].Role != "tester" || !strings.HasSuffix(got.Constraints[len(got.Constraints)-1], "no force pushes "+Mark) {
		t.Errorf("seeded %+v left %v", got, left)
	}
	for _, r := range got.Team {
		if r.Role == "code reviewer" && r.Model != "opus" {
			t.Errorf("model not tuned: %+v", r)
		}
	}
	if _, left := f.Seed(base, 3); len(left) != 1 || left[0] != "tester" {
		t.Errorf("past the cap at seed time: %v", left)
	}
	if len(base.Team) != 3 {
		t.Error("the template itself changed")
	}
	f.Apply("template.code-review.add_criterion", "auditor signs off", ctx(5), now)
	if c := f.Criteria("code-review"); len(c) != 1 || c[0] != "auditor signs off "+Mark {
		t.Errorf("criteria %v", c)
	}
}

// A prompt proposal is shown whole: at most MaxProposal bytes, never cut.
func TestProposalText(t *testing.T) {
	if _, err := ProposalText(strings.Repeat("x", MaxProposal+1)); err == nil {
		t.Error("an over-long proposal was taken")
	}
	if s, err := ProposalText("line one\n\x1b[2Jline two" + string(rune(0x202e))); err != nil || strings.ContainsAny(s, "\x1b"+string(rune(0x202e))) {
		t.Errorf("%q %v", s, err)
	}
	if _, err := ProposalText(" \n "); err == nil {
		t.Error("an empty proposal")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
