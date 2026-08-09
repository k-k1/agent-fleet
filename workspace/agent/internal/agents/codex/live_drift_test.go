//go:build driftlive

// codex CLI ドリフト検知（Tier 2 / **実ターンを消費する**）。build tag `driftlive` で
// 通常の `go test ./...` からも Tier 1（tag `drift`）からも隔離され、CI では
// workflow_dispatch の live 入力を立てた時だけ走る（codex-contract.yml の live-drift）。
//
// Tier 1（drift_test.go）は API 到達前で完結する範囲＝無料・無認証を扱う。ここは
// 「実際に1ターン回さないと観測できない」4点だけを担当する:
//  1. Stop hook → status="idle"（TUI/CLI ルートの完了シグナル）
//  2. rollout の task_started / task_complete（Stop hook 取りこぼし時の自己修復の素）
//  3. request_user_input の function_call（質問あり状態）— **best-effort**
//  4. managed の turn/started・turn/completed（status working→idle を駆動）
//
// 実測コスト（"reply with exactly: pong" の1ターン）: **約 5k〜15k tokens とブレる**
// （scratch dir で 5,288 / このテストの隔離 HOME で 15,196 を実測）。差は codex 自身が
// 積む文脈（AGENTS.md・環境情報等）由来で、我々の制御外。全ケースで概ね 30〜60k
// tokens を見込む。auth_mode="chatgpt" ならサブスク枠の消費（API 課金ではない）。
// 各ターンの実コストは `MEASURED COST:` 行としてジョブログに出る。
//
// 設計方針は Tier 1 と同じ: **期待値を手で書き写さない**。hook の入れ子・-c フラグ・
// feature 名は buildProgram() の出力から取り、状態は本番の status ストア／
// latestRolloutLifecycle／PendingQuestionID／managed driver そのもので確認する。
// そうしないと「テスト同士が一致するだけ」になり、ドリフト検知にならない。
package codex

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// ---- 認証（失効時に「何が起きたか」が一目で分かることを最優先） -------------------
//
// CI: secrets.E2E_CODEX_AUTH_JSON（`codex login` 後の ~/.codex/auth.json の中身）を
// 隔離 HOME へ書く。chatgpt モードのトークンは refresh_token 前提なので、CI に置いた
// コピーはいずれ失効する（サーバ側ローテーションで古い refresh_token が無効化される）。
// 失効を「謎の 401」で終わらせないよう、(a) ターン前に `codex login status` で
// トークンフリーに検査し、(b) ターン中の 401 も文面で分類して、対処（再 login →
// secret 更新）まで書いたメッセージで落とす。
//
// 手元: E2E_CODEX_LOCAL_AUTH=1 で隔離 HOME の .codex を実 ~/.codex へ symlink する。
// **実 auth.json をコピーしない**のは意図的で、コピー先で refresh が走ると
// refresh_token がローテートし、**実フリートのログインが巻き添えで無効化されうる**
// ため（実測: last_refresh は数日前＝次の利用で refresh が走る状態だった）。symlink
// なら通常のセッションと同じ経路で in-place に更新され、乖離が起きない。
//
// ただし symlink の副作用として、ensureFolderTrusted が実 ~/.codex/config.toml へ
// テスト用 temp dir の `[projects."/tmp/TestLiveDrift…"]` を追記する（rollout も実
// ~/.codex/sessions に残る）。実害は無いが溜まるので、手元で回した後は掃除してよい。
// CI 経路（E2E_CODEX_AUTH_JSON）は HOME ごと隔離されるのでこの副作用は無い。
const (
	authJSONEnv  = "E2E_CODEX_AUTH_JSON"
	localAuthEnv = "E2E_CODEX_LOCAL_AUTH"
)

// authFailureRe spots an auth problem in codex's own output so the test can explain it
// instead of surfacing a bare non-zero exit.
var authFailureRe = regexp.MustCompile(`(?i)401|unauthorized|invalid_grant|invalid_request|refresh|token.*expired|not logged in`)

// tokensUsedRe reads codex exec's own accounting line ("tokens used  5,288"), so the
// job log records what the run actually cost.
var tokensUsedRe = regexp.MustCompile(`tokens used\s+([0-9,]+)`)

func liveCodexBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("codex not on PATH: %v", err)
	}
	return p
}

// liveHome provisions an isolated HOME (so the AF state stores under
// $HOME/.config/agent-fleet never touch the live fleet's) wired to real credentials,
// and refuses to continue unless codex reports itself logged in.
func liveHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")

	switch {
	case os.Getenv(authJSONEnv) != "":
		if err := os.MkdirAll(codexHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "auth.json"),
			[]byte(os.Getenv(authJSONEnv)), 0o600); err != nil {
			t.Fatal(err)
		}
	case os.Getenv(localAuthEnv) == "1":
		real := filepath.Join(realHome(t), ".codex")
		if _, err := os.Stat(filepath.Join(real, "auth.json")); err != nil {
			t.Fatalf("%s=1 but %s/auth.json is unreadable: %v (run `codex login`)", localAuthEnv, real, err)
		}
		// Symlink, never copy: a copy that refreshes would rotate the refresh token and
		// could invalidate this machine's real login.
		if err := os.Symlink(real, codexHome); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("no codex credentials.\n"+
			"  CI   : set secrets.%s to the contents of ~/.codex/auth.json (after `codex login`)\n"+
			"  local: run with %s=1 to borrow this machine's ~/.codex (symlinked, not copied)",
			authJSONEnv, localAuthEnv)
	}

	t.Setenv("HOME", home)

	// Token-free preflight. `codex login status` reads the stored credential and reports
	// "Logged in using ChatGPT" / "Not logged in" / a parse error — enough to tell a
	// missing secret from a malformed/expired one before spending a single token.
	out, err := exec.Command(liveCodexBin(t), "login", "status").CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		t.Fatalf("codex login status failed: %v\n%s\n\n"+
			"=> The stored codex credential is missing, malformed, or expired.\n"+
			"   Fix: run `codex login` on a trusted machine, then copy the FULL contents of\n"+
			"   ~/.codex/auth.json into secrets.%s (chatgpt tokens rotate, so a secret that\n"+
			"   worked before can go stale — re-copy it).", err, s, authJSONEnv)
	}
	t.Logf("auth preflight: %s", s)
	return home
}

func realHome(t *testing.T) string {
	t.Helper()
	// t.Setenv may already have moved HOME; ask the OS-independent way first.
	if h := os.Getenv("REAL_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot resolve real home: %v", err)
	}
	return h
}

// failAuthAware turns codex's raw output into an actionable message when the failure is
// an auth failure (the expected way this job rots), and reports it verbatim otherwise.
func failAuthAware(t *testing.T, what string, err error, out string) {
	t.Helper()
	if authFailureRe.MatchString(out) {
		t.Fatalf("%s failed and codex's output looks like an AUTH failure: %v\n%s\n\n"+
			"=> The chatgpt subscription token stored for CI has most likely expired or been\n"+
			"   rotated. Fix: `codex login` locally, then re-copy ~/.codex/auth.json into\n"+
			"   secrets.%s. (This is expected to recur — chatgpt auth refreshes in place, and\n"+
			"   a CI copy cannot persist the rotated token.)", what, err, out, authJSONEnv)
	}
	t.Fatalf("%s failed: %v\n%s", what, err, out)
}

// ---- 共通ヘルパ -----------------------------------------------------------------

// hookExeRe matches the leaf command's exe path in a production hook override so the
// live tests can point it at a freshly built agent binary while keeping the argv shape
// (`session-status <state> <sid> codex`) and the nesting exactly as production emits.
var hookExeRe = regexp.MustCompile(`command="[^"]*?session-status `)

// buildAgentBin builds the real workspace-agent so the injected hooks run production's
// own `session-status` subcommand (hook JSON stdin -> status store + RememberSid).
// Without this the hook would invoke the go test binary, which has no such subcommand.
//
// Call this BEFORE liveHome: with HOME already redirected, `go build` would treat the
// temp dir as GOPATH, re-download the module cache into it, and then fail the test in
// TempDir cleanup (the cache is written read-only).
func buildAgentBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "workspace-agent")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/k-k1/agent-fleet/workspace/agent")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build workspace-agent: %v\n%s", err, out)
	}
	return bin
}

