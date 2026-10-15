package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/mission"
)

// standingModel is controlModel with worker-otter, a live local session
// in a mode that asks, for send_message to reach.
func standingModel(t *testing.T, mode string) (*loop, string) {
	l, dir, _ := controlModel(t)
	old := sessionProc
	t.Cleanup(func() { sessionProc = old })
	sessionProc = func(pid int, _ int64, _ string) (api.AgentProc, bool) { return api.AgentProc{PID: pid, Start: 7}, true }
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "cc-socks", "7002.sock")
	l.m.base = append(l.m.base, fleet.Agent{Name: "Busy Otter", Status: fleet.Idle, Session: discover.Session{PID: 7002, SessionID: "s-otter", Name: "worker-otter", Socket: sock, Version: "2.1.296", CWD: "/work/otter", Alive: true}})
	l.m.ctl.modes = map[string]string{"s-otter": mode}
	return l, dir
}

// appTurn sends one orch.say and runs the loop to its end, answering a
// tool card with verb ("" leaves cards alone); it returns how many tool
// cards came up.
func appTurn(t *testing.T, l *loop, c *apiConn, text, verb string) int {
	t.Helper()
	cards := map[*inbox.Item]bool{}
	c.call("orch.say", map[string]string{"text": text})
	stream := chunks(c)
	l.until("the app's turn", func() bool {
		select {
		case <-stream:
			return true
		default:
			return false
		}
	}, func() {
		if it := waitingOf(l.m, inbox.ControlAsk); it != nil {
			cards[it] = true
			if verb != "" {
				answerCard(t, l.m, inbox.ControlAsk, verb)
			}
		}
	})
	l.until("the turn's result", func() bool { return l.m.ctl.turn == nil }, nil)
	return len(cards)
}

// /always on a card for send_message gives a standing allowance for that
// session: the next turn's message goes without a card, with a note in
// Den's chat; a new session that takes the name is asked about again.
func TestStandingSendMessage(t *testing.T) {
	l, dir := standingModel(t, "default")
	c := pairApp(t, l)
	if n := appTurn(t, l, c, "ping the otter MSG-PLEASE", "always"); n != 1 {
		t.Fatalf("%d cards on the first turn", n)
	}
	rules := l.m.api.srv.AllRules()
	if len(rules) != 1 || rules[0].SessionID != "s-otter" || rules[0].Peer != "worker-otter" || rules[0].PID != 7002 || rules[0].Start != 7 {
		t.Fatalf("rules %+v", rules)
	}
	if n := appTurn(t, l, c, "again MSG-PLEASE", ""); n != 0 {
		t.Errorf("%d cards with a standing allowance", n)
	}
	noted := false
	for _, e := range l.m.orch.chat {
		if strings.Contains(e.text, "allowed by your standing allowance") && e.full == "run the tests" {
			noted = true
		}
	}
	if !noted {
		t.Error("no chat note with the message a press away")
	}
	answers, _ := os.ReadFile(filepath.Join(dir, "answers"))
	if strings.Count(string(answers), `"behavior":"allow"`) != 2 {
		t.Errorf("answers %s", answers)
	}
	// worker-otter respawned: same name, another session.
	for i := range l.m.base {
		if l.m.base[i].Session.SessionID == "s-otter" {
			l.m.base[i].Session.SessionID, l.m.base[i].Session.PID = "s-otter-2", 7009
			l.m.base[i].Session.Socket = filepath.Join(filepath.Dir(l.m.base[i].Session.Socket), strconv.Itoa(7009)+".sock")
		}
	}
	l.m.ctl.modes["s-otter-2"] = "default"
	if n := appTurn(t, l, c, "third MSG-PLEASE", "deny"); n != 1 {
		t.Errorf("%d cards for a new session of the same name", n)
	}
	// /api disallow takes it back.
	mm, _ := l.m.apiCommand([]string{"disallow", "all"})
	*l.m = mm.(Model)
	if len(l.m.api.srv.AllRules()) != 0 {
		t.Error("disallow all left rules")
	}
}

// A session that does not ask (bypassPermissions, or a mode Den does not
// know) gets no /always: the card says why, and /always is refused.
func TestNoStandingForASessionThatDoesNotAsk(t *testing.T) {
	for _, mode := range []string{"bypassPermissions", ""} {
		l, _ := standingModel(t, mode)
		c := pairApp(t, l)
		c.call("orch.say", map[string]string{"text": "ping MSG-PLEASE"})
		stream := chunks(c)
		var card *inbox.Item
		l.until("the card", func() bool { card = waitingOf(l.m, inbox.ControlAsk); return card != nil }, nil)
		body := strings.Join(card.Body, "\n")
		if _, ok := l.m.ctl.always[card]; ok || !strings.Contains(body, "No standing allowance is offered here") {
			t.Errorf("mode %q: /always offered: %s", mode, body)
		}
		answerCard(t, l.m, inbox.ControlAsk, "always")
		if !strings.Contains(l.m.flash, "No standing allowance for this one") || waitingOf(l.m, inbox.ControlAsk) == nil {
			t.Errorf("mode %q: /always took: %q", mode, l.m.flash)
		}
		answerCard(t, l.m, inbox.ControlAsk, "deny")
		l.until("the turn", func() bool {
			select {
			case <-stream:
				return true
			default:
				return false
			}
		}, nil)
	}
}

// update_mission in an app's turn never applies directly, standing or not:
// Den asks the human, since the turn is not theirs.
func TestUpdateMissionInAnAppTurnAsks(t *testing.T) {
	m, ms := missionUpdateModel(t, originApp)
	if d := updateMission(t, &m, "continue"); d.Behavior != "allow" || !strings.Contains(d.Message, "asked the human") {
		t.Errorf("decision %+v", d)
	}
	if got := m.missions.Get(ms.ID); got.State != mission.Running || len(missionItems(m)) != 1 {
		t.Errorf("state %v, items %d", got.State, len(missionItems(m)))
	}
}
