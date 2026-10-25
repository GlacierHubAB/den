// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// The card-hash table: a card's text_sha256 as card.get sends it, with
// the fields it is over. A console copies the file and checks it gets
// the same hash from the same fields; Den's tests check that Den sends
// these fields and this hash for the raw card each row notes.
//
// CardHashCases are v1: SHA-256 of kind, agent, tool, title, note and
// body, one per line, over each field exactly as card.get sends it (the
// value Den cleaned, never the raw one). CardHashV2Cases, below, are card
// hash v2, the mission kinds' rows among them.

// CardHashCase is one card's fields as card.get sends them, and its hash.
type CardHashCase struct {
	Name                           string
	Kind, Agent, Tool, Title, Note string
	Body                           string
	TextSHA256                     string
}

// CardHashCases is the table both sides are pinned to.
var CardHashCases = []CardHashCase{
	{
		Name: "a plain card",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Body:       "go test ./...\nin /home/x/den",
		TextSHA256: "dc78fe9adb329276ff1d67b120d71b26730c4d2114e5565b91bbb200082e8f85",
	},
	{
		Name: "a plain card with a note",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "Busy Otter asked the same.",
		Body:       "go test ./...",
		TextSHA256: "3ef868aeedd8c2e15ff6519ceaf422a219d645332e413ba91adbf8a7dea958c8",
	},
	{
		// Raw agent: "Mossy\x07 Lemur\u202e" (a bell and a right-to-left
		// override): Den sends it with both removed.
		Name: "a name with controls and bidi, stripped",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Body:       "go test ./...",
		TextSHA256: "f8d571de5864da9cd54ce50a5d1c9b1e656750d58c6c1d6a02d85e3e6764c795",
	},
	{
		// Raw agent: "abcdefghij" nine times (90 runes): Den sends the
		// first 79 and "\u2026", 80 runes in all.
		Name: "an over-long name, capped",
		Kind: "permission", Agent: "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghi\u2026", Tool: "Bash", Title: "Run the tests?", Note: "",
		Body:       "go test ./...",
		TextSHA256: "fc8297aa1ff82b5d43298a2ec36b925a35e164d49f6dd3ab5507119568edd226",
	},
}

// Card hash v2: SHA-256 of kind, agent, tool, title, note, class, step,
// mission, criteria and body, one per line, the body last for every kind.
// step is a bare decimal; mission and criteria are "" on a card without
// them. Each criterion is its id, state, text and evidence joined by
// U+001F, the criteria joined by U+001E in card order. A console chooses
// v2 for its whole connection with "card_hash": 2 in hello.
//
// A row with Refused set has no hash: the hasher refuses its fields (a
// class outside the set, a step its class does not have, a newline before
// the body, a separator or newline inside a criterion), and a console's
// hasher must refuse it too.

// CardHashCriterion is one done criterion, as card hash v2 binds it.
type CardHashCriterion struct {
	ID, State, Text, Evidence string
}

// CardHashV2Case is one card's fields as card.get sends them on a v2
// connection, and its hash.
type CardHashV2Case struct {
	Name                           string
	Kind, Agent, Tool, Title, Note string
	Class                          string
	Step                           int
	Mission                        string
	Criteria                       []CardHashCriterion
	Body                           string
	TextSHA256                     string
	Refused                        bool
}

