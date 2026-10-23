package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/events"
)

func TestStatsReadsDensStateFolder(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	p := statsPaths()
	for name, got := range map[string]string{"events.jsonl": p.Events, "hooks.log": p.Hooks, "orchestrator.jsonl": p.Chat, "missions.jsonl": p.Missions, "metrics.jsonl": p.Metrics} {
		if want := filepath.Join(state, "den", name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}

	from := time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC)
	for _, ev := range []events.Event{
		{V: 1, Time: from.Add(time.Minute), Kind: events.TurnStart, Session: "s-1"},
		{V: 1, Time: from.Add(2 * time.Minute), Kind: events.Done, Session: "s-1"},
	} {
		if err := events.Append(p.Events, ev); err != nil {
			t.Fatal(err)
		}
	}
	var text bytes.Buffer
	if err := writeStats(&text, p, from, from.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 finished, by 1 agent\n", "working time          60 s, over 1 turn with", "merged pull requests  not recorded"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, text.String())
		}
	}
	var out bytes.Buffer
	if err := writeStats(&out, p, from, from.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Turns    struct{ Finished, Measured int }
		Messages *struct{}
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if got.Turns.Finished != 1 || got.Turns.Measured != 1 || got.Messages != nil {
		t.Errorf("json: %+v\n%s", got, out.String())
	}
}
