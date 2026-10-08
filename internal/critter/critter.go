// Package critter draws the same 16x16 pixel-art creature agent-badge gives
// a session, so the two tools show one face per agent. The generator is a
// port of agent-badge's hooks/register.tsx (species, spritePixels, poses).
// Den renders the grid with half-block characters, two pixels per cell, so
// it works in any terminal without the kitty graphics protocol.
package critter

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// N is the sprite size in pixels.
const N = 16

type rgb [3]float64

// Pose is one animation frame's parameters.
type Pose struct {
	Breathe bool
	Blink   bool
	Look    int // -1, 0, 1
	Hop     bool
}

type species struct {
	ears      string
	halfWidth float64
	top       int
	belly     bool
	spots     bool
	mouth     bool
}

// Colors is the palette the sprite is drawn with, mirroring agent-badge's.
type Colors struct {
	Bodies []string // candidate body colours; the seed picks one
	Dark   string   // background
	Blush  string
}

// Hash is agent-badge's seed for a session id (32-bit FNV-1a).
func Hash(text string) uint32 {
	acc := uint32(0x811c9dc5)
	for _, r := range text {
		acc ^= uint32(r)
		acc *= 0x01000193
	}
	return acc
}

func rng(seed uint32) func() float64 {
	a := seed
	if a == 0 {
		a = 1
	}
	return func() float64 {
		a += 0x6d2b79f5
		t := (a ^ (a >> 15)) * (1 | a)
		t = (t + (t^(t>>7))*(61|t)) ^ t
		return float64(t^(t>>14)) / 4294967296
	}
}

func pick[T any](xs []T, i int) T { return xs[i%len(xs)] }

func newSpecies(seed uint32) species {
	r := rng(seed ^ 0x51ed270b)
	ears := pick([]string{"cat", "bunny", "antenna", "horns", "round", "tuft"}, int(math.Floor(r()*6)))
	hw := 6.2
	if r() < 0.5 {
		hw = 5.5
	}
	top := 6
	if r() < 0.5 {
		top = 5
	}
	return species{ears: ears, halfWidth: hw, top: top, belly: r() < 0.7, spots: r() < 0.35, mouth: r() < 0.6}
}

func hexToRGB(h string) rgb {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return rgb{0, 0, 0}
	}
	var out rgb
	for i := 0; i < 3; i++ {
		v, _ := strconv.ParseInt(h[i*2:i*2+2], 16, 32)
		out[i] = float64(v)
	}
	return out
}

func toHex(c rgb) string {
	cl := func(v float64) int {
		i := int(math.Round(v))
		if i < 0 {
			return 0
		}
		if i > 255 {
			return 255
		}
		return i
	}
	return fmt.Sprintf("#%02x%02x%02x", cl(c[0]), cl(c[1]), cl(c[2]))
}

