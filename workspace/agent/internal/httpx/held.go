package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HeldHeartbeatInterval is how long a held response stays silent at most. The ALB drops a
// connection that moves no byte for its idle timeout (deploy/aws/ecs/cfn/30-ingress.yaml), so
// this plus the CP hop has to stay under it; TestHeldHeartbeatStaysUnderTheIngressIdleTimeout
// pins the relation.
const HeldHeartbeatInterval = 20 * time.Second

// heldHeartbeat is HeldHeartbeatInterval, shortened by tests.
var heldHeartbeat = HeldHeartbeatInterval

// HeldOpen wraps a handler whose answer can take longer than the ingress idle timeout (a model
// generating a plan, a compaction summary, an edit suggestion). A caller that sends
// `Accept: text/event-stream` gets 200 and an SSE body at once, a `: keepalive` comment every
// HeldHeartbeatInterval while the handler runs, and one final frame carrying what the handler
// would have answered:
//
//	data: {"status":404,"body":{"error":{…}}}
//
// Any other caller — the MCP tools calling the Agent directly, older Consoles — gets the
// handler's plain response unchanged, so the handler itself is written as an ordinary JSON
// handler. The request context still ends when the browser goes away, as before.
//
// The CP must relay these routes through its flushing stream proxy: its buffered REST relay
// would hold the heartbeats back and the ingress would cut the call anyway.
func HeldOpen(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok || !acceptsEventStream(r.Header.Get("Accept")) {
			next(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		rec := &heldRecorder{header: http.Header{}}
		// The heartbeat goroutine is the only writer to w until stopped is closed, so the final
		// frame below never interleaves with a keepalive. The deferred stop also runs when next
		// panics, which would otherwise leave the ticker writing to a dead response.
		done, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			tick := time.NewTicker(heldHeartbeat)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
						return
					}
					flusher.Flush()
				}
			}
		}()
		func() {
			defer func() { close(done); <-stopped }()
			next(rec, r)
		}()

		b, _ := json.Marshal(heldFrame{Status: rec.statusOr200(), Body: rec.jsonBody()})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
}

// heldFrame is the final frame of a held response.
type heldFrame struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
}

func acceptsEventStream(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		if mt, _, _ := strings.Cut(part, ";"); strings.EqualFold(strings.TrimSpace(mt), "text/event-stream") {
			return true
		}
	}
	return false
}

// heldRecorder collects the wrapped handler's response so it can travel in the final frame.
type heldRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *heldRecorder) Header() http.Header { return r.header }

func (r *heldRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *heldRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *heldRecorder) statusOr200() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// jsonBody returns the recorded body as JSON. A non-JSON body (none of the wrapped handlers
// writes one) travels as a JSON string rather than breaking the frame.
func (r *heldRecorder) jsonBody() json.RawMessage {
	b := bytes.TrimSpace(r.body.Bytes())
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return b
	}
	s, _ := json.Marshal(string(b))
	return s
}
