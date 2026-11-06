// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// FocusCase is one refused `focus` and what its error carries. A console
// keys only on Reason, error.data.reason: the code from a closed list.
// Message is an example of error.message, free text for a human that may
// change at any time; a console never keys on it. Data carries the reason
// only: never the target the app sent, nor what the compositor said.
type FocusCase struct {
	Name    string
	Code    string // error.code
	Reason  string // error.data.reason
	Message string // an example error.message: never keyed on
}

// FocusCases is the table both sides are pinned to: every reason, once.
var FocusCases = []FocusCase{
	{Name: "no agent by that id or name", Code: "not_found", Reason: "no_agent", Message: "no such agent"},
	{Name: "a name two agents share", Code: "not_found", Reason: "ambiguous", Message: "more than one agent has that name: name it by its id"},
	{Name: "an inbox item", Code: "unavailable", Reason: "inbox_item", Message: "open the Inbox in Den to see it"},
	{Name: "an agent on another machine", Code: "unavailable", Reason: "remote", Message: "it runs on another machine: open it in Den"},
	{Name: "an agent that has ended", Code: "unavailable", Reason: "not_running", Message: "that agent is gone"},
	{Name: "an agent waiting on its own terminal", Code: "unavailable", Reason: "waits_on_terminal", Message: "it waits on its terminal: open it in Den"},
	{Name: "the human typed in Den just now", Code: "unavailable", Reason: "human_typing", Message: "the human is typing in Den"},
	{Name: "a second focus within 3 s", Code: "unavailable", Reason: "rate_limited", Message: "too many focus requests"},
	{Name: "Den could not take it now", Code: "unavailable", Reason: "busy", Message: "Den is busy"},
	{Name: "Den's UI did not answer in time", Code: "unavailable", Reason: "no_answer", Message: "Den did not answer"},
	{Name: "no window for the agent", Code: "unavailable", Reason: "no_window", Message: "no terminal window found for Mossy Lemur"},
	{Name: "the compositor did not focus it", Code: "unavailable", Reason: "compositor_error", Message: "hyprctl: no such window"},
}
