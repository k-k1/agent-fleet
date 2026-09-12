package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// aggFixture is a claude session mid-turn: a prompt, an edit (so `files` is not empty) and the
// live reply. Returns a poll function taking the query string after `?`.
func aggFixture(t *testing.T) func(query string) map[string]any {
	t.Helper()
	home := withTempHome(t)
	// HOME alone does not redirect claude's state: the container running this suite has
	// CLAUDE_CONFIG_DIR pointing at the real tree.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	const name = "agg_sess"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude, Title: "題あり"})
	sid := session.UUID(dir, name)
	jsonl := filepath.Join(home, ".claude", "projects", "p", sid+".jsonl")
	writeFile(t, jsonl, strings.Join([]string{
		`{"type":"user","timestamp":"2026-09-12T10:00:00Z","cwd":"/home/dev/repos/x","message":{"role":"user","content":"直して"}}`,
		`{"type":"assistant","timestamp":"2026-09-12T10:00:05Z","cwd":"/home/dev/repos/x","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/home/dev/repos/x/a.ts","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"assistant","timestamp":"2026-09-12T10:00:09Z","cwd":"/home/dev/repos/x","message":{"role":"assistant","content":[{"type":"text","text":"直しました"}]}}`,
	}, "\n")+"\n")

	return func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/messages?"+query, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionMessages(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("?%s: status = %d, body = %s", query, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("?%s: not JSON: %v (%s)", query, err, rec.Body.String())
		}
		return out
	}
}

// The steady-state poll of a client that already holds the aggregates must carry neither the
// file list nor the ToDo list nor the answers — only the flag that says so.
func TestIncrementalPollDropsHeldAggregates(t *testing.T) {
	poll := aggFixture(t)

	first := poll("since=3")
	sig, _ := first["aggSig"].(string)
	if sig == "" {
		t.Fatalf("no aggSig to hold on to: %v", first)
	}
	if first["files"] == nil {
		t.Fatalf("the fixture produced no files, so this proves nothing: %v", first)
	}
	if first["aggSame"] != nil {
		t.Fatalf("a client that sent no digest must be sent the aggregates: %v", first)
	}

	held := poll("since=3&agg=" + sig)
	if held["aggSame"] != true {
		t.Fatalf("an unchanged digest must be answered with aggSame: %v", held)
	}
	for _, k := range []string{"files", "tasks", "answers"} {
		if held[k] != nil {
			t.Errorf("%s was sent although the client holds it: %v", k, held[k])
		}
	}
	// The status half of the response is still there — this drops the aggregates, not the poll.
	if held["cursor"] == nil || held["status"] == nil {
		t.Errorf("the poll lost its status fields: %v", held)
	}

	// A stale digest is answered in full, or the list would freeze on whatever the client had.
	if stale := poll("since=3&agg=0000000000000000"); stale["aggSame"] != nil || stale["files"] == nil {
		t.Errorf("a stale digest must be answered with the aggregates: %v", stale)
	}
}

// Reads that hand the client turns it has never held must carry the answers, whatever digest
// the client claims: the cards are patched onto turns as they arrive, so a withheld answer
// renders a decided question as undecided.
func TestWindowedReadsAlwaysCarryAggregates(t *testing.T) {
	poll := aggFixture(t)
	sig, _ := poll("since=3")["aggSig"].(string)
	if sig == "" {
		t.Fatal("no aggSig")
	}
	for _, q := range []string{
		"since=0&tail=1&limit=400&agg=" + sig, // first open
		"since=0&agg=" + sig,                  // legacy full load
		"before=2&agg=" + sig,                 // backward page
	} {
		got := poll(q)
		if got["aggSame"] != nil {
			t.Errorf("?%s was answered with aggSame: %v", q, got)
		}
		if got["files"] == nil {
			t.Errorf("?%s did not carry the aggregates: %v", q, got)
		}
	}
}
