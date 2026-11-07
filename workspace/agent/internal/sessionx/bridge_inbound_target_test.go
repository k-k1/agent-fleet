package sessionx

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A chat reply is refused for the kinds peer messages refuse, before any pane is touched, and
// the refusal reaches the thread as a reply.
func TestInjectSessionPromptRefusesNonPeerTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	for name, kind := range map[string]string{"brshell": session.KindShell, "brssm": session.KindSSM, "brunknown": "nonsense", "brblank": ""} {
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: kind})
		err := injectSessionPrompt(name, "ls", agents.Origin{Kind: agents.OriginDiscord})
		if !errors.Is(err, errInjectTargetForbidden) {
			t.Errorf("kind %q: err = %v, want errInjectTargetForbidden", kind, err)
		}
		if reason := injectFailureReason(err); !strings.Contains(reason, "⚠️") || strings.Contains(reason, "Couldn't deliver") || strings.Contains(reason, "届けられませんでした") {
			t.Errorf("kind %q: reply = %q, want the specific refusal", kind, reason)
		}
	}
	// A session with no meta at all is unknown too.
	if err := injectSessionPrompt("brghost", "ls", agents.Origin{Kind: agents.OriginDiscord}); !errors.Is(err, errInjectTargetForbidden) {
		t.Errorf("no meta: err = %v, want errInjectTargetForbidden", err)
	}
}
