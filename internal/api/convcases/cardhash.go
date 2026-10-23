// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// The card-hash table: a card's text_sha256 as card.get sends it, with
// the fields it is over. A console copies the file and checks it gets
// the same hash from the same fields; Den's tests check that Den sends
// these fields and this hash for the raw card each row notes.
//
// Every row is v1: SHA-256 of kind, agent, tool, title, note and body,
// one per line, over each field exactly as card.get sends it (the value
// Den cleaned, never the raw one). Rows for the mission kinds and for
// card hash v2 come with the Den code that makes them.

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
