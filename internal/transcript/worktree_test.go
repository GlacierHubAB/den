package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadWorktree(t *testing.T) {
	dir := t.TempDir()
	with := filepath.Join(dir, "with.jsonl")
	os.WriteFile(with, []byte(`{"type":"worktree-state","worktreeSession":{"originalCwd":"/r","worktreePath":"/r/.claude/worktrees/mossy","worktreeName":"mossy","worktreeBranch":"worktree-mossy","originalBranch":"main","originalHeadCommit":"abc123","sessionId":"s"},"sessionId":"s"}
{"type":"user","message":{"role":"user","content":"hi"},"cwd":"/r/.claude/worktrees/mossy"}
`), 0o644)
	got, ok := ReadWorktree(with)
	if !ok || got.Path != "/r/.claude/worktrees/mossy" || got.Branch != "worktree-mossy" || got.OriginalHead != "abc123" || got.OriginalCWD != "/r" || got.OriginalBranch != "main" {
		t.Errorf("got %+v ok=%v", got, ok)
	}
	without := filepath.Join(dir, "without.jsonl")
	os.WriteFile(without, []byte(`{"type":"user","message":{"role":"user","content":"worktree-state"}}`+"\n"), 0o644)
	if _, ok := ReadWorktree(without); ok {
		t.Error("found a state in an ordinary transcript")
	}
	if _, ok := ReadWorktree(filepath.Join(dir, "missing.jsonl")); ok {
		t.Error("found a state in a missing file")
	}
}