// prodArgs renders production's launch (buildProgram) into argv for a direct exec,
// swapping only the hook exe. Everything under test — bypass flags, -c overrides, hook
// nesting, the feature opt-in — comes from production.
func prodArgs(t *testing.T, slot, agentBin string) []string {
	t.Helper()
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", "") // CLI route
	prog := buildProgram("", "", slot, "", "")
	var args []string
	args = append(args, bypassFlagsLive(prog)...)
	for _, v := range configOverridesLive(prog) {
		args = append(args, "-c", hookExeRe.ReplaceAllLiteralString(v, `command="`+agentBin+` session-status `))
	}
	return args
}

// configOverridesLive / bypassFlagsLive mirror the Tier 1 helpers; duplicated because
// the two files sit behind different build tags.
func configOverridesLive(prog string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(prog[i:], "-c '")
		if j < 0 {
			return out
		}
		k := i + j + len("-c '")
		var sb strings.Builder
		for k < len(prog) {
			if prog[k] == '\'' {
				if strings.HasPrefix(prog[k:], `'\''`) {
					sb.WriteByte('\'')
					k += 4
					continue
				}
				break
			}
			sb.WriteByte(prog[k])
			k++
		}
		out = append(out, sb.String())
		i = k + 1
	}
}

func bypassFlagsLive(prog string) []string {
	var out []string
	for _, f := range strings.Fields(prog) {
		if strings.HasPrefix(f, "--dangerously") {
			out = append(out, f)
		}
	}
	return out
}

// logTurnCost reports what a turn actually cost, read out of the rollout with
// production's own tokenUsage parser (so the job log carries the real number rather than
// an estimate — and the parser gets exercised in passing). Best-effort: silent if the
// rollout has no token_count yet.
func logTurnCost(t *testing.T, label, cxID string) {
	t.Helper()
	p := rolloutPath(cxID)
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(lines[i]), &ev) != nil || ev.Type != "event_msg" {
			continue
		}
		if in, out, read, _, ok := tokenUsage(ev.Payload); ok {
			t.Logf("MEASURED COST [%s]: total=%d tokens (fresh_in=%d cached_read=%d out=%d)",
				label, in+read+out, in, read, out)
			return
		}
	}
}

