package opencode

// Surfacing a failed turn (errors.go). Feeds in a measured body (opencode 1.18.5, opencode
// Zen out of balance) verbatim and pins that the driver does not drop the failure while the
// status stays 200, and that the read layer does not throw the whole turn away because
// "parts is empty".

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// A measured body, in the shape the driver receives. HTTP is 200, the failure rides on
// info.error alone and parts is empty — read by status alone this looks like a clean
// completion.
const zenCreditsBody = `{"info":{"role":"assistant","modelID":"deepseek-v4-pro","providerID":"opencode",` +
	`"tokens":{"input":0,"output":0},` +
	`"error":{"name":"APIError","data":{"statusCode":401,"isRetryable":false,` +
	`"message":"Insufficient balance. Manage your billing here: https://opencode.ai/workspace/wrk_x/billing",` +
	`"metadata":{"url":"https://opencode.ai/zen/v1/chat/completions"}}}},"parts":[]}`

func TestDecodeTurnErrorReadsProviderFailureFrom200Body(t *testing.T) {
	e, ok := decodeTurnError(strings.NewReader(zenCreditsBody))
	if !ok {
		t.Fatal("provider failure in a 200 body must be detected")
	}
	if e.label() != "APIError (HTTP 401)" {
		t.Errorf("label = %q", e.label())
	}
	if !strings.HasPrefix(e.detail(), "Insufficient balance.") {
		t.Errorf("detail = %q", e.detail())
	}
	if !strings.HasPrefix(e.summary(), "[error] APIError (HTTP 401): Insufficient balance.") {
		t.Errorf("summary = %q", e.summary())
	}
	if p := e.part(); p.Kind != "error" || p.Info != e.label() || p.Text != e.detail() {
		t.Errorf("part = %+v", p)
	}
}

func TestDecodeTurnErrorKeepsRetryableFlag(t *testing.T) {
	body := `{"info":{"error":{"name":"APIError","data":{"statusCode":500,"isRetryable":true,"message":"Internal server error"}}},"parts":[]}`
	e, ok := decodeTurnError(strings.NewReader(body))
	if !ok || !e.retryable() {
		t.Fatalf("retryable provider error = %+v ok=%v", e, ok)
	}
}

// The transcript store keeps the info object itself as the row (no wrapper) — one decoder
// has to accept both shapes.
func TestDecodeMessageErrorAcceptsStoreRowShape(t *testing.T) {
	row := `{"role":"assistant","modelID":"glm-5.2",` +
		`"error":{"name":"ProviderAuthError","data":{"providerID":"opencode"}}}`
	e, ok := decodeMessageError([]byte(row))
	if !ok {
		t.Fatal("store-row error must be detected")
	}
	// Some names carry no message (measured) — it must fall back to providerID.
	if e.label() != "ProviderAuthError" || e.detail() != "provider: opencode" {
		t.Errorf("label=%q detail=%q", e.label(), e.detail())
	}
}

// An abort is not a failure: Interrupt has already recorded cancelled, and the partial
// answer is still in parts. Showing an error here makes the user's own interrupt look like
// an error every single time.
func TestDecodeMessageErrorIgnoresDeliberateAbort(t *testing.T) {
	if _, ok := decodeMessageError([]byte(`{"error":{"name":"MessageAbortedError","data":{}}}`)); ok {
		t.Error("a deliberate abort must not be reported as a failure")
	}
	if _, ok := decodeMessageError([]byte(`{"role":"assistant"}`)); ok {
		t.Error("a clean turn must not be reported as a failure")
	}
	if _, ok := decodeMessageError([]byte(`not json`)); ok {
		t.Error("an undecodable row must not be reported as a failure")
	}
}

// The core regression: a turn with 200 plus info.error must end failed, and the reason must
// reach MarkTurnEndErr (that is, the operator report and the chat bridge).
func TestTurnWith200ErrorBodyLandsFailedAndReportsReason(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnBody = zenCreditsBody
	h := newTestHandle(t, srv)

	got := make(chan string, 4)
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
		if state == agents.StateFailed {
			got <- excerpt
		}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_fail"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnFailed)
	select {
	case excerpt := <-got:
		if !strings.Contains(excerpt, "Insufficient balance") {
			t.Errorf("excerpt = %q, want the provider's message", excerpt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a failed turn must notify the failure, not a plain completion")
	}
}

func TestRetryableTurnLandsAbortedWithoutRepostingPrompt(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnBody = `{"info":{"error":{"name":"APIError","data":{"statusCode":500,"isRetryable":true,"message":"Internal server error"}}},"parts":[]}`
	h := newTestHandle(t, srv)

	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_abort"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnAborted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 {
		t.Fatalf("provider calls = %d, want 1; the original prompt must not be reposted", len(m.turns))
	}
}

// Read-layer regression: a failed turn has no parts, so a "nothing displayable" verdict threw
// the whole turn away and neither the mirror nor get_session_output showed anything. The
// error must be lifted into one part and must also land in Text (= /output, copy, the chat
// bridge).
func TestReadSessionKeepsFailedTurnWithErrorPart(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_e"
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"やって"}`)
	// A failed assistant turn: not a single part, and the reason lives only on the message row.
	insMsg(t, db, "m2", ses, 1100, `{"role":"assistant","modelID":"deepseek-v4-pro","time":{"created":1100},`+
		`"error":{"name":"APIError","data":{"statusCode":401,"message":"Insufficient balance."}}}`)

	turns := readSession(db, ses)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2 (the failed assistant turn must not be dropped)", len(turns))
	}
	got := turns[1]
	if got.Role != "assistant" || got.Model != "deepseek-v4-pro" {
		t.Fatalf("turn = %+v", got)
	}
	if len(got.Parts) != 1 || got.Parts[0].Kind != "error" {
		t.Fatalf("parts = %+v, want a single error part", got.Parts)
	}
	if got.Parts[0].Info != "APIError (HTTP 401)" || got.Parts[0].Text != "Insufficient balance." {
		t.Errorf("error part = %+v", got.Parts[0])
	}
	if !strings.Contains(got.Text, "Insufficient balance.") {
		t.Errorf("text = %q, want the failure to reach the flattened form", got.Text)
	}
}

// Both an error and partial output (it ran as far as a tool, then died): keep the body and
// append the error at the end.
func TestReadSessionAppendsErrorAfterPartialOutput(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_p"
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","time":{"created":1000},`+
		`"error":{"name":"APIError","data":{"statusCode":429,"message":"Rate limited."}}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"調べます"}`)

	turns := readSession(db, ses)
	if len(turns) != 1 || len(turns[0].Parts) != 2 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Parts[0].Kind != "text" || turns[0].Parts[1].Kind != "error" {
		t.Fatalf("parts = %+v, want text then error", turns[0].Parts)
	}
	if turns[0].Text != "調べます\n\n[error] APIError (HTTP 429): Rate limited." {
		t.Errorf("text = %q", turns[0].Text)
	}
}
