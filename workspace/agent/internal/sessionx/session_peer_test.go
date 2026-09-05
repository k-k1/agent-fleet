package sessionx

// docs/log/58 / ADR 0041 — pins the invariants session-to-session messaging has to hold. A
// failure here means a bypass has opened up.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestPeerTargetAllowedExcludesShellAndSSM(t *testing.T) {
	// Sending to shell / ssm is arbitrary command execution outright (ADR 0041 decision 5).
	// An empty Kind is refused too: NormalizeKind folds unknown/empty into claude, so letting
	// it through would be a bypass.
	for _, kind := range []string{session.KindShell, session.KindSSM, "", "nonsense"} {
		if peerTargetAllowed(kind) {
			t.Errorf("peerTargetAllowed(%q) = true, want false", kind)
		}
	}
	for _, kind := range []string{
		session.KindClaude, session.KindCodex, session.KindOpencode,
		session.KindCursor, session.KindKiro, session.KindAgy, session.KindCopilot,
	} {
		if !peerTargetAllowed(kind) {
			t.Errorf("peerTargetAllowed(%q) = false, want true", kind)
		}
	}
}

func TestPeerPolicyRejections(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindCodex})
	session.WriteMeta(session.Meta{Name: "peershell", Dir: t.TempDir(), Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "peergone", Dir: t.TempDir(), Kind: session.KindClaude, Archived: true})

	if _, err := peerPolicy("peersrc", "peerdst"); err != nil {
		t.Fatalf("claude → codex should be allowed, got %v", err)
	}
	for _, tc := range []struct{ from, to, wantCode string }{
		{"peersrc", "peersrc", "peer_self"},
		{"peersrc", "peershell", "peer_target_forbidden"},
		{"peershell", "peerdst", "peer_from_forbidden"},
		{"peersrc", "peergone", "peer_target_unknown"},
		{"peersrc", "nosuch", "peer_target_unknown"},
		{"nosuch", "peerdst", "peer_from_unknown"},
		{"peersrc", "bad name!", "bad_name"},
	} {
		_, err := peerPolicy(tc.from, tc.to)
		rej, ok := err.(*peerRejection)
		if !ok {
			t.Errorf("peerPolicy(%q,%q) err = %v, want *peerRejection", tc.from, tc.to, err)
			continue
		}
		if rej.Code != tc.wantCode {
			t.Errorf("peerPolicy(%q,%q) code = %q, want %q", tc.from, tc.to, rej.Code, tc.wantCode)
		}
	}
}

func TestPeerEnvelopeNamesTheSenderAndTheReplyPolicy(t *testing.T) {
	// The server always attaches the envelope. It is the receiver's only clue to who a message
	// came from when all it has is the body, and workspace-notes' standing rules hang off that
	// mark. intent / reply ride on the same line because reply discipline only bites at the
	// moment the message arrives (docs/log/58 §58.14).
	got := peerEnvelope("s7abc12", "notice", "none", "  develop を rebase した  ")
	if got != "[agent-fleet:peer from=s7abc12 intent=notice reply=none] develop を rebase した" {
		t.Fatalf("peerEnvelope = %q", got)
	}
	// The mirror parses the envelope back with a regexp (console/.../transcript/model.ts). Its
	// shape survives more words being added after the name, but from= coming first is a
	// contract.
	if !strings.HasPrefix(got, "[agent-fleet:peer from=s7abc12 ") {
		t.Fatalf("envelope does not start with from=: %q", got)
	}
}

func TestPeerResolveIntentDerivesReplyPolicy(t *testing.T) {
	// The sender does not get to choose the reply policy, or it could build a `notice` whose
	// envelope requires a reply.
	for intent, want := range map[string]string{
		"request": "only-if-blocked", "question": "required", "answer": "none", "notice": "none",
	} {
		got, err := peerResolveIntent(intent)
		if err != nil || got != want {
			t.Errorf("peerResolveIntent(%q) = %q, %v; want %q", intent, got, err, want)
		}
	}
	// Neither empty nor unknown is defaulted. Any default is wrong: a request gets silently
	// ignored, or a plain share draws a reply.
	for _, bad := range []string{"", "  ", "fyi", "REQUEST"} {
		if _, err := peerResolveIntent(bad); err == nil {
			t.Errorf("peerResolveIntent(%q) returned no error", bad)
		}
	}
}

func TestSessionInputRequiresPeerIntent(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input",
		strings.NewReader(`{"prompt":"hi","peer_from":"peersrc"}`))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	HandleSessionInput(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_peer_intent") {
		t.Fatalf("status = %d, body = %s, want 400 bad_peer_intent", rec.Code, rec.Body.String())
	}
}

