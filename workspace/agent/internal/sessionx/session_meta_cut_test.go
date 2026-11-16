package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A meta file that exists but cannot be read (empty, or cut in the middle of an object) is in
// doubt for every reader that decides something: nothing is stopped or started on it, and it is
// reported. A file that does not exist stays "absent".
var cutMetaBodies = map[string]string{"empty": "", "half-object": `{"name":"c","kind":"cl`}

func writeCutMeta(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(session.MetaDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.MetaPath(name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRetireTreeNamesAnUnreadableMetaAndIsNotComplete(t *testing.T) {
	for label, body := range cutMetaBodies {
		t.Run(label, func(t *testing.T) {
			newRetireEnv(t)
			writeMeta(t, session.Meta{Name: "p"})
			writeCutMeta(t, "c", body) // may be a child of p: unknown
			code, resp := postRetire(t, "p")
			if code != http.StatusOK {
				t.Fatalf("retire = %d", code)
			}
			if resp.Complete {
				t.Fatal("the retire answered complete with a meta in doubt")
			}
			if len(resp.Sessions) == 0 || !strings.Contains(strings.Join(resp.Sessions[0].Unended, "|"), "meta of c ") {
				t.Fatalf("the unreadable meta is not named in unended: %+v", resp.Sessions)
			}
			if got, _ := os.ReadFile(session.MetaPath("c")); string(got) != body {
				t.Fatalf("the retire touched the unreadable file: %q", got)
			}
		})
	}
}

func TestRetireOfAnUnreadableRootIsNotNotFound(t *testing.T) {
	for label, body := range cutMetaBodies {
		t.Run(label, func(t *testing.T) {
			newRetireEnv(t)
			writeCutMeta(t, "c", body)
			if code, _ := postRetire(t, "c"); code != http.StatusServiceUnavailable {
				t.Fatalf("retire of an unreadable root = %d, want 503 (404 would say it is gone)", code)
			}
		})
	}
}

func TestAutomaticStartOfAnUnreadableMetaIsRefused(t *testing.T) {
	for label, body := range cutMetaBodies {
		t.Run(label, func(t *testing.T) {
			r := newRetireEnv(t)
			writeMeta(t, session.Meta{Name: "c", RetiredAt: "2026-11-16T00:00:00Z"})
			// The lifecycle gate read a whole meta; the handler's own read then meets a cut one.
			afterStartGate = func() { writeCutMeta(t, "c", body) }
			t.Cleanup(func() { afterStartGate = nil })
			req := httptest.NewRequest(http.MethodPost, "/sessions/c/start", nil)
			req.SetPathValue("name", "c")
			rec := httptest.NewRecorder()
			HandleStartSession(rec, asAutomaticStart(req))
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") {
				t.Fatalf("automatic start = %d %s, want a session_retired refusal", rec.Code, rec.Body)
			}
			if len(r.started) != 0 {
				t.Fatalf("started on a meta that could not be read: %v", r.started)
			}
		})
	}
}

func TestRecordPersonActOnAnUnreadableMetaIsAnError(t *testing.T) {
	for label, body := range cutMetaBodies {
		t.Run(label, func(t *testing.T) {
			newRetireEnv(t)
			writeCutMeta(t, "c", body)
			if err := recordPersonAct("c"); err == nil {
				t.Fatal("recordPersonAct answered nil for a meta it could not read: the act reads as recorded")
			}
			if got, _ := os.ReadFile(session.MetaPath("c")); string(got) != body {
				t.Fatalf("the file was rewritten: %q", got)
			}
		})
	}
	t.Run("absent stays nil", func(t *testing.T) {
		newRetireEnv(t)
		if err := recordPersonAct("ghost"); err != nil {
			t.Fatalf("an absent meta is not an error here: %v", err)
		}
	})
}

func TestWatchBarredTreatsAnUnreadableMetaAsRetired(t *testing.T) {
	for label, body := range cutMetaBodies {
		t.Run(label, func(t *testing.T) {
			newRetireEnv(t)
			writeCutMeta(t, "c", body)
			if !watchBarred("c") {
				t.Fatal("a subscription may be made for a session whose retired mark could not be read")
			}
		})
	}
	newRetireEnv(t)
	if watchBarred("ghost") {
		t.Fatal("an absent session is barred")
	}
}
