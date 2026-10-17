package hypr

import "sync"

// windowsMu is the one lock for Den's changes to windows: a view switch
// (internal/view) and the closing of a window (internal/closer) never run
// at the same time.
var windowsMu sync.Mutex

// LockWindows waits until no other change to windows is running, and
// returns what releases it. Whoever holds it keeps to a bounded amount of
// work.
func LockWindows() (unlock func()) {
	windowsMu.Lock()
	return windowsMu.Unlock
}

// TryLockWindows is LockWindows that does not wait: ok is false while
// another change to windows runs.
func TryLockWindows() (unlock func(), ok bool) {
	if !windowsMu.TryLock() {
		return nil, false
	}
	return windowsMu.Unlock, true
}
