//go:build tui_contract

// プラン承認の実 TUI 契約プローブ — Tier 2（claude-tui-contract.yml から走る）。
//
// 守る対象は「Console のプランカードのバッジが実際の決着と一致すること」。ここは
// 2 回壊れており、どちらも**単体テストが緑のまま**だった:
//   - 2026-07-22: 却下が位置固定キー（Down×3）で、短いメニューでは Yes に回り込み
//     **却下が承認になった**。
//   - 2026-08-31: 承認結果に計画本文が丸ごと付くようになり、キーワード判定が本文の
//     「却下」を拾って**承認に 却下 バッジ**が付いた。
//
// 内訳（どちらもロックでは検知できない層）:
//
//	A. 承認キーの前提 — ExitPlanMode メニューの既定行（❯）が必ず "Yes" である。
//	   production の承認は「Enter＝既定を選ぶ」なので、既定が Yes でなくなった日に
//	   承認ボタンが承認でなくなる。testdata の固定キャプチャではなく**実物**で見る。
//	B. 決着テキストの読み方 — 実際に承認まで通し、production の転写読み出しが返す
//	   Answer が「承認」と読め、かつ計画本文を巻き込んでいないこと。
//
// 課金: 実ターン 1 回（短いプラン 1 本＋承認後の一手）。
package main

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const (
	planModalWait   = 4 * time.Minute
	planOutcomeWait = 3 * time.Minute
)

// 計画を出させるだけの小さな依頼。plan モードなので claude は ExitPlanMode で決着を
// 求めて止まる（承認後の実作業も 1 ファイル作るだけで終わる）。
const planContractPrompt = "Plan only: propose creating a file notes.txt whose single line is the word tmux. " +
	"Keep the plan to two short bullets and present it for approval now."

// planMenuLineRe は ExitPlanMode の選択肢行。tmuxx/plan_approval_test.go（固定キャプチャ
// のロック）と同じ形で、こちらは実物のフレームに当てる。
var planMenuLineRe = regexp.MustCompile(`^(❯\s+)?([0-9]+)\.\s+(.*\S)\s*$`)

type planMenuOption struct {
	n         int
	label     string
	isDefault bool
}

func parseLivePlanMenu(capture string) []planMenuOption {
	var opts []planMenuOption
	for _, ln := range strings.Split(capture, "\n") {
		m := planMenuLineRe.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		n := 0
		_, _ = fmt.Sscanf(m[2], "%d", &n)
		opts = append(opts, planMenuOption{n: n, label: m[3], isDefault: m[1] != ""})
	}
	return opts
}

