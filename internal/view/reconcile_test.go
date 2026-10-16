package view

import (
	"context"
	"errors"
	"testing"

	"github.com/GlacierHubAB/den/internal/hypr"
)

// putBack moves every window to a workspace, as a restart of the compositor
// or a hand does, with no word to Den.
func (r *rig) putBack() {
	r.wm.mu.Lock()
	defer r.wm.mu.Unlock()
	for i := range r.wm.cs {
		r.wm.cs[i].Workspace.ID, r.wm.cs[i].Workspace.Name = 1, "1"
	}
}

func (r *rig) clientsCalls() int {
	n := 0
	for _, l := range r.wm.logs() {
		if l == "clients" {
			n++
		}
	}
	return n
}

// TestReconcileDropsUserViewWhenNoHiddenWindowIsHidden: the record says User
// view and every window it names is on a workspace: Den says Agent view, and
// saves it, rather than claim hidden what it cannot see.
func TestReconcileDropsUserViewWhenNoHiddenWindowIsHidden(t *testing.T) {
	r := newRig(t, 2)
	addr := r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
	r.must(User)
	if r.sw.Current() != User {
		t.Fatal("not in User view")
	}
	r.putBack()
	changed, n, err := r.sw.Reconcile(context.Background())
	if err != nil || !changed || n != 4 {
		t.Fatalf("reconcile: %v %d %v", changed, n, err)
	}
	if got := r.sw.Current(); got != Agent {
		t.Errorf("view %q after reconcile", got)
	}
	st, _ := loadState(r.sw.path())
	if st.View != Agent || len(st.Moved) != 0 {
		t.Errorf("record %+v", st)
	}
	if w := r.wm.where(addr); w != "1" {
		t.Errorf("reconcile moved a window to %q", w)
	}
	// Idempotent: nothing left to drop, and no listing asked for.
	before := r.clientsCalls()
	if changed, _, _ := r.sw.Reconcile(context.Background()); changed || r.clientsCalls() != before {
		t.Errorf("a second reconcile changed %v or asked again", changed)
	}
}

// TestReconcileKeepsUserViewWhileAWindowIsHidden: one recorded window still
// on special:den is enough: the view stands.
func TestReconcileKeepsUserViewWhileAWindowIsHidden(t *testing.T) {
	r := newRig(t, 2)
	r.must(User)
	r.wm.mu.Lock()
	for i := range r.wm.cs {
		if r.wm.cs[i].Address == "0xa1" {
			r.wm.cs[i].Workspace.ID, r.wm.cs[i].Workspace.Name = 1, "1" // one put back
		}
	}
	r.wm.mu.Unlock()
	if changed, _, err := r.sw.Reconcile(context.Background()); changed || err != nil {
		t.Fatalf("reconcile: %v %v", changed, err)
	}
	if r.sw.Current() != User {
		t.Error("the view changed while windows are hidden")
	}
}

// TestReconcileClaimsNothingItCannotSee: in Agent view, with no record, with a
// listing that fails, or while a switch runs, it changes nothing.
func TestReconcileClaimsNothingItCannotSee(t *testing.T) {
	r := newRig(t, 1)
	if changed, _, _ := r.sw.Reconcile(context.Background()); changed || r.clientsCalls() != 0 {
		t.Errorf("Agent view: changed %v, %d listings", changed, r.clientsCalls())
	}
	r.must(User)
	r.putBack()
	r.wm.listErr = errors.New("hyprctl is gone")
	if changed, _, err := r.sw.Reconcile(context.Background()); changed || !errors.Is(err, ErrList) {
		t.Errorf("a listing that fails: %v %v", changed, err)
	}
	if r.sw.Current() != User {
		t.Error("the view changed though nothing could be seen")
	}
	r.wm.listErr = nil
	unlock, ok := hypr.TryLockWindows()
	if !ok {
		t.Fatal("the windows lock was taken")
	}
	changed, _, err := r.sw.Reconcile(context.Background())
	unlock()
	if changed || !errors.Is(err, ErrBusy) {
		t.Errorf("while a switch runs: %v %v", changed, err)
	}
	// And with the lock free the stale record goes.
	if changed, _, err := r.sw.Reconcile(context.Background()); !changed || err != nil {
		t.Errorf("after: %v %v", changed, err)
	}
}

// TestReconcileKnowsEveryKindOfRecord: a marker, Den's own window and a
// window found by title each count as hidden when they are on special:den.
func TestReconcileKnowsEveryKindOfRecord(t *testing.T) {
	for name, addr := range map[string]string{"a marker": "0xa1", "Den's own window": "0xden", "a title": "0xh1"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 1)
			r.handAgent(1, "old-agent", hypr.AgentTitle("old-agent"))
			r.must(User)
			r.putBack()
			r.wm.mu.Lock()
			for i := range r.wm.cs {
				if r.wm.cs[i].Address == addr {
					r.wm.cs[i].Workspace.ID, r.wm.cs[i].Workspace.Name = -99, Workspace
				}
			}
			r.wm.mu.Unlock()
			if changed, _, err := r.sw.Reconcile(context.Background()); changed || err != nil {
				t.Errorf("%s hidden, yet reconcile changed %v (%v)", name, changed, err)
			}
		})
	}
}
