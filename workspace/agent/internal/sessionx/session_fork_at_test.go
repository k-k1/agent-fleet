package sessionx

// The fork-point handling of POST /sessions/{name}/fork (docs/log/55). What must never
// happen here is "a point was named, but the whole conversation was forked", so every
// fork point that cannot be resolved stops with a 4xx. An older client that names no fork
// point still goes through as before (backward compatibility).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func forkReq(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/fork", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/fork", strings.NewReader(body))
	}
	r.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleForkSession(rec, r)
	return rec
}

// A kind that does not support forking at all is refused before `at` is even considered.
func TestForkUnsupportedKind(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "cur1", Dir: t.TempDir(), Kind: session.KindCursor})
	rec := forkReq(t, "cur1", `{"at":"some-uuid"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fork_unsupported_kind") {
		t.Fatalf("body = %s; want fork_unsupported_kind", rec.Body.String())
	}
}

// Even on a supported kind, the CLI (TUI) route has no way to pass a fork point
// (opencode/codex). The UI hides it too, but a direct call must stop here. This also pins
// that the reason is reported BEFORE "there is no conversation yet": telling a session on
// the wrong route to "add more conversation" would not fix anything.
func TestForkAtRefusedOnCLIRoute(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{
		Name: "oc1", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI,
	})
	rec := forkReq(t, "oc1", `{"at":"msg_1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fork_at_unsupported") {
		t.Fatalf("body = %s; want fork_at_unsupported", rec.Body.String())
	}
}

// claude has no managed driver. While managed was required across the board, claude was
// rejected here forever, so pin that a TUI claude is not refused on account of its route
// (it still fails for another reason, since there is no conversation — what is checked is
// that the reason is not fork_at_unsupported).
func TestForkAtClaudeTUINotRefusedByRoute(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{
		Name: "cl1", Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverTUI,
	})
	rec := forkReq(t, "cl1", `{"at":"some-uuid"}`)
	if strings.Contains(rec.Body.String(), "fork_at_unsupported") {
		t.Fatalf("body = %s; claude only has TUI, so it must not be refused on account of its route", rec.Body.String())
	}
}

// Silently ignoring a malformed body would turn a request that meant to name a point into
// a fork of the whole conversation.
func TestForkRejectsMalformedBody(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "oc2", Dir: t.TempDir(), Kind: session.KindOpencode})
	rec := forkReq(t, "oc2", `{"at":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bad_request") {
		t.Fatalf("body = %s; want bad_request", rec.Body.String())
	}
}

// Backward compatibility: with no body (a client older than docs/log/55) the fork-point
// checks are never entered. It stops at not_resumable because there is no conversation
// yet, and that the code is not fork_at_* is what shows none of the new fork-point gates
// were touched.
func TestForkWithoutBodyKeepsWholeConversationPath(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "oc3", Dir: t.TempDir(), Kind: session.KindOpencode})
	for _, body := range []string{"", "{}"} {
		rec := forkReq(t, "oc3", body)
		if got := rec.Body.String(); strings.Contains(got, "fork_at_unsupported") || strings.Contains(got, "fork_bad_anchor") {
			t.Fatalf("body %q → %s; the anchor gates must not fire without `at`", body, got)
		}
	}
}
