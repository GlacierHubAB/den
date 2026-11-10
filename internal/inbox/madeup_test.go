package inbox

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/events"
)

// madeUpHook is a hook event as a sender could write it: the session id in
// the envelope, in the hook's own payload, or both.
func madeUpHook(event, envelopeSID, payloadSID string) *bridge.Request {
	p, _ := json.Marshal(map[string]any{
		"session_id": payloadSID, "hook_event_name": event, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}, "notification_type": "idle_prompt", "message": "hi",
		"last_assistant_message": "Done. More.",
	})
	return bridge.TestRequest(bridge.Envelope{Event: event, SessionID: envelopeSID, Payload: p})
}

// A session id Den makes up is no agent's: Apply refuses it, whichever way
// it is sent, whatever the event, and makes no item. The sender is told.
func TestApplyRefusesAMadeUpSessionID(t *testing.T) {
	const agentSID = "0d2b8f3a-1c4e-4a6b-9d7e-2f5a8c1b3e90"
	for _, prefix := range MadeUpPrefixes() {
		id := prefix + "anything"
		for _, event := range []string{"PermissionRequest", "PreToolUse", "Notification", "PostToolUseFailure", "Stop", "SessionStart"} {
			for name, ids := range map[string][2]string{"envelope": {id, agentSID}, "payload": {agentSID, id}, "both": {id, id}} {
				s := New()
				r := madeUpHook(event, ids[0], ids[1])
				if it := s.Apply(r); it != nil || len(s.Items()) != 0 {
					t.Errorf("%s %s (%s): item %+v, %d items", prefix, event, name, it, len(s.Items()))
				}
				if d, ok := r.Decided(); !ok || d.Behavior != "deny" {
					t.Errorf("%s %s (%s): sender told %+v (answered %v)", prefix, event, name, d, ok)
				}
			}
		}
	}
}

// An agent's session id, even one that has a prefix inside it, is taken.
func TestApplyTakesAnAgentsSessionID(t *testing.T) {
	for _, sid := range []string{"0d2b8f3a-1c4e-4a6b-9d7e-2f5a8c1b3e90", "s1", "mossy-lemur", "a-den:b", "denver", "tune", "xmission:1", "Den:setup", "DEN:setup"} {
		s := New()
		r := madeUpHook("PermissionRequest", sid, sid)
		if it := s.Apply(r); it == nil || it.SessionID != sid || it.Kind != Permission {
			t.Errorf("%q: item %+v", sid, it)
		}
	}
}

// Den's own constructors are not Apply: its cards still have their ids.
func TestDensOwnCardsKeepTheirMadeUpIDs(t *testing.T) {
	s := New()
	for sid, it := range map[string]*Item{
		DesktopSession: s.AddDesktopMission("t", nil, nil), SetupSession: s.AddSetup("t", nil), MirrorSession: s.AddMirrorAsk("t", nil),
		MissionPrefix + "m1": s.AddMissionDone(MissionPrefix+"m1", "t", nil), TunePrefix + "/r": s.AddProcessTuned(TunePrefix+"/r", "t", nil),
		MemoryPrefix + "/r": s.AddRulesPick(MemoryPrefix+"/r", "t", nil), HostPrefix + "box": s.AddInstall("box", "t", nil, nil),
	} {
		if it == nil || it.SessionID != sid || !MadeUpSession(it.SessionID) {
			t.Errorf("%q: item %+v", sid, it)
		}
	}
}

// A host's event with such an id is dropped; a host agent's is shown.
func TestApplyRemoteRefusesAMadeUpSessionID(t *testing.T) {
	for _, prefix := range MadeUpPrefixes() {
		s := New()
		ev := events.New(events.NeedsYou, prefix+"anything")
		ev.Request, ev.Tool, ev.Host = "permission", "Bash", "devbox"
		if it := s.ApplyRemote(ev); it != nil || len(s.Items()) != 0 {
			t.Errorf("%s: item %+v, %d items", prefix, it, len(s.Items()))
		}
	}
	if it := New().ApplyRemote(remoteEvent(events.NeedsYou, "permission", "Bash")); it == nil {
		t.Error("a host agent's event was dropped")
	}
}

// The list holds every prefix this file defines, and every *Session
// constant is under it: a new kind of Den session cannot be left out, and
// so cannot be sent in by a hook.
func TestMadeUpPrefixesAreAllOfThem(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "inbox.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, p := range madeUpPrefixes {
		listed[p] = true
		if !strings.HasSuffix(p, ":") {
			t.Errorf("prefix %q does not end in a colon", p)
		}
	}
	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, _ := strconv.Unquote(lit.Value)
				switch {
				case strings.HasSuffix(id.Name, "Prefix") && strings.HasSuffix(v, ":"):
					found++
					if !listed[v] {
						t.Errorf("%s = %q is not in madeUpPrefixes", id.Name, v)
					}
				case strings.HasSuffix(id.Name, "Session"):
					found++
					if !MadeUpSession(v) {
						t.Errorf("%s = %q is not a made-up session id", id.Name, v)
					}
				}
			}
		}
		return true
	})
	if found < 14 {
		t.Errorf("found only %d Prefix and Session constants: the test lost its way", found)
	}
}
