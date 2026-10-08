package ui

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // decoders for the thumbnails
	_ "image/jpeg" //
	_ "image/png"  //
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// attachment is an image the command line will send along with the text.
// It shows as a chip, "[Image #1]", and the path travels with the message.
type attachment struct {
	Num     int // the number in the chip, counting from 1
	ImageID int // the kitty image id behind the thumbnail
	Path    string
}

// Chip is the text that stands for the attachment in the command line.
func (a attachment) Chip() string { return fmt.Sprintf("[Image #%d]", a.Num) }

// thumbCols and thumbRows are the size of an inline thumbnail in cells.
const (
	thumbCols = 6
	thumbRows = 3
	// kittyProbeID is the image id of the support query; replies carry it.
	kittyProbeID = 31
	// Image ids live in 16..255: a foreground colour index carries the id to
	// the terminal, and indexes below 16 would be sent as basic colours.
	minImageID = 16
	maxImageID = 255
)

var chipRE = regexp.MustCompile(`\[Image #(\d+)\]`)

// kittyProbe asks the terminal whether it speaks the kitty graphics
// protocol. A terminal that does replies with an OK carrying the probe id;
// one that does not stays silent, so support is never assumed.
func kittyProbe() tea.Cmd {
	return tea.Raw(ansi.KittyGraphics([]byte("AAAA"), "i="+strconv.Itoa(kittyProbeID), "s=1", "v=1", "a=q", "t=d", "f=24", "q=1"))
}

// kittyReply reports whether a graphics event is the probe's OK.
func kittyReply(ev uv.KittyGraphicsEvent) bool {
	return ev.Options.ID == kittyProbeID && bytes.HasPrefix(ev.Payload, []byte("OK"))
}

// imagePaths returns the image files a paste names: one path per line, as a
// plain path, a file:// URL or a shell-escaped path the way a drag-and-drop
// writes it. Anything that is not an existing image file makes it return
// nothing, so ordinary pasted text is never swallowed.
func imagePaths(text string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := line
		if strings.HasPrefix(p, "file://") {
			u, err := url.Parse(p)
			if err != nil {
				return nil
			}
			p = u.Path
		}
		p = strings.ReplaceAll(p, `\ `, " ")
		p = strings.Trim(p, `"'`)
		if !isImageFile(p) {
			return nil
		}
		out = append(out, p)
	}
	return out
}

func isImageFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
	default:
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// attach adds an image to the command line and, when the terminal can show
// it, transmits a thumbnail.
func (m *Model) attach(path string) tea.Cmd {
	m.nextImage++
	a := attachment{Num: m.nextImage, ImageID: minImageID + (m.nextImage-1)%(maxImageID-minImageID+1), Path: path}
	m.attachments = append(m.attachments, a)
	m.input.InsertString(a.Chip() + " ")
	m.fitInput()
	if !m.kitty {
		return nil
	}
	return transmitThumb(a)
}

// transmitThumb sends the image to the terminal as a virtual placement the
// placeholder cells refer to. Decoding happens off the UI thread.
func transmitThumb(a attachment) tea.Cmd {
	return func() tea.Msg {
		f, err := os.Open(a.Path)
		if err != nil {
			return nil
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil {
			return nil // a format Go cannot decode (webp): the chip alone
		}
		img = shrink(img, 160)
		var buf bytes.Buffer
		opts := &kitty.Options{
			Action:           kitty.TransmitAndPut,
			ID:               a.ImageID,
			Format:           kitty.RGBA,
			Transmission:     kitty.Direct,
			Chunk:            true,
			VirtualPlacement: true,
			Columns:          thumbCols,
			Rows:             thumbRows,
			Quiet:            2,
		}
		if err := kitty.EncodeGraphics(&buf, img, opts); err != nil {
			return nil
		}
		return tea.RawMsg{Msg: buf.String()}
	}
}

// forgetThumbs frees the terminal's copies once the attachments are gone.
func forgetThumbs(list []attachment) tea.Cmd {
	if len(list) == 0 {
		return nil
	}
	var b strings.Builder
	for _, a := range list {
		b.WriteString(ansi.KittyGraphics(nil, "a=d", "d=I", "i="+strconv.Itoa(a.ImageID), "q=2"))
	}
	return tea.Raw(b.String())
}

// shrink scales an image down so its longer side is at most max pixels,
// by sampling; a thumbnail needs no better.
func shrink(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	scale := float64(max) / float64(w)
	if h > w {
		scale = float64(max) / float64(h)
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			out.Set(x, y, img.At(b.Min.X+x*w/nw, sy))
		}
	}
	return out
}

