package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/ingresstest"
)

// A held response is silent for at most one heartbeat, so that plus the CP hop has to stay under
// the ingress idle timeout or the ALB cuts the call before the answer arrives (#1151).
func TestHeldHeartbeatStaysUnderTheIngressIdleTimeout(t *testing.T) {
	idle := ingresstest.IdleTimeout(t)
	if HeldHeartbeatInterval+ingresstest.HopMargin > idle {
		t.Errorf("HeldHeartbeatInterval = %s; with the %s hop margin it reaches the ingress idle timeout %s",
			HeldHeartbeatInterval, ingresstest.HopMargin, idle)
	}
}

func shortHeartbeat(t *testing.T, d time.Duration) {
	t.Helper()
	prev := heldHeartbeat
	heldHeartbeat = d
	t.Cleanup(func() { heldHeartbeat = prev })
}

// released blocks the handler until the test has seen a heartbeat, which is what proves the
// heartbeat is written while the handler still runs rather than being a fixed prefix.
func TestHeldOpenBeatsWhileTheHandlerRunsAndCarriesItsAnswer(t *testing.T) {
	shortHeartbeat(t, 5*time.Millisecond)
	release := make(chan struct{})
	h := HeldOpen(func(w http.ResponseWriter, r *http.Request) {
		<-release
		WriteErr(w, http.StatusNotFound, "chat_conversation_not_found", "conversation not found")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), ": keepalive\n\n") {
		t.Fatalf("first bytes before the answer = %q, %v; want a keepalive comment", buf[:n], err)
	}
	close(release)

	var rest strings.Builder
	chunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(chunk)
		rest.Write(chunk[:n])
		if err != nil {
			break
		}
	}
	var data string
	for _, frame := range strings.Split(rest.String(), "\n\n") {
		if strings.HasPrefix(frame, "data: ") {
			if data != "" {
				t.Fatalf("more than one data frame: %q", rest.String())
			}
			data = strings.TrimPrefix(frame, "data: ")
		}
	}
	var got struct {
		Status int `json:"status"`
		Body   struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatalf("final frame %q: %v", data, err)
	}
	if got.Status != http.StatusNotFound || got.Body.Error.Code != "chat_conversation_not_found" {
		t.Fatalf("final frame = %+v", got)
	}
}

// Callers that do not ask for a stream — the MCP tools call /chat/ask directly — keep the plain
// JSON answer, status and all.
func TestHeldOpenPassesPlainCallersThrough(t *testing.T) {
	h := HeldOpen(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusCreated, map[string]string{"ok": "yes"})
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/json" ||
		strings.TrimSpace(rec.Body.String()) != `{"ok":"yes"}` {
		t.Fatalf("code=%d ct=%q body=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestAcceptsEventStream(t *testing.T) {
	for accept, want := range map[string]bool{
		"text/event-stream":                       true,
		"application/json, text/event-stream;q=1": true,
		"TEXT/EVENT-STREAM":                       true,
		"":                                        false,
		"application/json":                        false,
		"*/*":                                     false,
	} {
		if got := acceptsEventStream(accept); got != want {
			t.Errorf("acceptsEventStream(%q) = %v, want %v", accept, got, want)
		}
	}
}
