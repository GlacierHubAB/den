// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT

package convcases

// LowerCase is one grants.lower and what Den does with it.
type LowerCase struct {
	Name string
	// Holder is who sends it: "" the console paired now, with its powers;
	// "unproved" a connection with no proof; "other" a proved connection
	// of a console that is not the one paired now; "locked" the console
	// paired now while the human's /api lock holds; "nopowers" the console
	// paired now without its powers.
	Holder string
	// Before is the session caps already held, level by class.
	Before map[string]int
	// Request is grants.lower's params; "$seq" stands for the read's seq
	// now and "$old" for one less.
	Request string
	// Code is the refusal, "" when it is taken: then the read's seq rose
	// and the class's session holds the level. A refusal changes nothing:
	// the same seq, the same read, no grants event.
	Code string
	// Class and Cap are the class the row is about and its cap after it,
	// min(den_level, session.level): what a console checks, never the
	// effective level, which a lock or no powers puts at 0.
	Class string
	Cap   int
}

// LowerCases is the table both sides are pinned to. The defaults: routine
// 4, destructive 1, code 1, desktop 0, outward 0.
var LowerCases = []LowerCase{
	{Name: "a lowering", Request: `{"class":"routine","level":2,"seq":$seq}`, Class: "routine", Cap: 2},
	{Name: "a lowering to 0", Request: `{"class":"code","level":0,"seq":$seq}`, Class: "code", Cap: 0},
	{Name: "a raise", Request: `{"class":"code","level":2,"seq":$seq}`, Code: "not_lower", Class: "code", Cap: 1},
	{Name: "the same level", Request: `{"class":"routine","level":4,"seq":$seq}`, Code: "not_lower", Class: "routine", Cap: 4},
	{Name: "a class at 0 already", Request: `{"class":"desktop","level":0,"seq":$seq}`, Code: "not_lower", Class: "desktop", Cap: 0},
	{Name: "above its session, below its Den level", Before: map[string]int{"routine": 2}, Request: `{"class":"routine","level":3,"seq":$seq}`, Code: "not_lower", Class: "routine", Cap: 2},
	{Name: "a re-send of a lowering Den holds", Before: map[string]int{"routine": 2}, Request: `{"class":"routine","level":2,"seq":$seq}`, Code: "not_lower", Class: "routine", Cap: 2},
	{Name: "below its session", Before: map[string]int{"routine": 2}, Request: `{"class":"routine","level":1,"seq":$seq}`, Class: "routine", Cap: 1},
	{Name: "a stale seq", Request: `{"class":"routine","level":2,"seq":$old}`, Code: "stale", Class: "routine", Cap: 4},
	{Name: "a class Den does not have", Request: `{"class":"admin","level":0,"seq":$seq}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "a level past 5", Request: `{"class":"routine","level":6,"seq":$seq}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "a level below 0", Request: `{"class":"routine","level":-1,"seq":$seq}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "a level as a string", Request: `{"class":"routine","level":"2","seq":$seq}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "a level with a fraction", Request: `{"class":"routine","level":2.5,"seq":$seq}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "a null level", Request: `{"class":"routine","level":null,"seq":$seq}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "no seq", Request: `{"class":"routine","level":2}`, Code: "bad_request", Class: "routine", Cap: 4},
	{Name: "the kept form", Request: `{"kept":"Close","seq":$seq}`, Code: "unsupported", Class: "routine", Cap: 4},
	{Name: "a connection with no proof", Holder: "unproved", Request: `{"class":"routine","level":2,"seq":$seq}`, Code: "forbidden", Class: "routine", Cap: 4},
	{Name: "a console not paired now", Holder: "other", Request: `{"class":"routine","level":2,"seq":$seq}`, Code: "forbidden", Class: "routine", Cap: 4},
	{Name: "a lowering while locked", Holder: "locked", Request: `{"class":"routine","level":2,"seq":$seq}`, Class: "routine", Cap: 2},
	{Name: "a lowering without powers", Holder: "nopowers", Request: `{"class":"routine","level":2,"seq":$seq}`, Class: "routine", Cap: 2},
}
