package sessionx

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// TestCreateLedger exercises the in-memory idempotency ledger's state machine directly:
// a first begin claims the key; a concurrent begin sees inflight; complete flips it to
// done (with a replay body); a later begin replays; and fail only clears an inflight key.
func TestCreateLedger(t *testing.T) {
	l := &createSessionLedger{m: map[string]*createLedgerEntry{}}

	if _, dup := l.begin("k1", false); dup {
		t.Fatal("first begin must own the key (dup=false)")
	}
	prev, dup := l.begin("k1", false)
	if !dup || prev.state != createInflight {
		t.Fatalf("second begin: dup=%v state=%v, want true/inflight", dup, prev.state)
	}

	l.complete("k1", []byte(`{"name":"sabc"}`))
	prev, dup = l.begin("k1", false)
	if !dup || prev.state != createDone || string(prev.body) != `{"name":"sabc"}` {
		t.Fatalf("after complete: dup=%v state=%v body=%q", dup, prev.state, prev.body)
	}

	// fail must NOT drop a completed entry (replay must survive a later errored retry).
	l.fail("k1")
	if _, ok := l.lookup("k1"); !ok {
		t.Fatal("completed entry was wrongly dropped by fail")
	}

	// fail DROPS an inflight entry so a genuine retry can proceed.
	if _, dup := l.begin("k2", false); dup {
		t.Fatal("k2 first begin should own")
	}
	l.fail("k2")
	if _, dup := l.begin("k2", false); dup {
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
	// A space (ADR 0142) is part of the intent, and only when present: a fingerprint without one
	// stays what it was.
	home := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Kind: "claude", InitialPrompt: "go"})
	inA := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Kind: "claude", InitialPrompt: "go", Space: &SpaceTarget{Folder: "a-abcdef"}})
	inB := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Kind: "claude", InitialPrompt: "go", Space: &SpaceTarget{Folder: "b-abcdef"}})
	inNew := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Kind: "claude", InitialPrompt: "go", Space: &SpaceTarget{Name: "notes"}})
	again := createIdempotencyKey(&CreateReq{ReportTo: "conv1", Kind: "claude", InitialPrompt: "go", Space: &SpaceTarget{Folder: " a-abcdef "}})
	if inA == home || inA == inB || inA == inNew || inNew == home {
		t.Fatalf("a space must be part of the key: home %q, a %q, b %q, new %q", home, inA, inB, inNew)
	}
	if again != inA {
		t.Fatal("the same space spelled with spaces around it must keep the key")
	}
	if home != createIdempotencyKey(&CreateReq{ReportTo: "conv1", Kind: "claude", InitialPrompt: "go", Space: nil}) {
		t.Fatal("no space must keep the old key")
	}
}

// TestCreateSessionKeyStable pins the tool-side key: deterministic across identical args
// (so an LLM retry reproduces it) and sensitive to a changed arg.
func TestCreateSessionKeyStable(t *testing.T) {
	k1 := mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "", "task", true, "main", "feat", false)
	k2 := mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "", "task", true, "main", "feat", false)
	if k1 == "" || k1 != k2 {
		t.Fatalf("key not stable: %q vs %q", k1, k2)
	}
	if mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "", "task2", true, "main", "feat", false) == k1 {
		t.Fatal("changed prompt must change the key")
	}
	// A different subdir is a different launch intent — two sessions in the same repo
	// but different folders must not collapse onto one another.
	if mcpx.CreateSessionKey("conv", "/d", "console", "claude", "opus", "", "task", true, "main", "feat", false) == k1 {
		t.Fatal("changed subdir must change the key")
	}
	// Two creates that differ only by effort are different requests; the server-side intent
	// fingerprint already counts effort, and the tool key must not be coarser than it.
	if mcpx.CreateSessionKey("conv", "/d", "", "claude", "opus", "high", "task", true, "main", "feat", false) == k1 {
		t.Fatal("changed effort must change the key")
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
	testguard.IsolateTmux(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	// The ledger is process-wide: a previous -count round's "cs_test" would replay here.
	prevLedger := createLedger
	createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}}
	t.Cleanup(func() { createLedger = prevLedger })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", HandleListSessions)
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	mux.HandleFunc("GET /sessions-idempotency/{key}", HandleIdempotencyLookup)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := map[string]any{"dir": parent, "kind": "shell", "idempotency_key": "cs_test"}

	var first session.Session
	do(t, srv, "POST", "/sessions", body, http.StatusCreated, &first)
	defer tmuxx.Cmd("kill-session", "-t", session.TmuxName(first.Name)).Run()

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

