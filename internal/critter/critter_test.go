package critter

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSpriteIsDeterministicAndBounded(t *testing.T) {
	c := Colors{Bodies: []string{"#66cb8e", "#e8b84a", "#ea9e9e", "#f8f8f8"}, Dark: "#000000", Blush: "#f4c4c4"}
	for _, id := range []string{"11111111-2222-4333-8444-555555555501", "11111111-2222-4333-8444-555555555503", "11111111-2222-4333-8444-555555555504", "11111111-2222-4333-8444-555555555502"} {
		seed := Hash(id)
		a := Pixels(seed, c, Pose{})
		b := Pixels(seed, c, Pose{})
		if a != b {
			t.Errorf("%s: not deterministic", id[:8])
		}
		filled := 0
		for y := 0; y < N; y++ {
			for x := 0; x < N; x++ {
				if a[y][x] != "" {
					filled++
				}
			}
		}
		if filled < 60 || filled > 220 {
			t.Errorf("%s: %d filled pixels looks wrong", id[:8], filled)
		}
		if os.Getenv("DEN_SHOW") != "" {
			fmt.Println(id[:8])
			for _, row := range Rows(a) {
				var sb strings.Builder
				for _, cell := range row {
					if cell.Char == " " {
						sb.WriteString("  ")
					} else if cell.Top != "" && cell.Bottom != "" {
						sb.WriteString("██")
					} else {
						sb.WriteString(cell.Char + cell.Char)
					}
				}
				fmt.Println(sb.String())
			}
		}
	}
	p := PoseAt(1, 0, false)
	if p.Hop || p.Look != 0 {
		t.Errorf("resting pose = %+v", p)
	}
	if w := PoseAt(1, 8, true); w.Look != -1 || w.Breathe {
		t.Errorf("working pose = %+v", w)
	}
}

func TestOctantRowsCoverTheSprite(t *testing.T) {
	seed := Hash("octant-test")
	px := Pixels(seed, Colors{}, Pose{})
	ink := Ink(seed, Colors{})
	rows := OctantRows(px, ink)
	painted, inked := 0, 0
	for _, row := range rows {
		for _, c := range row {
			if c.Char != " " {
				painted++
				if c.Top == "" {
					t.Errorf("painted cell without a colour: %+v", c)
				}
				if c.Bottom == ink {
					inked++
				}
			}
		}
	}
	if painted < 8 || inked == 0 {
		t.Errorf("painted %d cells, %d with ink", painted, inked)
	}
	// The ink the generator used must be the ink the renderer looks for.
	found := false
	for _, row := range px {
		for _, v := range row {
			if v == ink {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("ink %s not present in the sprite", ink)
	}
	if octants[0b00000001] != 0x1CEA8 || octants[0b11111111] != '█' || octants[0b01010101] != '▌' || octants[0b00000111] < 0x1CD00 {
		t.Errorf("octant table is off: %x %x %x %x", octants[1], octants[255], octants[0x55], octants[7])
	}
}
