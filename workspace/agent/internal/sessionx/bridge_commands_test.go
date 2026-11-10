package sessionx

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func chatCmdFixture(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
}

// "!attach" applies the reply gate to its target: shell / ssm / unknown / archived / missing
// sessions are all refused, an ordinary agent session passes.
func TestCheckChatAttachGate(t *testing.T) {
	chatCmdFixture(t)
	session.WriteMeta(session.Meta{Name: "cc-ok", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "cc-shell", Dir: t.TempDir(), Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "cc-ssm", Dir: t.TempDir(), Kind: session.KindSSM})
	session.WriteMeta(session.Meta{Name: "cc-arch", Dir: t.TempDir(), Kind: session.KindClaude, Archived: true})

	if reason, err := checkChatAttach("cc-ok"); err != nil || reason != "" {
		t.Fatalf("ordinary session refused: %q, %v", reason, err)
	}
	for _, name := range []string{"cc-shell", "cc-ssm", "cc-arch", "cc-ghost", "../etc"} {
		reason, err := checkChatAttach(name)
		if err == nil || !strings.Contains(reason, "⚠️") {
			t.Errorf("%s: want a refusal with a reason, got %q, %v", name, reason, err)
		}
	}
}

// "!start" builds the create from the thread's session ONLY (kind, dir, model, driver) plus the
// typed prompt, tags it with the chat source and the message key, and refuses a shell source.
func TestStartFromChatInheritsFromThreadSession(t *testing.T) {
	chatCmdFixture(t)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "cc-src", Dir: dir, Kind: session.KindClaude, Model: "m1", Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: "cc-shell", Dir: dir, Kind: session.KindShell})
	var got CreateReq
	prev := createChatTarget
	createChatTarget = func(req CreateReq) (int, []byte) {
		got = req
		b, _ := json.Marshal(map[string]string{"name": "cc-new"})
		return http.StatusCreated, b
	}
	t.Cleanup(func() { createChatTarget = prev })

	reply, err := startFromChat("cc-src", "fix the bug", "discord", "discord:42")
	if err != nil || !strings.Contains(reply, "cc-new") {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if got.Kind != session.KindClaude || got.Dir != dir || got.Model != "m1" || got.Driver != session.DriverManaged ||
		got.InitialPrompt != "fix the bug" || got.IdempotencyKey != "discord:42" || got.chatSource != "discord" {
		t.Fatalf("create request = %+v", got)
	}
	if got.Worktree || got.RemoteURL != "" || got.ReportTo != "" || got.Origin != "" {
		t.Fatalf("a chat start must not reach beyond the source's working copy: %+v", got)
	}

	got = CreateReq{}
	if _, err := startFromChat("cc-shell", "ls", "discord", "k"); !errors.Is(err, errInjectTargetForbidden) || got.Kind != "" {
		t.Fatalf("a shell thread must not start sessions: err=%v req=%+v", err, got)
	}
	if _, err := startFromChat("cc-ghost", "x", "discord", "k"); err == nil || got.Kind != "" {
		t.Fatalf("an unknown thread session must not start sessions: %v", err)
	}
}

// The launch task is badged with the chat provider, like a reply typed in that thread, and a
// JSON body cannot claim that source (chatSource is unexported).
func TestChatStartLaunchTaskIsBadged(t *testing.T) {
	chatCmdFixture(t)
	req := CreateReq{InitialPrompt: "task from phone", chatSource: TurnSourceSlack}
	noteCreateOrigin("cc-new", &req, "", session.OriginUser)
	if got := injectionSourceOf("cc-new", "task from phone"); got != TurnSourceSlack {
		t.Fatalf("badge = %q, want %q", got, TurnSourceSlack)
	}
	if o := createTurnOrigin(&req, ""); o.Kind != "slack" {
		t.Fatalf("turn origin = %+v", o)
	}
	var viaJSON CreateReq
	_ = json.Unmarshal([]byte(`{"chatSource":"slack","chat_source":"slack","initial_prompt":"x"}`), &viaJSON)
	if viaJSON.chatSource != "" {
		t.Fatal("a request body must not be able to set chatSource")
	}
}

// A redelivered chat event whose first create is still launching is "starting", not a failure
// (a failure message invites a retype, which would start a second session), and a created
// session whose body carries no name still gets a sensible reply.
func TestStartFromChatReplies(t *testing.T) {
	chatCmdFixture(t)
	session.WriteMeta(session.Meta{Name: "cc-src", Dir: t.TempDir(), Kind: session.KindClaude})
	prev := createChatTarget
	t.Cleanup(func() { createChatTarget = prev })

	createChatTarget = func(CreateReq) (int, []byte) {
		return http.StatusConflict, []byte(`{"code":"create_in_progress"}`)
	}
	reply, err := startFromChat("cc-src", "x", "slack", "k")
	if err != nil || !strings.Contains(reply, "starting") && !strings.Contains(reply, "開始中") {
		t.Fatalf("in-progress reply = %q, %v", reply, err)
	}

	createChatTarget = func(CreateReq) (int, []byte) { return http.StatusCreated, []byte(`{}`) }
	reply, err = startFromChat("cc-src", "x", "slack", "k2")
	if err != nil || strings.Contains(reply, "session  ") || strings.Contains(reply, "セッション  ") {
		t.Fatalf("nameless reply = %q, %v", reply, err)
	}

	createChatTarget = func(CreateReq) (int, []byte) { return http.StatusBadRequest, []byte(`{"code":"bad_dir"}`) }
	if _, err = startFromChat("cc-src", "x", "slack", "k3"); err == nil {
		t.Fatal("a refused create must be an error")
	}
}
