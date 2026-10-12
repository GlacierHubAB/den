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

// DefaultModel is the model a session runs outside plan mode, "" when the
// transcript does not say. Claude Code 2.1.289 may run plan mode on
// another model: entering it writes a model attachment for the plan model
// and then a plan_mode attachment; leaving it writes the default model's
// attachment and then plan_mode_exit. So when plan_mode is the newest
// marker, the default model is the first model record before the plan
// model's attachment; otherwise it is the model in use. A session that
// started in plan mode has no record of its default model. It is for
// restoring a session, so it reads further back than Read does.
func DefaultModel(path string) string {
	if path == "" {
		return ""
	}
	var recs []modelRec // newest first
	model, known := "", false
	scanBackwards(path, 16<<20, func(line []byte) bool {
		if mode := planMarker(line); mode != "" {
			recs = append(recs, modelRec{mode: mode})
		} else if m := lineModel(line); m != "" {
			recs = append(recs, modelRec{model: m, attach: bytes.Contains(line, []byte(`"type":"attachment"`))})
		}
		model, known = defaultFrom(recs)
		return known
	})
	if known {
		return model
	}
	// No plan mode in what was read: the model in use is the default one.
	// With a plan mode marker that did not settle it, it is not recorded.
	for _, r := range recs {
		if r.mode != "" {
			return ""
		}
	}
	return firstModel(recs)
}

// modelRec is a transcript record that bears on the model: a model in use
// (from a model attachment, or a real reply), or a plan mode marker.
type modelRec struct {
	model  string
	attach bool
	mode   string // "plan_mode" or "plan_mode_exit"
}

// defaultFrom is the default model from recs, newest first, and whether
// they settle it.
func defaultFrom(recs []modelRec) (string, bool) {
	for i, r := range recs {
		switch r.mode {
		case "plan_mode_exit":
			if m := firstModel(recs[:i]); m != "" {
				return m, true
			}
			// Left plan mode with no reply since: the attachment that came
			// with the exit, just before it.
			if i+1 < len(recs) && recs[i+1].model != "" {
				return recs[i+1].model, true
			}
			return "", false
		case "plan_mode":
			rest := recs[i+1:]
			if len(rest) > 0 && rest[0].attach {
				rest = rest[1:] // the plan model's own attachment
			}
			if m := firstModel(rest); m != "" {
				return m, true
			}
			return "", false
		}
	}
	return "", false
}

// firstModel is the first model in recs, "" when there is none.
func firstModel(recs []modelRec) string {
	for _, r := range recs {
		if r.model != "" {
			return r.model
		}
	}
	return ""
}

// planMarker is "plan_mode" or "plan_mode_exit" for a line that is that
// attachment, "" otherwise.
func planMarker(line []byte) string {
	if !bytes.Contains(line, []byte(`"type":"attachment"`)) || !bytes.Contains(line, []byte(`"plan_mode`)) {
		return ""
	}
	var a struct {
		Type       string `json:"type"`
		Attachment struct {
			Type string `json:"type"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &a) != nil || a.Type != "attachment" {
		return ""
	}
	if a.Attachment.Type == "plan_mode" || a.Attachment.Type == "plan_mode_exit" {
		return a.Attachment.Type
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