func waitStatus(slot, want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if st, ok := status.Read(slot); ok && st.State == want {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ---- 1 + 2: Stop hook -> idle / rollout lifecycle ---------------------------------

// TestLiveDriftCodexStopHookAndRollout runs ONE real turn through production's own
// launch (hooks included) and checks the two completion signals the CLI route depends
// on. They share a turn deliberately: each is ~5.3k tokens, and both are observable
// from the same completed turn.
//
//	Stop hook  -> status "idle"                     (the state the Console reads)
//	rollout    -> task_started / task_complete      (WireLive's independent self-heal)
//
// Both go through production code (the real `session-status` subcommand, the real
// latestRolloutLifecycle), so a codex change to hook delivery or rollout event names
// fails here rather than silently stranding sessions on 進行中.
func TestLiveDriftCodexStopHookAndRollout(t *testing.T) {
	bin := liveCodexBin(t)
	agentBin := buildAgentBin(t) // before liveHome: needs the real module cache
	liveHome(t)

	work := t.TempDir()
	m := session.Meta{Name: "live-drift", Dir: work, Kind: session.KindCodex}
	slot := session.UUID(m.Dir, m.Name)

	args := append(prodArgs(t, slot, agentBin), "exec", "--skip-git-repo-check", "reply with exactly: pong")
	cmd := exec.Command(bin, args...)
	cmd.Dir = work
	started := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil {
		failAuthAware(t, "codex exec", err, string(out))
	}
	if mm := tokensUsedRe.FindStringSubmatch(string(out)); mm != nil {
		t.Logf("MEASURED COST [exec, codex's own accounting]: %s tokens", mm[1])
	}

	// 1. Stop hook -> idle. The hook is the CLI route's only completion signal.
	if !waitStatus(slot, "idle", 20*time.Second) {
		st, ok := status.Read(slot)
		t.Fatalf("status = %+v (present=%v), want State=idle.\n"+
			"The Stop hook did not reach production's session-status writer, so a finished "+
			"codex session would stay 進行中 in the Console forever.\ncodex output:\n%s", st, ok, out)
	}

	// The same hooks capture codex's own session id (it has no --session-id flag);
	// everything rollout-based hangs off it.
	cxID := sids.Read(slot)
	if cxID == "" {
		t.Fatalf("no codex session id captured — the hook payload no longer carries session_id, " +
			"so resume/fork and every rollout probe (transcript, questions, self-heal) break")
	}

	logTurnCost(t, "stop-hook+rollout", cxID)

	// 2. rollout lifecycle. rolloutCompletedAfter is what WireLive uses to heal a missed Stop.
	path := rolloutPath(cxID)
	if path == "" {
		t.Fatalf("rollout for %s not found under $HOME/.codex/sessions — the layout or naming changed", cxID)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state, at := latestRolloutLifecycle(strings.Split(string(b), "\n"))
	if state != "task_complete" {
		t.Fatalf("latestRolloutLifecycle = %q, want \"task_complete\".\n"+
			"codex renamed/dropped its task_started|task_complete event_msg payloads; "+
			"WireLive can no longer self-heal a missed Stop hook.\nrollout: %s", state, path)
	}
	if at.IsZero() || at.Before(started.Add(-time.Minute)) {
		t.Fatalf("task_complete timestamp %v is missing/stale (turn started %v) — "+
			"rolloutCompletedAfter compares against the working timestamp and would misfire", at, started)
	}
	if !rolloutCompletedAfter(m, started) {
		t.Fatalf("rolloutCompletedAfter = false for a turn that just completed — the self-heal path is broken")
	}
	t.Logf("ok: Stop hook -> idle, rollout task_complete at %s", at.Format(time.RFC3339))
}

// ---- 3: request_user_input（best-effort） -----------------------------------------

// TestLiveDriftCodexPendingQuestion checks the question path end to end: the model must
// actually call request_user_input, which is **not deterministic** — so a model that
// simply answers instead of asking SKIPS rather than fails (best-effort, non-blocking).
//
// One outcome is NOT tolerated: codex refusing the tool ("unavailable in … mode"). That
// is the exact regression 9c46074 fixed (the Default-mode opt-in silently lapsing), and
// because an unknown -c key is ignored without complaint, a feature rename upstream
// would reintroduce it invisibly. That case fails loudly.
func TestLiveDriftCodexPendingQuestion(t *testing.T) {
	bin := liveCodexBin(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not on PATH: %v", err)
	}
	agentBin := buildAgentBin(t) // before liveHome: needs the real module cache
	liveHome(t)

	work := t.TempDir()
	m := session.Meta{Name: "live-drift-q", Dir: work, Kind: session.KindCodex}
	slot := session.UUID(m.Dir, m.Name)

	// Production's BuildLaunch pre-accepts codex's per-directory trust gate before it
	// ever reaches tmux; buildProgram alone does not. Without this the TUI parks on
	// "Do you trust the contents of this directory?" and never runs the turn — which
	// this test would otherwise misread as "the model chose not to ask".
	ensureFolderTrusted(work)

	// The TUI route, not exec: request_user_input needs an interactive session to park
	// on. The prompt is a positional arg because send-keys text is unreliable on some
	// codex builds (measured).
	prompt := "Use the request_user_input tool right now to ask me whether I prefer " +
		"option A or option B. Do not answer yourself; ask me."
	args := prodArgs(t, slot, agentBin)
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, session.ShellQuote(a))
	}
	tn := fmt.Sprintf("af-live-drift-q-%d", os.Getpid())
	_ = exec.Command("tmux", "kill-session", "-t", tn).Run()
	launch := fmt.Sprintf("env HOME=%s %s %s %s", os.Getenv("HOME"), bin,
		strings.Join(quoted, " "), session.ShellQuote(prompt))
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tn,
		"-x", "200", "-y", "50", "-c", work, launch).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", tn).Run() }()

	pane := func() string {
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", tn).Output()
		return string(out)
	}

	// Poll production's own probe — the one WireLive calls to light 質問あり.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if id := PendingQuestionID(m); id != "" {
			t.Logf("ok: request_user_input pending, call id %s (HasPendingQuestion=%v)", id, HasPendingQuestion(m))
			logTurnCost(t, "question", sids.Read(slot))
			return
		}
		s := pane()
		// A skip must mean "the model chose not to ask" and nothing else. If the TUI is
		// parked on a gate, the turn never ran, and silently skipping would report false
		// confidence in a path that was never exercised.
		if strings.Contains(s, "Do you trust") {
			t.Fatalf("codex is parked on the directory-trust gate — production's "+
				"ensureFolderTrusted no longer satisfies it (its config.toml format or the gate "+
				"itself changed). Every codex TUI session would stall at launch.\n%s", lastLines(s, 8))
		}
		if strings.Contains(s, "Sign in with ChatGPT") || strings.Contains(s, "Provide your own API key") {
			t.Fatalf("codex is showing its login screen — the credential did not take effect "+
				"(expired/rotated token, or a changed auth.json format).\n%s", lastLines(s, 8))
		}
		if strings.Contains(s, "request_user_input tool is unavailable") ||
			(strings.Contains(s, "unavailable in") && strings.Contains(s, "mode")) {
			t.Fatalf("codex refused request_user_input:\n%s\n\n"+
				"=> The Default-mode opt-in (-c features.…=true from buildProgram) is no longer "+
				"taking effect, so the CLI route's 質問あり state can never light. This is the "+
				"regression f0c74e9 fixed; an unknown -c key is ignored silently, so suspect a "+
				"feature rename upstream (Tier 1's TestDriftCodexFeatureFlagsKnown should also be red).",
				strings.TrimSpace(s))
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Non-blocking by design: the model chose not to ask.
	t.Skipf("best-effort: the model did not call request_user_input within the window "+
		"(no refusal seen, so the opt-in itself looks fine). Pane tail:\n%s", lastLines(pane(), 6))
}

