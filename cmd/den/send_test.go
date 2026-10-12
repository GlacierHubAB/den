package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

func TestRemoteHostOf(t *testing.T) {
	for who, want := range map[string]string{"mossy@devbox": "devbox", "Mossy Lemur@gpu": "gpu", "mossy": "", "mossy@local": "", "@devbox": "", "mossy@": ""} {
		if host, ok := remoteHostOf(who); host != want && ok || (want != "") != ok {
			t.Errorf("%q -> %q %v", who, host, ok)
		}
	}
}

// den send name@host asks the running Den; the text travels in the
// envelope and the answer is printed.
func TestSendViaDen(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "den.sock")
	srv, err := bridge.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	got := make(chan bridge.SendInput, 1)
	go func() {
		for r := range srv.Requests() {
			var in bridge.SendInput
			json.Unmarshal(r.Payload, &in)
			if r.Event == bridge.SendEvent && r.WantsReply() {
				got <- in
			}
			if strings.Contains(in.Text, "refuse") {
				r.Reply(bridge.Decision{Behavior: "deny", Message: "devbox is offline; nothing was sent"})
				continue
			}
			r.Reply(bridge.Decision{Behavior: "allow", Message: "delivered to Mossy Lemur on devbox"})
		}
	}()
	out, err := sendViaDen(sock, "mossy@devbox", "rebase `id` $(id)")
	if err != nil || out != "delivered to Mossy Lemur on devbox" {
		t.Fatalf("out %q err %v", out, err)
	}
	if in := <-got; in.Who != "mossy@devbox" || in.Text != "rebase `id` $(id)" || !strings.Contains(in.AskedBy, "den send") {
		t.Errorf("request = %+v", in)
	}
	if _, err := sendViaDen(sock, "mossy@devbox", "refuse"); err == nil || !strings.Contains(err.Error(), "offline; nothing was sent") {
		t.Errorf("denied: %v", err)
	}
}

func TestSendViaDenWithoutDen(t *testing.T) {
	_, err := sendViaDen(filepath.Join(t.TempDir(), "none.sock"), "mossy@devbox", "hi")
	if err == nil || !strings.Contains(err.Error(), "Den is not attached to devbox") {
		t.Errorf("err = %v", err)
	}
}
