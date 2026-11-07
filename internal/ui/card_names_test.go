package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/inbox"
)

// A card's agent names its session, and nothing more: it is no proof of who
// wrote the card. These tests hold what the name can be, so that a card of
// Den's or of a mission never reads as an agent's or, for a mission, as
// Den's.

// A mission's cards show it as "mission <name>", whatever the name is, so a
// mission cannot be named "Den" or as an agent. No card of a mission names
// an agent id.
func TestMissionCardsNameTheirMission(t *testing.T) {
	m, ms := missionUpdateModel(t, originHuman)
	key := missionSessionPrefix + ms.ID
	for _, name := range []string{"Den release", "Den", "Mossy Lemur", "@Mossy Lemur", "drey"} {
		ms.Name = name
		m.missions.Save(ms)
		cards := map[string]*inbox.Item{
			"mission update":   m.store.AddMissionUpdate(key, "t", nil),
			"mission criteria": m.store.AddMissionCriteria(key, "t", nil),
			"mission done":     m.store.AddMissionDone(key, "t", nil),
			"budget":           m.store.AddBudget(key, "t", nil),
			"move":             m.store.AddMissionMove(key, "t", nil),
		}
		for kind, it := range cards {
			if it.Kind.String() != kind {
				t.Fatalf("the %s constructor made a %q", kind, it.Kind)
			}
			c := m.dockCard(it)
			if c.Agent != "mission "+name || c.AgentID != "" || c.Kind != kind {
				t.Errorf("mission %q, %s card: agent %q, agent id %q, kind %q", name, kind, c.Agent, c.AgentID, c.Kind)
			}
		}
	}
	// A mission with no name, or one that is gone, is "mission" alone.
	ms.Name = ""
	m.missions.Save(ms)
	if got := m.nameFor(key); got != "mission" {
		t.Errorf("a mission with no name is %q", got)
	}
	if got := m.nameFor(missionSessionPrefix + "m-gone"); got != "mission" {
		t.Errorf("a gone mission is %q", got)
	}
}

// The tuning cards are Den's to raise but every word in them is the
// orchestrator's: they read "orchestrator", with no agent id.
func TestTuningSessionIsTheOrchestrators(t *testing.T) {
	m := missionModel()
	for _, repo := range []string{"proj-aaaaaaaaaaaa", "/home/x/proj", "den:setup"} {
		if got := m.nameFor(tuneSessionPrefix + repo); got != "orchestrator" {
			t.Errorf("tune:%s is %q", repo, got)
		}
		if id := m.dockAgentID(tuneSessionPrefix + repo); id != "" {
			t.Errorf("tune:%s has agent id %q", repo, id)
		}
	}
}

// Every session id Den's own cards use is a *Session constant that starts
// with inbox.SessionPrefix, and every one reads "Den" with no agent id. The
// test reads inbox.go itself, so a new Den card cannot fall through to a
// cut-off session id (den:powers shown as "den:powe"): a new constant is
// held to the prefix, and a SessionID that is not a parameter, a Session
// constant, a host or a den: literal fails here.
func TestEveryDenSessionIsNamedDen(t *testing.T) {
	m := missionModel()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../inbox/inbox.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if i >= len(vs.Values) || !strings.HasSuffix(id.Name, "Session") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s is not a string literal", id.Name)
					continue
				}
				v, _ := strconv.Unquote(lit.Value)
				consts[id.Name] = v
			}
		}
		return true
	})
	if len(consts) < 9 {
		t.Fatalf("found only %d Session constants in inbox.go: %v", len(consts), consts)
	}
	for name, v := range consts {
		if !strings.HasPrefix(v, inbox.SessionPrefix) {
			t.Errorf("%s = %q does not start with %q", name, v, inbox.SessionPrefix)
		}
		if got := m.nameFor(v); got != "Den" {
			t.Errorf("%s = %q is shown as %q, not Den", name, v, got)
		}
		if id := m.dockAgentID(v); id != "" {
			t.Errorf("%s = %q has agent id %q", name, v, id)
		}
	}
	// Every SessionID a card is made with.
	checked := 0
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "SessionID" {
			return true
		}
		checked++
		switch v := kv.Value.(type) {
		case *ast.BasicLit:
			s, _ := strconv.Unquote(v.Value)
			if !strings.HasPrefix(s, inbox.SessionPrefix) {
				t.Errorf("%s: SessionID %s is a literal outside %q", fset.Position(v.Pos()), v.Value, inbox.SessionPrefix)
			} else if got := m.nameFor(s); got != "Den" {
				t.Errorf("%s: %q is shown as %q", fset.Position(v.Pos()), s, got)
			}
		case *ast.Ident:
			if _, ok := consts[v.Name]; !ok && v.Name != "sessionID" {
				t.Errorf("%s: SessionID %s is not a parameter or a Session constant", fset.Position(v.Pos()), v.Name)
			}
		case *ast.SelectorExpr:
			if v.Sel.Name != "SessionID" {
				t.Errorf("%s: SessionID comes from .%s", fset.Position(v.Pos()), v.Sel.Name)
			}
		case *ast.BinaryExpr:
			if id, ok := v.X.(*ast.Ident); !ok || id.Name != "HostPrefix" {
				t.Errorf("%s: SessionID is a sum that does not start with HostPrefix", fset.Position(v.Pos()))
			}
		default:
			t.Errorf("%s: SessionID is a %T this test does not know", fset.Position(kv.Pos()), kv.Value)
		}
		return true
	})
	if checked < 30 {
		t.Errorf("only %d SessionIDs found in inbox.go: the test lost its way", checked)
	}
}
