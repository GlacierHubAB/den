package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Tokens is a count of tokens by what Claude Code bills them as.
type Tokens struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
}

// Total is every token counted, cached or not.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheCreation + t.CacheRead }

// Add returns the sum of two counts.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.Output + o.Output, t.CacheCreation + o.CacheCreation, t.CacheRead + o.CacheRead}
}

func (t Tokens) max(o Tokens) Tokens {
	return Tokens{max(t.Input, o.Input), max(t.Output, o.Output), max(t.CacheCreation, o.CacheCreation), max(t.CacheRead, o.CacheRead)}
}

// Spend is one assistant message's usage.
type Spend struct {
	At    time.Time
	Model string
	Tokens
}

// Reported is what Claude Code wrote about its own cost into the transcript
// (the last "cost-state" line). It lags the messages, so Den only shows it
// next to its own figure.
type Reported struct {
	USD     float64
	Unknown bool // Claude Code could not price a model
}

// Account is a session's usage: its own messages and its subagents', by
// model.
type Account struct {
	Main     map[string]Tokens
	Sub      map[string]Tokens
	Reported *Reported
}

// Ledger reads usage from transcripts incrementally: it remembers how far
// into each file it has read, so a refresh only parses what was appended.
// The format is internal to Claude Code; whatever does not parse is skipped.
type Ledger struct {
	mu    sync.Mutex
	files map[string]*ledgerFile
}

// Shared is the ledger the rest of Den uses.
var Shared = &Ledger{}

type ledgerFile struct {
	offset   int64
	index    map[string]int // message id -> position in spends
	spends   []Spend
	byModel  map[string]Tokens
	reported *Reported
}

func newLedgerFile() *ledgerFile {
	return &ledgerFile{index: map[string]int{}, byModel: map[string]Tokens{}}
}

// Account returns the usage of the session whose transcript is at path,
// reading only what was appended since the last call.
func (l *Ledger) Account(path string) Account {
	a := Account{Main: map[string]Tokens{}, Sub: map[string]Tokens{}}
	if path == "" {
		return a
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	main := l.refresh(path)
	for k, v := range main.byModel {
		a.Main[k] = v
	}
	a.Reported = main.reported
	for _, p := range subagentFiles(path) {
		for k, v := range l.refresh(p).byModel {
			a.Sub[k] = a.Sub[k].Add(v)
		}
	}
	return a
}

// Window sums the usage of the session and its subagents between from and
// to, by model; a zero bound is open.
func (l *Ledger) Window(path string, from, to time.Time) map[string]Tokens {
	out := map[string]Tokens{}
	if path == "" {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range append([]string{path}, subagentFiles(path)...) {
		for _, s := range l.refresh(p).spends {
			if (!from.IsZero() && s.At.Before(from)) || (!to.IsZero() && s.At.After(to)) {
				continue
			}
			out[s.Model] = out[s.Model].Add(s.Tokens)
		}
	}
	return out
}

// subagentFiles lists the transcripts of a session's subagents, which Claude
// Code keeps beside the session's own file.
func subagentFiles(path string) []string {
	m, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "*.jsonl"))
	return m
}

// refresh reads the new complete lines of one file. The caller holds mu.
func (l *Ledger) refresh(path string) *ledgerFile {
	if l.files == nil {
		l.files = map[string]*ledgerFile{}
	}
	f := l.files[path]
	if f == nil {
		f = newLedgerFile()
		l.files[path] = f
	}
	fh, err := os.Open(path)
	if err != nil {
		return f
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return f
	}
	if st.Size() < f.offset {
		// Rewritten or replaced: start over.
		*f = *newLedgerFile()
	}
	if st.Size() == f.offset {
		return f
	}
	br := bufio.NewReaderSize(io.NewSectionReader(fh, f.offset, st.Size()-f.offset), 256<<10)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			// A last line without its newline is still being written.
			break
		}
		f.offset += int64(len(line))
		f.take(line)
	}
	return f
}

// take reads one transcript line.
func (f *ledgerFile) take(line []byte) {
	switch {
	case bytes.Contains(line, []byte(`"type":"assistant"`)) && bytes.Contains(line, []byte(`"usage"`)):
		var rec struct {
			Timestamp string `json:"timestamp"`
			RequestID string `json:"requestId"`
			Message   struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					Input         int64 `json:"input_tokens"`
					Output        int64 `json:"output_tokens"`
					CacheCreation int64 `json:"cache_creation_input_tokens"`
					CacheRead     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil || strings.HasPrefix(rec.Message.Model, "<") {
			return
		}
		tk := Tokens{rec.Message.Usage.Input, rec.Message.Usage.Output, rec.Message.Usage.CacheCreation, rec.Message.Usage.CacheRead}
		// Claude Code writes one line per content block, so a message with
		// several blocks repeats its usage; count each message once.
		id := rec.Message.ID
		if id == "" {
			id = rec.RequestID
		}
		if i, seen := f.index[id]; id != "" && seen {
			old := f.spends[i]
			if merged := old.Tokens.max(tk); merged != old.Tokens {
				f.byModel[old.Model] = subTokens(f.byModel[old.Model], old.Tokens).Add(merged)
				f.spends[i].Tokens = merged
			}
			return
		}
		at, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if id != "" {
			f.index[id] = len(f.spends)
		}
		f.spends = append(f.spends, Spend{At: at, Model: rec.Message.Model, Tokens: tk})
		f.byModel[rec.Message.Model] = f.byModel[rec.Message.Model].Add(tk)
	case bytes.Contains(line, []byte(`"type":"cost-state"`)):
		var rec struct {
			Total   float64 `json:"totalCostUSD"`
			Unknown bool    `json:"hasUnknownModelCost"`
		}
		if json.Unmarshal(line, &rec) == nil {
			f.reported = &Reported{USD: rec.Total, Unknown: rec.Unknown}
		}
	}
}

func subTokens(a, b Tokens) Tokens {
	return Tokens{a.Input - b.Input, a.Output - b.Output, a.CacheCreation - b.CacheCreation, a.CacheRead - b.CacheRead}
}
