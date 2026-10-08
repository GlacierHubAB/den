package inbox

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// record is one finished turn on disk.
type record struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session"`
	Role      string    `json:"role,omitempty"`
	Task      string    `json:"task,omitempty"`
	Title     string    `json:"title"`
}

// HistoryPath is where finished turns are appended, one JSON line each.
func HistoryPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "history.jsonl")
}

// AppendHistory writes a finished turn to path. Errors are the caller's to
// ignore: the inbox works without the file.
func AppendHistory(path string, it *Item) error {
	if path == "" || it.Kind != Done {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(record{At: it.At, SessionID: it.SessionID, Role: it.Role, Task: it.Task, Title: it.Title})
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	return err
}

// LoadHistory returns the newest n finished turns from path, oldest first.
func LoadHistory(path string, n int) []*Item {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []*Item
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.SessionID == "" {
			continue
		}
		out = append(out, &Item{At: r.At, SessionID: r.SessionID, Role: r.Role, Task: r.Task, Title: r.Title, Kind: Done, State: Resolved})
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
