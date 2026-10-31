package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The card may remember that a session has no transcript (display_miss.go); nothing that
// decides what to launch may. These pin both halves.

func writeBridgeLog(t *testing.T, sid string) {
	t.Helper()
	dir := filepath.Join(ConfigDir(), "projects", "-some-project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"bridge-session","bridgeSessionId":"cse_abc"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestWireLiveRemembersMissForCardOnly: once the card has seen no transcript, a log that appears
// right after is not looked for again until the TTL runs out — that is the sweep it saves — while
// SessionJSONLExists, the resume decision, sees the log at once.
func TestWireLiveRemembersMissForCardOnly(t *testing.T) {
	m, sid := ledgerSession(t)
	if got := (agentImpl{}).WireLive(m, true).RemoteURL; got != "" {
		t.Fatalf("RemoteURL = %q with no transcript", got)
	}
	writeBridgeLog(t, sid)

	if got := (agentImpl{}).WireLive(m, true).RemoteURL; got != "" {
		t.Errorf("RemoteURL = %q: the card looked for the transcript again inside the TTL", got)
	}
	if !SessionJSONLExists(sid) {
		t.Fatal("SessionJSONLExists answered from the card's remembered miss; a resume would start fresh")
	}

	displayMisses.note(ConfigDir()+"\x00"+sid, true, time.Now().Add(-displayMissTTLLive))
	if got := (agentImpl{}).WireLive(m, true).RemoteURL; got != "https://claude.ai/code/session_abc" {
		t.Errorf("RemoteURL = %q after the TTL, want the bridge link", got)
	}
}

func TestDisplayMissExpiresAndFollowsLiveness(t *testing.T) {
	m := missMemo{ttlLive: displayMissTTLLive, ttlStopped: displayMissTTLStopped}
	now := time.Now()
	m.note("k", true, now)
	if !m.holds("k", true, now.Add(displayMissTTLLive-time.Second)) {
		t.Error("a live miss is not remembered inside its TTL")
	}
	if m.holds("k", true, now.Add(displayMissTTLLive)) {
		t.Error("a live miss outlived its TTL: the card would lag the first turn")
	}
	if m.holds("k", false, now) {
		t.Error("a miss seen while live answered for the stopped session")
	}
	m.note("k", false, now)
	if !m.holds("k", false, now.Add(displayMissTTLStopped-time.Second)) {
		t.Error("a stopped miss is not remembered inside its TTL")
	}
	if m.holds("k", true, now) {
		t.Error("a miss seen while stopped answered for the restarted session")
	}
	m.forget("k")
	if m.holds("k", false, now) {
		t.Error("forget left the miss in place")
	}
}
