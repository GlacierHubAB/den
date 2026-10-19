package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/discover"
)

// metaAllowed is every JSON field Meta may carry. Adding a field to Meta
// fails this test until it is added here, which is the moment to check it
// is metadata under the observe policy (docs/REMOTE.md).
var metaAllowed = []string{
	"index", "session", "status", "name", "peer", "role", "task", "model", "claude", "repo", "branch", "dirty",
	"status_age_ms", "started_age_ms", "last_active_ms",
	"context_tokens", "context_window", "has_usage", "usage_from_badge",
	"limits", "tokens", "subagent_tokens", "reported_usd", "reported_unpriced",
	// MetaLimits
	"context", "filled", "window", "five_hour", "five_hour_resets", "seven_day", "seven_day_resets", "usd",
	// transcript.Tokens
	"input", "output", "cache_creation", "cache_read",
}

// fullAllowed is every JSON field Meta.Full may carry: content, which only a
// host under the full policy sends (docs/REMOTE.md).
var fullAllowed = []string{
	"full",
	// MetaFull
	"title", "cwd", "tail", "tail_cut",
	// MetaEntry
	"age_ms", "kind", "text", "tool", "code", "output", "changes",
	// MetaChange, MetaHunk
	"path", "created", "deleted", "unsafe", "hunks", "old_start", "new_start", "lines",
}

// metaForbidden are names a content field would plausibly carry.
var metaForbidden = []string{"tail", "title", "path", "cwd", "messagingsocketpath", "socket", "transcript", "text", "code", "prompt", "reply", "input_text", "tool_input", "command", "changes"}

// jsonFields collects the JSON names of t's fields, recursively; a field
// named in skip is left out with everything under it.
func jsonFields(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool, skip ...string) {
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
		if slices.Contains(skip, name) {
			continue
		}
		out[name] = true
		jsonFields(f.Type, seen, out, skip...)
	}
}

// TestMetaFieldsAreAllowlisted proves the field sets per policy: outside
// Full, Meta (what observe and approve send) has only allowlisted metadata
// and no content-like name; Full has only its own allowlist.
func TestMetaFieldsAreAllowlisted(t *testing.T) {
	check := func(what string, got map[string]bool, list []string, content bool) {
		allowed := map[string]bool{}
		for _, f := range list {
			allowed[f] = true
		}
		var extra []string
		for f := range got {
			if !allowed[f] {
				extra = append(extra, f)
			}
			for _, bad := range metaForbidden {
				if !content && strings.ToLower(f) == bad {
					t.Errorf("%s serialises a content field %q", what, f)
				}
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			t.Errorf("%s has fields not on its allowlist: %v; check what they carry, then add them", what, extra)
		}
	}
	meta := map[string]bool{}
	jsonFields(reflect.TypeFor[Meta](), map[reflect.Type]bool{}, meta, "full")
	check("Meta under observe and approve", meta, metaAllowed, false)
	full := map[string]bool{}
	jsonFields(reflect.TypeFor[MetaFull](), map[reflect.Type]bool{}, full)
	full["full"] = true
	check("Meta.Full", full, fullAllowed, true)
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
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), `"full"`) {
		t.Fatalf("Meta leaks content: %s", raw)
	}
	// Full is where the content goes, and only there.
	withFull := m
	withFull.Full = a.FullMeta(time.Now(), FullTailBudget)
	raw2, _ := json.Marshal(withFull)
	for _, want := range []string{"SECRET-reply", "SECRET-title", "SECRET-command", "SECRET-cwd-dir"} {
		if !strings.Contains(string(raw2), want) {
			t.Errorf("FullMeta lacks %s: %s", want, raw2)
		}
	}
	if back := FromMeta(withFull, "devbox", time.Now(), cost.NewTable(nil)); back.Title != "SECRET-title" || back.Session.CWD != "" || !strings.Contains(back.RemoteCWD, "SECRET-cwd-dir") || len(back.Tail) == 0 {
		t.Errorf("FromMeta with Full = title %q cwd %q remote cwd %q tail %d", back.Title, back.Session.CWD, back.RemoteCWD, len(back.Tail))
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
	// Without a host a name means this machine; an agent elsewhere needs
	// its host (names.Resolve).
	if a, err := Lookup(agents, "mossy"); err != nil || a.Session.SessionID != "aaaa" {
		t.Errorf("bare name = %v %v", a.Session.SessionID, err)
	}
	if a, err := Lookup(agents, "mossy@devbox"); err != nil || a.Session.SessionID != "bbbb" {
		t.Errorf("mossy@devbox = %v %v", a.Session.SessionID, err)
	}
	if a, err := Lookup(agents, "@Mossy Lemur@local"); err != nil || a.Session.SessionID != "aaaa" {
		t.Errorf("mossy@local = %v %v", a.Session.SessionID, err)
	}
	if _, err := Lookup(agents, "tiny"); err == nil || !strings.Contains(err.Error(), "Tiny Owl@devbox") {
		t.Errorf("a remote agent without its host: %v", err)
	}
	if a, ok := Find(agents, "tiny@devbox"); !ok || a.Host != "devbox" {
		t.Errorf("tiny@devbox = %+v", a)
	}
	if _, err := Lookup(agents, "tiny@gpu"); err == nil {
		t.Error("an unknown host matched")
	}
	// A remote agent has no pid; "0" must not match it.
	if _, ok := Find(agents, "0"); ok {
		t.Error("pid 0 matched")
	}
}

// A host cannot choose the critter name Den shows and mentions: it is
// computed from the session id here, as the badge does on the host.
func TestFromMetaIgnoresTheHostsName(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	a := FromMeta(Meta{Session: id, Name: "Mossy Lemur fix the login", Peer: "worker", Status: "idle"}, "devbox", time.Now(), cost.NewTable(nil))
	if want := badge.Name(id); a.Name != want || a.Session.Name != "worker" {
		t.Errorf("name %q (want %q), peer %q", a.Name, want, a.Session.Name)
	}
}

func TestWireLenMatchesEncodingJSON(t *testing.T) {
	for _, s := range []string{"", "plain", `"\` + "\n\t\r", "<a & b>", "\x01\x1f", "\u2028\u2029", "é漢字", strings.Repeat("\u2028", 333)} {
		raw, _ := json.Marshal(s)
		if got := WireLen(s); got != len(raw) {
			t.Errorf("WireLen(%q) = %d, json %d", s, got, len(raw))
		}
	}
}

// Two agents on this machine with one critter name: the name is neither,
// and the error names both with their peer handles.
func TestLookupRefusesASharedName(t *testing.T) {
	mk := func(id, peer string) Agent {
		return Agent{Session: discover.Session{SessionID: id, Name: peer}, Name: "Zippy Wombat"}
	}
	agents := []Agent{mk("aaaa", "drey-lead"), mk("bbbb", "demo-a")}
	if _, err := Lookup(agents, "zippy wombat"); err == nil || !strings.Contains(err.Error(), "drey-lead") || !strings.Contains(err.Error(), "demo-a") {
		t.Errorf("shared name: %v", err)
	}
	if _, err := Lookup(agents, "zippy"); err == nil {
		t.Error("a shared prefix picked one")
	}
	if a, err := Lookup(agents, "bbbb"); err != nil || a.Session.Name != "demo-a" {
		t.Errorf("by session id: %+v %v", a, err)
	}
}
