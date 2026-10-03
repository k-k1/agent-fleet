package sessionx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// peekFixture isolates HOME, the claude config dir, the session metas and PATH (no tmux, so
// every session reads as stopped), and resets the peek limiter.
func peekFixture(t *testing.T, peerMessaging bool) (home string) {
	t.Helper()
	home = withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("PATH", t.TempDir())
	p := uiprefs.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"peerMessaging": peerMessaging})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	old := peekRate
	peekRate = &peekLimiter{reads: map[string][]time.Time{}}
	t.Cleanup(func() { peekRate = old })
	return home
}

// writeAssistantTranscript writes a claude jsonl whose assistant text is the given lines, one
// assistant event per line.
func writeAssistantTranscript(t *testing.T, m session.Meta, lines []string) {
	t.Helper()
	p := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "p", session.UUID(m.Dir, m.Name)+".jsonl")
	var sb strings.Builder
	sb.WriteString(`{"type":"user","message":{"content":"go"}}` + "\n")
	for _, l := range lines {
		ev, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": l}},
		}})
		sb.Write(ev)
		sb.WriteString("\n")
	}
	writeFile(t, p, sb.String())
}

func getOutput(t *testing.T, name, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/output?"+query, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionOutput(rec, req)
	return rec
}

func TestPeekPolicyRejections(t *testing.T) {
	peekFixture(t, true)
	session.WriteMeta(session.Meta{Name: "pk_src", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "pk_dst", Dir: t.TempDir(), Kind: session.KindCodex})
	session.WriteMeta(session.Meta{Name: "pk_shell", Dir: t.TempDir(), Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "pk_gone", Dir: t.TempDir(), Kind: session.KindClaude, Archived: true})

	if err := peekPolicy("pk_src", "pk_dst"); err != nil {
		t.Fatalf("claude peeking at codex should be allowed, got %v", err)
	}
	for _, tc := range []struct{ from, to, wantCode string }{
		{"pk_src", "pk_src", "peek_self"},
		{"pk_src", "pk_shell", "peek_target_forbidden"},
		{"pk_shell", "pk_dst", "peek_from_forbidden"},
		{"pk_src", "pk_gone", "peek_target_unknown"},
		{"pk_src", "nosuch", "peek_target_unknown"},
		{"nosuch", "pk_dst", "peek_from_unknown"},
		{"bad name!", "pk_dst", "bad_peek_from"},
	} {
		err := peekPolicy(tc.from, tc.to)
		rej, ok := err.(*peerRejection)
		if !ok || rej.Code != tc.wantCode {
			t.Errorf("peekPolicy(%q,%q) = %v, want %s", tc.from, tc.to, err, tc.wantCode)
		}
	}
}

