package remote

import "time"

// clockOffset fakes a host whose clock is off by that much: serve shifts
// remote_now and event times by it (ages are differences on one clock and
// do not change). It is always 0 in a release build; only a build with
// the sshtest tag sets it (clock_sshtest.go), for the acceptance tests.
var clockOffset time.Duration
