package sessionx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// cpWaker books the Control Plane's wake-only schedule target for a time watch (ADR 0133 §7). It
// uses the member's schedule token like rate_limit_resume.go, but its own routes: the rows are
// hidden from the schedule API, so /internal/schedules cannot create or delete them.
type cpWaker struct{}

// Replaceable by tests: they have no Control Plane.
var cpWakeDo = mcpx.CPScheduleDo

func (cpWaker) Book(_ context.Context, at time.Time) (string, error) {
	body, _ := json.Marshal(map[string]string{"at": at.UTC().Format(time.RFC3339)})
	out, err := cpWakeDo(http.MethodPost, "/internal/wakes", body)
	if err != nil {
		var ce *mcpx.CPScheduleError
		if errors.As(err, &ce) && ce.Status >= 400 && ce.Status < 500 && ce.Status != http.StatusUnauthorized {
			// The Control Plane refused: no scheduler on this deployment, a limit, a bad instant.
			return "", &watch.ErrRefused{Reason: "the Control Plane would not book the wake: " + cpErrorMessage(ce.Body)}
		}
		return "", fmt.Errorf("could not book the wake with the Control Plane: %w", err)
	}
	var wire struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(out), &wire) != nil || wire.ID == "" {
		return "", fmt.Errorf("could not read the wake id: %s", out)
	}
	return wire.ID, nil
}

// Cancel returns nil when the Control Plane no longer holds the booking: it deleted it now, or
// answered 404 because it had already (it deletes a booking itself once it has fired).
func (cpWaker) Cancel(_ context.Context, id string) error {
	_, err := cpWakeDo(http.MethodDelete, "/internal/wakes/"+url.PathEscape(id), nil)
	var ce *mcpx.CPScheduleError
	if errors.As(err, &ce) && ce.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// cpErrorMessage is the message of a Control Plane error body, or the body cut short.
func cpErrorMessage(body string) string {
	var w struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &w) == nil {
		if w.Error.Message != "" {
			return w.Error.Message
		}
		if w.Message != "" {
			return w.Message
		}
	}
	return watch.CleanText(body, 200)
}
