// events.go — the single push channel to the Console, to cut its traffic.
//
// GET /api/events is SSE (text/event-stream) and folds the four permanent polls the Console
// used to run separately every 4-5s (workspace / sessions / stats / notifications) into one
// connection. The server drives the same payload builders (workspacePayload /
// sessionsPayload / containerStats / listPayload) on a per-connection tick and frames only
// the streams whose JSON changed since the last send, so an unchanged tick costs zero bytes
// between browser and CP. That beats even 304 polling, because on a mobile link the request
// headers and cookie round trip are the bulk of the cost.
//
// A frame is `data: {"stream":"<name>","data":<same shape as the REST reply>}\n\n`. Keeping
// the shape identical to the REST responses lets the Console reuse one store-apply path for
// both this and the polling fallback — and against an older CP the client sees 404 here and
// falls back on its own, which is what makes a version skew survivable.
//
// Both the gzip and etagJSON middlewares pass text/event-stream through untouched (gzip.go
// / etag.go). Auth is the same withResolved gate as the rest of the REST surface (cookie +
// X-AF-Tenant header). Like polling, this connection must not touch the idle clock: a tab
// left open may not block the idle reaper's decision to stop the workspace.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

const (
	eventsTick      = 4 * time.Second  // the same floor the Console's polling used
	eventsPingEvery = 20 * time.Second // silence this long: a comment ping shows we are alive
)

type eventsAPI struct {
	memberAuth
	ws    workspaceAPI
	notif notificationAPI
	wi    workItemsAPI
	tick  time.Duration
	ping  time.Duration
}

func newEventsAPI(m *manager, autostart bool) eventsAPI {
	return eventsAPI{memberAuth{m}, newWorkspaceAPI(m, autostart), newNotificationAPI(m),
		newWorkItemsAPI(m), eventsTick, eventsPingEvery}
}

func registerEventsRoutes(mux *http.ServeMux, cfg config) {
	ev := newEventsAPI(cfg.mgr, cfg.autostart)
	mux.HandleFunc("GET /api/events", ev.withResolved(ev.stream))
}

// statsPayload rounds the jittery gauges before diffing: memory.current moves
// by bytes on every read and cpu_pct by fractions, so diffing the raw values
// would push a stats frame every tick and defeat the suppression. The WS-bar
// chip displays rounded percent / 0.1 GiB anyway — an 8 MiB floor and integer
// CPU percent lose nothing visible. The REST endpoint keeps serving raw values.
//
// state is handed in — the State workspacePayload already resolved on this tick
// (docs/log/63 §63.9). On ecs-ec2, State() is real DescribeVolumes + DescribeServices calls
// running once per subscriber every 4 seconds, so resolving it twice within one tick would
// double those AWS calls for a value that cannot have changed.
func statsPayload(ctx context.Context, m *manager, rt runtime.Runtime, state string) map[string]any {
	return roundStats(workspaceStats(ctx, m, rt, func() string { return state }))
}

func roundStats(m map[string]any) map[string]any {
	if v, ok := m["mem_used"].(uint64); ok {
		m["mem_used"] = v &^ (8<<20 - 1)
	}
	if v, ok := m["cpu_pct"].(float64); ok {
		m["cpu_pct"] = math.Round(v)
	}
	return m
}

// stream serves one subscriber: initial full snapshot, then diff-only pushes.
// Returns when the client disconnects (request context cancellation).
func (a eventsAPI) stream(w http.ResponseWriter, r *http.Request, res *resolved) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "internal", "streaming unsupported"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	ctx := r.Context()
	last := map[string]string{}
	lastWrite := time.Now()
	// emit sends one stream's frame when its serialized payload changed since the
	// last send. json.Marshal sorts map keys, so equal content gives equal bytes and a
	// plain string comparison is a sound diff.
	emit := func(stream string, payload any) bool {
		// A disconnect while this tick's payload was being built is an abort, not a
		// change. sessions falls back to the DB mirror (a different shape) when its
		// HTTP call to the Agent fails on context cancellation, so without this guard
		// a subscriber that is already gone still gets a "sessions changed" frame
		// written for it, and last[] is polluted with the fallback shape.
		if ctx.Err() != nil {
			return false
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if last[stream] == string(b) {
			return false
		}
		last[stream] = string(b)
		frame, _ := json.Marshal(map[string]any{"stream": stream, "data": json.RawMessage(b)})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		return true
	}
	tickAll := func() {
		state := res.rt.State(ctx)
		wrote := emit("workspace", a.ws.workspacePayload(ctx, res, state))
		wrote = emit("stats", statsPayload(ctx, a.mgr, res.rt, state)) || wrote
		wrote = emit("sessions", a.ws.sessionsPayload(ctx, res)) || wrote
		// A transient failure draining notifications (a DB error, say) drops this
		// tick only and leaves the stream up; the next tick recovers.
		if p, aerr := a.notif.listPayload(ctx, res); aerr == nil {
			wrote = emit("notifications", p) || wrote
		}
		// Work items (docs/log/80). This payload only reads the DB cache; fetching
		// from the provider is refreshAsync's job on its own goroutine. A tick that
		// waited on an external API round trip would stall every other stream this
		// subscriber has.
		if p, aerr := a.wi.workItemsPayload(ctx, res, state); aerr == nil {
			wrote = emit("workitems", p) || wrote
		}
		if wrote {
			lastWrite = time.Now()
		} else if time.Since(lastWrite) >= a.ping {
			_, _ = fmt.Fprint(w, ": ping\n\n")
			lastWrite = time.Now()
			wrote = true
		}
		if wrote {
			fl.Flush()
		}
	}
	tickAll()
	t := time.NewTicker(a.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickAll()
		}
	}
}
