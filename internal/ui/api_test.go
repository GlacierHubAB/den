package ui

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// apiEnv gives a test its own runtime folder, state folder and config.
func apiEnv(t *testing.T) (cfgPath, run string) {
	t.Helper()
	run = filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	cfgPath = filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("DEN_CONFIG", cfgPath)
	return cfgPath, run
}

func apiItem(m *Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.APIOn && it.State == inbox.Waiting {
			return it
		}
	}
	return nil
}

// /api on asks first; only the human's ^Y turns it on and writes the
// config; /api off drops apps at once and keeps the path Den's.
func TestAPIOnNeedsTheHumansYes(t *testing.T) {
	_, run := apiEnv(t)
	m := testModel(140, 50)
	if m.apiChip() != "" {
		t.Error("a chip while the API is off")
	}
	mm, _ := m.apiCommand([]string{"on"})
	m = mm.(Model)
	it := apiItem(&m)
	if it == nil || m.api.srv != nil || config.Load().API.Enabled {
		t.Fatal("/api on must only ask")
	}
	if !strings.Contains(strings.Join(it.Body, " "), "costs money") {
		t.Errorf("the question does not say what it enables: %q", it.Body)
	}
	// The request path agents' hooks use cannot answer it.
	if m.store.Answer(it, "allow") {
		t.Fatal("the request path answered /api on")
	}
	readToEnd(t, &m, it)
	answerGuidance(&m, "allow")
	if m.api.srv == nil || !config.Load().API.Enabled {
		t.Fatalf("^Y did not turn it on: %q", m.flash)
	}
	defer m.api.srv.Close()
	sock := filepath.Join(run, "den-api.sock")
	if fi, err := os.Lstat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}
	if m.apiChip() == "" {
		t.Error("no chip with the API on")
	}
	mm, _ = m.apiCommand([]string{"off"})
	m = mm.(Model)
	if config.Load().API.Enabled || !m.api.srv.Paused() || m.apiChip() != "" {
		t.Error("/api off")
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Error("/api off gave up the path")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := c.Read(buf)
	c.Close()
	if !strings.Contains(string(buf[:n]), `"unavailable"`) {
		t.Errorf("a client after /api off got %q", buf[:n])
	}
}

// apiOn turns the API on the way the human does.
func apiOn(t *testing.T, m *Model) {
	t.Helper()
	mm, _ := m.apiCommand([]string{"on"})
	*m = mm.(Model)
	readToEnd(t, m, apiItem(m))
	answerGuidance(m, "allow")
	if m.api.srv == nil {
		t.Fatalf("API not on: %q", m.flash)
	}
	t.Cleanup(m.api.srv.Close)
}

// apiConn is a raw client of den-api.sock.
type apiConn struct {
	t  *testing.T
	nc net.Conn
	r  *bufio.Reader
	id int
}

func dialAPI(t *testing.T, m *Model) *apiConn {
	t.Helper()
	nc, err := net.Dial("unix", m.api.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	c := &apiConn{t: t, nc: nc, r: bufio.NewReader(nc)}
	c.call("hello", map[string]any{"v": 1, "client": "test"})
	return c
}

func (c *apiConn) call(op string, params any) map[string]any {
	c.t.Helper()
	c.id++
	raw, _ := json.Marshal(map[string]any{"v": 1, "id": c.id, "op": op, "params": params})
	c.nc.Write(append(raw, '\n'))
	for {
		l := c.read()
		if id, ok := l["id"].(float64); ok && int(id) == c.id {
			return l
		}
	}
}

func (c *apiConn) read() map[string]any {
	c.t.Helper()
	c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatal(err)
	}
	var l map[string]any
	json.Unmarshal(raw, &l)
	return l
}

func grantItem(m *Model) *inbox.Item {
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Grant && it.State == inbox.Waiting {
			return it
		}
	}
	return nil
}

// An app's grant request becomes a card: Den's frame, the body exactly as
// sent, ^Y only once all of it was shown, never "always", and the answer
// reaches the app with the hash of what was shown.
func TestGrantCardAndAnswer(t *testing.T) {
	apiEnv(t)
	m := testModel(140, 30)
	apiOn(t, &m)
	c := dialAPI(t, &m)
	c.call("grant.events", nil)
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "line "+strconv.Itoa(i)+" of the manifest")
	}
	body := strings.Join(lines, "\n")
	r := c.call("grant.request", map[string]any{"manifest": map[string]any{"id": "app", "name": "Cost view", "version": 3, "capabilities": []string{"den.fleet:read"}, "body": body}})
	if r["ok"] != true {
		t.Fatalf("request: %v", r)
	}
	var act api.Act
	select {
	case act = <-m.api.srv.Acts():
	case <-time.After(3 * time.Second):
		t.Fatal("no act")
	}
	m.applyAPIAct(act)
	it := grantItem(&m)
	if it == nil || it.Title != "App grant · Cost view v3" {
		t.Fatalf("card: %+v", it)
	}
	if !strings.Contains(it.Body[0], "requested by") || it.Body[1] != "capabilities: den.fleet:read" {
		t.Errorf("Den's frame: %q", it.Body[:2])
	}
	if strings.Join(it.Body[3:len(it.Body)-1], "\n") != body {
		t.Error("the card does not show the body exactly as sent")
	}
	for _, it := range m.rosterFor(false) {
		if strings.Contains(strings.Join(it.Waiting, " "), "Cost view") {
			t.Error("a grant card reached the orchestrator's roster")
		}
	}
	m.tab = tabInbox
	m.refresh()
	m.selectItem(it)
	answerGuidance(&m, "allow")
	if it.State != inbox.Waiting || !strings.Contains(m.flash, "before ^Y") {
		t.Fatalf("^Y before the whole body was shown: %q", m.flash)
	}
	readToEnd(t, &m, it)
	answerGuidance(&m, "always")
	if it.State != inbox.Waiting {
		t.Fatal("/always answered a grant")
	}
	answerGuidance(&m, "allow")
	ev := c.read()
	data, _ := ev["data"].(map[string]any)
	if ev["event"] != "grant" || data["answer"] != "granted" || data["hash"] != api.HashOf(body) || data["version"] != float64(3) {
		t.Errorf("grant event: %v", ev)
	}
}

// /api and the chip say what the lane is doing and how many wait.
func TestLaneStateWords(t *testing.T) {
	if got := laneChip(api.LaneState{State: "ready"}); got != "" {
		t.Errorf("ready: %q", got)
	}
	if got := laneChip(api.LaneState{State: "starting", Queued: 2}); got != "lane starting · 2 queued" {
		t.Errorf("starting: %q", got)
	}
	if got := laneChip(api.LaneState{State: "failed: Claude Code 9.9.9 is not verified"}); !strings.HasPrefix(got, "lane failed: Claude Code 9.9.9") {
		t.Errorf("failed: %q", got)
	}
	if got := laneWords(api.LaneState{State: "ready", Running: true, Queued: 1}); got != "ready · 1 question running · 1 queued" {
		t.Errorf("/api: %q", got)
	}
}

// ^N leaves the API off.
func TestAPIOnDeclined(t *testing.T) {
	apiEnv(t)
	m := testModel(140, 50)
	mm, _ := m.apiCommand([]string{"on"})
	m = mm.(Model)
	readToEnd(t, &m, apiItem(&m))
	answerGuidance(&m, "deny")
	if m.api.srv != nil || config.Load().API.Enabled {
		t.Error("^N turned the API on")
	}
}
