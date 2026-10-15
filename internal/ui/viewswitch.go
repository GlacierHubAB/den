package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

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

// viewRescueMsg is the end of /view rescue.
type viewRescueMsg struct {
	moved, left int
	err         error
}

// viewCommand is /view and /view rescue, for the human at Den's own screen:
// no verb of den-api.sock reaches it. /view says which view Den is in;
// /view rescue brings back every window on special:den, whatever it is,
// the way out when one is stranded there.
func (m Model) viewCommand(args []string) (tea.Model, tea.Cmd) {
	sw := viewSwitcher()
	cur := sw.Current()
	if cur == "" {
		return m.say("This Den cannot switch views: there is no Hyprland to move windows with.", true)
	}
	switch strings.ToLower(strings.Join(args, " ")) {
	case "", "status":
		return m.say("View: "+cur+". /view rescue brings back every window on special:den.", false)
	case "rescue":
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), view.Budget+time.Second)
			defer cancel()
			moved, left, err := sw.Rescue(ctx)
			return viewRescueMsg{moved: moved, left: left, err: err}
		}
	}
	return m.say("/view shows the view Den is in; /view rescue brings back every window on special:den.", true)
}

// applyViewRescue says what /view rescue did and keeps the published view
// right.
func (m Model) applyViewRescue(msg viewRescueMsg) (tea.Model, tea.Cmd) {
	m.api.view = viewSwitcher().Current()
	m.publishAPI()
	switch {
	case errors.Is(msg.err, view.ErrBusy):
		return m.say("A view switch or a close is running: try /view rescue again in a moment.", true)
	case msg.err != nil:
		return m.say("/view rescue: "+msg.err.Error()+".", true)
	case msg.left > 0:
		return m.say("Brought back "+strconv.Itoa(msg.moved)+" window(s); "+strconv.Itoa(msg.left)+" would not move and are still on special:den.", true)
	case msg.moved == 0:
		return m.say("No window is on special:den.", false)
	}
	return m.say("Brought back "+strconv.Itoa(msg.moved)+" window(s) from special:den.", false)
}
