package orch

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Guidance is text the human chose to shape how the orchestrator answers
// (orchestrator.guidance): the name of guidance Den ships, or a file of
// the human's own. It goes into the orchestrator's prompt in a
// <human_guidance> block after Den's fixed rules, only once the human has
// approved its SHA-256 in Den, and never to a host.
type Guidance struct {
	Setting string // orchestrator.guidance as configured
	Source  string // where the text comes from, as the question shows it
	Text    string // exactly what goes inside <human_guidance>
	SHA256  string // hex SHA-256 of Text
}

// MaxGuidance caps the text, in bytes, after its frontmatter is stripped.
const MaxGuidance = 8192

// maxGuidanceFile caps what Den reads of a file, frontmatter included.
const maxGuidanceFile = 64 << 10

// The guidance Den ships: i-have-adhd's SKILL.md with Den's four edits
// (guidance/i-have-adhd: the upstream file, its LICENSE, den.patch and
// PROVENANCE).
const (
	ADHDName   = "i-have-adhd"
	adhdSource = "i-have-adhd, shipped with Den: github.com/ayghri/i-have-adhd @ 1ecea1993b7f (v0.4.1, MIT, © 2026 Ayoub Ghriss), with Den's four edits"
)

//go:embed guidance/i-have-adhd/SKILL.md
var adhdSKILL []byte

// StripFrontmatter drops a leading YAML frontmatter block (a "---" line,
// anything, a "---" line) and one blank line after it. Text without one
// comes back as it is.
func StripFrontmatter(b []byte) []byte {
	if !bytes.HasPrefix(b, []byte("---\n")) {
		return b
	}
	end := bytes.Index(b[3:], []byte("\n---\n"))
	if end < 0 {
		return b
	}
	return bytes.TrimPrefix(b[3+end+5:], []byte("\n"))
}

// SHA256 is the hex SHA-256 of text.
func SHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// LoadGuidance reads the guidance a setting names: ADHDName, or an
// absolute path to a regular file the human owns that only they can write.
// The text is refused when it is empty, over MaxGuidance bytes, not UTF-8,
// carries a control, bidi or invisible character, or names the block it
// goes in. A path inside Den's state folder, where hosts' and agents'
// data lands, is refused too.
func LoadGuidance(setting string) (Guidance, error) {
	g := Guidance{Setting: setting}
	var raw []byte
	switch {
	case setting == ADHDName:
		g.Source, raw = adhdSource, adhdSKILL
	case filepath.IsAbs(setting):
		b, err := readOwnFile(setting)
		if err != nil {
			return g, err
		}
		g.Source, raw = setting, b
	default:
		return g, fmt.Errorf("orchestrator.guidance is %q: give %q or the absolute path of a file of your own", setting, ADHDName)
	}
	text := StripFrontmatter(raw)
	if err := checkGuidance(text); err != nil {
		return g, err
	}
	g.Text = string(text)
	g.SHA256 = SHA256(g.Text)
	return g, nil
}

func checkGuidance(text []byte) error {
	if len(bytes.TrimSpace(text)) == 0 {
		return errors.New("the guidance is empty")
	}
	if len(text) > MaxGuidance {
		return fmt.Errorf("the guidance is %d bytes after its frontmatter; the cap is %d bytes (8 KB)", len(text), MaxGuidance)
	}
	if !utf8.Valid(text) {
		return errors.New("the guidance is not valid UTF-8")
	}
	if r, bad := sanitize.Hidden(string(text)); bad {
		return fmt.Errorf("the guidance holds a control, bidi or invisible character (%U)", r)
	}
	if strings.Contains(strings.ToLower(string(text)), "human_guidance") {
		return errors.New("the guidance names the human_guidance block it would go in")
	}
	return nil
}

// readOwnFile reads a guidance file: a clean absolute path, not a symlink,
// a regular file owned by this user and writable by no one else, outside
// Den's state folder and /proc.
func readOwnFile(path string) ([]byte, error) {
	if filepath.Clean(path) != path {
		return nil, fmt.Errorf("guidance path %s is not clean; write it without . or .. parts", path)
	}
	for _, dir := range []string{denStateDir(), "/proc"} {
		if dir != "" && (path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))) {
			return nil, fmt.Errorf("guidance path %s is inside %s, which hosts and agents write; keep it elsewhere", path, dir)
		}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("guidance file does not read: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("guidance file does not read: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("guidance path %s is not a regular file", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("guidance file %s is not yours", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("guidance file %s is writable by others; chmod go-w it", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxGuidanceFile+1))
	if err != nil {
		return nil, fmt.Errorf("guidance file does not read: %w", err)
	}
	if len(b) > maxGuidanceFile {
		return nil, fmt.Errorf("guidance file %s is over %d bytes; the cap is %d bytes (8 KB) after its frontmatter", path, maxGuidanceFile, MaxGuidance)
	}
	return b, nil
}

// denStateDir is $XDG_STATE_HOME/den, else ~/.local/state/den.
func denStateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den")
}

// Prompt is the system prompt Den appends: SystemPrompt, then, when the
// human approved guidance, its text in a <human_guidance> block of its own.
func Prompt(guidance string) string {
	if guidance == "" {
		return SystemPrompt
	}
	return SystemPrompt + "\n\n<human_guidance>\n" + guidance + "\n</human_guidance>"
}
