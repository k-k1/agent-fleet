package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// oldResetAt is sessionx's usageLimitResetAt claude branch as it was before agents.UsageLimiter.
func oldResetAt(sid string, menu bool, now time.Time) (time.Time, string, bool) {
	at, source, ok := ResetAt(sid, now)
	if ok && !menu && !strings.HasPrefix(source, "banner") {
		ok = false
	}
	return at, source, ok
}

// TestUsageLimitTraitsThroughRealTranscript drives the registered capability over a real
// transcript tail and statusline capture and compares it with the pre-capability functions: the
// sid it derives, the LimitKind it passes on, and the menu / banner trust rule.
func TestUsageLimitTraitsThroughRealTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	now := time.Now()
	capture, _ := json.Marshal(capturedUsage{
		FiveHour:   &capturedWindow{UsedPercent: 100, ResetsAt: now.Add(time.Hour).Unix()},
		SevenDay:   &capturedWindow{UsedPercent: 75, ResetsAt: now.Add(96 * time.Hour).Unix()},
		CapturedAt: now.Unix(),
	})
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(usageCapturePath(), capture, 0o600); err != nil {
		t.Fatal(err)
	}
	tr := agentImpl{}.UsageLimitTraits()

	for i, tc := range []struct {
		name       string
		tail       string
		kind       LimitKind
		wantSource string
		trustedOff bool // the answer is refused without a menu
	}{
		{"per-model limit, no time: capture only", apiErr("You've reached your Fable 5 limit. Run /usage-credits to continue", 429), LimitWindow, "capture", true},
		{"account window with a banner time", apiErr("You've hit your session limit · resets 7:50pm (Asia/Tokyo)", 0), LimitWindow, "banner", false},
		{"spend cap", apiErr("You've hit your org's monthly spend limit · run /usage-credits to raise it", 429), LimitSpend, "capture", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := session.Meta{Name: fmt.Sprintf("ultr%d", i), Dir: filepath.Join(home, fmt.Sprintf("w%d", i)), Kind: session.KindClaude}
			sid := session.UUID(m.Dir, m.Name)
			dir := filepath.Join(ConfigDir(), "projects", "-proj")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			body := userLine("go") + "\n" + tc.tail + "\n"
			if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			_, wantKind, wantAt := UsageLimitAbort(sid)
			kind, at := tr.AtLimit(m)
			if kind != wantKind || at != wantAt || kind != tc.kind || !at {
				t.Fatalf("AtLimit = %q/%v, old %q/%v, want %q/true", kind, at, wantKind, wantAt, tc.kind)
			}
			for _, menu := range []bool{false, true} {
				gAt, gSrc, gOK := tr.ResetAt(m, menu, now)
				wAt, wSrc, wOK := oldResetAt(sid, menu, now)
				if !gAt.Equal(wAt) || gSrc != wSrc || gOK != wOK {
					t.Errorf("menu=%v: ResetAt = %v/%q/%v, old %v/%q/%v", menu, gAt, gSrc, gOK, wAt, wSrc, wOK)
				}
				if !strings.HasPrefix(gSrc, tc.wantSource) {
					t.Errorf("menu=%v: source = %q, want %q (fixture no longer exercises the rule)", menu, gSrc, tc.wantSource)
				}
				if wantOK := menu || !tc.trustedOff; gOK != wantOK {
					t.Errorf("menu=%v: ok = %v, want %v", menu, gOK, wantOK)
				}
			}
		})
	}
}
