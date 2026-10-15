package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/api/denconformance"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// conformanceLane is a fake claude that behaves like the read-only lane
// (see internal/api's fakeLane): --version, the control answers, init with
// the first turn, and one reply, a second after the suite's first turn
// came. Every later turn (the suite's ask.cancel ones) runs until Den stops
// it, as a long real one does: the suite cancels and supersedes them, and a
// turn that ended by itself first, on a slow machine, is not what it
// measures.
const conformanceLane = `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.289 (Claude Code)"; exit 0; fi
sid=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--session-id" ] || [ "$prev" = "--resume" ]; then sid="$a"; fi
  prev="$a"
done
inited=""
while IFS= read -r line; do
  case "$line" in
    *'"control_request"'*)
      id=$(printf '%s\n' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
      case "$line" in
        *'"initialize"'*) printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"'"$id"'","response":{"commands":[],"current_permission_mode":"dontAsk"}}}' ;;
        *'"mcp_status"'*) printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"'"$id"'","response":{"mcpServers":[]}}}' ;;
      esac
      continue ;;
  esac
  if [ -z "$inited" ]; then
    printf '%s\n' '{"type":"system","subtype":"init","session_id":"'"$sid"'","model":"m","claude_code_version":"2.1.289","tools":[],"mcp_servers":[]}'
    inited=1
  fi
  if [ -e "$(dirname "$0")/answered" ]; then continue; fi
  : > "$(dirname "$0")/answered"
  sleep 1 # a turn takes a moment, as a real one does
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"About 12 dollars."}}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"About 12 dollars.","total_cost_usd":0.01}'
done
`

// errNotYet asks onModel to try again: what it waits for has not reached
// the model yet.
var errNotYet = errors.New("not yet")

