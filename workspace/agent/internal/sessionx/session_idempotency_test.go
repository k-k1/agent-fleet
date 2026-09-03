package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestCreateLedger exercises the in-memory idempotency ledger's state machine directly:
// a first begin claims the key; a concurrent begin sees inflight; complete flips it to
// done (with a replay body); a later begin replays; and fail only clears an inflight key.
func TestCreateLedger(t *testing.T) {
	l := &createSessionLedger{m: map[string]*createLedgerEntry{}}

	if _, dup := l.begin("k1"); dup {
		t.Fatal("first begin must own the key (dup=false)")
	}
	prev, dup := l.begin("k1")
	if !dup || prev.state != createInflight {
		t.Fatalf("second begin: dup=%v state=%v, want true/inflight", dup, prev.state)
	}

	l.complete("k1", []byte(`{"name":"sabc"}`))
	prev, dup = l.begin("k1")
	if !dup || prev.state != createDone || string(prev.body) != `{"name":"sabc"}` {
		t.Fatalf("after complete: dup=%v state=%v body=%q", dup, prev.state, prev.body)
	}

	// fail must NOT drop a completed entry (replay must survive a later errored retry).
	l.fail("k1")
	if _, ok := l.lookup("k1"); !ok {
		t.Fatal("completed entry was wrongly dropped by fail")
	}

	// fail DROPS an inflight entry so a genuine retry can proceed.
	if _, dup := l.begin("k2"); dup {
		t.Fatal("k2 first begin should own")
	}
	l.fail("k2")
	if _, dup := l.begin("k2"); dup {
		t.Fatal("after fail, k2 must be claimable again (dup=false)")
	}
}

// TestCreateIdempotencyKey pins the server-side key policy: explicit key wins; otherwise a
// report_to-scoped fingerprint that is stable for identical intent and differs when intent
// differs; and no dedupe (empty key) when there is neither an explicit key nor a conversation.
func TestCreateIdempotencyKey(t *testing.T) {
	if got := createIdempotencyKey(&CreateReq{IdempotencyKey: "explicit"}); got != "explicit" {
		t.Fatalf("explicit key not honored: %q", got)
	}
	if got := createIdempotencyKey(&CreateReq{Dir: "/x", InitialPrompt: "hi"}); got != "" {
		t.Fatalf("no key + no report_to must not dedupe, got %q", got)
	}
	a := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Dir: "/x", Kind: "claude", InitialPrompt: "go"})
	b := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Dir: "/x", Kind: "claude", InitialPrompt: "go"})
	if a == "" || a != b {
		t.Fatalf("identical intent must yield identical key: %q vs %q", a, b)
	}
	if c := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Dir: "/x", Kind: "claude", InitialPrompt: "GO"}); c == a {
		t.Fatal("different prompt must yield a different key")
	}
	if c := createIdempotencyKey(&CreateReq{ReportTo: "conv2", Dir: "/x", Kind: "claude", InitialPrompt: "go"}); c == a {
		t.Fatal("different conversation must yield a different key")
	}
}

// TestCreateSessionKeyStable pins the tool-side key: deterministic across identical args
// (so an LLM retry reproduces it) and sensitive to a changed arg.
func TestCreateSessionKeyStable(t *testing.T) {
	k1 := mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "task", true, "main", "feat")
	k2 := mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "task", true, "main", "feat")
	if k1 == "" || k1 != k2 {
		t.Fatalf("key not stable: %q vs %q", k1, k2)
	}
	if mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "task2", true, "main", "feat") == k1 {
		t.Fatal("changed prompt must change the key")
	}
	// A different subdir is a different launch intent — two sessions in the same repo
	// but different folders must not collapse onto one another.
	if mcpx.CreateSessionKey("conv", "/d", "console", "claude", "opus", "task", true, "main", "feat") == k1 {
		t.Fatal("changed subdir must change the key")
	}
}

// TestCreateSessionDedupeHTTP drives two identical POST /sessions with the same
// idempotency_key over real HTTP: the second must REPLAY the first session (same name, 200)
// rather than spawn a duplicate, and the reconcile lookup must return that same session.
func TestCreateSessionDedupeHTTP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", HandleListSessions)
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	mux.HandleFunc("GET /sessions-idempotency/{key}", HandleIdempotencyLookup)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := map[string]any{"dir": parent, "kind": "shell", "idempotency_key": "cs_test"}

	var first session.Session
	do(t, srv, "POST", "/sessions", body, http.StatusCreated, &first)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(first.Name)).Run()

	// Retry with the same key: the backend already finished, so this must REPLAY (200 +
	// same session) — not create a second one.
	status, raw := roundtrip(t, srv, "POST", "/sessions", body)
	if status != http.StatusOK {
		t.Fatalf("retry status = %d, want 200 replay", status)
	}
	var second session.Session
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if second.Name != first.Name {
		t.Fatalf("replay returned a different session: %q vs %q (DUPLICATE)", second.Name, first.Name)
	}

	// Exactly one session was created in our dir (the host's tmux may host unrelated
	// orphan sessions, so scope the count to this test's working copy — a failed dedupe
	// would have spawned a SECOND session with a different random name in the same dir).
	var list struct{ Sessions []session.Session }
	do(t, srv, "GET", "/sessions", nil, http.StatusOK, &list)
	n := 0
	for _, s := range list.Sessions {
		if s.Dir == parent {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one session in %s, got %d (DUPLICATE)", parent, n)
	}

	// The reconcile lookup resolves the key to that same session.
	var looked session.Session
	do(t, srv, "GET", "/sessions-idempotency/cs_test", nil, http.StatusOK, &looked)
	if looked.Name != first.Name {
		t.Fatalf("lookup returned %q, want %q", looked.Name, first.Name)
	}
}
