package main

// The work item family lives in internal/workitems, and its contract tests build their own copy
// of these routes (internal/workitems/mux_test.go). What only the real route table can show stays
// here: that routes.go wraps the two writes in memberorigin.Require with their limits.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/workitems"
)

var workItemWriteRoutes = map[string]int{
	"/work-items/comment": workitems.WorkItemCommentMaxRequest,
	"/work-items/merge":   workitems.WorkItemMergeMaxRequest,
}

func workItemReq(p, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, p, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer agent-token")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// AGENT_TOKEN and the relay hint alone are refused by the real route table (ADR 0113). The
// handlers refuse an unmarked request themselves too, so this alone would stay green without the
// wrapper; the two tests below would not.
func TestWorkItemWriteRoutesRequireTheMark(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	memberorigintest.Install(t)
	mux := buildMux()
	for p := range workItemWriteRoutes {
		r := workItemReq(p, `{}`)
		r.Header.Set("X-AF-Relay", "cp")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), memberorigin.CodeRequired) {
			t.Errorf("%s without a mark: %d %s", p, w.Code, w.Body)
		}
	}
}

// A marked request gets past the wrapper to the handler: an empty body is its validation error.
func TestWorkItemWriteRoutesAdmitAMarkedRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := memberorigintest.Install(t)
	mux := buildMux()
	for p := range workItemWriteRoutes {
		r := workItemReq(p, `{}`)
		s.Sign(r, []byte(`{}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "member_origin") {
			t.Errorf("%s with a mark: %d %s", p, w.Code, w.Body)
		}
	}
}

// The wrapper on each route carries that route's limit: a marked body one byte over it is refused
// before the handler, and text/plain (what a cross-origin page sends without a preflight) is too.
func TestWorkItemWriteRoutesCarryTheirLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := memberorigintest.Install(t)
	mux := buildMux()
	for p, limit := range workItemWriteRoutes {
		body := `{"key":"` + strings.Repeat("a", limit) + `"}`
		r := workItemReq(p, body)
		s.Sign(r, []byte(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s over %d bytes: %d %s", p, limit, w.Code, w.Body)
		}
		plain := workItemReq(p, `{}`)
		plain.Header.Set("Content-Type", "text/plain")
		s.Sign(plain, []byte(`{}`))
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, plain)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s text/plain: %d %s", p, w.Code, w.Body)
		}
	}
}
