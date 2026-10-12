//go:build !linux

package closer

import "errors"

// Hold refuses where there is no pidfd: Den cannot be sure the pid is
// still the agent's, and does not guess.
func (System) Hold(t Target) (Held, error) {
	return nil, errors.New("closing needs Linux pidfds to be sure of the process; nothing was signalled")
}
