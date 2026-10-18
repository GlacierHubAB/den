// Package metrics keeps the figures Den reads about how missions go: what
// they cost, what they landed, how long their branches lived. Den alone
// writes them, from its own reads of git, the fleet and the transcripts,
// never from what an agent says. They are advisory: the branches they are
// read from can be moved by the agents.
package metrics

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Kinds of record.
const (
	Ended = "ended" // a mission ended
	Daily = "daily" // a live mission, once a day
)

// Record is one mission's figures at one moment.
type Record struct {
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	Day     string    `json:"day"` // the local date, 2006-01-02
	Mission string    `json:"mission"`
	Name    string    `json:"name"`
	Project string    `json:"project,omitempty"`
	State   string    `json:"state"`
	Runs    int       `json:"runs"`
	Tokens  int64     `json:"tokens"`
	USD     float64   `json:"usd"`
	Partial bool      `json:"partial,omitempty"` // a model with no known price
	Merged  int       `json:"merged"`
	// BranchHours are the median and longest lifetimes of the merged
	// branches, from their oldest commit of their own to when Den saw them
	// land; 0 with none.
	BranchHoursMedian float64 `json:"branch_hours_median"`
	BranchHoursMax    float64 `json:"branch_hours_max"`
	Overlaps          int     `json:"overlaps"` // overlap pairs Den told
	Nudges            int     `json:"nudges"`   // branch nudges Den sent
}

// MaxBytes is the size at which the file is rotated, one old file kept.
const MaxBytes = 1 << 20

// Path is metrics.jsonl in Den's state folder ($XDG_STATE_HOME/den).
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "metrics.jsonl")
}

// Append adds records to the file at path, which only Den reads and
// writes (folder 0700, file 0600), first moving it to path.1 once it has
// reached MaxBytes.
func Append(path string, rs ...Record) error {
	if len(rs) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return errors.New("metrics: not a regular file")
		}
		if fi.Size() >= MaxBytes {
			if err := os.Rename(path, path+".1"); err != nil {
				return err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range rs {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		w.Write(append(line, '\n'))
	}
	return w.Flush()
}

// LastDay is the latest day a Daily record in the file at path has, ""
// for none or no file.
func LastDay(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Kind == Daily && r.Day > last {
			last = r.Day
		}
	}
	return last
}