func TestClaudePlanApprovalContractLive(t *testing.T) {
	for _, bin := range []string{"tmux", "claude"} {
		if _, err := exec.LookPath(bin); err != nil {
			requireTUIContract(t, false, fmt.Sprintf("%s が PATH にありません: %v", bin, err))
		}
	}
	if v, err := exec.Command("claude", "--version").Output(); err == nil {
		t.Logf("claude version: %s", strings.TrimSpace(string(v)))
	}

	name := fmt.Sprintf("contract-plan-%d", os.Getpid())
	sock := fmt.Sprintf("af-plan-contract-%d", os.Getpid())
	t.Setenv("AF_TMUX_SOCKET", sock)
	defer func() {
		_ = tmuxx.Cmd("kill-server").Run()
		time.Sleep(750 * time.Millisecond)
	}()

	// production の起動計画をそのまま使う（plan モードのフラグもフォルダ信頼も本番経路）。
	meta := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Mode: "plan"}
	agent := claude.New()
	launch, err := agent.BuildLaunch(meta, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	tn := session.TmuxName(name)
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "-c", launch.Cwd, launch.Program).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}

	// composer readiness は Console の起動シードと同じ判定。plan モードで起動したので
	// モードチップも Plan のはず（ここが違えば --permission-mode の扱いが変わっている）。
	deadline := time.Now().Add(tuiContractReadyWait)
	mode := ""
	for time.Now().Before(deadline) {
		// 起動時ダイアログは**必ず行を選んでから** Enter する。既定は上流の都合で
		// 入れ替わり、実際 2.1.248 で信頼ダイアログの既定が「No, exit」になった
		// （盲打ちの Enter がセッションを終了させる）。production の起動は
		// --allow-dangerously-skip-permissions を渡すので、ack が保存されていない
		// 環境ではこの確認が出るのが正常。
		frame := tmuxx.CapturePane(tn)
		switch {
		case strings.Contains(frame, "Bypass Permissions mode") && strings.Contains(frame, "Yes, I accept"):
			chooseDialogRow(t, tn, "Yes, I accept")
			time.Sleep(2 * time.Second)
			continue
		case strings.Contains(frame, "trust this folder") || strings.Contains(frame, "Do you trust the files"):
			chooseDialogRow(t, tn, "Yes, I trust")
			time.Sleep(2 * time.Second)
			continue
		}
		if mode = sessionx.PaneMode(session.KindClaude, tn); mode != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if mode == "" {
		t.Fatalf("%s 以内に composer を認識できませんでした\npane:\n%s", tuiContractReadyWait, tmuxx.CapturePane(tn))
	}
	if !strings.EqualFold(mode, "Plan") {
		t.Errorf("mode chip = %q, want Plan — plan モードでの起動フラグが変わった可能性", mode)
	}

	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "-l", planContractPrompt).CombinedOutput(); err != nil {
		t.Fatalf("send prompt: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("submit prompt: %v: %s", err, out)
	}

	// --- A: 実物のメニューで「既定は必ず Yes」を確かめる ---------------------------
	var opts []planMenuOption
	deadline = time.Now().Add(planModalWait)
	for time.Now().Before(deadline) {
		frame := tmuxx.CapturePane(tn)
		if o := parseLivePlanMenu(frame); len(o) >= 2 && hasYesRow(o) {
			opts = o
			t.Logf("ExitPlanMode モーダル:\n%s", frame)
			break
		}
		time.Sleep(time.Second)
	}
	if opts == nil {
		t.Fatalf("%s 以内にプラン承認モーダルが出ませんでした（モデルが ExitPlanMode を呼ばなかった／"+
			"モーダルの形が変わった）\npane:\n%s", planModalWait, tmuxx.CapturePane(tn))
	}
	def := -1
	for i, o := range opts {
		if o.isDefault {
			def = i
		}
	}
	if def < 0 {
		t.Fatalf("既定行（❯）が見つかりません — production の承認は Enter＝既定選択なので、"+
			"何が選ばれるか分からない状態です:\n%+v", opts)
	}
	if !isYesLabel(opts[def].label) {
		t.Fatalf("既定行が Yes ではありません: %q。**Console の承認ボタン（Enter）が承認以外を"+
			"選ぶ状態** — planDecision.ts の PLAN_APPROVE_KEYS を見直すこと\n%+v", opts[def].label, opts)
	}
	t.Logf("承認キーの前提 OK: 既定 = %q（選択肢 %d 件）", opts[def].label, len(opts))

	// production の承認と同じ打鍵（planDecision.ts の PLAN_APPROVE_KEYS = ["Enter"]）。
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("approve: %v: %s", err, out)
	}

	// --- B: 決着テキストを production の読み出しで分類する -------------------------
	// /messages と同じ読み方をする: CollectTurns（窓内解決）で拾い、空なら Console と
	// 同じく CollectInteractionAnswers（全転写の qid→決着マップ）で埋める。claude は
	// generic な Agent.Transcript を持たない（package main の /messages が直接この
	// 2 つを呼ぶ）ので、ここもその 2 つを呼ぶのが本番経路。
	sid := session.UUID(meta.Dir, meta.Name)
	var part *transcript.Part
	outcome := ""
	deadline = time.Now().Add(planOutcomeWait)
	for time.Now().Before(deadline) && outcome == "" {
		lines := claude.TranscriptLines(sid)
		turns := claude.CollectTurns(lines, 0, len(lines))
		answers := claude.CollectInteractionAnswers(lines)
		for i := range turns {
			for pi := range turns[i].Parts {
				p := &turns[i].Parts[pi]
				if p.Kind != "plan" {
					continue
				}
				part = p
				if outcome = p.Answer; outcome == "" {
					outcome = answers[p.QID].Text
				}
			}
		}
		if outcome == "" {
			time.Sleep(time.Second)
		}
	}
	if part == nil {
		t.Fatalf("%s 以内にプランが転写に出ませんでした（ExitPlanMode の記録形式が変わった可能性）"+
			"\nsid=%s\npane:\n%s", planOutcomeWait, sid, tmuxx.CapturePane(tn))
	}
	if outcome == "" {
		t.Fatalf("%s 以内にプランの決着が転写に出ませんでした（承認は通っているのに tool_result を"+
			"読めていない = カードが決着しないまま残る）\nsid=%s\npane:\n%s", planOutcomeWait, sid, tmuxx.CapturePane(tn))
	}
	t.Logf("ExitPlanMode の決着（planAnswerHead 後）: %q", outcome)
	if v := claude.PlanVerdict(outcome); v != claude.PlanApproved {
		t.Errorf("承認したのに判定が %q — この文言では Console のバッジが 承認 になりません。"+
			"planDecision.ts / plan_verdict.go の語彙を実物に合わせ直すこと（2026-08-31 の再発）:\n  %q",
			v, outcome)
	}
	// 承認結果に計画本文が付く形（`## Approved Plan:`）が別の目印に変わると、本文が
	// 判定に流れ込んでバッジが化ける。切り落とし後に計画が残っていないことで見張る。
	if body := strings.TrimSpace(part.Plan); body != "" && strings.Contains(outcome, body) {
		t.Errorf("決着テキストに計画本文が丸ごと残っています = 埋め込みの目印が変わった。"+
			"planAnswerHead / PLAN_BODY_MARKER を実物に合わせること:\n  %q", outcome)
	}
}

// chooseDialogRow moves the ❯ marker onto the row containing want and only then presses
// Enter. Never blind-Enter a startup dialog: the highlighted row is whatever upstream
// decided this week, and on 2.1.248 that was "No, exit".
func chooseDialogRow(t *testing.T, tn, want string) {
	t.Helper()
	for i := 0; i < 5; i++ {
		cur := ""
		for _, ln := range strings.Split(tmuxx.CapturePane(tn), "\n") {
			if strings.Contains(ln, "❯") {
				cur = ln
				break
			}
		}
		if cur == "" {
			break
		}
		if strings.Contains(cur, want) {
			_ = tmuxx.Cmd("send-keys", "-t", tn, "Enter").Run()
			return
		}
		_ = tmuxx.Cmd("send-keys", "-t", tn, "Down").Run()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("起動時ダイアログで %q の行を選べませんでした（選択肢の形が変わった可能性）\npane:\n%s",
		want, tmuxx.CapturePane(tn))
}

func isYesLabel(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "yes")
}

func hasYesRow(opts []planMenuOption) bool {
	for _, o := range opts {
		if isYesLabel(o.label) {
			return true
		}
	}
	return false
}
