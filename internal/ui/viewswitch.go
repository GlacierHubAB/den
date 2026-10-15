package ui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/view"
)

// viewSwitcher builds what moves Den's windows between Agent view and User
// view (internal/view); tests replace it.
var viewSwitcher = view.New

// viewDoneMsg is the end of a view switch: the view Den is in now, "" when
// it cannot switch.
type viewDoneMsg struct{ view string }

// applyView carries out an app's view.show or view.hide. The live agents on
// this machine are handed over here, on the UI's goroutine; the windows
// move off it, and the answer goes back to the API when they have. Den's
// own window stays up while anything waits on the human here.
func (m *Model) applyView(r *api.ViewReq) tea.Cmd {
	rq := view.Request{Want: r.Want}
	for _, a := range m.agents {
		if a.Host != "" || a.Status == fleet.Gone || a.Session.PID <= 1 {
			continue
		}
		rq.Sessions = append(rq.Sessions, view.Session{ID: a.Session.SessionID, PID: a.Session.PID})
	}
	if m.store.Waiting() > 0 {
		rq.Keep = "a card waits"
	}
	sw := viewSwitcher()
	return func() tea.Msg {
		res, err := sw.Switch(context.Background(), rq)
		rep := api.ViewReply{}
		switch {
		case err == nil:
			rep.Result = api.ViewResult{View: res.View, Shown: res.Shown, Total: res.Total, Reason: res.Reason}
		case errors.Is(err, view.ErrBusy):
			rep.Code, rep.Msg = api.CodeBusy, "a view switch or a close is running"
		default:
			rep.Code, rep.Msg = api.CodeUnavailable, err.Error()
		}
		r.Reply <- rep
		return viewDoneMsg{view: sw.Current()}
	}
}
