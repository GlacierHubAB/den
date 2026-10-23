// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

// Package convcases is the follow-up table that Den's mirror and a
// console's reading of it are both pinned to: conversation entries about
// work from a console turn, as Den sends them, and what a console takes
// from each. Den's tests check that Den sends these entries; a console
// copies the file and checks that it reads them so.
//
// What Den does around the entries (when a report goes with which card,
// the grace before a no_report line, no line for a failed card) is Den's
// own and tested in Den; docs/DREY-API.md says it in words.
package convcases

// OwnTurn is the receiving console's own turn in the table: it matches
// the turn of the console's own (self) origin console entries. OtherTurn
// is another console's.
const (
	OwnTurn   = "t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	OtherTurn = "t-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// FollowupCase is one entry and what a console takes from it.
type FollowupCase struct {
	Name string
	// Entry is the conversation entry's JSON as Den sends it, without id
	// and at.
	Entry string
	// Turn is the console turn the entry is about, "" for none: only "t-"
	// and 32 lowercase hex digits, and only on origin report or desktop
	// (and console, as before).
	Turn string
	// Own says the entry belongs under the console's own request: Turn is
	// OwnTurn.
	Own bool
	// Ending is the ending a console keys on, "" for a plain line: an
	// unknown value is a plain line.
	Ending string
}

// FollowupCases is the table both sides are pinned to.
var FollowupCases = []FollowupCase{
	{Name: "a report from the console's turn", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s1","text":"Report from Mossy Lemur."}`, Turn: OwnTurn, Own: true},
	{Name: "the orchestrator's reply to it", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"report","speaker":"orchestrator","agent":"Mossy Lemur","agent_id":"s1","text":"Mossy Lemur fixed the login test."}`, Turn: OwnTurn, Own: true},
	{Name: "a report from Den's own chat", Entry: `{"origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s2","text":"Report from Mossy Lemur."}`},
	{Name: "finished without a report", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s3","ending":"no_report","text":"No report from Mossy Lemur: it finished without one."}`, Turn: OwnTurn, Own: true, Ending: "no_report"},
	{Name: "finished without a report, a name Den will not show", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"report","speaker":"den","agent":"an agent","agent_id":"s4","ending":"no_report","text":"No report from an agent: it finished without one."}`, Turn: OwnTurn, Own: true, Ending: "no_report"},
	{Name: "a desktop report from the console's turn", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"desktop","speaker":"den","agent":"Quiet Panda","agent_id":"s5","text":"Report from Quiet Panda."}`, Turn: OwnTurn, Own: true},
	{Name: "a desktop agent finished without a report", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"desktop","speaker":"den","agent":"Quiet Panda","agent_id":"s6","ending":"no_report","text":"No report from Quiet Panda: it finished without one."}`, Turn: OwnTurn, Own: true, Ending: "no_report"},
	{Name: "a report about another console's turn", Entry: `{"turn":"t-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s7","text":"Report from Mossy Lemur."}`, Turn: OtherTurn},
	{Name: "past the report limits", Entry: `{"origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s1","text":"More reports from Mossy Lemur are in Den."}`},
	{Name: "not a turn's shape", Entry: `{"turn":"t-1","origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s1","text":"Report from Mossy Lemur."}`},
	{Name: "upper-case hex", Entry: `{"turn":"t-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s1","text":"Report from Mossy Lemur."}`},
	{Name: "an unknown ending", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"report","speaker":"den","agent":"Mossy Lemur","agent_id":"s1","ending":"later","text":"Report from Mossy Lemur."}`, Turn: OwnTurn, Own: true},
	{Name: "an ending on another speaker", Entry: `{"turn":"t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","origin":"report","speaker":"orchestrator","agent":"Mossy Lemur","agent_id":"s1","ending":"no_report","text":"Done."}`, Turn: OwnTurn, Own: true},
}
