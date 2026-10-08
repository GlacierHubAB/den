//go:build live

package spawn

// A real spawn on this machine: opens a terminal with a Claude Code session
// in it, on the workspace you are looking at. Run by hand:
//
//	go test -tags live -run TestLiveSpawn -v ./internal/spawn
//
// It costs one short Claude turn and leaves the session open for you to close.

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveSpawn(t *testing.T) {
	home, _ := os.UserHomeDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, Request{Dir: home, Agent: true, Brief: "This is a test of Den's spawn feature. Reply with one line saying hello and which directory you are in, then stop. Do not report back.", Orchestrator: "den-orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
	t.Log(res.Describe())
	if res.Window == "" || !res.Registered {
		t.Errorf("expected a window and a registered session: %+v", res)
	}
}
