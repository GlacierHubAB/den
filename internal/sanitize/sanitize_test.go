package sanitize

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "go test ./...", "go test ./..."},
		{"newline and tab kept", "a\n\tb", "a\n\tb"},
		{"crlf", "a\r\nb", "a\nb"},
		{"lone cr", "a\rb", "a\nb"},
		{"cr cr lf", "a\r\r\nb", "a\n\nb"},
		{"sgr", "\x1b[31mred\x1b[0m", "red"},
		{"clear and home", "x\x1b[2J\x1b[Hy", "xy"},
		{"csi private", "\x1b[?1049hz", "z"},
		{"csi cut by newline", "\x1b[12\nok", "\nok"},
		{"csi cut by esc", "\x1b[1\x1b[2Jok", "ok"},
		{"csi unterminated", "a\x1b[123", "a"},
		{"osc 52 bel", "a\x1b]52;c;SGVsbG8=\x07b", "ab"},
		{"osc 52 st", "a\x1b]52;c;SGVsbG8=\x1b\\b", "ab"},
		{"osc 0 title", "\x1b]0;pwned\x07name", "name"},
		{"osc 8 link", "\x1b]8;;http://evil\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"osc unterminated", "a\x1b]52;c;AAAA\nmore", "a"},
		{"osc cut by esc", "a\x1b]0;t\x1b[2Jb", "ab"},
		{"osc 8-bit st", "a\x1b]0;t\u009cb", "ab"},
		{"osc cancelled", "a\x1b]0;t\x18b", "ab"},
		{"dcs", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"apc", "a\x1b_Gf=24;AAAA\x1b\\b", "ab"},
		{"pm", "a\x1b^secret\x1b\\b", "ab"},
		{"sos", "a\x1bXsecret\x1b\\b", "ab"},
		{"bel ends any string", "a\x1bPx\x07y\x1b\\b", "ayb"},
		{"two byte", "a\x1b7b\x1b8c\x1bcd", "abcd"},
		{"nF charset", "a\x1b(Bb", "ab"},
		{"lone esc at end", "a\x1b", "a"},
		{"esc before newline", "a\x1b\nb", "a\nb"},
		{"esc before utf8", "a\x1bé", "aé"},
		{"c1 csi", "a\u009b2Jb", "ab"},
		{"c1 osc", "a\u009d52;c;AAAA\u0007b", "ab"},
		{"c1 osc st", "a\u009d0;t\u009cb", "ab"},
		{"c1 dcs", "a\u0090x\x1b\\b", "ab"},
		{"c1 apc", "a\u009fx\u009cb", "ab"},
		{"c1 pm sos", "a\u009ex\u009cb\u0098y\u009cc", "abc"},
		{"c1 other", "a\u0085b\u0080c", "abc"},
		{"raw 8-bit csi byte", "a\x9b2Jb", "a2Jb"},
		{"c0", "a\x00b\x07c\x08d\x0be\x0cf\x7fg", "abcdefg"},
		{"bidi", "a\u202ecba\u202c\u2066x\u2069\u200e\u200f\u061cz", "acbaxz"},
		{"invalid utf8", "a\xffb\xc3c\xe2\x82", "abc"},
		{"cjk", "日本語のテキスト", "日本語のテキスト"},
		{"emoji zwj", "👩\u200d👩\u200d👧 family 🏳\ufe0f\u200d🌈", "👩\u200d👩\u200d👧 family 🏳\ufe0f\u200d🌈"},
		{"combining", "e\u0301 n\u0303", "e\u0301 n\u0303"},
		{"joiners between latin letters go", "a\u200db\u200cc", "abc"},
		{"persian zwnj kept", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645"},
		{"devanagari zwj kept", "\u0915\u094d\u200d\u0937", "\u0915\u094d\u200d\u0937"},
		{"a run of joiners keeps one", "\u0645\u200c\u200c\u200d\u062e 👩\u200d\u200d👩", "\u0645\u200d\u062e 👩\u200d👩"},
		{"tag characters", "ok\U000e0049\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065 done", "ok done"},
		{"subdivision flag loses its tags", "\U0001f3f4\U000e0067\U000e0062\U000e007f", "\U0001f3f4"},
		{"zero-width and format", "a\u200bb\u2060c\u2061d\u2064e\ufeff\u00adf\u180eg\u034fh", "abcdefgh"},
		{"hangul fillers", "x\u3164\u115f\u1160\uffa0y", "xy"},
		{"variation selectors", "\u8fbb\U000e0100 x\ufe00 \u2764\ufe0f\ufe0f a\ufe0e", "\u8fbb x \u2764\ufe0f a\ufe0e"},
		{"joiner after an escape", "👩\u200d\x1b[31m👩", "👩👩"},
		{"rtl text kept", "שלום مرحبا", "שלום مرحبا"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Text(c.in); got != c.want {
				t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  a\n\tb  c\r\n", "a b c"},
		{"one", "one"},
		{"\x1b]0;title\x07 name ", "name"},
		{"\n\n", ""},
		{"a\x1b[2J\nb", "a b"},
	}
	for _, c := range cases {
		if got := Line(c.in); got != c.want {
			t.Errorf("Line(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\tif  x {\r", "\tif  x { "},
		{"a\x1b[2Jb\nc", "ab c"},
		{"/x/a\u202e.go", "/x/a.go"},
	}
	for _, c := range cases {
		if got := Code(c.in); got != c.want {
			t.Errorf("Code(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLabel(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 80, "short"},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcde…"},
		{"ab cdefg", 4, "ab…"},
		{"日本語テキスト", 4, "日本語…"},
		{"a\nb", 0, "a b"},
		{"abc", 1, "…"},
		{"\x1b]52;c;AAAA\x07role", 80, "role"},
	}
	for _, c := range cases {
		got := Label(c.in, c.max)
		if got != c.want {
			t.Errorf("Label(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
		if c.max > 0 && utf8.RuneCountInString(got) > c.max {
			t.Errorf("Label(%q, %d) = %q is longer than max", c.in, c.max, got)
		}
	}
}

func TestStyled(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[1;38;5;208mDen\x1b[0m", "\x1b[1;38;5;208mDen\x1b[0m"},
		{"\x1b[38:2::1:2:3mx\x1b[m", "\x1b[38:2::1:2:3mx\x1b[m"},
		{"\x1b[31mok\x1b]52;c;AAAA\x07\x1b[2J\x1b[0m", "\x1b[31mok\x1b[0m"},
		{"\x1b]8;;http://evil\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"\x1b[?25l\x1b[1Ax", "x"},
		{"\x1b[", ""},
		{"a\u202eb", "ab"},
	}
	for _, c := range cases {
		if got := Styled(c.in); got != c.want {
			t.Errorf("Styled(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJSON(t *testing.T) {
	in := map[string]string{"s": "a\x1b]52;c;AAAA\x07\u009b2J\u202eb\x7f\xff日"}
	raw, _ := json.Marshal(in)
	out := JSON(raw)
	for _, r := range string(out) {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || bidi(r) {
			t.Fatalf("JSON(%q) = %q keeps %U", raw, out, r)
		}
	}
	var back, want map[string]string
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &want)
	if back["s"] != want["s"] {
		t.Errorf("decodes to %q, want %q", back["s"], want["s"])
	}
	if plain := []byte(`{"a":"b"}`); &JSON(plain)[0] != &plain[0] {
		t.Error("plain JSON was copied")
	}
}

func TestCleanReturnsInput(t *testing.T) {
	s := strings.Repeat("plain text\n", 10)
	if got := Text(s); got != s {
		t.Fatal("clean text changed")
	}
}

// TestLinear feeds multi-megabyte inputs, including one long unterminated
// OSC and many short sequences, and fails if any takes long enough to suggest
// quadratic behaviour.
func TestLinear(t *testing.T) {
	big := strings.Repeat("x", 4<<20)
	inputs := map[string]string{
		"long line":           big,
		"long line non-ascii": strings.Repeat("é日", 1<<20),
		"unterminated osc":    "a\x1b]52;c;" + big,
		"unterminated dcs":    "a\x1bP" + big,
		"unterminated csi":    "a\x1b[" + strings.Repeat("1;", 2<<20),
		"many sequences":      strings.Repeat("\x1b[31mx\x1b]0;t\x07", 1<<18),
		"many escs":           strings.Repeat("\x1b", 4<<20),
		"many crs":            strings.Repeat("\r", 4<<20),
	}
	for name, in := range inputs {
		start := time.Now()
		out := Text(in)
		Line(in)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: %d bytes took %v", name, len(in), d)
		}
		if strings.ContainsRune(out, esc) {
			t.Errorf("%s: ESC survived", name)
		}
	}
}

func FuzzText(f *testing.F) {
	for _, s := range []string{
		"", "plain", "a\r\nb\rc", "\x1b]52;c;SGVsbG8=\x07", "\x1b[2J\x1b[H",
		"\x1b]8;;http://x\x1b\\y\x1b]8;;\x1b\\", "\u009b31m", "\u009d0;t\u009c",
		"\x1bP\x1b\\", "\x1b(B", "\u202eabc", "\xff\xfe", "👩\u200d👩\u200d👧", "e\u0301",
		"\x1b", "\x1b]", "\x1b[", "\xc2", "\xc2\x9c", "a\u2028b\u2029c", "\u2028\r\u2029",
		"a\u200bb\U000e0041", "\u0645\u200c\u062e", "\u2764\ufe0f\u200d🔥", "x\ufe0f\ufe0f\u200d", "\u00ad\u3164\U000e0101",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Text(in)
		if !utf8.ValidString(out) {
			t.Fatalf("Text(%q) = %q is not valid UTF-8", in, out)
		}
		for _, r := range out {
			switch {
			case r == '\n' || r == '\t':
			case r < 0x20 || r == del:
				t.Fatalf("Text(%q) = %q keeps C0 %U", in, out, r)
			case r >= 0x80 && r <= 0x9f:
				t.Fatalf("Text(%q) = %q keeps C1 %U", in, out, r)
			case bidi(r):
				t.Fatalf("Text(%q) = %q keeps bidi %U", in, out, r)
			case r == 0x2028 || r == 0x2029:
				t.Fatalf("Text(%q) = %q keeps a line or paragraph separator %U", in, out, r)
			case invisible(r) && r != zwj && r != zwnj && r != vs15 && r != vs16:
				t.Fatalf("Text(%q) = %q keeps invisible %U", in, out, r)
			}
		}
		// A kept joiner or variation selector is never the first rune, and
		// never next to another invisible one: at most one per gap.
		rs := []rune(out)
		for i, r := range rs {
			if !invisible(r) {
				continue
			}
			if i == 0 || (invisible(rs[i-1]) && !(r == zwj && rs[i-1] == vs16)) {
				t.Fatalf("Text(%q) = %q has %U where it joins nothing", in, out, r)
			}
			if (r == zwj || r == zwnj) && (i+1 == len(rs) || invisible(rs[i+1])) {
				t.Fatalf("Text(%q) = %q has a joiner %U before nothing", in, out, r)
			}
		}
		if again := Text(out); again != out {
			t.Fatalf("not idempotent: Text(%q) = %q, Text of that = %q", in, out, again)
		}
		line := Line(in)
		if strings.ContainsAny(line, "\n\t") || strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ") || strings.Contains(line, "  ") {
			t.Fatalf("Line(%q) = %q is not one trimmed line", in, line)
		}
		styled := Styled(in)
		if Styled(styled) != styled || Text(styled) != out {
			t.Fatalf("Styled(%q) = %q is not Text plus SGR", in, styled)
		}
		if code := Code(in); strings.Contains(code, "\n") || Code(code) != code {
			t.Fatalf("Code(%q) = %q is not one stable line", in, code)
		}
		if Line(line) != line {
			t.Fatalf("Line not idempotent on %q", in)
		}
	})
}

// U+2028 and U+2029 break a line in some terminals and show nothing in
// others: Text makes them a newline, Line and Code a space, JSON an escape.
func TestLineAndParagraphSeparators(t *testing.T) {
	in := "sep\u2028arator\u2029end"
	if got := Text(in); got != "sep\narator\nend" {
		t.Errorf("Text = %q", got)
	}
	if got := Line(in); got != "sep arator end" {
		t.Errorf("Line = %q", got)
	}
	if got := Code(in); got != "sep arator end" {
		t.Errorf("Code = %q", got)
	}
	if got := string(JSON([]byte(`"` + in + `"`))); got != "\"sep\\u2028arator\\u2029end\"" {
		t.Errorf("JSON = %q", got)
	}
}

func TestHidden(t *testing.T) {
	for _, ok := range []string{"", "plain text\n\twith a tab", "café, 日本語, 👍", "# heading\n- item"} {
		if r, bad := Hidden(ok); bad {
			t.Errorf("Hidden(%q) refused %U", ok, r)
		}
	}
	for in, want := range map[string]rune{
		"a\rb":           '\r',
		"a\x1b[31mb":     0x1b,
		"a\x7fb":         0x7f,
		"a\u0085b":       0x85,
		"a\u202eb":       0x202e,
		"a\u2066b":       0x2066,
		"a\u200bb":       0x200b,
		"a\u200db":       0x200d,
		"a\U000E0041b":   0xe0041,
		"a\ufeffb":       0xfeff,
		"a\u00adb":       0xad,
		"a\u2028b":       0x2028,
		"a\xffb":         utf8.RuneError,
		"a\u034fb\u3164": 0x34f,
	} {
		if r, bad := Hidden(in); !bad || r != want {
			t.Errorf("Hidden(%q) = %U %v, want %U", in, r, bad, want)
		}
	}
}
