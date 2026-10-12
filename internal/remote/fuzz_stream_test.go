package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// Whole-stream fuzz targets: the bytes a host's serve could write (Den's
// side), and the bytes a Den could write to serve (serve's side), through
// the real Manager and the real Serve. They run in a scratch directory with
// stub sessions, no ssh and no network; nothing reads the real home.

// hostStream is one fuzz input as a host's stdout: its hello, then data.
const hostHello = `{"type":"hello","proto":1,"den":"v0.4.0","claude":"2.1.289","hostname":"devbox","remote_now":"2026-10-12T12:00:00Z","policy":"full","labels":true}` + "\n"

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func runHostStream(t *testing.T, data []byte, policy Policy) {
	t.Helper()
	dir := t.TempDir()
	out := bytes.NewReader(data)
	var dialed atomic.Bool
	dial := func(ctx context.Context, h Host) (*Conn, error) {
		if dialed.Swap(true) {
			return nil, errors.New("one session per input")
		}
		return &Conn{In: nopWriteCloser{io.Discard}, Out: out, Wait: func() error { return nil }, Kill: func() {}}, nil
	}
	h := Host{Label: "devbox", Target: "devbox", Policy: policy, Labels: true, Enabled: true}
	m := NewManager(Config{Hosts: []Host{h}}, Options{
		Version: "v0.4.0", StateDir: dir, EventsPath: filepath.Join(dir, "events.jsonl"), Controller: "c", Dial: dial,
		Backoff: time.Hour, MaxBackoff: time.Hour,
	})
	m.Start()
	go func() {
		for range m.Updates() {
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.Hosts(); len(st) == 1 && st[0].State != Connecting && st[0].State != Live {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	hosts, agents := m.Hosts(), m.Agents()
	m.Close()
	checkClean(t, "Hosts", hosts, false)
	checkClean(t, "Agents", agents, true)
	for _, a := range agents {
		if a.Host != "devbox" || !isSessionID(a.Session.SessionID) {
			t.Fatalf("agent %q on host %q", a.Session.SessionID, a.Host)
		}
	}
	for _, st := range hosts {
		if st.Label != "devbox" {
			t.Fatalf("host label %q: it must come from remotes.toml", st.Label)
		}
		if st.Applied.rank() > st.Policy.rank() {
			t.Fatalf("applied %s above configured %s", st.Applied, st.Policy)
		}
	}
	// The scratch directory is all that was written.
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil {
		raw, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var ev map[string]any
			if json.Unmarshal(line, &ev) != nil {
				t.Fatalf("a line in the event log that is not JSON: %q", line)
			}
			if ev["host"] != "devbox" {
				t.Fatalf("an event logged for host %v", ev["host"])
			}
			checkClean(t, "event log", ev, false)
		}
	}
}

func addStreamSeeds(f *testing.F) {
	for _, s := range append(append([]string(nil), seedLines...), edgeLines()...) {
		f.Add([]byte(s+"\n"), uint8(1))
		f.Add([]byte(s+"\n"+s+"\n"), uint8(0))
	}
	f.Add([]byte(strings.Repeat("x", MaxLine+10)+"\n"), uint8(1))
	f.Add([]byte(seedLines[2]+"\n"+seedLines[4]+"\n"+seedLines[6]+"\n"+seedLines[10]+"\n"), uint8(1))
}

// What a host writes after its hello, under each policy Den may have set:
// nothing panics, the host stays labelled as remotes.toml says, and every
// string Den keeps or shows (status, flags, agents, the event log) is clean.
func FuzzHostStream(f *testing.F) {
	addStreamSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte, pol uint8) {
		policy := []Policy{Observe, Approve, Full}[int(pol)%3]
		runHostStream(t, append([]byte(hostHello), data...), policy)
	})
}

// The same, with a host that sends no hello first.
func FuzzHostStreamWithoutHello(f *testing.F) {
	addStreamSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte, pol uint8) {
		runHostStream(t, data, []Policy{Observe, Approve, Full}[int(pol)%3])
	})
}

// What a Den writes to serve after its request: serve exits at EOF without
// panicking, answers with one JSON object per line, each of a type serve
// sends, with only the keys that type allows and within MaxLine; and it
// never acts on a line that is not in its closed set (no session of the
// stub fleet is touched).
func FuzzServeFromDen(f *testing.F) {
	for _, s := range append(append([]string(nil), seedLines...), edgeLines()...) {
		f.Add([]byte(s + "\n"))
	}
	f.Add([]byte(seedLines[16] + "\n" + seedLines[17] + "\n" + seedLines[18] + "\n"))
	f.Add([]byte(strings.Repeat("y", MaxLine+10) + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		yes := false
		in := `{"type":"hello","proto":1,"den":"v0.4.0","policy":"observe","labels":false}` + "\n" + string(data)
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := Serve(ctx, strings.NewReader(in), &out, ServeOptions{
			Version: "v0.4.0", EventsPath: filepath.Join(dir, "events.jsonl"), Watch: []string{dir},
			Snapshot: func() ([]fleet.Agent, error) { return nil, nil },
			Debounce: time.Millisecond, SnapshotEvery: time.Hour, PollEvents: 20 * time.Millisecond, EventPace: 0,
			RequestWait: time.Second, Silence: time.Second, Orphaned: func() bool { return false },
			Questions: &yes, Log: func(string) {}, CloseSelf: closer.Self{PID: os.Getpid(), IsAncestor: func(int) bool { return false }},
			Socket: filepath.Join(dir, "r.sock"),
		})
		_ = err
		lr := newLineReader(&out, MaxLine)
		n := 0
		for {
			line, rerr := lr.next()
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					t.Fatalf("serve wrote a bad line: %v", rerr)
				}
				break
			}
			n++
			typ, unknown, perr := peek(line)
			if perr != nil || len(unknown) > 0 || allowed[typ] == nil {
				t.Fatalf("serve wrote %q: type %q unknown %v err %v", line, typ, unknown, perr)
			}
			if n == 1 && typ != TypeHello && typ != TypeError {
				t.Fatalf("serve's first message is a %s", typ)
			}
		}
	})
}
