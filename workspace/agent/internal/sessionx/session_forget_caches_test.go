package sessionx

// The per-name read caches (folded file list, priced spend) are dropped when a session is
// folded away by delete, archive, recreate or the stopped-TTL sweep, and kept across a halt.
// Archive, recreate, halt and the sweep go through their real handlers; delete is pinned
// end to end by TestTrashSessionForgetsSessionCaches in package main (trashSession lives there).

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func seedSessionCaches(t *testing.T, name string) {
	t.Helper()
	SeedSessionCachesForTest(name)
	t.Cleanup(func() { forgetSessionCaches(name) })
}

func assertCaches(t *testing.T, name string, want bool) {
	t.Helper()
	if f, s := sessionCachesHeld(name); f != want || s != want {
		t.Fatalf("caches held (files=%v spend=%v), want both %v", f, s, want)
	}
}

func TestDeleteForgetsSessionCaches(t *testing.T) {
	home := writebackEnv(t)
	const name = "fgtdel1"
	m := session.Meta{Name: name, Dir: home, Kind: session.KindClaude}
	session.WriteMeta(m)
	seedSessionCaches(t, name)
	ForgetRuntime(m) // the step trashSession runs; the full path is tested in package main
	assertCaches(t, name, false)
}

func TestArchiveForgetsSessionCaches(t *testing.T) {
	home := writebackEnv(t)
	const name = "fgtarc1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	seedSessionCaches(t, name)
	if w := callHandler(HandleArchiveSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
		t.Fatalf("archive = %d %s", w.Code, w.Body)
	}
	assertCaches(t, name, false)
}

func TestRecreateForgetsOldSessionCaches(t *testing.T) {
	home := writebackEnv(t)
	const name = "fgtrec1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	seedSessionCaches(t, name)
	orig := launchTmuxFn
	launchTmuxFn = func(session.Meta, bool) error { return http.ErrAbortHandler } // the launch is not under test
	t.Cleanup(func() { launchTmuxFn = orig })
	// The launch failure makes recreate answer 500, after the old identity was folded away.
	if w := callHandler(HandleRecreateSession, http.MethodPost, name, ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("recreate = %d %s, want the launch failure (500)", w.Code, w.Body)
	}
	assertCaches(t, name, false)
}

// The stopped-TTL auto-archive in the session list is a second archive path that does not go
// through ArchiveSession.
func TestStoppedTTLAutoArchiveForgetsSessionCaches(t *testing.T) {
	home := writebackEnv(t)
	t.Setenv("AF_SESSION_STOPPED_TTL", "1h")
	const name = "fgtttl1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude,
		StoppedAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339)})
	seedSessionCaches(t, name)
	prev := deps.FinalizeSessionUsage
	deps.FinalizeSessionUsage = func(session.Meta) {} // the usage ledger is not under test
	t.Cleanup(func() { deps.FinalizeSessionUsage = prev })
	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	HandleListSessions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta(name); !m.Archived {
		t.Fatal("the list did not auto-archive the stopped session; the test setup is wrong")
	}
	assertCaches(t, name, false)
}

// A halted session is resumed in place, so its entries stay: the spend cap is enforced from
// the same figure after a stop-then-resume.
func TestHaltKeepsSessionCaches(t *testing.T) {
	home := writebackEnv(t)
	const name = "fgthalt1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	seedSessionCaches(t, name)
	if w := callHandler(HandleHaltSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
		t.Fatalf("halt = %d %s", w.Code, w.Body)
	}
	assertCaches(t, name, true)
}
