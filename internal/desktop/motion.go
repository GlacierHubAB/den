package desktop

import (
	"math"
	"math/rand/v2"
)

// Human-like pointer paths, a port of the generator in the research demo
// (demo/motion.py): a slightly curved cubic Bézier, timed with the
// minimum-jerk profile, lasting a Fitts-style duration, with a small
// overshoot and a corrective glide on long moves and a hand tremor that
// fades out on arrival. The last sample is always exactly the target.

// overshootMinDist is the shortest move that overshoots, in pixels.
const overshootMinDist = 260.0

// Sample is one point of a glide, t seconds from its start.
type Sample struct{ T, X, Y float64 }

// Rect is a rectangle in global logical pixels: a glide never leaves it.
type Rect struct {
	X, Y, W, H int
}

// Clamp is the nearest point inside the rectangle.
func (r Rect) Clamp(x, y float64) (float64, float64) {
	x = math.Max(float64(r.X), math.Min(x, float64(r.X+r.W-1)))
	y = math.Max(float64(r.Y), math.Min(y, float64(r.Y+r.H-1)))
	return x, y
}

// Contains reports whether a point lies inside it.
func (r Rect) Contains(x, y float64) bool {
	return x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H)
}

func minJerk(s float64) float64 {
	s = math.Min(1, math.Max(0, s))
	return s * s * s * (10 - 15*s + 6*s*s)
}

// glideDuration is Fitts-style: longer moves take longer, but not
// proportionally.
func glideDuration(dist, speed float64) float64 {
	return (0.20 + 0.16*math.Log2(1+dist/40)) / speed
}

// Glide is the samples, at hz a second, from start to end, every point
// clamped into bounds (start and end too).
func Glide(start, end [2]float64, bounds Rect, r *rand.Rand, hz float64) []Sample {
	ax, ay := bounds.Clamp(start[0], start[1])
	bx, by := bounds.Clamp(end[0], end[1])
	const curve, jitter, speedVar = 0.16, 1.2, 0.08
	// Every draw happens up front, in a fixed order: a seed fixes the path.
	sign := 1.0
	if r.Float64() < 0.5 {
		sign = -1
	}
	c1 := sign * curve * (0.7 + r.Float64()*0.6)
	flip := 1.0
	c2 := sign * curve * (0.2 + r.Float64()*0.6)
	if r.Float64() < 0.25 {
		flip = -1
	}
	c2 *= flip
	overK, corrS := 0.035+r.Float64()*0.04, 0.16+r.Float64()*0.08
	f1, f2 := 6+r.Float64()*3, 11+r.Float64()*4
	ph1, ph2, ph3 := r.Float64()*2*math.Pi, r.Float64()*2*math.Pi, r.Float64()*2*math.Pi
	speed := 1 + (r.Float64()*2-1)*speedVar
	dx, dy := bx-ax, by-ay
	dist := math.Hypot(dx, dy)
	if dist < 1e-9 {
		return []Sample{{0, bx, by}}
	}
	ux, uy := dx/dist, dy/dist
	nx, ny := -uy, ux
	tMain, tCorr := glideDuration(dist, speed), 0.0
	tipX, tipY := bx, by
	if dist > overshootMinDist {
		tipX, tipY = bx+ux*dist*overK, by+uy*dist*overK
		tMain, tCorr = tMain*0.9, corrS
	}
	ex, ey := tipX-ax, tipY-ay
	length := math.Hypot(ex, ey)
	p1x, p1y := ax+ex*0.28+nx*length*c1, ay+ey*0.28+ny*length*c1
	p2x, p2y := ax+ex*0.72+nx*length*c2, ay+ey*0.72+ny*length*c2
	at := func(t float64) (float64, float64) {
		if t >= tMain+tCorr {
			return bx, by
		}
		if t > tMain {
			k := (t - tMain) / tCorr
			k = k * k * (3 - 2*k)
			return tipX + (bx-tipX)*k, tipY + (by-tipY)*k
		}
		prog := t / tMain
		u := minJerk(prog)
		w0, w1, w2, w3 := (1-u)*(1-u)*(1-u), 3*(1-u)*(1-u)*u, 3*(1-u)*u*u, u*u*u
		x := w0*ax + w1*p1x + w2*p2x + w3*tipX
		y := w0*ay + w1*p1y + w2*p2y + w3*tipY
		amp := jitter * math.Min(1, prog*6) * math.Pow(1-prog, 1.3)
		wob := amp * (math.Sin(2*math.Pi*f1*t+ph1) + 0.5*math.Sin(2*math.Pi*f2*t+ph2))
		a := amp * 0.4 * math.Sin(2*math.Pi*f2*t+ph3)
		return x + nx*wob + ux*a, y + ny*wob + uy*a
	}
	total := tMain + tCorr
	n := int(math.Max(1, math.Ceil(total*hz)))
	out := make([]Sample, 0, n+1)
	for i := 0; i <= n; i++ {
		t := math.Min(total, float64(i)/hz)
		if i == n {
			t = total
		}
		x, y := at(t)
		x, y = bounds.Clamp(x, y)
		out = append(out, Sample{t, x, y})
	}
	out[len(out)-1] = Sample{total, bx, by}
	return quantize(out)
}

// quantize keeps whole pixels and drops repeats: the compositor puts the
// cursor on integer pixels, so a repeat would only cost a call.
func quantize(in []Sample) []Sample {
	var out []Sample
	for _, s := range in {
		s.X, s.Y = math.Round(s.X), math.Round(s.Y)
		if len(out) == 0 || out[len(out)-1].X != s.X || out[len(out)-1].Y != s.Y {
			out = append(out, s)
		}
	}
	return out
}