func lastLines(s string, n int) string {
	ln := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ln) > n {
		ln = ln[len(ln)-n:]
	}
	return strings.Join(ln, "\n")
}

// ---- 4: managed turn/started・turn/completed ---------------------------------------

// TestLiveDriftCodexManagedTurnNotifications drives the REAL managed driver (supervisor
// -> app-server -> thread/start -> turn/start) and asserts the status transitions that
// production derives purely from the turn notifications:
//
//	turn/started   -> status "working"
//	turn/completed -> status "idle"   + TurnCompleted event
//
// Going through the driver (rather than a raw RPC client) is the point: driver_test's
// mock server supplies these method names itself, so it can never notice a rename. Here
// the names come from the real binary — if codex renames them, status never moves.
func TestLiveDriftCodexManagedTurnNotifications(t *testing.T) {
	liveCodexBin(t)
	liveHome(t)

	// A private port: the live fleet's own app-server owns the default (:7798) on this
	// host and must not be disturbed.
	addr := fmt.Sprintf("ws://127.0.0.1:%d", freePort(t))
	t.Setenv(appServerAddrEnv, addr)
	defer Serve().Shutdown()

	work := t.TempDir()
	m := session.Meta{Name: "live-drift-mgd", Dir: work, Kind: session.KindCodex, Driver: session.DriverManaged}
	slot := session.UUID(m.Dir, m.Name)

	h, err := NewDriver().(interface {
		Resume(session.Meta) (agents.ThreadHandle, error)
	}).Resume(m)
	if err != nil {
		failAuthAware(t, "managed Resume (thread/start)", err, err.Error())
	}

	// The docs/30 報告 seam: main wires recordSessionNotification here, and a managed
	// turn's completion is what consumes the arm. Capture it from the real
	// turn/completed rather than a mock, so a codex rename that stops driving the
	// transition also surfaces as "報告が飛ばなくなった".
	notified := make(chan [2]string, 8)
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
		notified <- [2]string{previous, state}
	})
	defer agents.SetStateNotifier(nil)

	// turn/started is transient (it lasts only as long as the turn), so watch for it
	// from before the send rather than sampling after.
	sawWorking := make(chan struct{})
	go func() {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if st, ok := status.Read(slot); ok && st.State == "working" {
				close(sawWorking)
				return
			}
			select {
			case <-sawWorking:
				return
			default:
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	if err := h.Send(agents.TurnInput{Prompt: "reply with exactly: pong"}); err != nil {
		failAuthAware(t, "managed Send (turn/start)", err, err.Error())
	}

	var completed bool
	deadline := time.After(120 * time.Second)
	for !completed {
		select {
		case ev := <-h.Events():
			switch ev.TurnState {
			case agents.TurnCompleted:
				completed = true
			case agents.TurnFailed:
				t.Fatalf("managed turn failed (turn/completed status=failed). If this is an auth " +
					"expiry, re-issue the credential per the message in liveHome.")
			}
		case <-deadline:
			t.Fatalf("no TurnCompleted event within the window — codex's turn/completed notification " +
				"never arrived (or its threadId/turn payload changed shape), so managed sessions " +
				"would hang on 進行中 with no completion.")
		}
	}

	select {
	case <-sawWorking:
		t.Log("ok: turn/started -> status working")
	default:
		t.Error("status never became \"working\": codex's turn/started notification did not reach " +
			"dispatchNotification (renamed, or its threadId/turn.id payload changed) — the Console " +
			"would show no 進行中 and no stop button while the turn runs")
	}
	if !waitStatus(slot, "idle", 20*time.Second) {
		st, _ := status.Read(slot)
		t.Fatalf("status = %+v after completion, want idle — turn/completed no longer persists idle", st)
	}
	// 完了が報告 seam へ届いたか（docs/30 — managed は hook を持たないので、ここが
	// 切れると完了しても【セッション報告】が一切飛ばない）。
	// 通知は非同期（readLoop を塞がないため）なので待って拾う。
	var reported bool
	for wait := time.After(10 * time.Second); !reported; {
		select {
		case tr := <-notified:
			reported = tr[1] == "idle"
		case <-wait:
			goto checked
		}
	}
checked:
	if !reported {
		t.Error("完了が状態通知 seam に届かなかった: managed セッションの turn/completed が " +
			"agents.MarkTurnEnd を通っていない — オペレーターへの完了報告(docs/30)が飛ばない")
	} else {
		t.Log("ok: turn/completed -> 報告 seam (recordSessionNotification) へ通知")
	}
	logTurnCost(t, "managed", sids.Read(slot))
	t.Log("ok: turn/completed -> status idle + TurnCompleted event")
}

// ---- 5: 発言時点からの分岐（docs/55）— lastTurnId が包含であること ------------------

// TestLiveDriftCodexForkAtLastTurn is the one claim about codex that only a real run can
// settle: `thread/fork`'s lastTurnId is **inclusive**, so branching "before the user's Nth
// prompt" means sending the (N-1)th turn id. ResolveForkAt does that translation, and an
// off-by-one there is invisible in the mirror — the branch just quietly carries the very
// prompt the user wanted to retake.
//
// The mock in fork_at_test.go can only prove we SENT lastTurnId; the schema's word
// "inclusive" is documentation, not behaviour. This drives two real turns, branches at the
// second, and counts what the fork actually got.
//
// Cost: 2 turns (see this file's header for the measured range).
func TestLiveDriftCodexForkAtLastTurn(t *testing.T) {
	liveCodexBin(t)
	liveHome(t)

	addr := fmt.Sprintf("ws://127.0.0.1:%d", freePort(t))
	t.Setenv(appServerAddrEnv, addr)
	defer Serve().Shutdown()

	work := t.TempDir()
	m := session.Meta{Name: "live-drift-forkat", Dir: work, Kind: session.KindCodex, Driver: session.DriverManaged}
	slot := session.UUID(m.Dir, m.Name)

	h, err := NewDriver().(interface {
		Resume(session.Meta) (agents.ThreadHandle, error)
	}).Resume(m)
	if err != nil {
		failAuthAware(t, "managed Resume (thread/start)", err, err.Error())
	}
	liveDriftTurn(t, h, "reply with exactly: ALPHA")
	liveDriftTurn(t, h, "reply with exactly: BETA")

	srcTid := sids.Read(slot)
	if srcTid == "" {
		t.Fatal("no codex thread id recorded for the slot")
	}
	logTurnCost(t, "fork-at source", srcTid)

	// The anchor the Console would send: the SECOND user turn's AnchorID, straight off the
	// real rollout (so a change in how codex records turn ids fails here too).
	td, _ := readTranscript(m)
	var userAnchors []string
	for _, tn := range td.Turns {
		if tn.Role == "user" && tn.AnchorID != "" {
			userAnchors = append(userAnchors, tn.AnchorID)
		}
	}
	if len(userAnchors) < 2 {
		t.Fatalf("transcript has %d anchored user turns after 2 turns — turn ids are no longer "+
			"recoverable from the rollout, so the mirror can't offer 「ここから分岐」", len(userAnchors))
	}
	first, second := userAnchors[0], userAnchors[len(userAnchors)-1]
	if first == second {
		t.Fatalf("both user turns share turn id %s — task_started no longer delimits turns", first)
	}
	resolved, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: second})
	if err != nil {
		t.Fatalf("ResolveForkAt(%s): %v", second, err)
	}
	if resolved != first {
		t.Fatalf("ResolveForkAt returned %s, want the PREVIOUS turn %s", resolved, first)
	}

	cl, err := newAppClient(addr)
	if err != nil {
		t.Fatalf("app client: %v", err)
	}
	defer cl.close()
	go cl.readLoop()

	st, err := threadFork(cl, srcTid, work, resolved)
	if err != nil {
		t.Fatalf("threadFork(lastTurnId=%s): %v", resolved, err)
	}
	if st.threadID == "" {
		t.Fatal("fork returned no thread id")
	}

	// What the fork actually carries. thread/read populates turns on demand, so this needs
	// no further model spend.
	got := liveThreadTurnIDs(t, cl, st.threadID)
	src := liveThreadTurnIDs(t, cl, srcTid)
	if len(src) != 2 {
		t.Fatalf("source thread has %d turns, want 2 (setup did not produce what this test assumes)", len(src))
	}
	if len(got) != 1 {
		t.Fatalf("fork carries %d turns, want 1: lastTurnId is not inclusive-through-that-turn anymore. "+
			"ResolveForkAt's -1 translation (docs/55 §55.3) is now wrong in the other direction — "+
			"branches would carry the prompt the user meant to retake. src=%v fork=%v", len(got), src, got)
	}
	if got[0] != resolved {
		t.Errorf("fork kept turn %s, want %s (the turn we forked through)", got[0], resolved)
	}

	// §55.10-1: does a fork get a rollout before its first turn? Only informational — the
	// managed route never needs it, but the TUI route (`codex resume <fork>`) would.
	if p := rolloutPath(st.threadID); p == "" {
		t.Log("note: the fork has no rollout before its first turn — a TUI resume of a fresh fork " +
			"cannot work (docs/55 §55.10-1); managed is unaffected")
	} else {
		t.Log("note: the fork's rollout exists immediately (docs/55 §55.10-1)")
	}
}

