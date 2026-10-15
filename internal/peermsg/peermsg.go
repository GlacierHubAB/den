// Package peermsg reads the messages other Claude Code sessions send Den's
// orchestrator, and frames the ones Den admits as its own turn.
//
// Claude Code hands a peer's message to the receiving session as a user
// prompt wrapped in a <cross-session-message> envelope. The orchestrator
// must never take such a prompt as the human's: Den's UserPromptSubmit
// hook blocks every envelope, and Den itself, having checked the sender
// against its fleet, sends the body on as an <agent_report> turn whose
// text is data. Parse is strict on purpose: anything it does not fully
// understand is refused, never guessed at.
package peermsg

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

const (
	openTag  = "<cross-session-message"
	closeTag = "</cross-session-message>"
)

// Envelope is a peer message Parse accepted.
type Envelope struct {
	From     string // the sender's messaging socket, uds:/…/<pid>.sock
	FromName string // the sender's session name, as it claims it
	FromMode string
	PID      int // from From
	Body     string
}

// tagLike is anything a reader could take for an envelope's tag: either
// case, spaces after the bracket, opening or closing.
var tagLike = regexp.MustCompile(`(?i)<\s*/?\s*cross-session-message`)

// HasEnvelope reports whether text holds anything that looks like a peer
// envelope's tag, anywhere. Such a prompt is always blocked.
func HasEnvelope(text string) bool { return tagLike.MatchString(text) }

// Defuse makes every envelope-like tag in Den's own text harmless (its
// bracket becomes ‹), so a roster that quotes an agent talking about
// envelopes is not taken for one by the hook or the backstop.
func Defuse(text string) string {
	return tagLike.ReplaceAllStringFunc(text, func(t string) string { return "‹" + t[1:] })
}

// attrValue is what an attribute value may hold.
var attrValue = regexp.MustCompile(`^[^"<>\n\r\x00-\x1f]*$`)

// fromPath is a messaging socket of this user, in either place Claude Code
// keeps them.
var fromPath = regexp.MustCompile(`^uds:(?:/run/user/(\d+)/cc-socks|/tmp/cc-socks-(\d+))/(\d+)\.sock$`)

// Parse reads a prompt that must be exactly one envelope: the opening tag
// at offset 0 with the attributes from, from-name and from-mode, each
// once and nothing else, and the closing tag at the very end (one newline
// after it aside). The body may hold no envelope or system-reminder tag of
// its own. from must be a messaging socket of this user.
func Parse(prompt string) (Envelope, error) {
	var e Envelope
	if !utf8.ValidString(prompt) {
		return e, errors.New("not UTF-8")
	}
	s := strings.TrimSuffix(prompt, "\n")
	if !strings.HasPrefix(s, openTag) || !strings.HasSuffix(s, closeTag) {
		return e, errors.New("not a single envelope")
	}
	rest := s[len(openTag) : len(s)-len(closeTag)]
	attrs := map[string]string{}
	for {
		if strings.HasPrefix(rest, ">") {
			rest = rest[1:]
			break
		}
		if !strings.HasPrefix(rest, " ") {
			return e, errors.New("malformed opening tag")
		}
		rest = rest[1:]
		eq := strings.Index(rest, `="`)
		if eq <= 0 {
			return e, errors.New("malformed attribute")
		}
		name := rest[:eq]
		switch name {
		case "from", "from-name", "from-mode":
		default:
			return e, fmt.Errorf("unexpected attribute %q", sanitize.Label(name, 40))
		}
		if _, dup := attrs[name]; dup {
			return e, fmt.Errorf("attribute %s given twice", name)
		}
		rest = rest[eq+2:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return e, errors.New("unterminated attribute")
		}
		v := rest[:end]
		if !attrValue.MatchString(v) {
			return e, fmt.Errorf("attribute %s holds characters it may not", name)
		}
		attrs[name] = v
		rest = rest[end+1:]
	}
	if len(attrs) != 3 {
		return e, errors.New("missing attribute")
	}
	low := strings.ToLower(rest)
	for _, tag := range []string{"<cross-session-message", "</cross-session-message", "<system-reminder", "</system-reminder"} {
		if strings.Contains(low, tag) {
			return e, errors.New("an envelope or reminder inside the body")
		}
	}
	m := fromPath.FindStringSubmatch(attrs["from"])
	if m == nil {
		return e, errors.New("from is not a messaging socket")
	}
	uid := m[1] + m[2]
	if uid != strconv.Itoa(os.Getuid()) {
		return e, errors.New("from is another user's socket")
	}
	pid, err := strconv.Atoi(m[3])
	if err != nil || pid <= 1 {
		return e, errors.New("from names no process")
	}
	e.From, e.FromName, e.FromMode, e.PID = attrs["from"], attrs["from-name"], attrs["from-mode"], pid
	e.Body = strings.TrimPrefix(strings.TrimSuffix(rest, "\n"), "\n")
	return e, nil
}

// Hash is how Den and its hook name a prompt: the SHA-256 of its bytes, in
// hex. Den records it for each turn it writes; the hook passes a prompt
// without an envelope only when Den recognises the hash.
func Hash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// MaxReport caps a report's body.
const MaxReport = 16 << 10

// Report is Den's own turn for an admitted message: the body as data,
// sanitised, with angle brackets made harmless, capped, inside an
// <agent_report> naming the agent by its critter name.
func Report(critter, body string) string {
	b := sanitize.Text(body)
	b = strings.NewReplacer("<", "‹", ">", "›").Replace(b)
	if len(b) > MaxReport {
		cut := MaxReport
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		b = b[:cut] + "\n[… cut by Den at 16 KiB]"
	}
	name := strings.NewReplacer(`"`, "'", "<", "‹", ">", "›").Replace(sanitize.Label(critter, 60))
	return `<agent_report agent="` + name + `">` + "\n" + b + "\n</agent_report>"
}
