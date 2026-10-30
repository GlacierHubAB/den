// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// AnswerCase is one console verb on one card under the levels: whether
// card.answer takes it (Code "" when taken), and whether the card lists
// the verb (Listed). The class and step are the card's, as inside its v2
// hash. The listing sets the lock and the powers aside (they are refused at
// answer time only), so a row can be Listed and still refused
// powers_locked. "deny" is always taken while the console has its powers.
// The class limit (rate_limited) is not in these rows.
type AnswerCase struct {
	Name string
	// The state: the console's powers, the human's /api lock, God mode,
	// the start confirm, the kept ids held, and the classes lowered for
	// this Den session (class to level).
	Powers, Locked, God, Confirmed bool
	Kept                           []string
	Lowered                        map[string]int
	// The card: its class and step, the kept id that may cover its allow,
	// whether God mode makes it Den's alone, and whether a console raises
	// its kind itself (never allowed from a console).
	Class      string
	Step       int
	KeptID     string
	GodTTY     bool
	SelfRaised bool
	Verb       string
	// What Den does: card.answer's code, and whether card.get lists the
	// verb.
	Code   string
	Listed bool
}

// allKept is every kept id.
var allKept = []string{"permission", "plan", "close", "budget", "restore", "merge", "cleanup", "mission_update", "always"}

// AnswerCases is the table both sides are pinned to. Den's defaults:
// routine 4, destructive 1, code 1, desktop 0, outward 0.
var AnswerCases = []AnswerCase{
	{Name: "a read-only tool, within routine", Powers: true, Confirmed: true, Class: "routine", Step: 3, KeptID: "permission", Verb: "allow", Listed: true},
	{Name: "a command, within code", Powers: true, Confirmed: true, Class: "code", Step: 1, KeptID: "permission", Verb: "allow", Listed: true},
	{Name: "a close, within destructive", Powers: true, Confirmed: true, Class: "destructive", Step: 1, KeptID: "close", Verb: "allow", Listed: true},
	{Name: "a merge, above outward", Powers: true, Confirmed: true, Class: "outward", Step: 2, KeptID: "merge", GodTTY: true, Verb: "allow", Code: "below_level"},
	{Name: "deny on a card above its level", Powers: true, Confirmed: true, Class: "outward", Step: 2, KeptID: "merge", GodTTY: true, Verb: "deny", Listed: true},
	{Name: "a cleanup with ignored files, above destructive", Powers: true, Confirmed: true, Class: "destructive", Step: 2, KeptID: "cleanup", Verb: "allow", Code: "below_level"},
	{Name: "a den_only card, nothing kept", Powers: true, Confirmed: true, Class: "den_only", Step: 0, KeptID: "budget", GodTTY: true, Verb: "allow", Code: "below_level"},
	{Name: "a den_only card, kept", Powers: true, Confirmed: true, Kept: allKept, Class: "den_only", Step: 0, KeptID: "budget", GodTTY: true, Verb: "allow", Listed: true},
	{Name: "kept, before the start confirm", Powers: true, Kept: allKept, Class: "outward", Step: 2, KeptID: "merge", GodTTY: true, Verb: "allow", Code: "below_level"},
	{Name: "kept, its class at its default", Powers: true, Confirmed: true, Kept: allKept, Class: "outward", Step: 2, KeptID: "merge", GodTTY: true, Verb: "allow", Listed: true},
	{Name: "kept, its class lowered below the card's step", Powers: true, Confirmed: true, Kept: allKept, Lowered: map[string]int{"destructive": 0}, Class: "destructive", Step: 1, KeptID: "close", Verb: "allow", Code: "below_level"},
	{Name: "kept, its class lowered but not below the step", Powers: true, Confirmed: true, Kept: allKept, Lowered: map[string]int{"routine": 3}, Class: "routine", Step: 3, KeptID: "permission", Verb: "allow", Listed: true},
	{Name: "a class lowered below the card's step", Powers: true, Confirmed: true, Lowered: map[string]int{"code": 0}, Class: "code", Step: 1, KeptID: "permission", Verb: "allow", Code: "below_level"},
	{Name: "a self-raised kind, everything kept", Powers: true, Confirmed: true, Kept: allKept, Class: "den_only", Step: 0, GodTTY: true, SelfRaised: true, Verb: "allow", Code: "below_level"},
	{Name: "deny on a self-raised kind", Powers: true, Confirmed: true, Kept: allKept, Class: "den_only", Step: 0, GodTTY: true, SelfRaised: true, Verb: "deny", Listed: true},
	{Name: "always on a self-raised kind, always kept", Powers: true, Confirmed: true, Kept: allKept, Class: "den_only", Step: 0, GodTTY: true, SelfRaised: true, Verb: "always", Code: "below_level"},
	{Name: "always, kept", Powers: true, Confirmed: true, Kept: allKept, Class: "code", Step: 1, KeptID: "permission", Verb: "always", Listed: true},
	{Name: "always, not kept", Powers: true, Confirmed: true, Class: "code", Step: 1, KeptID: "permission", Verb: "always", Code: "below_level"},
	{Name: "locked", Powers: true, Locked: true, Confirmed: true, Class: "code", Step: 1, KeptID: "permission", Verb: "allow", Code: "powers_locked", Listed: true},
	{Name: "deny while locked", Powers: true, Locked: true, Confirmed: true, Class: "code", Step: 1, KeptID: "permission", Verb: "deny", Code: "powers_locked", Listed: true},
	{Name: "no powers", Confirmed: true, Class: "code", Step: 1, KeptID: "permission", Verb: "allow", Code: "powers_locked", Listed: true},
	{Name: "God mode, a kind only Den answers then", Powers: true, God: true, Confirmed: true, Kept: allKept, Class: "outward", Step: 2, KeptID: "merge", GodTTY: true, Verb: "allow", Code: "tty_only"},
	{Name: "God mode, always", Powers: true, God: true, Confirmed: true, Kept: allKept, Class: "code", Step: 1, KeptID: "permission", Verb: "always", Code: "tty_only"},
	{Name: "God mode, an everyday answer", Powers: true, God: true, Confirmed: true, Class: "routine", Step: 1, Verb: "allow", Listed: true},
}