// useCreateOutcomeDir points the durable records and the ledger at a fresh place.
func useCreateOutcomeDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "outcomes")
	prevDir, prevLedger := createOutcomeDir, createLedger
	createOutcomeDir = func() string { return dir }
	createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}}
	t.Cleanup(func() { createOutcomeDir, createLedger = prevDir, prevLedger })
	return dir
}

// TestCreateLedgerKeepsInflightPastTTL: a create that runs longer than the TTL (a slow clone)
// is still running. Forgetting it would answer "no such create" while it may still make the
// session (issue #2040).
func TestCreateLedgerKeepsInflightPastTTL(t *testing.T) {
	useCreateOutcomeDir(t)
	createLedger.m["slow"] = &createLedgerEntry{state: createInflight, at: time.Now().Add(-createLedgerTTL - time.Second)}
	createLedger.m["old-done"] = &createLedgerEntry{state: createDone, body: []byte(`{}`), at: time.Now().Add(-createLedgerTTL - time.Second)}
	if e, ok := createLedger.lookup("slow"); !ok || e.state != createInflight {
		t.Fatalf("a create past the TTL = %+v ok=%v, want still inflight", e, ok)
	}
	if _, ok := createLedger.lookup("old-done"); ok {
		t.Fatal("a finished record past the TTL is still kept in memory")
	}
}

