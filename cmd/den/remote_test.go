package main

import (
	"encoding/json"
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
