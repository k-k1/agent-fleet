package mcpreg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The probe reports every failure of its own as a stable code plus parameters, so the Console
// can render it in the member's language. Error is the English fallback.

func TestProbeCodeUnsupportedTransport(t *testing.T) {
	d := stdioDef("x")
	d.Transport = "carrier-pigeon"
	res := Probe(context.Background(), d)
	if res.Code != codeUnsupportedTransport || res.Params["transport"] != "carrier-pigeon" {
		t.Fatalf("got %+v", res)
	}
	assertEnglish(t, res)
}

func TestProbeCodeStartFailed(t *testing.T) {
	d := stdioDef("broken")
	d.Command = "/nonexistent/mcp-server"
	res := Probe(context.Background(), d)
	if res.Code != codeStartFailed || !strings.Contains(res.Params["error"], "/nonexistent/mcp-server") {
		t.Fatalf("got %+v", res)
	}
	assertEnglish(t, res)
}

func TestProbeCodeProcessExited(t *testing.T) {
	d := stdioDef("exits")
	d.Command = "sh"
	d.Args = []string{"-c", "read line; exit 0"}
	res := Probe(context.Background(), d)
	if res.Code != codeProcessExited {
		t.Fatalf("got %+v", res)
	}
	assertEnglish(t, res)
}

func TestProbeCodeTimeoutKeepsBudget(t *testing.T) {
	d := stdioDef("sleeps")
	d.Command = "sleep"
	d.Args = []string{"5"}
	d.TimeoutMS = 300
	res := Probe(context.Background(), d)
	if res.Code != codeTimeout || res.Params["timeoutMs"] != "300" {
		t.Fatalf("got %+v", res)
	}
	assertEnglish(t, res)
}

func TestProbeCodeRPCRejectedKeepsMethodAndMessage(t *testing.T) {
	// server/discover answers → stateless; tools/list then errors.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Mcp-Method") == "server/discover" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2026-07-28"]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"boom"}}`))
	}))
	defer srv2.Close()
	res := Probe(context.Background(), httpDef("remote", srv2.URL))
	if res.Code != codeRPCRejected || res.Params["method"] != "tools/list" || res.Params["message"] != "boom" {
		t.Fatalf("got %+v", res)
	}
	assertEnglish(t, res)
}

func TestProbeCodeBadAndSSEResponses(t *testing.T) {
	for _, tc := range []struct{ ctype, body, code string }{
		{"application/json", "<html>", codeBadResponse},
		{"text/event-stream", "event: ping\n\n", codeSSENoResponse},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tc.ctype)
			_, _ = w.Write([]byte(tc.body))
		}))
		res := Probe(context.Background(), httpDef("remote", srv.URL))
		srv.Close()
		if res.Code != tc.code {
			t.Errorf("%s: got %+v", tc.ctype, res)
		}
		assertEnglish(t, res)
	}
}

// A result whose Error is the OS's or the server's own text carries no code.
func TestProbeHTTPStatusErrorHasNoCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "x", http.StatusInternalServerError)
	}))
	defer srv.Close()
	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if res.Code != "" || res.Error != "HTTP 500" {
		t.Fatalf("got %+v", res)
	}
}

// assertEnglish: the fallback text must be readable without the Console's catalogue.
func assertEnglish(t *testing.T, res ProbeResult) {
	t.Helper()
	if res.Error == "" {
		t.Fatalf("no fallback text: %+v", res)
	}
	for _, r := range res.Error {
		if r > 0x2fff {
			t.Fatalf("fallback is not English: %q", res.Error)
		}
	}
}