func TestSessionInputRejectsPeerIntentWithoutPeerFrom(t *testing.T) {
	// A bare input carrying only the intent gets no envelope. Ignoring it silently would leave
	// the caller sending an ordinary interrupt while believing it conveyed reply discipline.
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input",
		strings.NewReader(`{"prompt":"hi","peer_intent":"notice"}`))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	HandleSessionInput(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "peer_intent_without_from") {
		t.Fatalf("status = %d, body = %s, want 400 peer_intent_without_from", rec.Code, rec.Body.String())
	}
}

func TestPeerLimiterDropsDuplicatesAndThrottles(t *testing.T) {
	l := &peerLimiter{sends: map[string][]time.Time{}, recent: map[string]time.Time{}}
	base := time.Unix(1_800_000_000, 0)

	if err := l.allow("a", "b", "same", base); err != nil {
		t.Fatalf("first send rejected: %v", err)
	}
	// The same (target, body) sent again is the shape of a ping-pong loop.
	err := l.allow("a", "b", "same", base.Add(time.Second))
	if rej, ok := err.(*peerRejection); !ok || rej.Code != "peer_duplicate" {
		t.Fatalf("duplicate err = %v, want peer_duplicate", err)
	}
	// Past the window the same text is allowed through again.
	if err := l.allow("a", "b", "same", base.Add(peerDuplicateWindow+time.Second)); err != nil {
		t.Fatalf("after duplicate window: %v", err)
	}

	l2 := &peerLimiter{sends: map[string][]time.Time{}, recent: map[string]time.Time{}}
	for i := 0; i < peerRatePerWindow; i++ {
		if err := l2.allow("a", "b", string(rune('A'+i)), base); err != nil {
			t.Fatalf("send %d rejected: %v", i, err)
		}
	}
	err = l2.allow("a", "b", "one too many", base)
	if rej, ok := err.(*peerRejection); !ok || rej.Code != "peer_rate_limited" {
		t.Fatalf("over-limit err = %v, want peer_rate_limited", err)
	}
	// Once the window rolls past it recovers; this is not a permanent ban.
	if err := l2.allow("a", "b", "later", base.Add(peerRateWindow+time.Second)); err != nil {
		t.Fatalf("after rate window: %v", err)
	}
}

// No route may put a peer message on the instruction ledger (arm): docs/log/51's reconciler
// then reads it as a new instruction from the user and settles early. AF delivers by typing
// into the TUI, and unlike the native path the receiving transcript cannot tell it apart from
// ordinary input (docs/log/58 §58.12), so refusing at the entrance is the only defence.
func TestSessionInputRefusesPeerFromWithReportTo(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input",
		strings.NewReader(`{"prompt":"hi","peer_from":"peersrc","report_to":"11111111-1111-1111-1111-111111111111"}`))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	HandleSessionInput(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s (want 400)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "peer_with_report_to") {
		t.Fatalf("body = %s, want peer_with_report_to", rec.Body.String())
	}
}

func TestSessionInputPeerPolicyIsEnforcedServerSide(t *testing.T) {
	// Swapping out the MCP layer must not bypass the policy: /input is what refuses.
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peershell", Dir: t.TempDir(), Kind: session.KindShell})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peershell/input",
		strings.NewReader(`{"prompt":"rm -rf /","peer_from":"peersrc"}`))
	req.SetPathValue("name", "peershell")
	rec := httptest.NewRecorder()
	HandleSessionInput(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s (want 403)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "peer_target_forbidden") {
		t.Fatalf("body = %s, want peer_target_forbidden", rec.Body.String())
	}
}

func TestSessionInputRejectsOversizePeerMessage(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	body, err := json.Marshal(map[string]string{
		"prompt":    strings.Repeat("x", peerMaxMessageBytes+1),
		"peer_from": "peersrc",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input", strings.NewReader(string(body)))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	HandleSessionInput(rec, req)

	// Never a silent truncation: "it was sent, but the second half is gone" is the worst
	// failure of all.
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "message_too_long") {
		t.Fatalf("status = %d, body = %s, want 400 message_too_long", rec.Code, rec.Body.String())
	}
}

func TestPeerValidateMessageAccepts16KiBBoundary(t *testing.T) {
	if err := peerValidateMessage(strings.Repeat("x", peerMaxMessageBytes)); err != nil {
		t.Fatalf("message at %d byte limit rejected: %v", peerMaxMessageBytes, err)
	}
}
