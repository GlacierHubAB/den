package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
)

// A change to a watched file right after serve reads its first snapshot
// (a badge rewritten as Den goes live) brings a new snapshot at once, not
// at the next periodic one: the watch starts before that first read. The
// first read writes the change itself, so the order is exact, not timed.
func TestServeWatchesBeforeItsFirstSnapshot(t *testing.T) {
	dir := t.TempDir()
	var reads atomic.Int32
	o := ServeOptions{
		Version:       "v0.2.0",
		EventsPath:    filepath.Join(t.TempDir(), "events.jsonl"),
		Watch:         []string{dir},
		Debounce:      20 * time.Millisecond,
		SnapshotEvery: time.Hour, // only a change can bring a second one
		PollEvents:    time.Hour,
		Orphaned:      func() bool { return false },
		Snapshot: func() ([]fleet.Agent, error) {
			if reads.Add(1) == 1 {
				os.WriteFile(filepath.Join(dir, "badge.json"), []byte(`{"role":"coder"}`), 0o644)
			}
			return nil, nil
		},
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, inR, outW, o) }()
	t.Cleanup(func() {
		cancel()
		inW.Close()
		outR.Close()
		<-done
	})
	req, _ := json.Marshal(Request{Type: TypeHello, Proto: Proto, Policy: Observe})
	go inW.Write(append(req, '\n'))

	types := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, MaxLine), MaxLine)
		for sc.Scan() {
			var m struct {
				Type string `json:"type"`
			}
			json.Unmarshal(sc.Bytes(), &m)
			types <- m.Type
		}
		close(types)
	}()
	snapshots := 0
	deadline := time.After(5 * time.Second)
	for snapshots < 2 {
		select {
		case typ, ok := <-types:
			if !ok {
				t.Fatalf("serve ended after %d snapshots", snapshots)
			}
			if typ == TypeSnapshot {
				snapshots++
			}
		case <-deadline:
			t.Fatalf("%d snapshot(s) in 5 s: the change after the first read was missed", snapshots)
		}
	}
}
