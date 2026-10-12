package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/discover"
)

// metaAllowed is every JSON field Meta may carry. Adding a field to Meta
// fails this test until it is added here, which is the moment to check it
// is metadata under the observe policy (docs/REMOTE.md).
var metaAllowed = []string{
	"index", "session", "status", "name", "peer", "role", "task", "model", "claude", "repo", "branch",
	"status_age_ms", "started_age_ms", "last_active_ms",
	"context_tokens", "context_window", "has_usage", "usage_from_badge",
	"limits", "tokens", "subagent_tokens", "reported_usd", "reported_unpriced",
	// MetaLimits
	"context", "filled", "window", "five_hour", "five_hour_resets", "seven_day", "seven_day_resets", "usd",
	// transcript.Tokens
	"input", "output", "cache_creation", "cache_read",
}

// metaForbidden are names a content field would plausibly carry.
var metaForbidden = []string{"tail", "title", "path", "cwd", "messagingsocketpath", "socket", "transcript", "text", "code", "prompt", "reply", "input_text", "tool_input", "command", "changes"}

func jsonFields(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] || t == reflect.TypeFor[time.Time]() {
		return
	}
	seen[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
		jsonFields(f.Type, seen, out)
	}
}

func TestMetaFieldsAreAllowlisted(t *testing.T) {
	got := map[string]bool{}
	jsonFields(reflect.TypeFor[Meta](), map[reflect.Type]bool{}, got)
	allowed := map[string]bool{}
	for _, f := range metaAllowed {
		allowed[f] = true
	}
	var extra []string
	for f := range got {
		if !allowed[f] {
			extra = append(extra, f)
		}
		for _, bad := range metaForbidden {
			if strings.ToLower(f) == bad {
				t.Errorf("Meta serialises a content field %q", f)
			}
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("Meta has fields not on the allowlist: %v; check they are metadata, then add them to metaAllowed", extra)
	}
}

// TestMetaCarriesNoSecrets: a session whose prompt,
// reply, command, cwd and title carry SECRET markers is snapshot and
// projected, and no marker reaches the JSON.
func TestMetaCarriesNoSecrets(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	repo := filepath.Join(t.TempDir(), "SECRET-cwd-dir", "den")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	cwd := filepath.Join(repo, "SECRET-subdir")
	os.MkdirAll(cwd, 0o755)

	id := "11111111-2222-4333-8444-555555555555"
	os.MkdirAll(filepath.Join(cfg, "sessions"), 0o755)
	sess := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"status":"busy","statusUpdatedAt":%d,"messagingSocketPath":"/tmp/SECRET-socket","version":"2.1.300","name":"worker"}`,
		os.Getpid(), id, cwd, time.Now().Add(-time.Hour).UnixMilli(), time.Now().Add(-time.Minute).UnixMilli())
	os.WriteFile(filepath.Join(cfg, "sessions", "1.json"), []byte(sess), 0o600)

	proj := filepath.Join(cfg, "projects", "-SECRET-project")
	os.MkdirAll(proj, 0o755)
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"SECRET-prompt please"}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-fable-5-1","content":[{"type":"text","text":"SECRET-reply here"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude-fable-5-1","content":[{"type":"tool_use","name":"Bash","input":{"command":"echo SECRET-command"}}],"usage":{"input_tokens":20,"output_tokens":7}}}`,
		`{"type":"ai-title","aiTitle":"SECRET-title"}`,
	}
	os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	agents, err := Snapshot()
	if err != nil || len(agents) != 1 {
		t.Fatalf("snapshot: %v %d", err, len(agents))
	}
	a := agents[0]
	// The snapshot itself does carry content; the projection must not.
	full, _ := json.Marshal(a)
	if !strings.Contains(string(full), "SECRET-reply") || !strings.Contains(string(full), "SECRET-title") {
		t.Fatalf("fixture did not exercise the content fields: %s", full)
	}
	m := a.Meta(0, time.Now(), true)
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("Meta leaks content: %s", raw)
	}
	if m.Repo != "den" || m.Branch != "main" || m.Status != "working" || m.Model != "claude-fable-5-1" || m.Tokens["claude-fable-5-1"].Total() == 0 {
		t.Errorf("Meta lost metadata: %s", raw)
	}
	// And back: the agent rebuilt locally has no content either.
	back := FromMeta(m, "devbox", time.Now(), cost.NewTable(nil))
	raw, _ = json.Marshal(back)
	if strings.Contains(string(raw), "SECRET") || back.Host != "devbox" || back.Cost.Total() == 0 {
		t.Errorf("FromMeta = %s", raw)
	}
}

