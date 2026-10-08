package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// TimedChange is a file change together with when it landed and the tool
// that made it.
type TimedChange struct {
	At   time.Time
	Tool string
	FileChange
}

// changeLog is what Changes remembers about one transcript between calls:
// how far it has read, the calls still waiting for their result, and every
// change found so far.
type changeLog struct {
	offset  int64
	modTime time.Time
	pending map[string]string // tool_use id -> tool name
	changes []TimedChange
}

var (
	changeMu   sync.Mutex
	changeLogs = map[string]*changeLog{}
)

// Changes returns every file change in the transcript at path made at or
// after since (a zero since means all), oldest first. Unlike Tail it reads
// the whole file, once: later calls only read what was appended, so it is
// cheap to call on every refresh.
func Changes(path string, since time.Time) []TimedChange {
	changeMu.Lock()
	defer changeMu.Unlock()

	st, err := os.Stat(path)
	if err != nil {
		delete(changeLogs, path)
		return nil
	}
	cl := changeLogs[path]
	if cl == nil || st.Size() < cl.offset {
		// New, or truncated and rewritten: start over.
		cl = &changeLog{pending: map[string]string{}}
		changeLogs[path] = cl
	}
	if st.Size() > cl.offset || !st.ModTime().Equal(cl.modTime) {
		cl.read(path)
		cl.modTime = st.ModTime()
	}

	var out []TimedChange
	for _, c := range cl.changes {
		if since.IsZero() || c.At.IsZero() || !c.At.Before(since) {
			out = append(out, c)
		}
	}
	return out
}

// read consumes the complete lines after the saved offset. A partial last
// line is left for the next call, when the writer has finished it.
func (cl *changeLog) read(path string) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(cl.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(fh, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // EOF, possibly mid-line: leave the offset before it
		}
		cl.offset += int64(len(line))
		cl.consume(line)
	}
}

// consume handles one transcript line. Calls come before their results, so
// a tool_use is remembered by id until the result that carries the patch.
func (cl *changeLog) consume(line []byte) {
	var rec struct {
		Type      string `json:"type"`
		IsMeta    bool   `json:"isMeta"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.IsMeta || len(rec.Message.Content) == 0 || rec.Message.Content[0] != '[' {
		return
	}
	var blocks []struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		ToolUseID string `json:"tool_use_id"`
	}
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return
	}
	switch rec.Type {
	case "assistant":
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" {
				cl.pending[b.ID] = b.Name
			}
		}
	case "user":
		at, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			name, ok := cl.pending[b.ToolUseID]
			if !ok {
				continue
			}
			delete(cl.pending, b.ToolUseID)
			var r toolResult
			if len(rec.ToolUseResult) == 0 || json.Unmarshal(rec.ToolUseResult, &r) != nil {
				continue
			}
			for _, c := range r.changes(name) {
				cl.changes = append(cl.changes, TimedChange{At: at, Tool: name, FileChange: c})
			}
		}
	}
}
