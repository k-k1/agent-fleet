package codex

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ManagedProgressReporter = managedDriver{}

// ManagedProgress: the last notification that names this session's thread, or "now" while an item
// (a tool call, a message) is started and not completed. The app-server is one daemon shared by
// every session, so a tool process cannot be attributed from the process table; the notification
// stream is used instead. Measured on 0.163.0 through a `sleep 120`: the command's item opened
// when it started and closed after the sleep, and the model's polling produced further item
// events every 30-35 s. Only the open item is relied on, since polling is model behaviour; the
// set is unknown (the row keeps holding) until a turn boundary is seen on this connection.
func (managedDriver) ManagedProgress(m session.Meta) (time.Time, bool) {
	h := handleFor(m.Name)
	if h == nil {
		return time.Time{}, false
	}
	return h.act.Progress(time.Now(), h.items.State)
}
