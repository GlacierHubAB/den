package grants

// The answer check: whether a console's verb on a card is taken, from
// Den's own record of the card and the levels now. "deny" is a lowering and
// always taken; "allow" needs the class's level at the card's step, or an
// active kept entry; "always" needs an active kept "always". A kind a
// console raises itself, when allowing it would make Den less careful, is
// never allowed from a console, by level or by kept.

// The refusals of an answer.
const (
	PowersLocked = "powers_locked"
	TTYOnly      = "tty_only"
	BelowLevel   = "below_level"
)

// DenOnlyClass is the class of a card no level reaches.
const DenOnlyClass = "den_only"

// Kept ids: what a pairing that existed when the levels came kept answering
// (upgrade only), a closed set. Each is a card kind but "always", the verb.
const (
	KeptPermission    = "permission"
	KeptPlan          = "plan"
	KeptClose         = "close"
	KeptBudget        = "budget"
	KeptRestore       = "restore"
	KeptMerge         = "merge"
	KeptCleanup       = "cleanup"
	KeptMissionUpdate = "mission_update"
	KeptAlways        = "always"
)

// keptTable is every kept id with Den's label, in the read's order.
var keptTable = []struct{ id, label string }{
	{KeptPermission, "Allow a tool once"},
	{KeptPlan, "Approve a plan"},
	{KeptClose, "Close an agent"},
	{KeptBudget, "Keep going over a budget"},
	{KeptRestore, "Reopen a closed agent"},
	{KeptMerge, "Merge a branch"},
	{KeptCleanup, "Clean up a worktree"},
	{KeptMissionUpdate, "Change a mission"},
	{KeptAlways, "Answer \"always\" on a card"},
}

// KeptAll is every kept id, in order: what the upgrade grants.
func KeptAll() []string {
	out := make([]string, len(keptTable))
	for i, k := range keptTable {
		out[i] = k.id
	}
	return out
}

// KeptLabel is kept id's label, "" for no id.
func KeptLabel(id string) string {
	for _, k := range keptTable {
		if k.id == id {
			return k.label
		}
	}
	return ""
}

// Answer is one verb on one card, as Den's record has the card: never
// anything from the console's request but the verb.
type Answer struct {
	// Kept is the kept id that may cover an allow ("" for none).
	Kept string
	// Class and Step are the card's, as inside the hash the console
	// matched.
	Class string
	Step  int
	Verb  string
	// GodTTY: while God mode is on, only Den's window answers the kind.
	GodTTY bool
	// SelfRaised: a console can raise the kind itself, and allowing it
	// would make Den less careful; never allowed from a console.
	SelfRaised bool
}

// Allow is the answer check's steps in order: the lock, a hold or no
// powers; God mode; deny; allow by level or kept; always by kept. It
// returns "" when the answer is taken, else the refusal; the class limit
// is the caller's.
func Allow(st State, a Answer) string {
	switch {
	case st.Locked || st.Held || !st.Powers:
		return PowersLocked
	case st.God && (a.GodTTY || a.Verb == "always"):
		return TTYOnly
	case a.Verb == "deny":
		return ""
	case a.SelfRaised:
		return BelowLevel
	case a.Verb == "allow":
		if st.levelAt(a.Class) >= a.Step && a.Class != DenOnlyClass && a.Step > 0 {
			return ""
		}
		if st.keptActive(a.Kept, a.Class, a.Step) {
			return ""
		}
	case a.Verb == "always":
		if st.keptActive(KeptAlways, a.Class, a.Step) {
			return ""
		}
	}
	return BelowLevel
}

// Listed reports whether a console may give the verb now as a card lists
// it: Allow, with the lock, a hold and the powers set aside, which are
// answer-time refusals only (the read carries them), so they never change
// a card's listing.
func Listed(st State, a Answer) bool {
	st.Locked, st.Held, st.Powers = false, false, true
	return Allow(st, a) == ""
}

// levelAt is class id's effective level for an answer: its cap, then 0
// for desktop and outward in God mode.
func (st State) levelAt(id string) int {
	for _, c := range table {
		if c.id == id {
			if st.God && godZero[c.id] {
				return 0
			}
			return st.cap(c)
		}
	}
	return 0
}

// keptActive reports whether kept id covers a card of class at step now:
// held, after the start confirm, and the class not lowered below the step.
// Lowered is a cap below the class's default; a class at its default never
// stops a kept entry. A card no level reaches (den_only) has no step, so
// only the lock, a hold, God mode, the start confirm or removal stops it.
func (st State) keptActive(id, class string, step int) bool {
	if id == "" || !st.Confirmed || !st.holds(id) {
		return false
	}
	for _, c := range table {
		if c.id == class {
			if cp := st.cap(c); cp < c.def && cp < step {
				return false
			}
		}
	}
	return true
}

// holds reports whether st keeps id.
func (st State) holds(id string) bool {
	for _, k := range st.Kept {
		if k == id {
			return true
		}
	}
	return false
}
