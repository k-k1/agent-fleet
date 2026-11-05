package session

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func TestRouteAtAndSince(t *testing.T) {
	b1 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	api := routes.Selection{Route: "anthropic-api"}
	sub := &routes.Selection{Route: "anthropic-subscription"}
	never := Meta{Route: sub}
	if got, ok := never.RouteAt(b1); !ok || got != sub || !never.RouteSince().IsZero() {
		t.Errorf("never switched: %v %v since=%v", got, ok, never.RouteSince())
	}
	// Without a switch an unreadable instant is still answered: unchanged behaviour.
	if got, ok := never.RouteAt(time.Time{}); !ok || got != sub {
		t.Errorf("never switched, zero time: %v %v", got, ok)
	}
	m := Meta{Route: &api, RouteSwitches: []RouteSwitch{{At: b1.Format(time.RFC3339Nano), From: sub, To: api}}}
	if got, ok := m.RouteAt(b1.Add(-time.Second)); !ok || got != sub {
		t.Errorf("before: %v %v", got, ok)
	}
	if got, ok := m.RouteAt(b1); !ok || got.Route != "anthropic-api" {
		t.Errorf("at the boundary: %v %v", got, ok)
	}
	if _, ok := m.RouteAt(time.Time{}); ok {
		t.Error("a switched session answered for an unreadable instant")
	}
	if !m.RouteSince().Equal(b1) {
		t.Errorf("since = %v", m.RouteSince())
	}
}