func mix(a, b rgb, t float64) rgb {
	return rgb{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

// Pixels returns the 16x16 grid as hex colours; "" is transparent.
func Pixels(seed uint32, c Colors, pose Pose) [N][N]string {
	s := newSpecies(seed)
	bodies := c.Bodies
	if len(bodies) == 0 {
		bodies = []string{"#66cb8e", "#e8b84a", "#ea9e9e"}
	}
	body := hexToRGB(pick(bodies, int(seed)))
	white := rgb{255, 255, 255}
	dark := c.Dark
	if dark == "" {
		dark = "#000000"
	}
	blushHex := c.Blush
	if blushHex == "" {
		blushHex = "#f4c4c4"
	}
	ink := mix(hexToRGB(dark), body, 0.15)
	outline := mix(body, ink, 0.6)
	light := mix(body, white, 0.35)
	shade := mix(body, ink, 0.25)
	bellyColor := mix(body, white, 0.5)
	blush := hexToRGB(blushHex)
	spotsRand := rng(seed ^ 0x2545f491)

	var grid [N][N]*rgb
	at := func(x, y int) *rgb {
		if x >= 0 && x < N && y >= 0 && y < N {
			return grid[y][x]
		}
		return nil
	}
	put := func(x, y int, col rgb) {
		if x >= 0 && x < N && y >= 0 && y < N {
			v := col
			grid[y][x] = &v
		}
	}
	mirror := func(x, y int, col rgb) {
		put(x, y, col)
		put(N-1-x, y, col)
	}

	lift := 0
	if pose.Hop {
		lift = 1
	}
	top := s.top - lift
	if pose.Breathe {
		top++
	}
	bottom := 13 - lift
	cy := float64(top+bottom)/2 + 0.5
	ry := float64(bottom-top+1) / 2
	rx := s.halfWidth
	if pose.Breathe {
		rx += 0.3
	}

	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			dx := (float64(x) + 0.5 - N/2) / rx
			dy := (float64(y) + 0.5 - cy) / ry
			if dx*dx+dy*dy > 1 {
				continue
			}
			col := body
			if dy > 0.45 {
				col = shade
			} else if dx < -0.25 && dy < -0.2 {
				col = light
			}
			put(x, y, col)
		}
	}

	ex := int(math.Round(N/2 - s.halfWidth + 1.5))
	switch s.ears {
	case "cat":
		for k := 0; k < 3; k++ {
			for i := 0; i <= 2-k; i++ {
				mirror(ex+i, top-k, body)
			}
		}
		mirror(ex+1, top, light)
	case "bunny":
		for k := 1; k <= 4; k++ {
			mirror(ex+1, top-k, body)
			col := body
			if k > 1 && k < 4 {
				col = bellyColor
			}
			mirror(ex+2, top-k, col)
		}
	case "antenna":
		for k := 1; k <= 2; k++ {
			mirror(ex+2, top-k, outline)
		}
		mirror(ex+1, top-3, light)
		mirror(ex+2, top-3, light)
	case "horns":
		mirror(ex+1, top-1, bellyColor)
		mirror(ex, top-2, bellyColor)
	case "round":
		for _, d := range [][2]int{{0, 0}, {1, 0}, {0, -1}, {1, -1}, {-1, 0}} {
			mirror(ex+d[0], top+d[1], body)
		}
		mirror(ex, top-1, light)
	case "tuft":
		put(7, top-1, body)
		put(8, top-2, body)
		put(8, top-1, light)
	}

	if s.belly {
		for y := 0; y < N; y++ {
			for x := 0; x < N; x++ {
				dx := (float64(x) + 0.5 - N/2) / (rx - 2.6)
				dy := (float64(y) + 0.5 - (cy + ry*0.45)) / (ry * 0.45)
				if dx*dx+dy*dy <= 1 && at(x, y) != nil {
					put(x, y, bellyColor)
				}
			}
		}
	}

	if s.spots {
		for i := 0; i < 3; i++ {
			x := 3 + int(math.Floor(spotsRand()*3))
			y := int(math.Round(cy - 1 + spotsRand()*3))
			if at(x, y) != nil {
				mirror(x, y, shade)
			}
		}
	}

	mirror(5, bottom+1, outline)
	mirror(4, bottom+1, outline)

	var filled [N][N]bool
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			filled[y][x] = grid[y][x] != nil
		}
	}
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			if filled[y][x] {
				continue
			}
			near := false
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx >= 0 && nx < N && ny >= 0 && ny < N && filled[ny][nx] {
					near = true
					break
				}
			}
			if near {
				put(x, y, outline)
			}
		}
	}

	eyeY := int(math.Round(cy - ry*0.2))
	look := pose.Look
	for _, side := range []int{-1, 1} {
		ex0 := 9 + look
		if side < 0 {
			ex0 = 5 + look
		}
		if pose.Blink {
			put(ex0, eyeY, ink)
			put(ex0+1, eyeY, ink)
		} else {
			put(ex0, eyeY-1, ink)
			put(ex0+1, eyeY-1, ink)
			put(ex0, eyeY, ink)
			put(ex0+1, eyeY, ink)
			sx := ex0
			if look > 0 {
				sx++
			}
			put(sx, eyeY-1, white)
		}
	}
	orBody := func(p *rgb) rgb {
		if p == nil {
			return body
		}
		return *p
	}
	put(4, eyeY+1, mix(orBody(at(4, eyeY+1)), blush, 0.7))
	put(11, eyeY+1, mix(orBody(at(11, eyeY+1)), blush, 0.7))
	if s.mouth {
		put(7, eyeY+1, mix(orBody(at(7, eyeY+1)), ink, 0.45))
		put(8, eyeY+1, mix(orBody(at(8, eyeY+1)), ink, 0.45))
	}

	var out [N][N]string
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			if grid[y][x] != nil {
				out[y][x] = toHex(*grid[y][x])
			}
		}
	}
	return out
}

