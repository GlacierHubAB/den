package desktop

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// LogFilter stands between hypruse's stderr and the mission's log. hypruse
// may echo a call's arguments (a traceback's locals, a validation error
// repeating its input), so typed text could reach the log: a line that
// holds any text the agent typed in this session is withheld; control,
// bidi and other format characters are removed; each line is cut at
// LogLine bytes and the log at LogMax, with a marker.
type LogFilter struct {
	W io.Writer

	mu      sync.Mutex
	buf     []byte
	written int
	full    bool
	typed   []string
}

// The log's limits.
const (
	LogLine = 512
	LogMax  = 1 << 20
	// minSecret is the shortest typed string matched: shorter ones (a
	// single key) would withhold nearly every line, and say nothing.
	minSecret  = 4
	maxSecrets = 256
)

// Typed records text the agent typed, so no log line repeats it.
func (f *LogFilter) Typed(s string) {
	if f == nil || utf8.RuneCountInString(s) < minSecret {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed = append(f.typed, s)
	if len(f.typed) > maxSecrets {
		f.typed = f.typed[len(f.typed)-maxSecrets:]
	}
}

// Write takes stderr as it comes and writes whole, filtered lines.
func (f *LogFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			if len(f.buf) > 4*LogLine {
				// A line with no end: cut it here.
				f.line(f.buf)
				f.buf = f.buf[:0]
			}
			return len(p), nil
		}
		f.line(f.buf[:i])
		f.buf = f.buf[i+1:]
	}
}

func (f *LogFilter) line(raw []byte) {
	if f.full {
		return
	}
	text := string(raw)
	for _, s := range f.typed {
		if strings.Contains(text, s) {
			text = "[a line was withheld: it held text the agent typed]"
			break
		}
	}
	var b strings.Builder
	for _, r := range text {
		if r == utf8.RuneError || unicode.IsControl(r) && r != '\t' || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > LogLine {
		cut := LogLine
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + " [cut]"
	}
	if f.written+len(out)+1 > LogMax {
		f.full = true
		io.WriteString(f.W, "[the log reached 1 MiB; the rest was dropped]\n")
		return
	}
	n, _ := io.WriteString(f.W, out+"\n")
	f.written += n
}
