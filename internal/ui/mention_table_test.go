// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT
// This file is part of the Den API conformance suite; the MIT licence text
// is in internal/api/denconformance/README.md.

// The mention table Den's routing and colouring and Drey's dock chips are
// pinned to: a copy of the conformance suite's mention.go at Drey #99
// (commit baa3ceb, package hash
// ae9d364ae500a55b7018ca29750530dc188284389f75f8e6336550ab1af6c521). Only
// the package, the names (Hit is tableHit, MentionCase tableCase,
// MentionCases tableCases), the licence pointer and this paragraph differ;
// the cases are byte for byte the suite's. When Den pins the suite with
// #99 in it (after #164, which its overlay link subtest needs), this copy
// goes and TestMentionTable reads denconformance.MentionCases.

package ui

// tableHit is one mention found in a text: the agent's name as the list gives it, the rune offset of the "@",
// and the rune offset just past the name (the punctuation that ended it is not part of it).
type tableHit struct {
	Name       string
	Start, End int
}

// tableCase is one input of the mention rule that Den's routing and a shell's colouring both follow.
// It is for "@" mentions only: a name written bare, without the "@", is not part of this rule.
//
// The rule, in words:
//   - An "@" starts a mention when the rune before it (a whole code point) is absent, or is not a Unicode
//     letter (category L), a Unicode number (category N), "_", "-" or "@".
//   - After the "@" comes one of Names. Names are compared with ASCII letters folded only: A-Z fold to a-z;
//     every other rune must match exactly (no Unicode case folding: a Kelvin sign is not a "k", a long s is
//     not an "s", "STRASSE" is not "straße"). The longest name is tried first.
//   - The name ends there only if the next rune is the end of the text, white space, "@", or one of
//     , : ; . ! ? ) ] } " U+201D ' U+2019 U+2026. Anything else after it (a letter, a digit, "-") means no
//     mention, and a shorter name is then tried.
//   - If two names are equal after the same ASCII fold, no name is known and nothing is a mention.
//
// Offsets are in runes.
type tableCase struct {
	Text  string
	Names []string
	Want  []tableHit
}

// tableCases is the table both sides are pinned to.
var tableCases = []tableCase{
	{Text: "ask @Lemur, now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur: now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur; now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur. now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur! now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur? now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur) now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur] now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur} now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur\" now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur\u201D now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur' now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur\u2019 now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur\u2026 now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "@Lemur", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}}},
	{Text: "ask @Lemur now", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur\tnow", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "ask @Lemur\nnow", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 4, 10}}},
	{Text: "(@Lemur)", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 1, 7}}},
	{Text: "@Lemur's turn", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}}},
	{Text: "@Lemur@host", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}}},
	{Text: "@@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "a_@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "a@Lemur.", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "1@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "-@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "\u00E9@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "\u2026@Lemur", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 1, 7}}},
	{Text: "\U0001F600 @Lemur, go", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 2, 8}}},
	{Text: "\U0001D400@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "\U0001D7CE@Lemur", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "\U0001D400 @Lemur", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 2, 8}}},
	{Text: "@Mossy Lemur, rebase", Names: []string{"Lemur", "Mossy Lemur"}, Want: []tableHit{{"Mossy Lemur", 0, 12}}},
	{Text: "@Mossy Lemur", Names: []string{"Mossy Lemur"}, Want: []tableHit{{"Mossy Lemur", 0, 12}}},
	{Text: "@backend-engineer-lead", Names: []string{"backend-engineer"}, Want: nil},
	{Text: "@backend-engineer2", Names: []string{"backend-engineer"}, Want: nil},
	{Text: "@Lemurs", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "@backend-engineer, go", Names: []string{"backend", "backend-engineer"}, Want: []tableHit{{"backend-engineer", 0, 17}}},
	{Text: "@backend-engineer2", Names: []string{"backend", "backend-engineer"}, Want: nil},
	{Text: "@Lemur", Names: []string{"Lemur", "lemur"}, Want: nil},
	{Text: "@lemur,", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}}},
	{Text: "@LEMUR!", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}}},
	{Text: "@Lemur, @Otter: go", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}, {"Otter", 8, 14}}},
	{Text: "@Lemur and @Otter.", Names: []string{"Lemur", "Otter"}, Want: []tableHit{{"Lemur", 0, 6}, {"Otter", 11, 17}}},
	{Text: "@Lemur", Names: nil, Want: nil},
	{Text: "no mention here, @nobody.", Names: []string{"Lemur", "Otter"}, Want: nil},
	{Text: "@\u212Aate,", Names: []string{"Kate"}, Want: nil},
	{Text: "@Kate,", Names: []string{"Kate", "\u212Aate"}, Want: []tableHit{{"Kate", 0, 5}}},
	{Text: "@\u212Aate,", Names: []string{"Kate", "\u212Aate"}, Want: []tableHit{{"\u212Aate", 0, 5}}},
	{Text: "@\u0130STANBUL,", Names: []string{"istanbul"}, Want: nil},
	{Text: "@STRASSE,", Names: []string{"stra\u00DFe"}, Want: nil},
	{Text: "@STRA\u00DFE,", Names: []string{"stra\u00DFe"}, Want: []tableHit{{"stra\u00DFe", 0, 7}}},
	{Text: "@\u0141UKASZ,", Names: []string{"\u0142ukasz"}, Want: nil},
	{Text: "@\u0142ukasz,", Names: []string{"\u0142ukasz"}, Want: []tableHit{{"\u0142ukasz", 0, 7}}},
	{Text: "@Mo\u017F\u017F,", Names: []string{"Moss"}, Want: nil},
	{Text: "@MOSS,", Names: []string{"Moss"}, Want: []tableHit{{"Moss", 0, 5}}},
}
