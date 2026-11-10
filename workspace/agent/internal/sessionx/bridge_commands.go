package sessionx

// The session half of the chat bridge's "!attach" and "!start" commands (ADR 0020, note of
// 2026-11-10). The bridge has already proved the sender is the bound member typing inside one of
// their own session threads; what is decided here is only whether the TARGET session may be
// bound or started from, and it is the same decision a reply makes (injectSessionPrompt).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// chatSessionGate is the shared refusal for a session a chat thread may not drive: unknown or
// archived, a kind that cannot take typed input (shell / ssm), or a session that moved.
func chatSessionGate(name string) (session.Meta, error) {
	if !session.ValidName(name) {
		return session.Meta{}, errChatNoSuchSession
	}
	lease, err := lifegate.Enter(context.Background(), name)
	if err != nil {
		return session.Meta{}, err
	}
	lease.Release()
	meta, ok := session.ReadMeta(name)
	if !ok || meta.Archived {
		return session.Meta{}, errChatNoSuchSession
	}
	if !peerTargetAllowed(meta.Kind) {
		return session.Meta{}, errInjectTargetForbidden
	}
	return meta, nil
}

var errChatNoSuchSession = fmt.Errorf("no such active session")

func chatGateReason(err error) string {
	en := BridgeAnswerEN()
	if err == errChatNoSuchSession {
		return Fb(en, "⚠️ その名前のセッションが見つかりません", "⚠️ No such session")
	}
	return injectFailureReason(err)
}

// checkChatAttach is ReceiverDeps.CheckAttach.
func checkChatAttach(target string) (string, error) {
	if _, err := chatSessionGate(target); err != nil {
		return chatGateReason(err), err
	}
	return "", nil
}

// startFromChat is ReceiverDeps.Start: a new session of the same kind, model, driver and
// working copy as `from`, whose launch task is the typed prompt. It never leaves the member's
// own workspace and takes nothing from the message but the prompt, so a chat user cannot pick
// a kind, directory or connection the thread's session did not already have.
func startFromChat(from, prompt, source, key string) (string, error) {
	en := BridgeAnswerEN()
	src, err := chatSessionGate(from)
	if err != nil {
		return chatGateReason(err), err
	}
	req := CreateReq{
		Kind: src.Kind, Dir: src.Dir, Subdir: src.Subdir, Model: src.Model, Driver: src.Driver,
		InitialPrompt: prompt, IdempotencyKey: key, chatSource: source,
	}
	status, body := createChatTarget(req)
	if status == http.StatusConflict && strings.Contains(string(body), "create_in_progress") {
		// The same chat message is already being launched (a redelivered event): that is not
		// a failure, and telling the member so would invite a retype that starts a second one.
		return Fb(en, "⏳ 同じ指示のセッションを開始中です。通知を待ってください",
			"⏳ That session is already starting — wait for its notification"), nil
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return Fb(en, "⚠️ セッションを開始できませんでした", "⚠️ Couldn't start the session"),
			fmt.Errorf("create from chat: status %d: %s", status, body)
	}
	var made struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &made)
	if made.Name == "" {
		return Fb(en, "✓ 新しいセッションを開始しました。通知は専用のスレッドに出ます",
			"✓ Started a new session — its notifications will open their own thread"), nil
	}
	return Fb(en, "✓ 新しいセッション "+made.Name+" を開始しました。通知は専用のスレッドに出ます",
		"✓ Started session "+made.Name+" — its notifications will open their own thread"), nil
}

// createChatTarget is the in-process create (the same shape as a move's target) behind a seam.
var createChatTarget = func(req CreateReq) (int, []byte) {
	r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/sessions", nil)
	c := &captureWriter{header: http.Header{}}
	createSession(c, r, req)
	return c.status, c.body.Bytes()
}