// PoseAt mirrors agent-badge's animation: a slow breath at rest with the odd
// blink; while working, no breathing but looking around and little hops.
func PoseAt(seed uint32, tick int, working bool) Pose {
	period := 6
	if working {
		period = 3
	}
	breathe := (tick/period)%2 == 1
	blink := (int(seed)+tick*7919)%37 == 0
	if !working {
		return Pose{Breathe: breathe, Blink: blink}
	}
	phase := (tick / 8) % 4
	look := 0
	if phase == 1 {
		look = -1
	} else if phase == 3 {
		look = 1
	}
	return Pose{Blink: blink, Look: look, Hop: tick%6 < 2}
}

// Cell is one half-block character with its colours; empty cells have no
// colours and render as a space.
type Cell struct {
	Char   string
	Top    string // colour of the upper pixel, "" if transparent
	Bottom string // colour of the lower pixel, "" if transparent
}

// Rows renders the grid as 8 rows of 16 half-block cells. The caller paints
// each cell: "▀" with Top as foreground and Bottom as background, or when
// only one half is filled, "▀"/"▄" with just a foreground.
func Rows(px [N][N]string) [N / 2][N]Cell {
	var rows [N / 2][N]Cell
	for r := 0; r < N/2; r++ {
		for x := 0; x < N; x++ {
			t, b := px[2*r][x], px[2*r+1][x]
			switch {
			case t == "" && b == "":
				rows[r][x] = Cell{Char: " "}
			case b == "":
				rows[r][x] = Cell{Char: "▀", Top: t}
			case t == "":
				rows[r][x] = Cell{Char: "▄", Bottom: b}
			default:
				rows[r][x] = Cell{Char: "▀", Top: t, Bottom: b}
			}
		}
	}
	return rows
}

// SmallRows renders the sprite at half size, 8 columns by 4 rows, for places
// where the full creature would not fit. Each 2x2 block becomes the colour
// most of its pixels share, so the silhouette and body colour survive.
func SmallRows(px [N][N]string) [N / 4][N / 2]Cell {
	var small [N / 2][N / 2]string
	for y := 0; y < N/2; y++ {
		for x := 0; x < N/2; x++ {
			counts := map[string]int{}
			var best string
			for _, c := range []string{px[2*y][2*x], px[2*y][2*x+1], px[2*y+1][2*x], px[2*y+1][2*x+1]} {
				if c == "" {
					continue
				}
				counts[c]++
				if best == "" || counts[c] > counts[best] {
					best = c
				}
			}
			if len(counts) > 0 && counts[best] < 2 {
				best = "" // a lone pixel in a mostly empty block is noise
			}
			small[y][x] = best
		}
	}
	var rows [N / 4][N / 2]Cell
	for r := 0; r < N/4; r++ {
		for x := 0; x < N/2; x++ {
			t, b := small[2*r][x], small[2*r+1][x]
			switch {
			case t == "" && b == "":
				rows[r][x] = Cell{Char: " "}
			case b == "":
				rows[r][x] = Cell{Char: "▀", Top: t}
			case t == "":
				rows[r][x] = Cell{Char: "▄", Bottom: b}
			default:
				rows[r][x] = Cell{Char: "▀", Top: t, Bottom: b}
			}
		}
	}
	return rows
}
