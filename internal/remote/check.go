package remote

import "github.com/GlacierHubAB/den/internal/fleet"

// checkAgents is what Den keeps of a snapshot's agents: each one checked
// (checkMeta), rechecked against the host's policy and the one it applies
// (overreach when it sent what they do not allow) and sanitised. flags says
// what was dropped, once per kind.
func checkAgents(in []fleet.Meta, h Host, applied Policy) (agents []fleet.Meta, flags []string, overreach bool) {
	agents = make([]fleet.Meta, 0, len(in))
	seen := map[string]bool{}
	for _, a := range in {
		a, why, ok := checkMeta(a)
		for _, f := range why {
			if !seen[f] {
				seen[f] = true
				flags = append(flags, f)
			}
		}
		if !ok {
			continue
		}
		a, dropped := recheck(a, h, applied)
		overreach = overreach || dropped
		agents = append(agents, cleanMeta(a))
	}
	return agents, flags, overreach
}

// checkMeta checks one agent from a host. ok is false when it must be
// dropped whole: its session id is not a UUID. why says what was dropped.
func checkMeta(a fleet.Meta) (_ fleet.Meta, why []string, ok bool) {
	if !isSessionID(a.Session) {
		return a, []string{"an agent whose session id is not a UUID"}, false
	}
	return a, why, true
}
