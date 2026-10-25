package sessionx

// Wiring of the expired-auth state (docs/log/47 §4-8): in a workspace whose credentials have
// expired, a claude session reads as expired auth rather than idle, and free-text sends are
// refused.
//
// The classification itself (two epochs → alive or not) is covered by unit tests in
// internal/agents/claude. What is checked here is the wiring: a pane that looks idle still
// resolves to auth, the send guard refuses that state as auth_expired, and nothing changes at
// all while the credentials are alive — a false positive stops every running session, so that
// side is pinned just as firmly.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// writeClaudeCreds puts credentials in an isolated CLAUDE_CONFIG_DIR, so the real fleet's are
// never read. access / refresh are relative to now.
func writeClaudeCreds(t *testing.T, access, refresh time.Duration) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	now := time.Now()
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken":           "tok",
		"expiresAt":             now.Add(access).UnixMilli(),
		"refreshTokenExpiresAt": now.Add(refresh).UnixMilli(),
	}})
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if want := refresh <= 0 && access <= 0; claude.AuthExpired() != want {
		t.Fatalf("could not set up the premise: AuthExpired = %v, want %v", claude.AuthExpired(), want)
	}
}

// TestDriveStateAuthExpired: a pane sitting at the waiting prompt (so it looks idle) must
// still resolve to auth when the credentials have expired, and the marker that made it look
// still-running must be folded away.
func TestDriveStateAuthExpired(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, -8*24*time.Hour+8*time.Hour, -8*24*time.Hour)
	m := paneShowing(t, "authexp1", "../tmuxx/testdata/footers/idle_bypass_hint.txt")
	sid := session.UUID(m.Dir, m.Name)
	// The shape right after a turn died on a 401: still working, with no Stop hook fired.
	status.Persist(sid, "working")

	if got := DriveState(m, true, true); got != agents.StateAuth {
		t.Fatalf("DriveState = %q, want %q (expired auth is not idle)", got, agents.StateAuth)
	}
	if st, ok := status.Read(sid); ok && st.State == "working" {
		t.Error("status marker still working - the self-repair did not run")
	}
	if got := DriveState(m, true, true); got != agents.StateAuth {
		t.Errorf("second DriveState = %q, want %q (the state is oscillating)", got, agents.StateAuth)
	}
}

// TestDriveStateAuthValid: with live credentials nothing changes and a waiting pane is idle.
func TestDriveStateAuthValid(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, 8*time.Hour, 25*24*time.Hour)
	m := paneShowing(t, "authexp2", "../tmuxx/testdata/footers/idle_bypass_hint.txt")
	if got := DriveState(m, true, true); got == agents.StateAuth {
		t.Fatalf("DriveState = %q - live credentials misread as expired auth", got)
	}
}

// TestPromptBlockerAuthExpired: the send guard. Free text sent to a session with expired auth
// must be refused as auth_expired. The TUI accepts the characters but no turn starts, so
// without the refusal the sender sees success and the mirror is left with nothing but a
// prompt waiting to be picked up.
func TestPromptBlockerAuthExpired(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, -8*24*time.Hour+8*time.Hour, -8*24*time.Hour)
	m := session.Meta{Name: "authexp3", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)

	st := promptBlocker(m.Name)
	if st != agents.StateAuth {
		t.Fatalf("promptBlocker = %q, want %q", st, agents.StateAuth)
	}
	if code := blockedErrCode(st); code != "auth_expired" {
		t.Errorf("blockedErrCode = %q, want auth_expired (the value the Console's err.<code> and CP's classification read)", code)
	}
	if blockedErrMessage(st) == "" {
		t.Error("blockedErrMessage is empty - nobody learns why the send was refused")
	}
}

// TestPromptBlockerAuthValid: live credentials must not block a send.
func TestPromptBlockerAuthValid(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, 8*time.Hour, 25*24*time.Hour)
	m := session.Meta{Name: "authexp4", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	if st := promptBlocker(m.Name); st == agents.StateAuth {
		t.Fatalf("promptBlocker = %q - refusing a send with live credentials", st)
	}
}

// TestTypeInitialPromptRefusedWhenAuthExpired: a launch's task must not be typed into a claude
// TUI that will accept it and start no turn. The tmux stand-in logs every call, so "nothing
// typed" is read off the log rather than assumed.
func TestTypeInitialPromptRefusedWhenAuthExpired(t *testing.T) {
	// No isolateAgentState: it routes tmux through a private socket (-L), which the stand-in
	// below does not parse. The stand-in is what keeps the real tmux server out of this test.
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_TEST_LOG\"\ncase \"$1\" in\n  has-session) exit 0 ;;\n  list-panes) printf '1 %%7\\n' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)

	const name = "authexp_initial"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, InitialPromptState: session.InitialPromptPending})

	writeClaudeCreds(t, -8*24*time.Hour+8*time.Hour, -8*24*time.Hour)
	deliverInitialPrompt(name, "do the task")

	if m, _ := session.ReadMeta(name); m.InitialPromptState != session.InitialPromptFailed {
		t.Errorf("InitialPromptState = %q, want %q", m.InitialPromptState, session.InitialPromptFailed)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "paste-buffer") || strings.Contains(string(b), "send-keys") {
		t.Errorf("the prompt was typed into an expired claude; tmux calls:\n%s", b)
	}
}

// TestInitialPromptFailsWithAuthExpiredReason: a create's initial prompt that is refused
// because the login has expired settles failed AND says why, so the Console can toast the
// reason instead of showing a bare failure. A prompt that is not a create's (state not
// pending) must not grow a reason.
func TestInitialPromptFailsWithAuthExpiredReason(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, -8*24*time.Hour+8*time.Hour, -8*24*time.Hour)
	session.WriteMeta(session.Meta{Name: "ipreason1", Dir: t.TempDir(), Kind: session.KindClaude,
		InitialPromptState: session.InitialPromptPending})
	session.WriteMeta(session.Meta{Name: "ipreason2", Dir: t.TempDir(), Kind: session.KindClaude})

	deliverInitialPrompt("ipreason1", "first task")
	deliverInitialPrompt("ipreason2", "not a create's prompt")

	m, _ := session.ReadMeta("ipreason1")
	if m.InitialPromptState != session.InitialPromptFailed || m.InitialPromptReason != session.InitialPromptReasonAuthExpired {
		t.Errorf("state/reason = %q/%q, want failed/%q", m.InitialPromptState, m.InitialPromptReason, session.InitialPromptReasonAuthExpired)
	}
	if got := wireSession(m, false).InitialPromptReason; got != session.InitialPromptReasonAuthExpired {
		t.Errorf("list wire reason = %q, want it carried", got)
	}
	if m2, _ := session.ReadMeta("ipreason2"); m2.InitialPromptState != "" || m2.InitialPromptReason != "" {
		t.Errorf("a non-create prompt got state/reason %q/%q, want none", m2.InitialPromptState, m2.InitialPromptReason)
	}
}
