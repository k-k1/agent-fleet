package browserx

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// Checks that SetHandoffResult resolves the ledger row and hands the right prompt to the
// delivery stage.
//
// Recording, resolving and delivering the ledger itself is covered by the file of the same
// name in package main - only that side can drive the real agentSendToSession. What stays
// here are the two cases that cannot move, because they depend on newFakeBrowserCDP and
// fakeAttachmentManager, fakes that live in this package; the delivery stage is taken by the
// double below. So what this file proves stops at "resolved and passed this body to
// delivery"; that it lands in the session's input is package main's to prove.
//
// Because delivery is a double here, a regression in agentSendToSession itself does not turn
// this file red. Do not remove the real-wiring coverage from package main.

// stubAgentInputServer replaces the delivery stage (agentSendToSession) and records the
// prompts handed to it. DeliverBrowserHandoff runs on its own goroutine, so it is called
// concurrently.
func stubAgentInputServer(t *testing.T) (prompts func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	previous := agentSendToSession
	agentSendToSession = func(_ string, body []byte) (string, bool, error) {
		var payload struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		got = append(got, payload.Prompt)
		mu.Unlock()
		return `{"sent":true}`, false, nil
	}
	t.Cleanup(func() { agentSendToSession = previous })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func waitForBrowserHandoff(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestBrowserAttachmentSetHandoffResultDeliversToRequestingSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prompts := stubAgentInputServer(t)

	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)

	if _, err := m.UpdateHandoff(created.ID, browserAttachmentHandoffRequest{
		Message: "予約投稿を確認して", ControlMode: attachmentControlUser, SessionName: "test-sess-5",
	}); err != nil {
		t.Fatalf("UpdateHandoff: %v", err)
	}
	// The request round is durable as soon as UpdateHandoff returns, before any
	// human has responded.
	l, ok := BrowserHandoffLedgers.Read("test-sess-5")
	if !ok || len(l.Rows) != 1 || l.Rows[0].Result != "" {
		t.Fatalf("ledger after UpdateHandoff = %+v ok=%v, want one pending row", l, ok)
	}

	if _, err := m.SetHandoffResult(created.ID, "completed"); err != nil {
		t.Fatalf("SetHandoffResult: %v", err)
	}

	// Delivery happens off the request goroutine (a human's click must not
	// block on it), so poll for it.
	waitForBrowserHandoff(t, func() bool { return len(prompts()) == 1 })
	got := prompts()[0]
	if !strings.Contains(got, "予約投稿を確認して") || !strings.Contains(got, "完了") || !strings.Contains(got, created.ID) {
		t.Fatalf("delivered prompt = %q", got)
	}
	waitForBrowserHandoff(t, func() bool {
		_, ok := BrowserHandoffLedgers.Read("test-sess-5")
		return !ok
	})
}

func TestBrowserAttachmentSetHandoffResultWithoutSessionDoesNotDeliver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prompts := stubAgentInputServer(t)

	cdp := newFakeBrowserCDP()
	m := fakeAttachmentManager(cdp, 0)
	created := createFakeAttachment(t, m)

	// A handoff started without request_browser_action's session context (e.g.
	// a bare API call) — SetHandoffResult must still succeed, it just has
	// nobody to notify.
	if _, err := m.UpdateHandoff(created.ID, browserAttachmentHandoffRequest{
		Message: "confirm", ControlMode: attachmentControlUser,
	}); err != nil {
		t.Fatalf("UpdateHandoff: %v", err)
	}
	if _, err := m.SetHandoffResult(created.ID, "completed"); err != nil {
		t.Fatalf("SetHandoffResult: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if got := prompts(); len(got) != 0 {
		t.Fatalf("a handoff with no session must never deliver, got %v", got)
	}
}