func TestMetaLabelsAndControlCharacters(t *testing.T) {
	a := Agent{Name: "Mossy Lemur", Status: Idle}
	a.Labels.Role = "coder\x1b]52;c;aGk=\x07"
	a.Labels.Task = strings.Repeat("x", 200)
	m := a.Meta(3, time.Now(), true)
	if m.Role != "coder" || len([]rune(m.Task)) != LabelMax || m.Index != 3 {
		t.Errorf("role %q task %d runes", m.Role, len([]rune(m.Task)))
	}
	if m = a.Meta(0, time.Now(), false); m.Role != "" || m.Task != "" {
		t.Errorf("labels=false still sent %q %q", m.Role, m.Task)
	}
}

func TestFromMetaAgesAreTheRemotes(t *testing.T) {
	// The remote's clock is ten minutes ahead; ages make that irrelevant.
	received := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m := Meta{Session: "s1", Status: "working", StatusAge: 90_000, StartedAge: 3_600_000, ActiveAge: 90_000}
	a := FromMeta(m, "b", received, cost.NewTable(nil))
	if got := received.Sub(a.Session.StatusChanged()); got != 90*time.Second {
		t.Errorf("status age = %v", got)
	}
	if got := received.Sub(a.LastActive); got != 90*time.Second {
		t.Errorf("last active age = %v", got)
	}
	if a.Status != Working || a.Session.Status != "busy" || !a.Session.Alive {
		t.Errorf("status = %v %q", a.Status, a.Session.Status)
	}
}

func TestLocalJSONHasNoHostFields(t *testing.T) {
	raw, _ := json.Marshal(Agent{Name: "X"})
	for _, f := range []string{"Host", "HostState", "Stale"} {
		if strings.Contains(string(raw), `"`+f+`"`) {
			t.Errorf("a local agent's JSON has %s: %s", f, raw)
		}
	}
	raw, _ = json.Marshal(Agent{Name: "X", Host: "b", Stale: true})
	if !strings.Contains(string(raw), `"Host":"b"`) || !strings.Contains(string(raw), `"Stale":true`) {
		t.Errorf("remote agent json = %s", raw)
	}
}

func TestLookupByHost(t *testing.T) {
	mk := func(id, name, host string) Agent {
		return Agent{Session: discover.Session{SessionID: id}, Name: name, Host: host}
	}
	agents := []Agent{mk("aaaa", "Mossy Lemur", ""), mk("bbbb", "Mossy Lemur", "devbox"), mk("cccc", "Tiny Owl", "devbox")}
	if _, err := Lookup(agents, "mossy"); err == nil || !strings.Contains(err.Error(), "Mossy Lemur@local") || !strings.Contains(err.Error(), "Mossy Lemur@devbox") {
		t.Errorf("ambiguous bare name: %v", err)
	}
	if a, err := Lookup(agents, "mossy@devbox"); err != nil || a.Session.SessionID != "bbbb" {
		t.Errorf("mossy@devbox = %v %v", a.Session.SessionID, err)
	}
	if a, err := Lookup(agents, "@Mossy Lemur@local"); err != nil || a.Session.SessionID != "aaaa" {
		t.Errorf("mossy@local = %v %v", a.Session.SessionID, err)
	}
	if a, ok := Find(agents, "tiny"); !ok || a.Host != "devbox" {
		t.Errorf("tiny = %+v", a)
	}
	if _, ok := Find(agents, "mossy"); ok {
		t.Error("Find should not pick one of two hosts")
	}
	if _, err := Lookup(agents, "tiny@gpu"); err == nil {
		t.Error("an unknown host matched")
	}
	// A remote agent has no pid; "0" must not match it.
	if _, ok := Find(agents, "0"); ok {
		t.Error("pid 0 matched")
	}
}
