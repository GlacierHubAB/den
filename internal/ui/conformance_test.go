package ui

import (
	"context"
	"encoding/json"
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
// the first turn, one reply per turn.
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
        *'"initialize"'*) echo '{"type":"control_response","response":{"subtype":"success","request_id":"'"$id"'","response":{"commands":[],"current_permission_mode":"dontAsk"}}}' ;;
        *'"mcp_status"'*) echo '{"type":"control_response","response":{"subtype":"success","request_id":"'"$id"'","response":{"mcpServers":[]}}}' ;;
      esac
      continue ;;
  esac
  if [ -z "$inited" ]; then
    echo '{"type":"system","subtype":"init","session_id":"'"$sid"'","model":"m","claude_code_version":"2.1.289","tools":[],"mcp_servers":[]}'
    inited=1
  fi
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"About 12 dollars."}}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"About 12 dollars.","total_cost_usd":0.01}'
done
`

// TestDreyInterface runs the pinned conformance suite against a real Den
// model with the API on: an isolated HOME, state, config and runtime
// folder, a fake claude for the lane, focus stubbed, and every grant
// answered the way the human answers it, by keys through Update (the
// card selected, read to its end, then ^Y). Nothing in Den's own code
// answers a grant.
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

	path := m.api.srv.Path()
	done := make(chan struct{})
	go func() {
		defer close(done)
		denconformance.Run(t, denconformance.Options{
			Dial: func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
			Focusable: sid,
			Skip:      denconformance.D1Skip,
		})
	}()
	press := func(k tea.KeyPressMsg) {
		mm, _ := m.Update(k)
		m = mm.(Model)
	}
	for {
		select {
		case <-done:
			return
		case act := <-m.api.srv.Acts():
			if act.Focus != nil {
				m.api.lastKey = time.Time{} // the human has not typed in Den
			}
			if cmd := m.applyAPIAct(act); cmd != nil {
				cmd()
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
		case <-time.After(30 * time.Second):
			t.Fatal("the conformance run did not finish")
		}
	}
}

var _ = api.HashOf
