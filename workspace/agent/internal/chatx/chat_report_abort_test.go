package chatx

// An aborted turn (docs/log/47) must be reported without depending on the marker.
//
// Measured: when a turn dies on an API error, claude does not fire the Stop hook. The only
// path that used to see such an abort was the pace (pane) heal route, whose entry depended on
// the marker being non-idle; after a spurious heal cleared the marker it was never evaluated
// again and the instruction hung there pending. This test writes NO marker at all, plants only
// the transcript, and pins down that the reconciler picks it up by level and reports exactly
// once.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestSessionReportDetectsAbortWithoutMarker(t *testing.T) {
	home := withTempHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Auto-resume (docs/log/47 §4-6) is OFF. What this test pins down is the report route
	// itself; the ON behaviour ("the Agent resumes first and reports only after giving up")
	// is pinned separately by TestSessionReportHeldWhileAutoResuming.
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false,"claudeAbortAutoResume":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", HandleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot55", Dir: t.TempDir(), Kind: session.KindClaude, Title: "中断検知"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	// A transcript timestamped after the instruction and ending in an API error. No marker is
	// written (the real state after a spurious heal cleared it). The bookkeeping records
	// appended afterwards also pin down that the abort decision uses an allowlist and looks
	// only at real records.
	at := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	body := `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"` + at + `","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Server error mid-response. The response above may be incomplete."}]}}` + "\n" +
		`{"type":"system","subtype":"turn_duration","timestamp":"` + at + `"}` + "\n" +
		`{"type":"custom-title","customTitle":"[AF] 中断検知"}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	AddInstruction(m.Name, conv.ID, "operator")

	reports := func() []ChatMessage {
		unlock := LockConv(conv.ID)
		defer unlock()
		c, err := LoadConv(conv.ID)
		if err != nil {
			return nil
		}
		var out []ChatMessage
		for i := range c.Messages {
			if c.Messages[i].Role == "report" {
				out = append(out, c.Messages[i])
			}
		}
		return out
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(reports()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := reports()
	if len(got) != 1 {
		t.Fatalf("report count = %d, want 1 (an abort is reported even with no marker)", len(got))
	}
	// The classification is "an abort a resend fixes" — the report wording must be the one
	// that prompts an auto-resume.
	if !strings.Contains(got[0].Content, "中断") {
		t.Fatalf("report card = %q, want the aborted-turn wording", got[0].Content)
	}
	awaitReported(t, m.Name)

	// No re-report while the transcript is unchanged (every tick reads the same level, so the
	// closed instruction line is the only thing stopping a duplicate report).
	time.Sleep(150 * time.Millisecond)
	if n := len(reports()); n != 1 {
		t.Fatalf("report count = %d after further ticks, want 1", n)
	}
}

// TestSessionReportHeldWhileAutoResuming: while auto-resume (docs/log/47 §4-6) has taken it
// on, no abort report goes out. That is the token saving itself — emitting one runs an
// assistant turn whose only content is "resume it", and the Agent is already resuming.
//
// It also pins down that the suppression is not one-way: the moment giving up (GaveUp) is
// written, the report must be delivered with the same transcript and the same instruction
// line. If that breaks, an abort a resend could not fix reaches nobody (back to v1's "stops
// silently").
func TestSessionReportHeldWhileAutoResuming(t *testing.T) {
	home := withTempHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil { // auto-resume defaults to ON
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", HandleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot56", Dir: t.TempDir(), Kind: session.KindClaude, Title: "自動再開中"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	body := `{"type":"user","timestamp":"` + at + `","message":{"content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"` + at + `","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Stream idle timeout - no chunks received"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Mid-resume (one send done, waiting for the next) — nothing is reported during this.
	// The state used to be written straight into main's abortResumeStates so that the real
	// abortResumeHolds returned true. chatx cannot reach main's var, so the same input (this
	// session is mid auto-resume) is injected at the seam instead. The decision itself belongs
	// to main's abort_resume_test.go; that is where the responsibility splits.
	stubAbortResumeHolds(t, m.Name, true)

	AddInstruction(m.Name, conv.ID, "operator")

	reports := func() int {
		unlock := LockConv(conv.ID)
		defer unlock()
		c, err := LoadConv(conv.ID)
		if err != nil {
			return 0
		}
		n := 0
		for i := range c.Messages {
			if c.Messages[i].Role == "report" {
				n++
			}
		}
		return n
	}

	time.Sleep(300 * time.Millisecond) // comfortably spans the 2-tick debounce
	if n := reports(); n != 0 {
		t.Fatalf("report count = %d, want 0 (reporting while auto-resume is still running)", n)
	}

	// Give up, and the report goes out with the transcript unchanged.
	// Inject "already gave up (GaveUp=capped) = no longer holding" at the seam.
	stubAbortResumeHolds(t, m.Name, false)
	deadline := time.Now().Add(3 * time.Second)
	for reports() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := reports(); n != 1 {
		t.Fatalf("report count = %d after giving up, want 1 (giving up still reaches nobody)", n)
	}
}
