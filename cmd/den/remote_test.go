package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/remote"
)

// den remote install only asks: the request goes to den.sock with the
// host's label, and what Den answers is printed.
func TestAskInstallGoesThroughDen(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "den.sock")
	t.Setenv("DEN_SOCKET", sock)
	srv, err := bridge.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	got := make(chan bridge.InstallInput, 1)
	go func() {
		r := <-srv.Requests()
		var in bridge.InstallInput
		json.Unmarshal(r.Payload, &in)
		if r.Event == bridge.InstallEvent && r.WantsReply() {
			got <- in
		}
		r.Reply(bridge.Decision{Behavior: "allow", Message: "installed: den v0.2.0"})
	}()
	out := captureStdout(t, func() { askInstall(remote.Host{Label: "devbox", Target: "devbox"}, "den remote install") })
	in := <-got
	if in.Host != "devbox" || !strings.Contains(in.AskedBy, "den remote install, pid") {
		t.Errorf("request = %+v", in)
	}
	if !strings.Contains(out, "installed: den v0.2.0") {
		t.Errorf("printed %q", out)
	}
}

// den remote spawn only asks: the brief goes from stdin to den.sock, never
// on a command line, and what Den answers is printed.
func TestRemoteSpawnGoesThroughDenWithTheBriefFromStdin(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "den.sock")
	t.Setenv("DEN_SOCKET", sock)
	t.Setenv("DEN_CONFIG", filepath.Join(dir, "config.toml"))
	os.WriteFile(filepath.Join(dir, "remotes.toml"), []byte("[hosts.devbox]\ntarget = \"devbox\"\npolicy = \"approve\"\nspawn = \"tmux\"\nspawn_dirs = [\"~/src\"]\n"), 0o644)
	srv, err := bridge.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	got := make(chan bridge.SpawnInput, 1)
	go func() {
		r := <-srv.Requests()
		var in bridge.SpawnInput
		json.Unmarshal(r.Payload, &in)
		if r.Event == bridge.SpawnEvent && r.WantsReply() {
			got <- in
		}
		r.Reply(bridge.Decision{Behavior: "allow", Message: "started Snug Panda on devbox"})
	}()
	pr, pw, _ := os.Pipe()
	pw.WriteString("Fix the login.\nThen the tests.\n")
	pw.Close()
	old := os.Stdin
	os.Stdin = pr
	defer func() { os.Stdin = old }()
	out := captureStdout(t, func() { remoteSpawn([]string{"devbox", "~/src/api", "--model", "sonnet"}) })
	in := <-got
	if in.Host != "devbox" || in.Dir != "~/src/api" || in.Model != "sonnet" || in.Brief != "Fix the login.\nThen the tests." || !strings.Contains(in.AskedBy, "den remote spawn, pid") {
		t.Errorf("request = %+v", in)
	}
	if !strings.Contains(out, "started Snug Panda on devbox") {
		t.Errorf("printed %q", out)
	}
}
