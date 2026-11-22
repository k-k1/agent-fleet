package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func customAPIPut(provider, url, key string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(customAPIConnReq{Provider: provider, URL: url, APIKey: key})
	w := httptest.NewRecorder()
	handlePutCustomAPIConn(w, httptest.NewRequest("PUT", "/connections/custom-api", strings.NewReader(string(body))))
	return w
}

func TestNormalizeCustomAPIURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://llm.example.test/v1/": "https://llm.example.test/v1",
		" http://box:8080 ":            "http://box:8080",
	} {
		if got, err := normalizeCustomAPIURL(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "box:80", "ftp://box", "http://", "https://u:p@box", "https://box/v1?x=1", "https://box/#f", "https://box/v1?", "https://box/v1#", "https://box:65536/v1", "https://box:0/v1", "https://[box.example.test]/v1"} {
		if got, err := normalizeCustomAPIURL(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestPutCustomAPIConnRejectsBadInputWithoutWriting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, c := range [][2]string{{"", "https://box"}, {"a/b", "https://box"}, {"mine", "nope"}, {"anthropic", "https://box"}, {"opencode", "https://box"}} {
		if w := customAPIPut(c[0], c[1], ""); w.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", c, w.Code)
		}
	}
	s, err := secrets.Load()
	if err != nil || s.CustomAPI != nil {
		t.Fatalf("stored %+v, %v", s.CustomAPI, err)
	}
}

func TestPutAndDeleteCustomAPIConn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w := customAPIPut("mine", "https://llm.example.test/v1/", " k ")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "\"k\"") || strings.Contains(w.Body.String(), "apiKey") {
		t.Errorf("response leaks the key: %s", w.Body)
	}
	s, _ := secrets.Load()
	if s.CustomAPI == nil || *s.CustomAPI != (secrets.CustomAPIConn{Provider: "mine", URL: "https://llm.example.test/v1", APIKey: "k"}) {
		t.Fatalf("stored %+v", s.CustomAPI)
	}
	dw := httptest.NewRecorder()
	handleDeleteCustomAPIConn(dw, httptest.NewRequest("DELETE", "/connections/custom-api", nil))
	if dw.Code != http.StatusOK {
		t.Fatalf("delete status %d", dw.Code)
	}
	if s, _ := secrets.Load(); s.CustomAPI != nil {
		t.Fatalf("still stored: %+v", s.CustomAPI)
	}
}

// Under a restriction the custom-api route is open only on an approved host: the writer gates on
// it, keeps the stored connection when it refuses, and DELETE stays possible.
func TestPutCustomAPIConnGatesOnTheApprovedEndpoint(t *testing.T) {
	memberHome(t)
	useHidingPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true,
		Allowed: []string{routes.RouteAnthropicAPI}, EndpointRoutes: []string{routes.RouteCustomAPI}, Endpoints: []string{"ok.example.test"}})
	if w := customAPIPut("mine", "https://ok.example.test/v1", ""); w.Code != http.StatusOK {
		t.Fatalf("approved host: %d %s", w.Code, w.Body)
	}
	if w := customAPIPut("mine", "https://other.example.test/v1", ""); w.Code != http.StatusForbidden {
		t.Fatalf("unapproved host: %d %s", w.Code, w.Body)
	}
	if s, _ := secrets.Load(); s.CustomAPI == nil || s.CustomAPI.URL != "https://ok.example.test/v1" {
		t.Fatalf("refused PUT changed the stored connection: %+v", s.CustomAPI)
	}
	dw := httptest.NewRecorder()
	handleDeleteCustomAPIConn(dw, httptest.NewRequest("DELETE", "/connections/custom-api", nil))
	if dw.Code != http.StatusOK {
		t.Fatalf("delete under restriction: %d", dw.Code)
	}
	if s, _ := secrets.Load(); s.CustomAPI != nil {
		t.Fatalf("still stored: %+v", s.CustomAPI)
	}
}

// Grafana's writers must neither drop the custom connection nor lose their own change.
func TestGrafanaWritersKeepCustomAPIConn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if w := customAPIPut("mine", "https://llm.example.test", ""); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := `{"url":"https://g.example.test","token":"t"}`
	w := httptest.NewRecorder()
	handlePutGrafanaConn(w, httptest.NewRequest("PUT", "/connections/grafana", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s, _ := secrets.Load()
	if s.Grafana == nil || s.Grafana.URL != "https://g.example.test" || s.Grafana.Token != "t" ||
		s.CustomAPI == nil || s.CustomAPI.URL != "https://llm.example.test" {
		t.Fatalf("after PUT: grafana=%+v custom=%+v", s.Grafana, s.CustomAPI)
	}
	dw := httptest.NewRecorder()
	handleDeleteGrafanaConn(dw, httptest.NewRequest("DELETE", "/connections/grafana", nil))
	if dw.Code != http.StatusOK {
		t.Fatalf("delete: %d", dw.Code)
	}
	if s, _ := secrets.Load(); s.Grafana != nil || s.CustomAPI == nil || s.CustomAPI.URL != "https://llm.example.test" {
		t.Fatalf("after DELETE: grafana=%+v custom=%+v", s.Grafana, s.CustomAPI)
	}
}