// TestDurableCreateOutcome is the record a handoff launch's outcome is reconciled with (issue
// #2040): it survives a restart; abandon settles only a reserved key, so a key with no record
// (one an Agent without records created under) stays unknown; a refusal is final, so a late
// one stays true; an unreadable record or one cut off by a restart is unknown, never "not
// created".
func TestDurableCreateOutcome(t *testing.T) {
	dir := useCreateOutcomeDir(t)
	restart := func() { createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}} }
	mustReserve := func(key string) {
		t.Helper()
		if err := createLedger.reserve(key); err != nil {
			t.Fatal(err)
		}
	}

	mustReserve("handoff-a")
	if _, dup := createLedger.begin("handoff-a", true); dup {
		t.Fatal("first begin after the reservation must own the key")
	}
	if e := createLedger.abandon("handoff-a"); e.state != createInflight {
		t.Fatalf("abandon of a running create = %+v, want inflight reported and nothing recorded", e)
	}
	createLedger.complete("handoff-a", []byte(`{"name":"s-a"}`))
	restart()
	if e, ok := createLedger.lookup("handoff-a"); !ok || e.state != createDone || string(e.body) != `{"name":"s-a"}` {
		t.Fatalf("after a restart = %+v ok=%v, want the done record", e, ok)
	}
	if e, dup := createLedger.begin("handoff-a", true); !dup || e.state != createDone {
		t.Fatalf("a create under a done key = %+v dup=%v, want the replay", e, dup)
	}

	// A reserved key nothing began under is abandoned, and a late create is refused.
	mustReserve("handoff-b")
	if e, ok := createLedger.lookup("handoff-b"); !ok || e.state != createReserved {
		t.Fatalf("lookup of a reserved key = %+v ok=%v", e, ok)
	}
	if e := createLedger.abandon("handoff-b"); e.state != createAbandoned {
		t.Fatalf("abandon of a reserved key = %+v", e)
	}
	restart()
	if e, dup := createLedger.begin("handoff-b", true); !dup || e.state != createAbandoned {
		t.Fatalf("a late create under an abandoned key = %+v dup=%v, want refused", e, dup)
	}

	// No record at all proves nothing: an Agent that kept none may have created under it.
	if e := createLedger.abandon("handoff-legacy"); e.state != createUnknown {
		t.Fatalf("abandon of a key with no record = %+v, want unknown", e)
	}

	// A refusal is final: a retry under the key is refused, so a refusal that arrives late
	// still means no session exists under it.
	mustReserve("handoff-c")
	if _, dup := createLedger.begin("handoff-c", true); dup {
		t.Fatal("begin")
	}
	createLedger.fail("handoff-c")
	if e, dup := createLedger.begin("handoff-c", true); !dup || e.state != createRefused {
		t.Fatalf("a retry under a refused key = %+v dup=%v, want refused", e, dup)
	}
	restart()
	if e, dup := createLedger.begin("handoff-c", true); !dup || e.state != createRefused {
		t.Fatalf("a retry under a refused key after a restart = %+v dup=%v", e, dup)
	}
	if e := createLedger.abandon("handoff-c"); e.state != createRefused {
		t.Fatalf("abandon of a refused key = %+v", e)
	}
	// A plain key is still dropped on failure, letting a genuine retry through.
	if _, dup := createLedger.begin("plain", false); dup {
		t.Fatal("begin")
	}
	createLedger.fail("plain")
	if _, dup := createLedger.begin("plain", false); dup {
		t.Fatal("a plain key stayed refused")
	}

	// A create cut off by a restart, and a record that cannot be read, are unknown.
	mustReserve("handoff-d")
	if _, dup := createLedger.begin("handoff-d", true); dup {
		t.Fatal("begin")
	}
	restart()
	prevProc := createOutcomeProc
	createOutcomeProc = "next-process"
	t.Cleanup(func() { createOutcomeProc = prevProc })
	if e := createLedger.abandon("handoff-d"); e.state != createUnknown {
		t.Fatalf("abandon of a create cut off by a restart = %+v, want unknown", e)
	}
	if err := os.WriteFile(createOutcomePath("handoff-e"), []byte(`{"key":"handoff-e","sta`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := createLedger.abandon("handoff-e"); e.state != createUnknown {
		t.Fatalf("abandon of an unreadable record = %+v, want unknown", e)
	}
	if e, ok := createLedger.lookup("handoff-e"); !ok || e.state != createUnknown {
		t.Fatalf("lookup of an unreadable record = %+v ok=%v", e, ok)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 5 { // a done, b abandoned, c refused, d inflight, e unreadable; no temp files
		t.Fatalf("records on disk = %d, want 5", len(ents))
	}
}

// TestDurableKeyNeverLogged: a durable key carries the caller's take token, so a failure to
// record it names a hash of it instead.
func TestDurableKeyNeverLogged(t *testing.T) {
	dir := useCreateOutcomeDir(t)
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	const key = "handoff-offer-secrettoken"
	_ = createLedger.reserve(key)
	_, _ = createLedger.begin(key, true)
	createLedger.m[key] = &createLedgerEntry{state: createInflight, at: time.Now(), durable: true}
	createLedger.complete(key, []byte(`{}`))
	createLedger.m[key] = &createLedgerEntry{state: createInflight, at: time.Now(), durable: true}
	createLedger.fail(key)
	if !strings.Contains(buf.String(), keyRef(key)) {
		t.Fatalf("no failure was logged at all: %q", buf.String())
	}
	if strings.Contains(buf.String(), "secrettoken") {
		t.Fatalf("the key reached the log: %q", buf.String())
	}
}

// TestDurableCreateHTTP drives a create under a durable key over real HTTP: its answer names
// the key and the outcome in headers no proxy writes, a refused one says refused and leaves
// nothing to find, abandon settles a key nothing was created under, and a create that arrives
// after the abandon is refused instead of run (issue #2040).
func TestDurableCreateHTTP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	testguard.IsolateTmux(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	useCreateOutcomeDir(t)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	mux.HandleFunc("GET /sessions-idempotency/{key}", HandleIdempotencyLookup)
	mux.HandleFunc("POST /sessions-idempotency/{key}/abandon", HandleIdempotencyAbandon)
	mux.HandleFunc("POST /sessions-idempotency/{key}/reserve", HandleIdempotencyReserve)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(path string, body any) *http.Response {
		t.Helper()
		var rd *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		} else {
			rd = strings.NewReader("")
		}
		resp, err := http.Post(srv.URL+path, "application/json", rd)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	for _, k := range []string{"handoff-o-1", "handoff-o-2", "handoff-o-3"} {
		if resp := post("/sessions-idempotency/"+k+"/reserve", nil); resp.StatusCode != http.StatusOK || resp.Header.Get(createKeyHeader) != k {
			t.Fatalf("reserve %s = %d", k, resp.StatusCode)
		}
	}
	resp := post("/sessions", map[string]any{"dir": parent, "kind": "shell", "idempotency_key": "handoff-o-1", "idempotency_durable": true})
	var created session.Session
	_ = json.NewDecoder(resp.Body).Decode(&created)
	defer tmuxx.Cmd("kill-session", "-t", session.TmuxName(created.Name)).Run()
	if resp.StatusCode != http.StatusCreated || resp.Header.Get(createKeyHeader) != "handoff-o-1" || resp.Header.Get(createOutcomeHeader) != "created" {
		t.Fatalf("create = %d %v", resp.StatusCode, resp.Header)
	}
	createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}} // the Agent restarted
	var looked session.Session
	do(t, srv, "GET", "/sessions-idempotency/handoff-o-1", nil, http.StatusOK, &looked)
	if looked.Name != created.Name {
		t.Fatalf("lookup after a restart = %q, want %q", looked.Name, created.Name)
	}

	resp = post("/sessions", map[string]any{"dir": parent, "kind": "shell", "title": strings.Repeat("x", 200),
		"idempotency_key": "handoff-o-2", "idempotency_durable": true})
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get(createOutcomeHeader) != "refused" || resp.Header.Get(createKeyHeader) != "handoff-o-2" {
		t.Fatalf("refused create = %d %v", resp.StatusCode, resp.Header)
	}
	// The refusal is final: a retry under the same key is refused again, and says so.
	resp = post("/sessions", map[string]any{"dir": parent, "kind": "shell", "idempotency_key": "handoff-o-2", "idempotency_durable": true})
	if resp.StatusCode != http.StatusConflict || resp.Header.Get(createOutcomeHeader) != "refused" {
		t.Fatalf("retry under a refused key = %d %v, want refused again", resp.StatusCode, resp.Header)
	}
	resp = post("/sessions-idempotency/handoff-o-2/abandon", nil)
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get(createKeyHeader) != "handoff-o-2" {
		t.Fatalf("abandon after the refusal = %d", resp.StatusCode)
	}
	resp = post("/sessions-idempotency/handoff-o-1/abandon", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("abandon of a created key = %d, want 200 with the session", resp.StatusCode)
	}

	resp = post("/sessions-idempotency/handoff-o-3/abandon", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("abandon of a reserved, unused key = %d", resp.StatusCode)
	}
	resp = post("/sessions", map[string]any{"dir": parent, "kind": "shell", "idempotency_key": "handoff-o-3", "idempotency_durable": true})
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), `"create_abandoned"`) || resp.Header.Get(createOutcomeHeader) != "" {
		t.Fatalf("late create under an abandoned key = %d %s %v, want refused without an outcome", resp.StatusCode, b, resp.Header)
	}

	// A session an Agent without records created under a key (the create did not ask for
	// durability), then the Agent restarted: abandon cannot say "not created".
	resp = post("/sessions", map[string]any{"dir": parent, "kind": "shell", "idempotency_key": "handoff-legacy"})
	var legacy session.Session
	_ = json.NewDecoder(resp.Body).Decode(&legacy)
	defer tmuxx.Cmd("kill-session", "-t", session.TmuxName(legacy.Name)).Run()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("legacy create = %d", resp.StatusCode)
	}
	createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}}
	resp = post("/sessions-idempotency/handoff-legacy/abandon", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("abandon of a key created under without a record = %d, want 503 unknown", resp.StatusCode)
	}
}