// TestDreyInterface runs the pinned conformance suite against a real Den
// model with the API on: an isolated HOME, state, config and runtime
// folder, a fake claude for the lane, focus stubbed, and every grant
// answered the way the human answers it, by keys through Update (the
// card selected, read to its end, then ^Y). Nothing in Den's own code
// answers a grant. The console's hooks act on the model in its own loop,
// the console card answered by keys as the human answers it.
func TestDreyInterface(t *testing.T) {
	apiEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	home, _ := os.UserHomeDir()
	for _, k := range []string{"HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "CLAUDE_CONFIG_DIR", "DEN_CONFIG"} {
		if v := os.Getenv(k); !strings.HasPrefix(v, os.TempDir()) {
			t.Fatalf("%s=%q is not a test folder (home %s)", k, v, home)
		}
	}
	dir := t.TempDir()
	lane := filepath.Join(dir, "claude")
	if err := os.WriteFile(lane, []byte(conformanceLane), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(c []string) { laneCommand = c }(laneCommand)
	laneCommand = []string{lane}
	defer func(f func(fleet.Agent) (string, error)) { focusAgent = f }(focusAgent)
	focusAgent = func(fleet.Agent) (string, error) { return "", nil }

	m := testModel(140, 40)
	m.base = append([]fleet.Agent(nil), m.agents...)
	m.cfg.API.AskBudgetUSD = 2
	apiOn(t, &m)
	// A request with a body, so inbox.body has something to serve.
	sid := m.agents[0].Session.SessionID
	p, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "make test"}})
	m.store.Apply(bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p}))
	m.tab = tabFleet
	m.refresh()
	m.api.lastKey = time.Time{}

	path, srv := m.api.srv.Path(), m.api.srv
	press := func(k tea.KeyPressMsg) {
		mm, _ := m.Update(k)
		m = mm.(Model)
	}
	// onModel runs f in the loop below, which owns the model, again every
	// 10 ms while it answers errNotYet.
	type modelCall struct {
		f    func() error
		done chan error
	}
	calls := make(chan modelCall)
	onModel := func(ctx context.Context, f func() error) error {
		for {
			c := modelCall{f, make(chan error, 1)}
			select {
			case calls <- c:
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := <-c.done; !errors.Is(err, errNotYet) {
				return err
			}
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	// answerConsole answers the open console card by keys, as the human
	// does: selected, read to its end, ^Y or ^N.
	answerConsole := func(ctx context.Context, key rune) error {
		return onModel(ctx, func() error {
			var card *inbox.Item
			for it := range m.con.cards {
				if it.Answerable() {
					card = it
				}
			}
			if card == nil {
				return errNotYet
			}
			m.tab = tabInbox
			m.refresh()
			m.selectItem(card)
			for i := 0; i < 200 && m.unreadRows(card) > 0; i++ {
				press(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			press(tea.KeyPressMsg{Code: key, Mod: tea.ModCtrl})
			m.tab = tabFleet
			m.refresh()
			if card.State != inbox.Resolved {
				return fmt.Errorf("the console card stayed open: %q", m.flash)
			}
			return nil
		})
	}
	// permissionCard raises an agent's request with a body of lines lines.
	cards := 0
	permissionCard := func(ctx context.Context, lines int) (string, error) {
		var id string
		err := onModel(ctx, func() error {
			cards++
			cmd := make([]string, lines)
			for i := range cmd {
				cmd[i] = fmt.Sprintf("echo card %d line %d", cards, i)
			}
			p, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": strings.Join(cmd, "\n")}})
			it := m.store.Apply(bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p}))
			if it == nil {
				return errors.New("no card for the request")
			}
			m.refresh()
			id = it.ID
			return nil
		})
		return id, err
	}
	console := &denconformance.ConsoleOptions{
		Offer: func(ctx context.Context) (string, error) {
			var code string
			err := onModel(ctx, func() error {
				if why := m.startConsoleCode(); why != "" {
					return errors.New(why)
				}
				code = strings.Join(m.con.code, " ")
				return nil
			})
			return code, err
		},
		Confirm:    func(ctx context.Context) error { return answerConsole(ctx, 'y') },
		Decline:    func(ctx context.Context) error { return answerConsole(ctx, 'n') },
		Card:       func(ctx context.Context) (string, error) { return permissionCard(ctx, 1) },
		LongCard:   func(ctx context.Context) (string, error) { return permissionCard(ctx, 12) },
		ThiefTest:  "TestConsoleThief",
		SocketPath: path,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		denconformance.Run(t, denconformance.Options{
			Dial: func(ctx context.Context) (net.Conn, error) {
				// Each subtest dials afresh while Den may not yet have seen
				// the last one's connections close: wait for room under the
				// per-process cap, so the cap is not what the suite
				// measures, but only while connections are still closing
				// (the count falls), and never into the subtest's deadline.
				// Connections the suite holds on purpose (the console's
				// cap) do not close, and a wait for them only eats the
				// subtest's time.
				end := time.Now().Add(2 * time.Second)
				if dl, ok := ctx.Deadline(); ok && dl.Add(-time.Second).Before(end) {
					end = dl.Add(-time.Second)
				}
				last, steady := len(srv.Clients()), time.Now()
				for n := last; n >= api.MaxConnsPerPID && time.Now().Before(end) && time.Since(steady) < 100*time.Millisecond; n = len(srv.Clients()) {
					if n != last {
						last, steady = n, time.Now()
					}
					time.Sleep(5 * time.Millisecond)
				}
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
			Focusable: sid,
			Skip:      denconformance.D1Skip,
			Console:   console,
		})
	}()
	for {
		select {
		case <-done:
			return
		case c := <-calls:
			c.done <- c.f()
		case act := <-m.api.srv.Acts():
			if act.Focus != nil {
				m.api.lastKey = time.Time{} // the human has not typed in Den
			}
			// As Bubble Tea does, a command runs on its own goroutine:
			// some are timers (a flash that clears in 4 s), and run here
			// they would hold this loop, and with it every hook below.
			if cmd := m.applyAPIAct(act); cmd != nil {
				go cmd()
			}
			if act.Grant == nil {
				continue
			}
			// The human answers the grant card: select it, read it to its
			// end, ^Y. Only the app-3 request (grant.events) is answered;
			// the others stay waiting, as a human might leave them.
			var card *inbox.Item
			for it, id := range m.api.grants {
				if id == act.Grant.ID {
					card = it
				}
			}
			if card == nil || !strings.Contains(card.Title, "Stand-up board") {
				continue
			}
			m.tab = tabInbox
			m.refresh()
			m.selectItem(card)
			for i := 0; i < 200 && m.unreadRows(card) > 0; i++ {
				press(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			press(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
			if card.State != inbox.Resolved {
				t.Errorf("^Y did not answer the grant card: %q", m.flash)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("the conformance run did not finish")
		}
	}
}

// TestConsoleThief is run by the conformance suite's "console thief", in
// another process, to prove with the console's key; run on its own it
// skips.
func TestConsoleThief(t *testing.T) {
	denconformance.ConsoleThiefHelper(t)
}

var _ = api.HashOf
