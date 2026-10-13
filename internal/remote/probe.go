package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Probe is what the read-only checks of `den remote add` and `status`
// found on a host. Nothing on the host is changed to find it.
type Probe struct {
	Uname         string // uname -sm, e.g. "Linux x86_64"
	DenPath       string // the den the session would run, "" when none
	DenVersion    string // its `den version`
	ClaudePath    string
	ClaudeVersion string // claude --version, when it answered within 5 s
	Hooks         bool   // Claude Code's settings.json on the host runs den hook
	RuntimeDir    bool   // XDG_RUNTIME_DIR is set there (approve needs it)
}

// probeScript is the fixed remote command of the checks. It only reads:
// uname, which den and claude are found and their versions, and whether
// the host's settings.json mentions den hook.
func probeScript(h Host) string {
	find := `d=$(command -v den 2>/dev/null)`
	switch {
	case strings.HasPrefix(h.DenPath, "~/"):
		find = `d="$HOME/` + strings.TrimPrefix(h.DenPath, "~/") + `"; [ -x "$d" ] || d=""`
	case h.DenPath != "":
		find = `d="` + h.DenPath + `"; [ -x "$d" ] || d=""`
	}
	return `sh -lc 'PATH="$PATH:$HOME/.local/bin"; export PATH
echo "uname=$(uname -sm)"
` + find + `
echo "den_path=$d"
if [ -n "$d" ]; then echo "den_version=$("$d" version 2>/dev/null | head -n 1)"; fi
c=$(command -v claude 2>/dev/null)
echo "claude_path=$c"
if [ -n "$c" ] && command -v timeout >/dev/null 2>&1; then echo "claude_version=$(timeout 5 "$c" --version 2>/dev/null | head -n 1)"; fi
s="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json"
if grep -q "den.\{0,1\} hook" "$s" 2>/dev/null; then echo hooks=yes; else echo hooks=no; fi
echo "runtime_dir=${XDG_RUNTIME_DIR:-}"
'`
}

// Probe runs the read-only checks on h. An ssh failure comes back as a
// *Failure; Classify says what it means.
func (s SSH) Probe(ctx context.Context, h Host) (Probe, error) {
	if err := CheckDenPath(h.DenPath); err != nil {
		return Probe{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.Hygiene(ctx, h.Target)
	out, err := s.Run(ctx, h.Target, probeScript(h))
	if err != nil {
		return Probe{}, err
	}
	return parseProbe(out), nil
}

func parseProbe(out []byte) Probe {
	var p Probe
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = sanitize.Label(v, 120)
		switch k {
		case "uname":
			p.Uname = v
		case "den_path":
			p.DenPath = v
		case "den_version":
			p.DenVersion = strings.TrimPrefix(v, "den ")
		case "claude_path":
			p.ClaudePath = v
		case "claude_version":
			p.ClaudeVersion = v
		case "hooks":
			p.Hooks = v == "yes"
		case "runtime_dir":
			p.RuntimeDir = v != ""
		}
	}
	return p
}

// Handshake starts `den remote serve` on h, reads its hello and ends the
// session: the full path a background connection takes, still read-only.
func (s SSH) Handshake(ctx context.Context, h Host) (Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := SSHDialer(s)(ctx, h)
	if err != nil {
		return Hello{}, err
	}
	defer func() {
		conn.Kill()
		conn.Wait()
	}()
	if err := writeMsg(conn.In, handshakeRequest(h)); err != nil {
		return Hello{}, conn.Wait()
	}
	line, err := newLineReader(conn.Out, MaxLine).next()
	if err != nil {
		conn.In.Close()
		return Hello{}, conn.Wait()
	}
	var hm Hello
	if typ, _, _ := peek(line); typ == TypeError {
		var e Error
		json.Unmarshal(line, &e)
		return Hello{}, fmt.Errorf("the host refused: %s", sanitize.Line(e.Message))
	}
	if json.Unmarshal(line, &hm) != nil || hm.Type != TypeHello {
		return Hello{}, fmt.Errorf("the host did not answer with hello")
	}
	hm.Hostname, hm.Den, hm.Claude = sanitize.Label(hm.Hostname, 64), sanitize.Label(hm.Den, 40), sanitize.Label(hm.Claude, 40)
	return hm, nil
}

// handshakeRequest is a check's request: observe, whatever the host's
// policy, and no controller, so a check never opens den-remote.sock on the
// host (hooks that fired meanwhile would be held by a session that cannot
// answer them) and never takes held requests over from the Den attached.
func handshakeRequest(h Host) Request {
	return Request{Type: TypeHello, Proto: Proto, Policy: Observe, Labels: h.Labels}
}

// Advice is what the human can do about what a probe found, one line
// each; empty when the host is ready.
func (p Probe) Advice(h Host, localVersion string) []string {
	var out []string
	switch {
	case p.DenPath == "":
		out = append(out, fmt.Sprintf("den is not installed on the host: den remote install %s", h.Label))
	case Older(p.DenVersion, localVersion):
		out = append(out, fmt.Sprintf("den on the host is %s, older than this %s: den remote install %s", p.DenVersion, localVersion, h.Label))
	}
	if p.ClaudePath == "" {
		out = append(out, "Claude Code (claude) was not found in a login shell's PATH on the host")
	}
	if !p.Hooks {
		out = append(out, "Den's hooks are not installed on the host, so it shows agents but no events: run den install-hooks there")
	}
	return out
}
