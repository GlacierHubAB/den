package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// With no Den running, den remote install never says it is asking for a
// ^Y: it prints the plan, says nothing changed, and reports that den is
// still to be installed (an exit 1 for den remote install).
func TestAskInstallWithoutDen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEN_SOCKET", filepath.Join(dir, "den.sock"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	uname := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if runtime.GOOS != "linux" || uname == "" {
		t.Skip("the fake host is linux on amd64 or arm64")
	}
	ssh := filepath.Join(dir, "ssh")
	os.WriteFile(ssh, []byte("#!/bin/sh\nprintf 'uname=Linux "+uname+"\\nden_path=\\nclaude_path=/usr/bin/claude\\nhooks=no\\nruntime_dir=/run/user/1000\\n'\n"), 0o755)
	t.Setenv("DEN_SSH", ssh)
	var ok bool
	out := captureStdout(t, func() { ok = askInstall(remote.Host{Label: "devbox", Target: "devbox"}, "den remote install") })
	if ok || strings.Contains(out, "^Y") || strings.Contains(out, "Asking Den") || !strings.Contains(out, "nothing was changed") {
		t.Errorf("ok %v, printed %q", ok, out)
	}
}

// den remote list says a host's state in words and puts a long reason
// under the table, so the columns stay in line.
func TestRemoteListStateInWords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEN_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("XDG_STATE_HOME", dir)
	os.WriteFile(filepath.Join(dir, "remotes.toml"), []byte("[hosts.devbox]\ntarget = \"devbox\"\npolicy = \"approve\"\n\n[hosts.gpu]\ntarget = \"gpu\"\n"), 0o600)
	reason := remote.ElsewhereNote("devbox") + ", since it connected from another terminal of this machine"
	os.MkdirAll(remote.StateDir(), 0o700)
	known, _ := json.Marshal(remote.Known{Label: "devbox", State: remote.AttachedElsewhere, Reason: reason})
	os.WriteFile(filepath.Join(remote.StateDir(), "devbox.json"), known, 0o600)

	out := captureStdout(t, func() { remoteList(false) })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 || strings.Contains(out, "attached_elsewhere") || strings.Contains(lines[1], reason) {
		t.Fatalf("list:\n%s", out)
	}
	if !strings.Contains(lines[1], "live, another Den controls it") || !strings.Contains(lines[2], "never connected") || lines[4] != "devbox: "+reason {
		t.Errorf("list:\n%s", out)
	}
	if col := strings.Index(lines[0], "AGENTS"); len(lines[1]) <= col || lines[1][col] != '0' {
		t.Errorf("columns:\n%s", out)
	}
}
