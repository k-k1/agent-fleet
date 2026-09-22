package chatx

// Live contract test for museChat (ADR 0095 P2-20). Run with:
//
//	MUSE_LIVE=1 go test -run TestMuseChatLive -v -timeout 5m .
//
// Gated behind MUSE_LIVE=1 so it never fires in CI (the binary is proprietary and not in the
// image). When the test passes, flip caps.headlessChat to true in registry.ts and the guide
// row to ✓.
//
// Security invariants (same as TestLiveContextUsage in internal/agents/muse/live_test.go):
//   - ~/.config/muse is accessed via symlink only — auth.json is NEVER copied.
//   - HOME is thrown away (a throwaway dir) so ~/.claude/CLAUDE.md is not sent to Meta.
//   - XDG_DATA_HOME is also thrown away so chat-exec sessions stay isolated.
//   - auth.json sha256 is compared before and after to confirm no write occurred.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
)

func museChatLiveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("MUSE_LIVE") != "1" {
		t.Skip("set MUSE_LIVE=1 to run the live muse chat contract test (spends subscription quota)")
	}
	if os.Getenv("AGENT_MUSE_BIN") == "" {
		if _, err := exec.LookPath("muse"); err != nil {
			t.Skip("no muse binary: set AGENT_MUSE_BIN or put muse in PATH")
		}
	}
	if !muse.HasCredential() {
		t.Skip("not signed in to muse: a real turn requires valid credentials")
	}
}

// museChatLiveHome isolates HOME so the member's ~/.claude/CLAUDE.md is not read by muse
// (foreign-personal-context clamp), while symlinking the real muse config directory so the
// CLI still uses the stored credential. The real auth.json is NEVER copied — only symlinked.
func museChatLiveHome(t *testing.T) (realAuthJSON string) {
	t.Helper()
	realHome, _ := os.UserHomeDir()
	home := t.TempDir()

	// Symlink the real muse config directory (contains auth.json and settings.json).
	if realHome != "" {
		src := filepath.Join(realHome, ".config", "muse")
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(home, ".config", "muse")
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(src, dst)
		}
		realAuthJSON = filepath.Join(realHome, ".config", "muse", "auth.json")
	}

	t.Setenv("HOME", home)
	// XDG_DATA_HOME thrown away: chat-exec sessions go to chat-muse-data (museChatDataHome),
	// but any residual muse session store also goes to the throwaway.
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return realAuthJSON
}

func museChatFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// TestMuseChatLive is the end-to-end live contract for museChat.Send. It spends at most two
// real subscription turns (three if the write-block turn runs; that one costs a turn only if
// the model actually tries to comply, which --disable-write prevents).
//
// What it checks:
//  1. Turn 1: a real reply arrives, MuseSessionID is captured, no error.
//  2. Turn 2: --session-id continuity works — a word planted in turn 1 echoes back from turn 2.
//  3. auth.json sha256 unchanged before and after (the credential was never written to).
func TestMuseChatLive(t *testing.T) {
	museChatLiveGate(t)

	realAuthJSON := museChatLiveHome(t)

	// Record auth.json sha256 BEFORE the turns, with the real path still accessible.
	var hashBefore string
	if realAuthJSON != "" {
		var err error
		hashBefore, err = museChatFileSHA256(realAuthJSON)
		if err != nil {
			t.Fatalf("sha256 before: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Turn 1: plain reply arrives, session id is captured.
	c := &ChatConversation{ID: "muse-chat-live", Agent: "muse"}
	reply1, err := museChat{}.Send(ctx, c, "Reply with exactly the single word: PONG")
	if err != nil {
		t.Fatalf("send #1: %v", err)
	}
	t.Logf("turn 1 reply: %q", reply1)
	if !strings.Contains(strings.ToUpper(reply1), "PONG") {
		t.Errorf("reply #1 missing PONG: %q", reply1)
	}
	if c.MuseSessionID == "" {
		t.Error("MuseSessionID not captured after send #1")
	}
	firstSID := c.MuseSessionID
	t.Logf("MuseSessionID after turn 1: %s", firstSID)

	// Turn 2: --session-id continuity — the word from turn 1 must survive in context.
	reply2, err := museChat{}.Send(ctx, c,
		"What exact single word did I ask you to reply with a moment ago? Answer with just that word.")
	if err != nil {
		t.Fatalf("send #2 (session continuity): %v", err)
	}
	t.Logf("turn 2 reply: %q", reply2)
	if !strings.Contains(strings.ToUpper(reply2), "PONG") {
		t.Errorf("session continuity lost: reply #2 %q does not contain PONG", reply2)
	}
	if c.MuseSessionID != firstSID {
		t.Errorf("MuseSessionID changed unexpectedly: %q → %q", firstSID, c.MuseSessionID)
	}

	// auth.json sha256 must be identical — the credential was never written to.
	if hashBefore != "" {
		hashAfter, err := museChatFileSHA256(realAuthJSON)
		if err != nil {
			t.Fatalf("sha256 after: %v", err)
		}
		if hashBefore != hashAfter {
			t.Errorf("auth.json was modified: sha256 %s → %s", hashBefore, hashAfter)
		} else {
			t.Logf("auth.json sha256 identical before and after (symlink, not copied)")
		}
	}
}
