package sessionx

// The per-name read caches (folded file list, priced spend) are dropped when a session is
// folded away by delete, archive or recreate, and kept across a halt. Each case goes through
// the real entry point; a direct forget* call would pass without the wiring.

import (
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func seedSessionCaches(t *testing.T, name string) {
	t.Helper()
	fileAggMu.Lock()
	fileAggs[name] = &fileAggEntry{used: time.Now()}
	fileAggMu.Unlock()
	spendCache.Lock()
	spendCache.m[name] = spendEntry{at: time.Now(), spend: session.Spend{USD: 1, Priced: true}}
	spendCache.Unlock()
	t.Cleanup(func() { forgetSessionCaches(name) })
}

func sessionCachesHeld(name string) (files, spend bool) {
	fileAggMu.Lock()
	_, files = fileAggs[name]
	fileAggMu.Unlock()
	spendCache.Lock()
	_, spend = spendCache.m[name]
	spendCache.Unlock()
	return files, spend
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
	ForgetRuntime(m) // the trash's cleanup step (cleanup_ops.go trashSession)
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
	callHandler(HandleRecreateSession, http.MethodPost, name, "")
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