// liveDriftTurn sends one prompt and waits for the driver's completion event.
func liveDriftTurn(t *testing.T, h agents.ThreadHandle, prompt string) {
	t.Helper()
	if err := h.Send(agents.TurnInput{Prompt: prompt}); err != nil {
		failAuthAware(t, "managed Send (turn/start)", err, err.Error())
	}
	deadline := time.After(180 * time.Second)
	for {
		select {
		case ev := <-h.Events():
			switch ev.TurnState {
			case agents.TurnCompleted:
				return
			case agents.TurnFailed:
				t.Fatalf("turn failed for prompt %q", prompt)
			}
		case <-deadline:
			t.Fatalf("no completion for prompt %q", prompt)
		}
	}
}

// liveThreadTurnIDs reads a thread's turn ids via thread/read (includeTurns).
func liveThreadTurnIDs(t *testing.T, cl *appClient, tid string) []string {
	t.Helper()
	res, err := cl.call("thread/read", map[string]any{"threadId": tid, "includeTurns": true}, 30*time.Second)
	if err != nil {
		t.Fatalf("thread/read %s: %v", tid, err)
	}
	var out struct {
		Thread struct {
			Turns []struct {
				ID string `json:"id"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("thread/read %s payload: %v", tid, err)
	}
	ids := make([]string, 0, len(out.Thread.Turns))
	for _, tn := range out.Thread.Turns {
		ids = append(ids, tn.ID)
	}
	return ids
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
