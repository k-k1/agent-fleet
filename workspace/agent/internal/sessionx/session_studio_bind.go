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
func bindStudioOnCreate(studio, name string) *SpawnRefusal {
	if imagegen.BindStudioSession == nil {
		return &SpawnRefusal{Status: http.StatusNotImplemented, Code: "studio_unavailable",
			Message: "this Agent has no image studio store, so a session cannot be bound to one"}
	}
	switch err := imagegen.BindStudioSession(studio, name, ""); {
	case err == nil:
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
	if err := imagegen.BindStudioSession(studio, "", name); err != nil {
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
	if err := imagegen.BindStudioSession(m.Studio, m.Name, previous); err != nil {
		log.Printf("studio %s: not moved from %s to %s on recreate: %v", m.Studio, previous, m.Name, err)
		m.Studio = ""
	}
}