// The workspace-wide peer-messaging switch closes peeking too, on the server: a caller that
// skips the MCP layer still gets nothing.
func TestPeekRefusedWhenPeerMessagingOff(t *testing.T) {
	peekFixture(t, false)
	src := session.Meta{Name: "pk_src", Dir: t.TempDir(), Kind: session.KindClaude}
	dst := session.Meta{Name: "pk_dst", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(src)
	session.WriteMeta(dst)
	writeAssistantTranscript(t, dst, []string{"secret plan"})

	rec := getOutput(t, "pk_dst", "peek_from=pk_src")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "peek_disabled") {
		t.Fatalf("status = %d body = %s, want 403 peek_disabled", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret plan") {
		t.Fatalf("refused peek leaked the output: %s", rec.Body.String())
	}
}

// A peek returns the tail within the caps whatever the caller asks for, and leaves an audit
// line in the fleet-graph ledger — and nothing that reaches the target.
func TestPeekClampsAndAudits(t *testing.T) {
	peekFixture(t, true)
	src := session.Meta{Name: "pk_src", Dir: t.TempDir(), Kind: session.KindClaude}
	dst := session.Meta{Name: "pk_dst", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(src)
	session.WriteMeta(dst)
	var lines []string
	for i := 1; i <= 300; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	writeAssistantTranscript(t, dst, lines)

	read := func(query string) (out string, clipped bool) {
		t.Helper()
		rec := getOutput(t, "pk_dst", query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body = %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Output  string `json:"output"`
			Clipped bool   `json:"clipped"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body.Output, body.Clipped
	}
	countLines := func(s string) int { return strings.Count(strings.TrimPrefix(s, sessionOutputClipNote), "\n") + 1 }

	out, clipped := read("peek_from=pk_src")
	if n := countLines(out); n != peekDefaultLines || !clipped || !strings.HasSuffix(out, "line 300") {
		t.Errorf("default peek = %d lines (clipped=%v), want the last %d", n, clipped, peekDefaultLines)
	}
	out, _ = read("peek_from=pk_src&lines=1000&tail=999999")
	if n := countLines(out); n != peekMaxLines || !strings.Contains(out, "line 101\n") {
		t.Errorf("lines=1000 peek = %d lines, want capped at %d", n, peekMaxLines)
	}
	out, _ = read("peek_from=pk_src&lines=3")
	if want := sessionOutputClipNote + "line 298\nline 299\nline 300"; out != want {
		t.Errorf("lines=3 peek = %q, want %q", out, want)
	}
	// The same read without peek_from (a parent reading its child) is not clamped.
	if rec := getOutput(t, "pk_dst", "tail=999999"); !strings.Contains(rec.Body.String(), "line 001") {
		t.Errorf("non-peek read was clamped: %.200s", rec.Body.String())
	}

	ledger, _ := os.ReadFile(filepath.Join(paths.AgentStateDir(), "fleet-graph", "activity-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if got := strings.Count(string(ledger), `"ev":"peek","ts":`); got != 3 {
		t.Errorf("ledger has %d peek lines, want 3 (one per peek, none for the plain read):\n%s", got, ledger)
	}
	if !strings.Contains(string(ledger), `"from":"pk_src","to":"pk_dst"`) {
		t.Errorf("peek line does not name reader and target:\n%s", ledger)
	}
	for _, ev := range []string{`"ev":"peer"`, `"ev":"instruct"`} {
		if strings.Contains(string(ledger), ev) {
			t.Errorf("a peek must not reach the target, but the ledger holds %s:\n%s", ev, ledger)
		}
	}
	if rows, _ := injectionStore.Read("pk_dst"); len(rows) != 0 {
		t.Errorf("a peek recorded an injection on the target: %v", rows)
	}
}

func TestPeekSelfAndShellAreRefusedOverHTTP(t *testing.T) {
	peekFixture(t, true)
	session.WriteMeta(session.Meta{Name: "pk_src", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "pk_shell", Dir: t.TempDir(), Kind: session.KindShell})
	for _, tc := range []struct {
		target string
		status int
		code   string
	}{
		{"pk_src", http.StatusBadRequest, "peek_self"},
		{"pk_shell", http.StatusForbidden, "peek_target_forbidden"},
	} {
		rec := getOutput(t, tc.target, "peek_from=pk_src")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("peek at %s: status = %d body = %s, want %d %s", tc.target, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestPeekRateLimitIsPerReaderAndSeparateFromSends(t *testing.T) {
	l := &peekLimiter{reads: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < peekRatePerWindow; i++ {
		if err := l.allow("a", now); err != nil {
			t.Fatalf("read %d refused: %v", i+1, err)
		}
	}
	if err, ok := l.allow("a", now).(*peerRejection); !ok || err.Code != "peek_rate_limited" {
		t.Fatalf("read over the cap = %v, want peek_rate_limited", err)
	}
	if err := l.allow("b", now); err != nil {
		t.Fatalf("another reader was throttled: %v", err)
	}
	if err := l.allow("a", now.Add(peekRateWindow)); err != nil {
		t.Fatalf("read after the window refused: %v", err)
	}
	// Peeks do not spend the peer-send budget (peerRate is a different limiter).
	if peekRatePerWindow <= peerRatePerWindow {
		t.Errorf("peek rate %d should be more generous than the send rate %d", peekRatePerWindow, peerRatePerWindow)
	}
}

// The expired-login refusal must not depend on the target being alive: DriveState answers
// "stopped" for a dead session before it looks at the login, so a check on the state alone let a
// stopped claude target through (review of #1549).
func TestPeekRefusesStoppedClaudeTargetWhileLoginExpired(t *testing.T) {
	peekFixture(t, true)
	writeClaudeCreds(t, -2*time.Hour, -time.Hour)
	if !claude.AuthExpired() {
		t.Fatal("fixture: credentials should read as expired")
	}
	src := session.Meta{Name: "pk_src", Dir: t.TempDir(), Kind: session.KindCodex}
	dst := session.Meta{Name: "pk_dst", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(src)
	session.WriteMeta(dst)
	writeAssistantTranscript(t, dst, []string{"secret plan"})

	rec := getOutput(t, "pk_dst", "peek_from=pk_src")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "peek_target_auth") {
		t.Fatalf("status = %d body = %s, want 409 peek_target_auth", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret plan") {
		t.Fatalf("refused peek leaked the output: %s", rec.Body.String())
	}

	// A live login lets the same read through.
	writeClaudeCreds(t, time.Hour, 24*time.Hour)
	writeAssistantTranscript(t, dst, []string{"secret plan"})
	if rec := getOutput(t, "pk_dst", "peek_from=pk_src"); rec.Code != http.StatusOK {
		t.Fatalf("live login: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// The byte cap holds for what is returned, clip notice included.
func TestPeekByteCapIncludesClipNotice(t *testing.T) {
	peekFixture(t, true)
	src := session.Meta{Name: "pk_src", Dir: t.TempDir(), Kind: session.KindClaude}
	dst := session.Meta{Name: "pk_dst", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(src)
	session.WriteMeta(dst)
	writeAssistantTranscript(t, dst, []string{strings.Repeat("x", peekMaxBytes+1)})

	rec := getOutput(t, "pk_dst", "peek_from=pk_src&tail=999999")
	var body struct {
		Output  string `json:"output"`
		Clipped bool   `json:"clipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %.200s", rec.Code, rec.Body.String())
	}
	if !body.Clipped || len(body.Output) > peekMaxBytes {
		t.Fatalf("output = %d bytes (clipped=%v), want clipped and at most %d", len(body.Output), body.Clipped, peekMaxBytes)
	}
}

func TestClipOutputLines(t *testing.T) {
	for _, tc := range []struct {
		in      string
		tail, n int
		want    string
		clipped bool
	}{
		{"a\nb\nc", 0, 2, sessionOutputClipNote + "b\nc", true},
		{"a\nb\nc\n", 0, 3, "a\nb\nc", false},
		{"a\nb\nc", 0, 5, "a\nb\nc", false},
		{"a\nb\nc", 0, 0, "a\nb\nc", false},
		{"aaaa\nbbbb", 3, 1, sessionOutputClipNote + "bbb", true},
	} {
		got, clipped := clipOutput(tc.in, tc.tail, tc.n)
		if got != tc.want || clipped != tc.clipped {
			t.Errorf("clipOutput(%q, %d, %d) = %q,%v want %q,%v", tc.in, tc.tail, tc.n, got, clipped, tc.want, tc.clipped)
		}
	}
}
