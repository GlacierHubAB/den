package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
)

// WorktreeState is what Claude Code records at the top of a session that
// runs in a worktree it made (claude --worktree, or EnterWorktree): where
// the worktree is, its branch, and the branch and commit it was made from.
type WorktreeState struct {
	OriginalCWD    string // the repository the session was started in
	Path           string // the worktree
	Name           string
	Branch         string // the worktree's branch
	OriginalBranch string
	OriginalHead   string // the commit the worktree was made from
}

// worktreeScanLimit is how far into the file the record is looked for; it
// is written before the first prompt.
const worktreeScanLimit = 256 << 10

// ReadWorktree returns the worktree state at the top of the transcript, if
// the session has one.
func ReadWorktree(path string) (WorktreeState, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return WorktreeState{}, false
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), worktreeScanLimit)
	for n := 0; sc.Scan() && n < 20; n++ {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"worktree-state"`)) {
			continue
		}
		var rec struct {
			Type    string `json:"type"`
			Session *struct {
				OriginalCWD    string `json:"originalCwd"`
				Path           string `json:"worktreePath"`
				Name           string `json:"worktreeName"`
				Branch         string `json:"worktreeBranch"`
				OriginalBranch string `json:"originalBranch"`
				OriginalHead   string `json:"originalHeadCommit"`
			} `json:"worktreeSession"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "worktree-state" || rec.Session == nil || rec.Session.Path == "" {
			continue
		}
		s := rec.Session
		return WorktreeState{OriginalCWD: s.OriginalCWD, Path: s.Path, Name: s.Name, Branch: s.Branch, OriginalBranch: s.OriginalBranch, OriginalHead: s.OriginalHead}, true
	}
	return WorktreeState{}, false
}
