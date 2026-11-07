// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// StartedHash is the text_sha256 (v1) of the close card the settled rows
// are about: kind "close", agent "Mossy Lemur", no tool, title "Close
// Mossy Lemur?", no note, body "Mossy Lemur · idle". A started card keeps
// it: started and by are not in the hash.
const StartedHash = "370cf5d7f276a3ae2d21a8d763a513b11e63acedc5c6040dc66c632937a66573"

// SettledCase is one thing Den sends about a card whose answer started its
// action, or about how a card settled, and what a console takes from it.
type SettledCase struct {
	Name string
	// Shape is what JSON is: "event" (a card event's data), "card" (a
	// card in the snapshot or from card.get, without the nonce) or
	// "refusal" (card.answer's error code).
	Shape string
	JSON  string
	// DenSends is false for a value Den never sends, which a console must
	// still read so (an outcome it does not know).
	DenSends bool
	// Running: the card is listed and its action runs; offer no answer.
	Running bool
	// Final: the card is settled, and nothing more comes about it.
	Final bool
	// Result, By and Outcome are what the console takes: Outcome as it
	// reads it, an unknown value as "lost", "" for none.
	Result, By, Outcome string
}

// SettledCases is the table both sides are pinned to.
var SettledCases = []SettledCase{
	{Name: "a started card's changed event", Shape: "event", DenSends: true, Running: true, By: "console",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","change":"changed","text_sha256":"` + StartedHash + `","started":true,"by":"console"}`},
	{Name: "a card Den's own answer started", Shape: "event", DenSends: true, Running: true, By: "den",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","change":"changed","text_sha256":"` + StartedHash + `","started":true,"by":"den"}`},
	{Name: "a started card in the snapshot", Shape: "card", DenSends: true, Running: true, By: "console",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","tool":"","title":"Close Mossy Lemur?","note":"","class":"destructive","step":1,"body":"Mossy Lemur · idle","text_sha256":"` + StartedHash + `","truncated":false,"verbs":[],"dock_answers":false,"started":true,"by":"console"}`},
	{Name: "an answer on a started card", Shape: "refusal", DenSends: true, Running: true, JSON: `"not_found"`},
	{Name: "its action was done", Shape: "event", DenSends: true, Final: true, Result: "allowed", By: "console", Outcome: "done",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","change":"settled","result":"allowed","by":"console","outcome":"done"}`},
	{Name: "its action failed", Shape: "event", DenSends: true, Final: true, Result: "allowed", By: "console", Outcome: "failed",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","change":"settled","result":"allowed","by":"console","outcome":"failed"}`},
	{Name: "Den lost track of its action", Shape: "event", DenSends: true, Final: true, Result: "allowed", By: "den", Outcome: "lost",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","change":"settled","result":"allowed","by":"den","outcome":"lost"}`},
	{Name: "an outcome a console does not know", Shape: "event", Final: true, Result: "allowed", By: "console", Outcome: "lost",
		JSON: `{"id":"c1","kind":"close","agent":"Mossy Lemur","change":"settled","result":"allowed","by":"console","outcome":"partial"}`},
	{Name: "a permission card's settled event", Shape: "event", DenSends: true, Final: true, Result: "allowed", By: "console",
		JSON: `{"id":"p1","kind":"permission","agent":"Mossy Lemur","change":"settled","result":"allowed","by":"console"}`},
	{Name: "a card that settled without an answer", Shape: "event", DenSends: true, Final: true, Result: "gone",
		JSON: `{"id":"p1","kind":"permission","agent":"Mossy Lemur","change":"settled","result":"gone"}`},
}

// CardEndCase is one of Den's lines in the mirror (origin card) saying
// how the action of a card a console answered ended, and what a console
// takes from it.
type CardEndCase struct {
	Name string
	// Entry is the conversation entry's JSON as Den sends it, without id
	// and at.
	Entry string
	// Refused: a console skips the entry; Den never sends it.
	Refused bool
	// Ending is the ending a console keys on: "" (it all went), "failed"
	// or "lost".
	Ending string
	// Card is the card it is about: show the end once, with that card's
	// settled event.
	Card string
}

// CardEndCases is the table both sides are pinned to.
var CardEndCases = []CardEndCase{
	{Name: "a close that was done", Card: "c1", Entry: `{"origin":"card","speaker":"den","card":"c1","text":"Closed Mossy Lemur."}`},
	{Name: "a close that partly failed", Card: "c1", Ending: "failed", Entry: `{"origin":"card","speaker":"den","ending":"failed","card":"c1","text":"Close ended: closed Mossy Lemur; could not close Quiet Panda."}`},
	{Name: "a reopen that failed", Card: "c2", Ending: "failed", Entry: `{"origin":"card","speaker":"den","ending":"failed","card":"c2","text":"Reopen ended: could not reopen Comet Newt."}`},
	{Name: "a close Den lost track of", Card: "c1", Ending: "lost", Entry: `{"origin":"card","speaker":"den","ending":"lost","card":"c1","text":"Den lost track of the close of Mossy Lemur; check the agents."}`},
	{Name: "an ending outside the set", Refused: true, Entry: `{"origin":"card","speaker":"den","ending":"later","card":"c1","text":"Closed Mossy Lemur."}`},
	{Name: "a card ending on another origin", Refused: true, Entry: `{"origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s1","ending":"failed","text":"Report from Mossy Lemur."}`},
}
