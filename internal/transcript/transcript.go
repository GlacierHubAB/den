// Package transcript reads the few facts Den needs from a session's JSONL
// transcript without parsing the whole file. The format is internal to
// Claude Code and changes between releases, so everything here is best
// effort and returns "" when unsure.
package transcript

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// ProjectsDir is where Claude Code keeps per-project transcripts.
func ProjectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// Path finds the transcript for a session id, or "" when none exists.
func Path(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(ProjectsDir(), "*", sessionID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// Facts is what Den reads from the end of a transcript.
type Facts struct {
	Title string // Claude Code's AI-generated title, also the terminal title
	Model string // raw model id in use: the newest model attachment or real assistant message
}

var modelRe = regexp.MustCompile(`"model":"([^"]+)"`)

// Read scans the transcript backwards and returns the latest title and model.
func Read(path string) Facts {
	var f Facts
	if path == "" {
		return f
	}
	err := scanBackwards(path, 2<<20, func(line []byte) bool {
		if f.Title == "" && bytes.Contains(line, []byte(`"type":"ai-title"`)) {
			var rec struct {
				Title string `json:"aiTitle"`
			}
			if json.Unmarshal(line, &rec) == nil {
				f.Title = sanitize.Line(rec.Title)
			}
		}
		if f.Model == "" {
			f.Model = lineModel(line)
		}
		return f.Title != "" && f.Model != ""
	})
	if err != nil {
		return Facts{}
	}
	return f
}

// lineModel is the model a transcript line says is in use, "" when it
// says none. Claude Code records the model it switches to (at start, and
// in and out of plan mode, which may run another model) as a "model"
// attachment before the next reply, and each reply carries its model. A
// record Claude Code makes up itself, for an API error or when logged out
// ("<synthetic>"), names no model.
func lineModel(line []byte) string {
	switch {
	case bytes.Contains(line, []byte(`"type":"assistant"`)):
		if bytes.Contains(line, []byte(`"isApiErrorMessage":true`)) {
			return ""
		}
		if m := modelRe.FindSubmatch(line); m != nil && string(m[1]) != "<synthetic>" {
			return sanitize.Line(string(m[1]))
		}
	case bytes.Contains(line, []byte(`"type":"attachment"`)) && bytes.Contains(line, []byte(`"modelId"`)):
		var rec struct {
			Type       string `json:"type"`
			Attachment struct {
				Type     string `json:"type"`
				Identity struct {
					ModelID string `json:"modelId"`
				} `json:"identity"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Type == "attachment" && rec.Attachment.Type == "model" {
			return sanitize.Line(rec.Attachment.Identity.ModelID)
		}
	}
	return ""
}

// scanBackwards feeds complete lines from the end of the file to fn until fn
// returns true, the file is exhausted, or limit bytes have been read.
func scanBackwards(path string, limit int64, fn func(line []byte) bool) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	const chunk = 64 << 10
	end := st.Size()
	read := int64(0)
	var carry []byte
	for end > 0 && read < limit {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, end-start)
		if _, err := fh.ReadAt(buf, start); err != nil && err != io.EOF {
			return err
		}
		read += int64(len(buf))
		buf = append(buf, carry...)
		lines := bytes.Split(buf, []byte{'\n'})
		// lines[0] may be a partial line; keep it for the next chunk unless
		// we are at the start of the file.
		first := 0
		if start > 0 {
			carry = append([]byte{}, lines[0]...)
			first = 1
		} else {
			carry = nil
		}
		for i := len(lines) - 1; i >= first; i-- {
			if len(lines[i]) == 0 {
				continue
			}
			if fn(lines[i]) {
				return nil
			}
		}
		end = start
	}
	return nil
}

// PrettyModel turns "claude-fable-5-1" into "Fable 5.1".
func PrettyModel(id string) string {
	if id == "" {
		return ""
	}
	parts := strings.Split(id, "-")
	if len(parts) >= 3 && parts[0] == "claude" {
		name := strings.ToUpper(parts[1][:1]) + parts[1][1:]
		ver := strings.Join(parts[2:], ".")
		// drop date suffixes like 20251001
		if i := strings.Index(ver, ".2025"); i > 0 {
			ver = ver[:i]
		}
		if i := strings.Index(ver, ".2026"); i > 0 {
			ver = ver[:i]
		}
		return name + " " + ver
	}
	return id
}
