// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// ColorRuleCase is one colour and the agent colour rule's answer for it
// (theme.AgentColor in Den, missions.Allowed in Drey): Allowed, or else
// Why, the first check it fails, in the rule's order: not_hex, dark,
// mid, light, chroma, purple, band, distance. Each edge row is clearly
// off its line, never on it to the last bit.
type ColorRuleCase struct {
	Hex     string
	Allowed bool
	Why     string
}

// ColorRuleCases is the table both sides are pinned to.
var ColorRuleCases = []ColorRuleCase{
	// The curated hues Den tops up from all pass.
	{Hex: "#a7d98c", Allowed: true}, // leaf
	{Hex: "#7fd3c4", Allowed: true}, // teal
	{Hex: "#86c6e8", Allowed: true}, // sky
	{Hex: "#9bb0f0", Allowed: true}, // periwinkle
	{Hex: "#a6b562", Allowed: true}, // olive
	{Hex: "#9fabc9", Allowed: true}, // slate, C 0.045
	{Hex: "#c9b89a", Allowed: true}, // sand, C 0.045

	// The hues the rule took out of Den's list.
	{Hex: "#ea9e9e", Why: "band"},   // rose, a meaning colour too
	{Hex: "#f0b07a", Why: "band"},   // apricot
	{Hex: "#e8d27a", Why: "band"},   // straw
	{Hex: "#66cb8e", Why: "band"},   // mint, a meaning colour too
	{Hex: "#b9a3f0", Why: "purple"}, // lilac
	{Hex: "#e39ad8", Why: "purple"}, // orchid
	{Hex: "#f0a8c0", Why: "band"},   // pink

	// Not "#" and six lower-case hex digits.
	{Hex: "#86C6E8", Why: "not_hex"},
	{Hex: "#86c6e", Why: "not_hex"},
	{Hex: "86c6e8", Why: "not_hex"},
	{Hex: "", Why: "not_hex"},

	// The checks, each from both sides.
	{Hex: "#4f4f4f", Why: "dark"},     // 2.4:1
	{Hex: "#f4705c", Why: "dark"},     // 6.9:1
	{Hex: "#e88018", Why: "mid"},      // 7.1:1 on dark, 2.9:1 on mid
	{Hex: "#7aa2f7", Why: "mid"},      // 3.25:1
	{Hex: "#b48fe0", Why: "mid"},      // 3.10:1, purple too
	{Hex: "#f874f8", Why: "mid"},      // 3.45:1
	{Hex: "#98a8e8", Allowed: true},   // 3.55:1
	{Hex: "#f0e8dc", Why: "light"},    // L 0.934
	{Hex: "#d8f84c", Allowed: true},   // L 0.926
	{Hex: "#c0c0c0", Why: "chroma"},   // a grey
	{Hex: "#acccc4", Why: "chroma"},   // C 0.036
	{Hex: "#b4d8c8", Allowed: true},   // C 0.044
	{Hex: "#b4bae8", Allowed: true},   // h 279.0
	{Hex: "#b0b4dc", Why: "purple"},   // h 280.5
	{Hex: "#e0a8c4", Why: "purple"},   // h 347.5
	{Hex: "#fc90c8", Allowed: true},   // h 348.6
	{Hex: "#fcb0d4", Allowed: true},   // h 349.5
	{Hex: "#f08cbc", Why: "band"},     // h 350.5
	{Hex: "#f89c68", Why: "band"},     // h 49.5
	{Hex: "#fc9454", Allowed: true},   // h 50.6
	{Hex: "#f6bc96", Allowed: true},   // h 54.5
	{Hex: "#f8b88c", Why: "band"},     // h 55.5
	{Hex: "#b8b830", Why: "band"},     // h 109.5
	{Hex: "#e8f24c", Allowed: true},   // h 110.6
	{Hex: "#50e010", Allowed: true},   // h 139.5
	{Hex: "#9cf08c", Why: "band"},     // h 140.5
	{Hex: "#24e8c8", Why: "band"},     // h 177.5
	{Hex: "#30ecce", Allowed: true},   // h 178.5
	{Hex: "#c4b48c", Allowed: true},   // in a band at C 0.057
	{Hex: "#c8ac84", Why: "band"},     // in a band at C 0.063
	{Hex: "#e8b84a", Why: "band"},     // a meaning colour, in its band
	{Hex: "#94ba60", Why: "distance"}, // 0.065 from a meaning colour
	{Hex: "#b8be7a", Allowed: true},   // 0.075 from one
}
