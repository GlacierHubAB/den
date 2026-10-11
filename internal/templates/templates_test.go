package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/mission"
)

func TestBuiltinsAreWellFormed(t *testing.T) {
	list, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 3 {
		t.Fatalf("only %d built-ins", len(list))
	}
	for _, tp := range list {
		if !tp.Builtin || !ValidName(tp.Name) || tp.Mission == "" || tp.DoneWhen == "" || len(tp.Team) == 0 {
			t.Errorf("incomplete built-in %+v", tp)
		}
		roles := map[string]bool{}
		for _, m := range tp.Team {
			roles[strings.ToLower(m.Role)] = true
			if m.Model != "haiku" && m.Model != "sonnet" && m.Model != "opus" {
				t.Errorf("%s: %s has model %q", tp.Name, m.Role, m.Model)
			}
		}
		for _, m := range tp.Team {
			if m.Under != "" && !roles[strings.ToLower(m.Under)] {
				t.Errorf("%s: %s reports to %q, who is not in the team", tp.Name, m.Role, m.Under)
			}
		}
	}
	if _, ok := Find(list, "Code-Review"); !ok {
		t.Error("code-review not found")
	}
}

func TestUserFileOverlaysAndSurvivesBadEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "templates.json")
	os.WriteFile(path, []byte(`[
	  {"name": "bugfix", "mission": "Mine for {target}", "shape": "fan-out"},
	  {"name": "Nightly", "mission": "Run the suite", "shape": "schedule", "cadence": "daily 02:00"},
	  {"name": "bad name", "mission": "x"},
	  {"name": "empty"}
	]`), 0o644)
	list, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	bf, _ := Find(list, "bugfix")
	if bf.Builtin || bf.Mission != "Mine for {target}" || bf.Shape != mission.FanOut {
		t.Errorf("bugfix = %+v", bf)
	}
	if n, ok := Find(list, "nightly"); !ok || n.Cadence != "daily 02:00" {
		t.Errorf("nightly = %+v", n)
	}
	if _, ok := Find(list, "bad name"); ok {
		t.Error("kept an invalid name")
	}
	if _, ok := Find(list, "empty"); ok {
		t.Error("kept a template without a purpose")
	}
	if _, ok := Find(list, "research"); !ok {
		t.Error("built-ins lost")
	}
}

func TestMalformedFileIsReportedAndNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "templates.json")
	os.WriteFile(path, []byte(`[{"name": "x",`), 0o644)
	list, err := Load(path)
	if err == nil || len(list) < 3 {
		t.Fatalf("err=%v len=%d", err, len(list))
	}
	if err := Save(path, Template{Name: "new", Mission: "m"}); err == nil {
		t.Fatal("saved over a file that does not parse")
	}
	if raw, _ := os.ReadFile(path); string(raw) != `[{"name": "x",` {
		t.Errorf("file changed: %q", raw)
	}
}

func TestSaveFromMissionRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den", "templates.json")
	ms := &mission.Mission{ID: "m1", Name: "Tests green", Mission: "Keep {target} green.", DoneWhen: "standing", Constraints: []string{"no skips"},
		Shape: mission.Loop, Created: time.Now(),
		Team: []mission.Member{{Role: "lead", Model: "opus"}, {Role: "coder", Model: "sonnet", Under: "lead"}}}
	if err := Save(path, FromMission("keep-green", ms)); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Template{Name: "other", Mission: "o"}); err != nil {
		t.Fatal(err)
	}
	ms.Mission = "Keep it all green."
	if err := Save(path, FromMission("keep-green", ms)); err != nil {
		t.Fatal(err)
	}
	list, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	kg, ok := Find(list, "keep-green")
	if !ok || kg.Mission != "Keep it all green." || kg.Shape != mission.Loop || len(kg.Team) != 2 || kg.Team[1].Under != "lead" || kg.Builtin {
		t.Errorf("keep-green = %+v", kg)
	}
	if _, ok := Find(list, "other"); !ok {
		t.Error("the other template was lost on the second save")
	}
	if err := Save(path, Template{Name: "Bad Name", Mission: "m"}); err == nil {
		t.Error("saved an invalid name")
	}
	if err := Save(path, Template{Name: "empty"}); err == nil {
		t.Error("saved a template without a purpose")
	}
}

func TestFillAndDescribe(t *testing.T) {
	tp := Template{Name: "t", Mission: "Review {target} now.", Shape: mission.FanOut, Team: []mission.Member{{Role: "lead", Model: "opus"}, {Role: "reader", Model: "haiku", Under: "lead"}}}
	if got := tp.Fill("~/code/demo-app"); got != "Review ~/code/demo-app now." {
		t.Errorf("fill = %q", got)
	}
	if got := (Template{Mission: "Do it."}).Fill("the API"); got != "Do it. (the API)" {
		t.Errorf("fill without blank = %q", got)
	}
	d := tp.Describe("demo")
	for _, want := range []string{`Template "t"`, "Review demo now.", "shape: fan-out", "team: lead on opus", "team: reader on haiku, under lead"} {
		if !strings.Contains(d, want) {
			t.Errorf("describe lacks %q:\n%s", want, d)
		}
	}
}
