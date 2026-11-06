// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// ColorAgent is one agent of a fleet: what Den knows of it and the
// `color` its fleet entry carries.
type ColorAgent struct {
	// ID is the agent's id, what a console keys the colour on.
	ID   string
	Name string
	// Host is the agent's host, "" for an agent of this machine.
	Host string
	// Drawn is the colour Den draws the agent in, as Den's colour record
	// holds it.
	Drawn string
	// Color is the fleet entry's `color`, "" when the entry has none.
	Color string
}

// ColorCase is one fleet and the colours its entries carry. A console
// takes `color` only from this field, by the agent's id, and only as
// "#" and six lower-case hex digits; it is Den's own display colour, not
// a closed set.
type ColorCase struct {
	Name   string
	Agents []ColorAgent
}

// ColorCases is the table both sides are pinned to.
var ColorCases = []ColorCase{
	{Name: "local agents, a remote one, two that share a name", Agents: []ColorAgent{
		{ID: "s1", Name: "Mossy Lemur", Drawn: "#ea9e9e", Color: "#ea9e9e"},
		{ID: "s2", Name: "Otter", Drawn: "#86c6e8", Color: "#86c6e8"},
		{ID: "s3", Name: "Remote Ray", Host: "devbox", Drawn: "#7fd3c4"},
		{ID: "s4", Name: "Quiet Panda", Drawn: "#a7d98c", Color: "#a7d98c"},
		{ID: "s5", Name: "Quiet Panda", Drawn: "#f0b07a", Color: "#f0b07a"},
	}},
	{Name: "a colour from the theme, in upper case", Agents: []ColorAgent{
		{ID: "s1", Name: "Mossy Lemur", Drawn: "#7AA2F7", Color: "#7aa2f7"},
	}},
	{Name: "a record that is not a colour", Agents: []ColorAgent{
		{ID: "s1", Name: "Mossy Lemur", Drawn: "red"},
		{ID: "s2", Name: "Otter", Drawn: "#86c6e"},
	}},
}
