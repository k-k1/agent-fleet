package sessionx

import (
	"errors"
	"log"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// launchTmuxFn is startSessionTmux behind a seam for the create and recreate paths, so a test
// can make the launch fail and see the studio binding handed back (the rollback only runs then).
var launchTmuxFn = startSessionTmux

// bindStudioOnCreate points the studio at the session being created (ADR 0100 decision 2 ③),
// answering the refusal to write when it cannot.
func bindStudioOnCreate(studio string, meta session.Meta) *SpawnRefusal {
	name := meta.Name
	if why := imagegen.StudioSessionUnsupported(meta.Kind, meta.DriverKind()); why != "" {
		return &SpawnRefusal{Status: http.StatusConflict, Code: "studio_kind_unsupported", Message: why}
	}
	if imagegen.BindStudioSession == nil {
		return &SpawnRefusal{Status: http.StatusNotImplemented, Code: "studio_unavailable",
			Message: "this Agent has no image studio store, so a session cannot be bound to one"}
	}
	replaced, err := imagegen.BindStudioSession(studio, name, "")
	switch {
	case err == nil:
		clearReplacedStudio(replaced, studio)
		return nil
	case errors.Is(err, imagegen.ErrStudioNotFound):
		return &SpawnRefusal{Status: http.StatusNotFound, Code: "no_studio", Message: err.Error()}
	case errors.Is(err, imagegen.ErrStudioBound):
		return &SpawnRefusal{Status: http.StatusConflict, Code: "studio_bound", Message: err.Error()}
	default:
		return &SpawnRefusal{Status: http.StatusInternalServerError, Code: "studio_bind_failed", Message: err.Error()}
	}
}

// unbindStudioAfterFailedLaunch hands the studio back when the session it was just bound to
// never started. Conditional on the studio still naming that session, so it cannot undo a
// binding somebody made in between.
func unbindStudioAfterFailedLaunch(studio, name string) {
	if studio == "" || imagegen.BindStudioSession == nil {
		return
	}
	if _, err := imagegen.BindStudioSession(studio, "", name); err != nil {
		log.Printf("studio %s: unbind after the failed launch of %s: %v", studio, name, err)
	}
}

// rebindStudioOnRecreate moves the studio from the old slot to the new one. When it cannot —
// no store, or the studio has moved on to another session meanwhile — the new slot starts
// unbound: a meta claiming a studio that does not name it back is a session every studio tool
// refuses and that generate_image refuses too, which leaves it no way to make a picture at all.
func rebindStudioOnRecreate(m *session.Meta, previous string) {
	if m.Studio == "" {
		return
	}
	if imagegen.BindStudioSession == nil {
		m.Studio = ""
		return
	}
	if _, err := imagegen.BindStudioSession(m.Studio, m.Name, previous); err != nil {
		log.Printf("studio %s: not moved from %s to %s on recreate: %v", m.Studio, previous, m.Name, err)
		m.Studio = ""
	}
}

// clearReplacedStudio drops the studio from the stopped session a create just took it from,
// under the meta lock, and only while that meta still names this studio.
func clearReplacedStudio(name, studio string) { SetSessionStudio(name, studio, "") }

// SetSessionStudio moves a session's Meta.Studio from `from` to `to` under the meta lock, and
// leaves a meta that names anything else by then alone. It is imagegen.SessionStudioCAS: the
// studio store writes the binding's truth on its own side and this copy for advertising.
func SetSessionStudio(name, from, to string) {
	if name == "" || !session.ValidName(name) {
		return
	}
	UpdateSessionMeta(name, func(m *session.Meta) bool {
		if m.Studio != from || from == to {
			return false
		}
		m.Studio = to
		return true
	})
}
