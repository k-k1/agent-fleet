package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAgentPreviewError answers every /proxy request the way the Agent does when its own
// relay fails: 502 + the marker header + a JSON body naming an internal address.
func fakeAgentPreviewError(kind string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(previewErrorHeader, kind)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"` + kind + `","message":"dial 10.0.0.9:1234 internal detail"}}`))
	}))
}

type relayGetter func(t *testing.T, agentURL string, hdr map[string]string) *httptest.ResponseRecorder

// The two relay entry points: the subdomain form and the /preview/{port} path form.
func relayForms() map[string]relayGetter {
	return map[string]relayGetter{
		"subdomain": func(t *testing.T, agentURL string, hdr map[string]string) *httptest.ResponseRecorder {
			e := newPreviewHostEnv(t, agentURL)
			slug := e.mintSlug(t)
			host := previewHostname(slug, 8080, e.domain)
			req := httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil)
			req.Host = host
			req.Header.Set("X-Forwarded-Proto", "https")
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			// Sign-in handshake is not under test: make the viewer the owner via the
			// public mode, which serves without one.
			e.setPublic(t)
			rec := httptest.NewRecorder()
			e.host.ServeHTTP(rec, req)
			return rec
		},
		"path": func(t *testing.T, agentURL string, hdr map[string]string) *httptest.ResponseRecorder {
			mux := newPreviewTenantTestEnv(t, agentURL)
			req := httptest.NewRequest(http.MethodGet, "/preview/8080/?tenant=default", nil)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			return rec
		},
	}
}

func TestPreviewRelayPassesAgentErrorKindThrough(t *testing.T) {
	t.Parallel()
	for form, get := range relayForms() {
		for _, kind := range []string{"preview_unreachable", "preview_not_http", "preview_tls", "preview_failed"} {
			t.Run(form+"/"+kind, func(t *testing.T) {
				t.Parallel()
				agent := fakeAgentPreviewError(kind)
				defer agent.Close()
				rec := get(t, agent.URL, map[string]string{"Accept-Language": "en", "Accept": "application/json"})
				if rec.Code != http.StatusBadGateway {
					t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
				}
				var body struct {
					Error struct{ Code, Message string }
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("body %q: %v", rec.Body, err)
				}
				if body.Error.Code != kind {
					t.Errorf("code = %q, want %q (not rewritten to the generic one)", body.Error.Code, kind)
				}
				if !strings.Contains(body.Error.Message, "8080") {
					t.Errorf("message %q does not name the port", body.Error.Message)
				}
				if strings.Contains(rec.Body.String(), "10.0.0.9") || strings.Contains(rec.Body.String(), "internal detail") {
					t.Errorf("Agent's internal text leaked: %s", rec.Body)
				}
				if rec.Header().Get(previewErrorHeader) != "" {
					t.Errorf("internal marker header leaked to the browser")
				}
			})
		}
	}
}

func TestPreviewRelayRendersLocalizedPageForNavigation(t *testing.T) {
	t.Parallel()
	agent := fakeAgentPreviewError("preview_tls")
	t.Cleanup(agent.Close)
	for form, get := range relayForms() {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			rec := get(t, agent.URL, map[string]string{"Accept": "text/html", "Accept-Language": "en"})
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("Content-Type = %q", ct)
			}
			if !strings.Contains(rec.Body.String(), "serving HTTPS") {
				t.Errorf("en page = %q", rec.Body)
			}
			rec = get(t, agent.URL, map[string]string{"Accept": "text/html", "Accept-Language": "ja"})
			if !strings.Contains(rec.Body.String(), "HTTPS で動いています") {
				t.Errorf("ja page = %q", rec.Body)
			}
		})
	}
}

// The CP's own dial failure toward the Agent keeps its own code and message.
func TestPreviewRelayDialFailureIsWorkspaceUnreachable(t *testing.T) {
	t.Parallel()
	// Port 0 can never be dialled, so no parallel test's server can answer in its place (a closed
	// httptest port could be reused by one).
	url := "http://127.0.0.1:0"
	for form, get := range relayForms() {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			rec := get(t, url, map[string]string{"Accept-Language": "en"})
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}
			var body struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rec.Body, err)
			}
			if body.Error.Code != previewWorkspaceUnreachable || !strings.Contains(body.Error.Message, "workspace is not running") {
				t.Errorf("got %+v", body.Error)
			}
			if strings.Contains(body.Error.Message, "listening") {
				t.Errorf("dial failure must not claim nothing is listening: %q", body.Error.Message)
			}
		})
	}
}

// An app's own 502 (no marker) is not the relay's to rewrite, and a forged marker with an
// unknown value is left alone.
func TestPreviewRelayLeavesAppBadGatewayAlone(t *testing.T) {
	t.Parallel()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream app says no", http.StatusBadGateway)
	}))
	t.Cleanup(agent.Close)
	for form, get := range relayForms() {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			rec := get(t, agent.URL, nil)
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream app says no") {
				t.Fatalf("got %d %q", rec.Code, rec.Body)
			}
		})
	}
}

func (e *previewHostEnv) setPublic(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptPrefersHTML(t *testing.T) {
	t.Parallel()
	for accept, want := range map[string]bool{
		"":                                    false,
		"*/*":                                 false,
		"text/html":                           true,
		"text/html,application/xhtml+xml,*/*": true,
		"application/json":                    false,
		"application/json,text/html;q=0":      false,
		"application/json, text/html":         false, // tie: first listed wins
		"text/html, application/json":         true,
		"text/html;q=0.5, application/json":   false,
		"application/json;q=0.2, text/html":   true,
	} {
		if got := acceptPrefersHTML(accept); got != want {
			t.Errorf("acceptPrefersHTML(%q) = %v, want %v", accept, got, want)
		}
	}
}

// The marker is read from the final response header only: declaring it as a trailer on an
// app's own 502 does not turn that response into a relay error page.
func TestPreviewRelayIgnoresMarkerInTrailer(t *testing.T) {
	t.Parallel()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", previewErrorHeader)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("app body"))
		w.Header().Set(previewErrorHeader, "preview_tls")
	}))
	t.Cleanup(agent.Close)
	for form, get := range relayForms() {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			rec := get(t, agent.URL, nil)
			if rec.Code != http.StatusBadGateway || rec.Body.String() != "app body" {
				t.Fatalf("got %d %q, want the app's own 502", rec.Code, rec.Body)
			}
		})
	}
}
