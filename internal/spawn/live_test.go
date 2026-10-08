//go:build live

package spawn

// A real spawn on this machine: opens a terminal with a Claude Code session
// in it, on the workspace you are looking at. Run by hand:
//
//	go test -tags live -run TestLiveSpawn -v ./internal/spawn
//
// It costs one short Claude turn and leaves the session open for you to close.
// The agent starts in the repository (DEN_LIVE_DIR overrides), which Claude
// Code already trusts; an untrusted folder would stop at the trust dialog.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveSpawn(t *testing.T) {
	dir := os.Getenv("DEN_LIVE_DIR")
	if dir == "" {
		wd, _ := os.Getwd()
		dir = filepath.Dir(filepath.Dir(wd)) // internal/spawn → the module root
	}
	if !Trusted(dir) {
		t.Logf("%s is not trusted by Claude Code; the agent will stop at the trust dialog", dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, Request{Dir: dir, Agent: true, Brief: "This is a test of Den's spawn feature. Reply with one line saying hello and which directory you are in, then stop. Do not report back.", Orchestrator: "den-orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
	t.Log(res.Describe())
	if res.Window == "" || (!res.Registered && !res.Untrusted) {
		t.Errorf("expected a window and a registered session: %+v", res)
	}
}
