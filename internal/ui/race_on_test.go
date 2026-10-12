//go:build race

package ui

// raceDetector says the tests run under -race, which makes code several
// times slower, so wall-clock budgets only guard against a hang there.
const raceDetector = true
