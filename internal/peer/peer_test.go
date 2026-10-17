package peer

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSendWritesOneJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()
	if err := Send(path, "  rebase on main first  "); err != nil {
		t.Fatal(err)
	}
	line := <-got
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("not JSON: %v: %q", err, line)
	}
	if msg.Type != "user" || msg.Message.Role != "user" || !strings.HasSuffix(msg.Message.Content, "rebase on main first") || !strings.HasPrefix(msg.Message.Content, Header) {
		t.Errorf("message = %+v", msg)
	}
	if err := Send(filepath.Join(t.TempDir(), "nope.sock"), "x"); err == nil {
		t.Error("expected an error for a missing socket")
	}
	if err := Send("", "x"); err == nil {
		t.Error("expected an error for no socket")
	}
}

// Den's own automatic messages say they are Den's, never the human's: no
// trace of Header, and the same socket checks as Send.
func TestSendFromDenIsNeverTheHumans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()
	if err := SendFromDen(path, "your branch is 2 days old"); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(<-got), &msg); err != nil {
		t.Fatal(err)
	}
	c := msg.Message.Content
	if !strings.HasPrefix(c, DenHeader+"\n\n") || strings.Contains(c, "typed by your human") || strings.Contains(c, Header) || !strings.Contains(DenHeader, "did not type it") {
		t.Errorf("content %q", c)
	}
	if err := SendFromDen(filepath.Join(t.TempDir(), "nope.sock"), "x"); err == nil {
		t.Error("a missing socket")
	}
	// The usage stop is Den's too: binding, as the human's rule, but never
	// typed by the human.
	if u := ComposeUsage("USAGE STOP"); strings.Contains(u, "typed by your human") || !strings.Contains(u, "did not type this") || !strings.Contains(u, "binding") {
		t.Errorf("usage frame %q", u)
	}
	if err := SendUsage(filepath.Join(t.TempDir(), "nope.sock"), "x"); err == nil {
		t.Error("a missing socket")
	}
}

// A path that is a symlink is refused, even to a socket of ours: the text
// must not be steerable to another service. Errors do not name the path.
func TestSendRefusesLinksAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "SECRET-real.sock")
	ln, err := net.Listen("unix", real)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			c.Close()
			got <- line
		}
	}()
	link := filepath.Join(dir, "SECRET-link.sock")
	os.Symlink(real, link)
	file := filepath.Join(dir, "SECRET-file")
	os.WriteFile(file, []byte("x"), 0o600)
	for name, p := range map[string]string{"symlink": link, "regular file": file, "missing": filepath.Join(dir, "SECRET-none")} {
		err := Send(p, "hi")
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	select {
	case l := <-got:
		t.Errorf("a refused send reached the socket: %q", l)
	case <-time.After(200 * time.Millisecond):
	}
	if err := Send(real, "hi"); err != nil {
		t.Errorf("a plain socket of ours: %v", err)
	}
	<-got
}