// CardHashV2Cases is the v2 table both sides are pinned to.
var CardHashV2Cases = []CardHashV2Case{
	{
		Name: "a command, code 1",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Class: "code", Step: 1, Mission: "",
		Body:       "go test ./...\nin /home/x/den",
		TextSHA256: "842e54b9b023f337cb309a4bc1f01ca27dda82c8524c1bec8fc6aa6332e5c0b8",
	},
	{
		Name: "a read inside the working directory, routine 3",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Read", Title: "Read README.md?", Note: "",
		Class: "routine", Step: 3, Mission: "",
		Body:       "/home/x/den/README.md",
		TextSHA256: "7c94b7b58fad6000abd8c6f159de95ed3b9d4adf07b6a06532ef83f772bdab6a",
	},
	{
		Name: "a write to the project's Claude settings, den_only",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Write", Title: "Write settings.json?", Note: "",
		Class: "den_only", Step: 0, Mission: "",
		Body:       "/home/x/den/.claude/settings.json",
		TextSHA256: "425333b4b1152913960cbb3b149cc84abd76845f5c43b857a5d2d72d299abfcd",
	},
	{
		Name: "a cleanup that deletes ignored files, destructive 2",
		Kind: "cleanup", Agent: "Mossy Lemur", Tool: "worktree_cleanup", Title: "Remove the worktree of feature?", Note: "",
		Class: "destructive", Step: 2, Mission: "",
		Body:       "Ignored files it deletes:\nnode_modules",
		TextSHA256: "495478d1748d6fb913a727d7d2f2ebef4a4f0a487ca785d22d010bbde0c879ba",
	},
	{
		Name: "a mission stop",
		Kind: "mission stop", Agent: "", Tool: "", Title: "Stop mission Ship v1?", Note: "",
		Class: "destructive", Step: 2, Mission: "Ship v1",
		Body:       "Two agents work on it.",
		TextSHA256: "ccbb0dfb94a59d213aafb7eb7371d560f8ddbae2380a02c785acc3ed74bbff05",
	},
	{
		Name: "a mission stop whose body ends like a mission line",
		Kind: "mission stop", Agent: "", Tool: "", Title: "Stop mission Ship v1?", Note: "",
		Class: "destructive", Step: 2, Mission: "",
		Body:       "Two agents work on it.\nShip v1",
		TextSHA256: "96245fd87757fd717e77084d461a1ef0ab6dc8a2e8b480a18d646ef6fbe8d213",
	},
	{
		Name: "a mission done with criteria",
		Kind: "mission done", Agent: "", Tool: "", Title: "Is mission Ship v1 done?", Note: "",
		Class: "den_only", Step: 0, Mission: "Ship v1",
		Criteria: []CardHashCriterion{
			{ID: "c1", State: "met", Text: "The tests pass", Evidence: "CI is green on dev"},
			{ID: "c2", State: "claimed", Text: "The docs say how", Evidence: ""},
		},
		Body:       "Every criterion is claimed.",
		TextSHA256: "572bf1078e9a1da0faae376bb902874f0574ffe255da9f46650c31709dd6a11a",
	},
	{
		Name: "v1's collision, first half: body x and y, mission z",
		Kind: "mission stop", Agent: "", Tool: "", Title: "Stop?", Note: "",
		Class: "destructive", Step: 2, Mission: "z",
		Body:       "x\ny",
		TextSHA256: "cd2ced27dfa75515afa00a4e2c980bdde34a6b1aa1c7e1f768aff8fcc8ff4bf0",
	},
	{
		Name: "v1's collision, second half: a newline in the mission",
		Kind: "mission stop", Agent: "", Tool: "", Title: "Stop?", Note: "",
		Class: "destructive", Step: 2, Mission: "y\nz",
		Body:    "x",
		Refused: true,
	},
	{
		Name: "a class outside the set",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Class: "admin", Step: 1, Mission: "",
		Body:    "go test ./...",
		Refused: true,
	},
	{
		Name: "a step code does not have",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Class: "code", Step: 2, Mission: "",
		Body:    "go test ./...",
		Refused: true,
	},
	{
		Name: "routine at step 0",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Class: "routine", Step: 0, Mission: "",
		Body:    "go test ./...",
		Refused: true,
	},
	{
		Name: "den_only at step 1",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "",
		Class: "den_only", Step: 1, Mission: "",
		Body:    "go test ./...",
		Refused: true,
	},
	{
		Name: "a newline in the title",
		Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run\nthe tests?", Note: "",
		Class: "code", Step: 1, Mission: "",
		Body:    "go test ./...",
		Refused: true,
	},
	{
		Name: "a record separator inside a criterion",
		Kind: "mission done", Agent: "", Tool: "", Title: "Done?", Note: "",
		Class: "den_only", Step: 0, Mission: "Ship v1",
		Criteria: []CardHashCriterion{
			{ID: "c1", State: "met", Text: "Tests\u001epass", Evidence: ""},
		},
		Body:    "b",
		Refused: true,
	},
}
