package desktop

import (
	"bytes"
	"strings"
	"testing"
)

// Typed text never reaches the log, whichever way hypruse echoes it.
func TestLogFilterWithholdsTypedText(t *testing.T) {
	var out bytes.Buffer
	f := &LogFilter{W: &out}
	f.Typed("my secret note")
	f.Typed("i") // too short to match
	f.Write([]byte("Traceback (most recent call last):\n  text = 'my secret note'\nValueError: bad input: my secret note\nall fine\ni\n"))
	got := out.String()
	if strings.Contains(got, "my secret note") {
		t.Fatalf("typed text in the log:\n%s", got)
	}
	if strings.Count(got, "withheld") != 2 || !strings.Contains(got, "all fine") || !strings.Contains(got, "Traceback") {
		t.Fatalf("log:\n%s", got)
	}
}

func TestLogFilterCleansAndCaps(t *testing.T) {
	var out bytes.Buffer
	f := &LogFilter{W: &out}
	f.Write([]byte("a\x1b[31mred\u202e\x07\n"))
	if got := out.String(); got != "a[31mred\n" {
		t.Fatalf("%q", got)
	}
	out.Reset()
	f.Write([]byte(strings.Repeat("x", 2000) + "\n"))
	if line := strings.TrimSuffix(out.String(), "\n"); len(line) > LogLine+10 || !strings.HasSuffix(line, "[cut]") {
		t.Fatalf("long line: %d bytes", len(line))
	}
	out.Reset()
	g := &LogFilter{W: &out}
	chunk := []byte(strings.Repeat("y", 500) + "\n")
	for i := 0; i < 3000; i++ {
		g.Write(chunk)
	}
	if out.Len() > LogMax+100 || !strings.Contains(out.String(), "reached 1 MiB") {
		t.Fatalf("log of %d bytes", out.Len())
	}
	// A line split across writes is one line.
	out.Reset()
	h := &LogFilter{W: &out}
	h.Typed("split secret")
	h.Write([]byte("has split "))
	h.Write([]byte("secret here\n"))
	if strings.Contains(out.String(), "split secret") {
		t.Fatal("a typed string split across writes reached the log")
	}
}
