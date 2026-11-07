package theme

import (
	"math"
	"strconv"
)

// The agent colour rule, shared with Drey (its missions.Allowed): a colour
// stands for an agent only when it reads on Drey's dark and mid surfaces,
// is no grey, no purple, sits in no band Drey uses for a meaning (God-mode
// red, waiting amber, done green) and is no near twin of one of Drey's
// meaning colours. Den draws an agent only in such a colour, so it is the
// same agent colour in Den and Drey. The thresholds and the order of the
// checks are part of the contract; convcases.ColorRuleCases pins them.

// Why a colour fails the rule: the first check it fails.
const (
	RuleNotHex   = "not_hex"
	RuleDark     = "dark"     // contrast below 7:1 against #0a0a0a
	RuleMid      = "mid"      // contrast below 3.5:1 against #4f4f4f
	RuleLight    = "light"    // OKLab L above 0.93
	RuleChroma   = "chroma"   // OKLCH C below 0.04
	RulePurple   = "purple"   // hue 280..348
	RuleBand     = "band"     // C at least 0.06 in a meaning band
	RuleDistance = "distance" // within 0.07 of a meaning colour in OKLab
)

// meaningColors are Drey's meaning colours an agent colour must stay apart
// from.
var meaningColors = []string{"#ff6b6d", "#ea9e9e", "#ec4899", "#e8b84a", "#34d399", "#66cb8e"}

// AgentColor reports whether hex, "#" and six lower-case hex digits, may
// stand for an agent, and else the first check it fails. Nothing is ever
// nudged into passing.
func AgentColor(hex string) (bool, string) {
	c, ok := lowerHex(hex)
	if !ok {
		return false, RuleNotHex
	}
	m := measure(c)
	switch {
	case m.contrastDark < 7.0:
		return false, RuleDark
	case m.contrastMid < 3.5:
		return false, RuleMid
	case m.l > 0.93:
		return false, RuleLight
	case m.c < 0.04:
		return false, RuleChroma
	case m.h >= 280 && m.h <= 348:
		return false, RulePurple
	case m.c >= 0.06 && (m.h >= 350 || m.h <= 50 || (m.h >= 55 && m.h <= 110) || (m.h >= 140 && m.h <= 178)):
		return false, RuleBand
	}
	for _, mc := range meaningColors {
		r, _ := lowerHex(mc)
		if oklabDistance(c, r) < 0.07 {
			return false, RuleDistance
		}
	}
	return true, ""
}

// agentColorOK is AgentColor's answer alone.
func agentColorOK(hex string) bool {
	ok, _ := AgentColor(hex)
	return ok
}

// lowerHex is the bytes of "#rrggbb" in lower case only.
func lowerHex(hex string) ([3]float64, bool) {
	var out [3]float64
	if len(hex) != 7 || hex[0] != '#' {
		return out, false
	}
	for _, r := range hex[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return out, false
		}
	}
	for i := range out {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		out[i] = float64(v)
	}
	return out, true
}

type colorMeasure struct {
	contrastDark, contrastMid float64
	l, c, h                   float64 // OKLCH, h in degrees in [0, 360)
}

func measure(c [3]float64) colorMeasure {
	dark, _ := lowerHex("#0a0a0a")
	mid, _ := lowerHex("#4f4f4f")
	l, a, b := oklab(c)
	h := math.Atan2(b, a) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return colorMeasure{
		contrastDark: contrast(c, dark),
		contrastMid:  contrast(c, mid),
		l:            l,
		c:            math.Hypot(a, b),
		h:            h,
	}
}

// linear is an sRGB byte as linear light.
func linear(v float64) float64 {
	x := v / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// relLuminance is WCAG's relative luminance.
func relLuminance(c [3]float64) float64 {
	return 0.2126*linear(c[0]) + 0.7152*linear(c[1]) + 0.0722*linear(c[2])
}

// contrast is WCAG's contrast ratio of two colours.
func contrast(x, y [3]float64) float64 {
	a, b := relLuminance(x), relLuminance(y)
	return (max(a, b) + 0.05) / (min(a, b) + 0.05)
}

// oklab is Björn Ottosson's OKLab of an sRGB colour.
func oklab(c [3]float64) (float64, float64, float64) {
	r, g, b := linear(c[0]), linear(c[1]), linear(c[2])
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s
}

func oklabDistance(x, y [3]float64) float64 {
	l1, a1, b1 := oklab(x)
	l2, a2, b2 := oklab(y)
	return math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (b1-b2)*(b1-b2))
}
