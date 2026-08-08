//go:build clicontract

// Tier B: the contract checks that need a REAL turn — the message/part JSON payloads only
// exist once a model has replied. opencode's free OpenCode Zen models make this
// credential-free, but they are an external service, so this tier is flaky by nature:
// gated behind OPENCODE_CONTRACT_LIVE=1 and reported non-blocking in CI.
//
//	OPENCODE_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLive ./internal/agents/opencode/
package opencode

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENCODE_CONTRACT_LIVE") != "1" {
		t.Skip("OPENCODE_CONTRACT_LIVE!=1 — live (free-model) contract tier skipped")
	}
}

// TestContractLiveTurnPayloads drives one real free-model turn through the managed driver
// and pins the payload shapes LiveState reads: message.data {role, time.completed} and the
// part rows. It also watches for the quiet degradation — LiveState must never answer ""
// across a turn whose state we know; "" means the store contract moved and the runtime
// would fall back to the plugin status without anyone noticing.
func TestContractLiveTurnPayloads(t *testing.T) {
	requireLive(t)
	addr, home := startServe(t)
	t.Setenv("HOME", home)
	dir := t.TempDir()
	ver := opencodeVersion(t)

	ses, err := serveCreateSession(addr, dir, "contract-live")
	if err != nil {
		t.Fatalf("serveCreateSession: %v", err)
	}
	h := &threadHandle{
		name: "contract", dir: dir, ocSid: "contract-sid",
		addr: addr, ses: ses, alive: true, gen: 1,
		events: make(chan agents.Event, 64),
	}
	if err := h.Send(agents.TurnInput{Prompt: "Reply with exactly: PONG", ClientMessageID: "contract_1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	m := session.Meta{Dir: dir, Name: "contract"}
	sawWorking, sawUnknown := false, false
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		switch LiveState(m) {
		case "":
			sawUnknown = true // the alarm: store contract moved mid-turn
		case "working", "question":
			sawWorking = true
		}
		h.mu.Lock()
		st := h.state
		h.mu.Unlock()
		if st == agents.TurnCompleted || st == agents.TurnFailed {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	h.mu.Lock()
	final := h.state
	h.mu.Unlock()
	if final != agents.TurnCompleted {
		t.Fatalf("turn state = %v, want completed — the blocking POST /session/{id}/message drive moved in opencode %s", final, ver)
	}
	if sawUnknown {
		t.Errorf("LiveState returned \"\" during a live turn on opencode %s — the store contract moved; "+
			"at runtime this degrades silently to the plugin status", ver)
	}
	if !sawWorking {
		t.Errorf("LiveState never reported working/question during a real turn on opencode %s — "+
			"the in-flight signal (assistant message without time.completed) moved", ver)
	}
	if st := LiveState(m); st != "idle" {
		t.Errorf("LiveState = %q after the turn completed on opencode %s, want idle", st, ver)
	}

	// The payload shape LiveState parses, straight off the real store.
	db, ok := openRO()
	if !ok {
		t.Fatalf("openRO: store unreadable")
	}
	defer db.Close()
	var data []byte
	if err := db.QueryRow(`SELECT data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`, ses).Scan(&data); err != nil {
		t.Fatalf("no message row after a completed turn on opencode %s: %v", ver, err)
	}
	var md struct {
		Role string `json:"role"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	}
	if err := json.Unmarshal(data, &md); err != nil {
		t.Fatalf("message.data is not JSON in opencode %s: %v", ver, err)
	}
	if md.Role != "assistant" {
		t.Errorf("newest message role = %q, want assistant — message.data.role moved in opencode %s", md.Role, ver)
	}
	if md.Time.Completed == 0 {
		t.Errorf("message.data.time.completed absent on a finished turn in opencode %s — "+
			"LiveState's idle test depends on it (its absence means in-flight)", ver)
	}
}

// liveTurn drives one real free-model turn and waits for it to LAND IN THE STORE.
//
// Waiting on h.state alone is not enough for a second turn: the handle is still sitting on
// the previous turn's TurnCompleted when Send returns, so the wait falls through instantly
// and the assertions run against a conversation that is one turn short. Poll the store —
// the thing the assertions actually read — for the assistant reply.
func liveTurn(t *testing.T, h *threadHandle, ses, prompt, id string, wantMsgs int) {
	t.Helper()
	if err := h.Send(agents.TurnInput{Prompt: prompt, ClientMessageID: id}); err != nil {
		t.Fatalf("Send(%s): %v", id, err)
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		db, ok := openRO()
		if !ok {
			t.Fatal("openRO: store unreadable")
		}
		n := len(readSession(db, ses))
		db.Close()
		if n >= wantMsgs {
			return
		}
		h.mu.Lock()
		st := h.state
		h.mu.Unlock()
		if st == agents.TurnFailed {
			t.Fatalf("turn %s failed (store has %d/%d messages)", id, n, wantMsgs)
		}
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatalf("turn %s never landed in the store", id)
}

// TestContractLiveForkAtMessage is the end-to-end proof for docs/55 §55.3: a fork pinned to
// a past user message carries the history up to — but NOT including — that message.
//
// Everything below this line in the stack is already covered by mocks (fork_at_test.go
// pins the request body against an httptest serve). What only a real run can tell us is
// whether opencode's fork endpoint actually MEANS what we think `messageID` means. The
// inclusivity is the whole feature: off by one turn and the branch silently carries the
// prompt the user wanted to retake, which reads as "it worked" in the mirror.
//
//	OPENCODE_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLiveForkAtMessage ./internal/agents/opencode/
func TestContractLiveForkAtMessage(t *testing.T) {
	requireLive(t)
	addr, home := startServe(t)
	t.Setenv("HOME", home)
	dir := t.TempDir()
	ver := opencodeVersion(t)

	ses, err := serveCreateSession(addr, dir, "fork-at-live")
	if err != nil {
		t.Fatalf("serveCreateSession: %v", err)
	}
	h := &threadHandle{
		name: "forkat", dir: dir, ocSid: "forkat-sid",
		addr: addr, ses: ses, alive: true, gen: 1,
		events: make(chan agents.Event, 64),
	}
	// 2 messages per turn (the prompt and the reply), so the store target doubles.
	liveTurn(t, h, ses, "Reply with exactly: ALPHA", "forkat_1", 2)
	liveTurn(t, h, ses, "Reply with exactly: BETA", "forkat_2", 4)

	db, ok := openRO()
	if !ok {
		t.Fatal("openRO: store unreadable")
	}
	defer db.Close()
	turns := readSession(db, ses)
	users := []transcript.Turn{}
	for _, tn := range turns {
		if tn.Role == "user" {
			users = append(users, tn)
		}
	}
	if len(users) != 2 {
		t.Fatalf("source has %d user turns, want 2 (opencode %s)", len(users), ver)
	}
	// The anchor the mirror hands out is this field — if the transcript stopped carrying
	// it, the Console would quietly hide the affordance instead of failing.
	anchor := users[1].AnchorID
	if anchor == "" {
		t.Fatalf("user turn carries no AnchorID on opencode %s — message ids moved", ver)
	}

	// The production resolver runs over the real store (it refuses unknown / sidechain
	// ids), so map the slot to this conversation the way the sid store does.
	m := session.Meta{Dir: dir, Name: "forkat", Kind: session.KindOpencode}
	sids.Write(session.UUID(dir, "forkat"), ses)
	resolved, err := (agentImpl{}).ResolveForkAt(m, anchor)
	if err != nil {
		t.Fatalf("ResolveForkAt(%s): %v", anchor, err)
	}

	forked, err := serveForkSession(addr, ses, dir, resolved)
	if err != nil {
		t.Fatalf("serveForkSession(at=%s): %v", resolved, err)
	}
	fdb, ok := openRO()
	if !ok {
		t.Fatal("openRO (fork): store unreadable")
	}
	defer fdb.Close()
	fturns := readSession(fdb, forked)

	var fUsers int
	for _, tn := range fturns {
		if tn.Role == "user" {
			fUsers++
		}
		if strings.Contains(tn.Text, "BETA") {
			t.Fatalf("the branch carried the anchored turn (%q) on opencode %s — messageID is "+
				"NOT exclusive anymore; docs/55 §55.3 and the anchor translation need revisiting", tn.Text, ver)
		}
	}
	if fUsers != 1 {
		t.Errorf("branch has %d user turns, want 1 (everything before the anchor) on opencode %s", fUsers, ver)
	}
	if len(fturns) == 0 {
		t.Errorf("branch carried no history at all on opencode %s", ver)
	}
	var sawAlpha bool
	for _, tn := range fturns {
		if strings.Contains(tn.Text, "ALPHA") {
			sawAlpha = true
		}
	}
	if !sawAlpha {
		t.Errorf("branch is missing the turn BEFORE the anchor on opencode %s — the cut is too early", ver)
	}

	// Baseline: no anchor still copies everything, so the pre-docs/55 behaviour is intact.
	whole, err := serveForkSession(addr, ses, dir, "")
	if err != nil {
		t.Fatalf("serveForkSession (whole): %v", err)
	}
	var wUsers int
	for _, tn := range readSession(fdb, whole) {
		if tn.Role == "user" {
			wUsers++
		}
	}
	if wUsers != 2 {
		t.Errorf("whole-conversation fork has %d user turns, want 2 on opencode %s", wUsers, ver)
	}
}

// TestContractLiveOAuthReadyWindow pins the startup race behind the 500 we hit in the
// wild（ref=err_91d98832, `OAuth method not found: opencode/device`, 起動 85ms 後の
// click）: /global/health answers before the plugin that registers the device method has
// loaded, so the auth call must wait for the METHOD, not for health. Starts a private
// serve on its own port so the workspace's shared daemon is untouched.
//
//	OPENCODE_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLiveOAuthReadyWindow ./internal/agents/opencode/
func TestContractLiveOAuthReadyWindow(t *testing.T) {
	requireLive(t)
	const addr = "http://127.0.0.1:7803"
	cmd := exec.Command("opencode", "serve", "--hostname", "127.0.0.1", "--port", "7803")
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		t.Skipf("opencode serve を起動できない: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Wait exactly as Supervisor.Ensure does — health only.
	deadline := time.Now().Add(30 * time.Second)
	for !healthy(addr) {
		if time.Now().After(deadline) {
			t.Fatal("serve が起動しなかった")
		}
		time.Sleep(100 * time.Millisecond)
	}
	healthAt := time.Now()
	ready := deviceMethodReady(addr)

	if err := waitOAuthMethod(addr, 30*time.Second); err != nil {
		t.Fatalf("device メソッドが現れない: %v", err)
	}
	t.Logf("health 到達時点で device メソッドあり=%v / メソッド確認まで health から %s", ready, time.Since(healthAt).Round(time.Millisecond))

	// そして実際に開始できること（attempt は作るだけ・承認しないので資格情報は変わらない）。
	var env envelope[attemptInfo]
	body := map[string]any{"methodID": oauthMethodID, "inputs": map[string]string{}}
	if err := daemonJSON("POST", addr, "/api/integration/"+oauthIntegrationID+"/connect/oauth", body, &env); err != nil {
		t.Fatalf("connect/oauth: %v", err)
	}
	if env.Data.AttemptID == "" || env.Data.URL == "" || env.Data.Mode != "auto" {
		t.Fatalf("attempt の形が変わった: %+v", env.Data)
	}
	if code := userCode(env.Data.URL, env.Data.Instructions); code == "" {
		t.Errorf("ユーザーコードを取り出せない: url=%q instructions=%q", env.Data.URL, env.Data.Instructions)
	}
	_ = daemonJSON[struct{}]("DELETE", addr, "/api/integration/attempt/"+env.Data.AttemptID, nil, nil)
}
