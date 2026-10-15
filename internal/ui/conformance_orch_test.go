package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/GlacierHubAB/den/internal/orch"
)

// conformanceOrchestrator is the fake claude behind the orchestrator in the
// conformance run: it replays each user turn as Claude Code does, answers an
// interrupt as a stopped turn, and ends every turn with a short reply, except
// that it holds the first one until the harness writes to its gate (a FIFO).
// The suite's "console orch.say" needs that first turn still running when it
// asks for a second and is refused; the harness opens the gate when it has
// seen that refusal on the wire, never after a delay.
const conformanceOrchestrator = `#!/bin/sh
PATH=/usr/bin:/bin
D=$(dirname "$0")
inited=""
while IFS= read -r line; do
  case "$line" in
    *'"control_response"'*) continue ;;
    *'"control_request"'*)
      case "$line" in *'"interrupt"'*) echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","num_turns":1}' ;; esac
      continue ;;
  esac
  # Claude Code sends its init only after the first message it reads.
  if [ -z "$inited" ]; then
    echo '{"type":"system","subtype":"init","session_id":"s-conf","model":"m","claude_code_version":"2.1.296","tools":["ListAgents"],"mcp_servers":[]}'
    inited=1
  fi
  printf '%s\n' "$line" | sed 's/}$/,"isReplay":true}/'
  # The first turn of the whole run is held, whichever process gets it.
  if [ ! -e "$D/held" ]; then : > "$D/held"; read -r _ < "$D/gate"; fi
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello."}}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"Hello.","num_turns":1}'
done
`

// orchestratorGate holds the conformance run's fake orchestrator.
type orchestratorGate struct {
	t    *testing.T
	dir  string
	fifo *os.File // open read-write, so that opening the gate never blocks

	mu        sync.Mutex
	released  bool
	byRefusal bool
	says      int // the orch.say requests sent so far, on every connection
}

// said counts one more orch.say sent, and gives its place in the run.
func (g *orchestratorGate) said() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.says++
	return g.says
}

// open lets the held turn go, once. refusal says it was the suite's second
// orch.say being refused that did it.
func (g *orchestratorGate) open(refusal bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.released {
		return
	}
	g.released, g.byRefusal = true, refusal
	g.fifo.WriteString("go\n")
}

// stubOrchestrator makes sure the conformance run can never start a real
// claude: the orchestrator's launch and version check are stubbed to the
// fake above, and PATH leads with a tripwire claude that records any use and
// fails (the usual system directories follow, for the fakes' sed and
// dirname). The test fails if the tripwire ran, or if claude would not
// resolve to it.
func stubOrchestrator(t *testing.T) *orchestratorGate {
	t.Helper()
	dir, trip := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, hook := filepath.Join(dir, "claude"), filepath.Join(dir, "den-hook")
	write(script, conformanceOrchestrator)
	write(hook, "#!/bin/sh\ncat >/dev/null\necho '{\"decision\":\"block\"}'\n")
	gatePath := filepath.Join(dir, "gate")
	if err := syscall.Mkfifo(gatePath, 0o600); err != nil {
		t.Fatal(err)
	}
	fifo, err := os.OpenFile(gatePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := &orchestratorGate{t: t, dir: dir, fifo: fifo}
	t.Cleanup(func() { fifo.Close() })

	tripped := filepath.Join(trip, "calls")
	write(filepath.Join(trip, "claude"), "#!/bin/sh\necho \"$0 $*\" >> '"+tripped+"'\nexit 1\n")
	t.Setenv("PATH", trip+":/usr/bin:/bin")
	resolves := func() {
		t.Helper()
		if got, err := exec.LookPath("claude"); err != nil || got != filepath.Join(trip, "claude") {
			t.Errorf("claude resolves to %q (%v), not to the tripwire: a real claude could run", got, err)
		}
	}
	resolves()
	t.Cleanup(func() {
		resolves()
		if raw, err := os.ReadFile(tripped); err == nil {
			t.Errorf("a launch that was not stubbed ran claude:\n%s", raw)
		}
		// A turn that was held must have been let go by the refusal of the
		// suite's second orch.say, not by this cleanup.
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, err := os.Stat(filepath.Join(dir, "held")); err == nil && !g.byRefusal {
			t.Error("the held turn was not let go by the second orch.say's refusal")
		}
	})

	was := launchOrchestrator
	t.Cleanup(func() { launchOrchestrator = was })
	launchOrchestrator = func(o orch.Options) (*orch.Orchestrator, error) {
		o.Command, o.HookCommand, o.Dir = []string{script}, []string{hook}, dir
		p, err := orch.Start(context.Background(), o)
		if p != nil {
			// Let a held turn go first, so closing never waits on it.
			t.Cleanup(func() { g.open(false); p.Close() })
		}
		return p, err
	}
	wasVersion := orchVersion
	t.Cleanup(func() { orchVersion = wasVersion })
	orchVersion = func(context.Context, []string) (string, error) { return "2.1.296", nil }
	return g
}

// tap wraps the suite's dialer so that the harness sees each orch.say the
// suite sends and what comes back for it. The first reply of code
// "unavailable" to an orch.say is the second one being refused while the
// first turn runs: that opens the gate. Later ones (the suite's retry just
// after a turn ends) change nothing.
func (g *orchestratorGate) tap(dial func(context.Context) (net.Conn, error)) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		nc, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		uc, ok := nc.(*net.UnixConn) // the suite wants a syscall.Conn, which this keeps
		if !ok {
			return nc, nil
		}
		return &tapConn{UnixConn: uc, g: g, says: map[int64]int{}}, nil
	}
}

// tapConn is a connection whose lines the harness reads along the way.
type tapConn struct {
	*net.UnixConn
	g *orchestratorGate

	mu      sync.Mutex
	out, in []byte
	says    map[int64]int // the orch.say requests it sent: id to place in the run
}

func (c *tapConn) Write(b []byte) (int, error) {
	n, err := c.UnixConn.Write(b)
	c.mu.Lock()
	c.out = append(c.out, b[:n]...)
	for _, l := range takeLines(&c.out) {
		var req struct {
			ID *int64 `json:"id"`
			Op string `json:"op"`
		}
		if json.Unmarshal(l, &req) == nil && req.ID != nil && req.Op == "orch.say" {
			c.says[*req.ID] = c.g.said()
		}
	}
	c.mu.Unlock()
	return n, err
}

func (c *tapConn) Read(b []byte) (int, error) {
	n, err := c.UnixConn.Read(b)
	c.mu.Lock()
	c.in = append(c.in, b[:n]...)
	var refused bool
	for _, l := range takeLines(&c.in) {
		var rep struct {
			ID    *int64 `json:"id"`
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		// Only a later orch.say than the run's first counts: a first one
		// refused is the suite's own failure, not the order this waits for.
		if json.Unmarshal(l, &rep) == nil && rep.ID != nil && c.says[*rep.ID] >= 2 && rep.Error != nil && rep.Error.Code == "unavailable" {
			refused = true
		}
	}
	c.mu.Unlock()
	if refused {
		c.g.open(true)
	}
	return n, err
}

// takeLines removes the complete lines from buf and returns them.
func takeLines(buf *[]byte) [][]byte {
	var out [][]byte
	for {
		i := bytes.IndexByte(*buf, '\n')
		if i < 0 {
			return out
		}
		out = append(out, append([]byte(nil), (*buf)[:i]...))
		*buf = (*buf)[i+1:]
	}
}
