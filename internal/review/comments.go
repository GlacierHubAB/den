package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Comment is one review note on a line, or a range of lines, of a file.
type Comment struct {
	ID        string    `json:"id"`
	Root      string    `json:"root"` // repository root
	Path      string    `json:"path"` // relative to Root
	Side      string    `json:"side"` // "new" for added and unchanged lines, "old" for removed ones
	Start     int       `json:"start"`
	End       int       `json:"end"`   // inclusive; equal to Start for one line
	Quote     []string  `json:"quote"` // the lines as they were when commented, each after its +, - or space marker
	Body      string    `json:"body"`
	SessionID string    `json:"sessionId"` // the agent the comment goes to
	Created   time.Time `json:"created"`
	Sent      time.Time `json:"sent,omitzero"` // zero while it is a draft
	// Via is "orchestrator" when the comment went to the orchestrator
	// because its agent's session had ended; "" when it went to the agent.
	Via string `json:"via,omitempty"`
	// Addressed is when Den saw the quoted lines change after sending;
	// zero while the comment is open.
	Addressed time.Time `json:"addressed,omitzero"`
}

// Draft reports whether the comment has not been sent yet.
func (c Comment) Draft() bool { return c.Sent.IsZero() }

// Open reports whether the comment was sent and its lines have not changed
// since.
func (c Comment) Open() bool { return !c.Sent.IsZero() && c.Addressed.IsZero() }

// CommentsPath is where review comments are kept between runs:
// $XDG_STATE_HOME/den/reviews.json, "" when there is no home directory.
func CommentsPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "reviews.json")
}

type commentsFile struct {
	Comments []Comment `json:"comments"`
}

// LoadComments reads saved comments; a missing or unreadable file is an
// empty list, never an error, so a bad file cannot stop Den starting.
func LoadComments(path string) []Comment {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f commentsFile
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f.Comments
}

// SaveComments writes the comments atomically (a temporary file renamed
// over the old one), readable only by you.
func SaveComments(path string, comments []Comment) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(commentsFile{Comments: comments}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reviews-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
