package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// heldCall posts body through h on a real server (a recorder never discards the request body the
// way net/http's HTTP/1 server does) and returns the status, headers and raw response body.
func heldCall(t *testing.T, h http.Handler, body io.Reader, accept string) (int, http.Header, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, body)
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// heldFrameOf parses the single final frame out of a held response body.
func heldFrameOf(t *testing.T, raw string) (int, json.RawMessage) {
	t.Helper()
	var data string
	for _, frame := range strings.Split(raw, "\n\n") {
		if strings.HasPrefix(frame, "data: ") {
			if data != "" {
				t.Fatalf("more than one data frame: %q", raw)
			}
			data = strings.TrimPrefix(frame, "data: ")
		}
	}
	var f heldFrame
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		t.Fatalf("no final frame in %q: %v", raw, err)
	}
	return f.Status, f.Body
}

// jsonEcho decodes the body the way the wrapped handlers do and answers with its length.
func jsonEcho(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Prompt string `json:"prompt"`
	}
	if !DecodeJSON(w, r, &v) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"n": len(v.Prompt)})
}

func jsonPrompt(n int) []byte {
	b, _ := json.Marshal(map[string]string{"prompt": strings.Repeat("a", n)})
	return b
}

func TestHeldOpenHandsTheHandlerTheBodyItWasSent(t *testing.T) {
	// 200 KiB is under net/http's 256 KiB post-handler discard limit, so the server drops it
	// deterministically once the headers are written. "near the bound" only calibrates the
	// accepted size: loopback delivers it before the headers, so it passes even unfixed.
	for name, n := range map[string]int{"small": 5, "discarded by the server": 200 << 10, "near the bound": heldMaxBody - 100} {
		t.Run(name, func(t *testing.T) {
			_, _, raw := heldCall(t, HeldOpen(jsonEcho), bytes.NewReader(jsonPrompt(n)), "text/event-stream")
			status, body := heldFrameOf(t, raw)
			if status != http.StatusOK || string(body) != `{"n":`+strconv.Itoa(n)+`}` {
				t.Fatalf("frame = %d %s, want 200 with n=%d", status, body, n)
			}
		})
	}
}

func TestHeldOpenCarriesAnErrorStatusForABodyItRead(t *testing.T) {
	h := HeldOpen(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		WriteErr(w, http.StatusConflict, "nope", string(b))
	})
	_, _, raw := heldCall(t, h, strings.NewReader(`{"prompt":"x"}`), "text/event-stream")
	status, body := heldFrameOf(t, raw)
	if status != http.StatusConflict || !strings.Contains(string(body), `{\"prompt\":\"x\"}`) {
		t.Fatalf("frame = %d %s", status, body)
	}
}

func TestHeldOpenPlainCallerKeepsItsPlainAnswer(t *testing.T) {
	status, hdr, raw := heldCall(t, HeldOpen(jsonEcho), bytes.NewReader(jsonPrompt(7)), "")
	if status != http.StatusOK || hdr.Get("Content-Type") == "text/event-stream" || strings.TrimSpace(raw) != `{"n":7}` {
		t.Fatalf("status=%d type=%q body=%q", status, hdr.Get("Content-Type"), raw)
	}
}

func TestHeldOpenRefusesABodyOverTheBound(t *testing.T) {
	called := false
	h := HeldOpen(func(w http.ResponseWriter, r *http.Request) { called = true })
	// An unknown length (chunked) cannot be refused up front, so the read itself has to stop.
	for name, body := range map[string]io.Reader{
		"known length": bytes.NewReader(jsonPrompt(heldMaxBody + 1)),
		"chunked":      io.MultiReader(bytes.NewReader(jsonPrompt(heldMaxBody + 1))),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, raw := heldCall(t, h, body, "text/event-stream")
			status, b := heldFrameOf(t, raw)
			if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(b), "too_large") {
				t.Fatalf("frame = %d %s, want 413 too_large", status, b)
			}
			if called {
				t.Fatal("the handler ran for an oversized body")
			}
		})
	}
}

// compact and plan/refresh never read their body; a client that sends one anyway must still get
// the handler's answer.
func TestHeldOpenHandlerThatIgnoresItsBody(t *testing.T) {
	h := HeldOpen(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	_, _, raw := heldCall(t, h, strings.NewReader(`{"x":1}`), "text/event-stream")
	status, body := heldFrameOf(t, raw)
	if status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("frame = %d %s", status, body)
	}
}

// A client that announces a body and disappears must end the read, not leave the call waiting.
func TestHeldOpenClientGoneMidBodyEndsTheCall(t *testing.T) {
	ran := make(chan struct{}, 1)
	returned := make(chan struct{})
	inner := HeldOpen(func(w http.ResponseWriter, r *http.Request) { ran <- struct{}{} })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		inner(w, r)
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nAccept: text/event-stream\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"pro")
	conn.Close()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("HeldOpen is still waiting for a body that will never arrive")
	}
	if len(ran) != 0 {
		t.Fatal("the handler ran on a truncated body")
	}
}