// thumbRowsFor draws the placeholder cells for an attachment: the terminal
// paints the image over them. The foreground colour carries the image id.
func thumbRowsFor(a attachment) []string {
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(a.ImageID)))
	rows := make([]string, thumbRows)
	for r := 0; r < thumbRows; r++ {
		var b strings.Builder
		for c := 0; c < thumbCols; c++ {
			b.WriteRune(kitty.Placeholder)
			b.WriteRune(kitty.Diacritic(r))
			b.WriteRune(kitty.Diacritic(c))
		}
		rows[r] = st.Render(b.String())
	}
	return rows
}

// referenced returns the attachments whose chip is still in the text, in
// the order they appear; a chip the user deleted drops its image.
func (m Model) referenced(text string) []attachment {
	byNum := map[int]attachment{}
	for _, a := range m.attachments {
		byNum[a.Num] = a
	}
	var out []attachment
	seen := map[int]bool{}
	for _, match := range chipRE.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(match[1])
		if a, ok := byNum[n]; ok && !seen[n] {
			out = append(out, a)
			seen[n] = true
		}
	}
	return out
}

// expandChips rewrites "[Image #1]" as "[Image #1: /path/to/file.png]" so
// the receiving session can open the file; the path travels intact.
func (m Model) expandChips(text string) string {
	byNum := map[int]attachment{}
	for _, a := range m.attachments {
		byNum[a.Num] = a
	}
	return chipRE.ReplaceAllStringFunc(text, func(chip string) string {
		n, _ := strconv.Atoi(chipRE.FindStringSubmatch(chip)[1])
		a, ok := byNum[n]
		if !ok {
			return chip
		}
		return fmt.Sprintf("[Image #%d: %s]", n, a.Path)
	})
}

// clipMsg is what the clipboard held: an image saved to a file, or text.
type clipMsg struct {
	path string
	text string
	err  error
}

// readClipboard fetches the clipboard through wl-paste: an image becomes a
// file under the state directory, text comes back as text.
func readClipboard() tea.Cmd {
	return func() tea.Msg {
		types, err := exec.Command("wl-paste", "--list-types").Output()
		if err != nil {
			return clipMsg{err: fmt.Errorf("wl-paste: %w", err)}
		}
		mime := ""
		for _, t := range strings.Fields(string(types)) {
			if t == "image/png" {
				mime = t
				break
			}
			if strings.HasPrefix(t, "image/") && mime == "" {
				mime = t
			}
		}
		if mime == "" {
			text, err := exec.Command("wl-paste", "--no-newline", "-t", "text").Output()
			if err != nil {
				return clipMsg{err: fmt.Errorf("wl-paste: %w", err)}
			}
			return clipMsg{text: string(text)}
		}
		data, err := exec.Command("wl-paste", "-t", mime).Output()
		if err != nil {
			return clipMsg{err: fmt.Errorf("wl-paste: %w", err)}
		}
		ext := ".png"
		switch mime {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
		dir := attachmentsDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return clipMsg{err: err}
		}
		p := filepath.Join(dir, "clip-"+time.Now().Format("20060102-150405")+ext)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return clipMsg{err: err}
		}
		return clipMsg{path: p}
	}
}

func attachmentsDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "attachments")
}
