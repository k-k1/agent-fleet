//go:build tui_contract

// claude TUI フッタ契約プローブ（P1）。
//
// 何を守るテストか: 状態検出（spinnerActive / atPromptFooter）は claude の TUI 表示という
// **非契約・無版管理の文字列**に依存しており、2026-07-17 時点で 3 回壊れている
// （79b582b → deac672 → fce5c5e）。3 回とも単体テストは緑のままで、実フリートで人力発見
// された。testdata/footers のゴールデンコーパスは「既知の形」を固定するだけで、CLI 側が
// 4 度目のドリフトを起こしても緑のままになる（＝ロックであって検知器ではない）。
// **実 CLI を実際に走らせて初めてドリフトが分かる** — それがこのファイル。
//
// なぜ agent モジュール内に置くか: e2e/ は独立モジュールで、Go の internal 制約により
// internal/tmuxx を import できない。判定ロジックを e2e 側で書き直したら「実コードを
// 検証していないテスト」になり、今回の失敗を繰り返す。実関数をそのまま呼べる場所＝ここ。
// CI は共通setup actionで Go・tmux・claudeをrunnerへ入れ、
// `go test -tags tui_contract ./internal/tmuxx/` として走らせる
// （claude-tui-contract.yml）。巨大なWorkspaceイメージ自体は必要ない。
//
// なぜ `claude -p` ではダメか: 既存 L4（e2e/live_test.go）は headless の -p を使うため
// **フッタもスピナーも一切描画されない**。だから 3 回の破壊を 1 度も検知できなかった。
// ここは対話 TUI を tmux で起動する必要がある。
//
// 課金: 実ターンを 2 回（manual / bypass）走らせる。プロンプトは短文エッセイ 1 本で
// 数百トークン程度。OAuth トークン（サブスク枠）なら追加課金なし。
package tmuxx

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 思考させたいので tool を呼ばないプロンプトにする（manual モードでは Bash 等が許可
// ダイアログを出してしまい、ターンがモーダルで止まる）。エッセイ生成なら manual/bypass
// どちらでも許可なしに 10〜30 秒級の思考＋ストリーミングになり、**トークン counter が
// まだ乗らない思考フェーズ**（今回の回帰そのもの）を確実に踏める。
const contractPrompt = "Think step by step, then write a 120-word essay about the tmux terminal multiplexer."

const (
	tick      = 500 * time.Millisecond
	readyWait = 90 * time.Second
	turnWait  = 3 * time.Minute
)

func TestClaudeTUIContractLive(t *testing.T) {
	for _, b := range []string{"tmux", "claude"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s が無い — このテストは実イメージ内で走らせる想定", b)
		}
	}
	if v, err := exec.Command("claude", "--version").Output(); err == nil {
		t.Logf("claude version: %s", strings.TrimSpace(string(v)))
	}

	for _, m := range []struct {
		name string
		args []string
	}{
		// 既定(manual)モード。フッタが「⏸ manual mode on · ← for agents」でヒントが
		// 一切出ない＝ AtIdlePrompt が壊れた側の条件。
		{"manual", nil},
		// 実フリートの主用途。
		{"bypass", []string{"--dangerously-skip-permissions"}},
	} {
		t.Run(m.name, func(t *testing.T) { runContract(t, m.name, m.args) })
	}
}

func runContract(t *testing.T, mode string, args []string) {
	t.Helper()
	name := "tuictr-" + mode
	tn := session.TmuxName(name) // AtIdlePrompt/IsBusy は claude_<name> を見るので合わせる
	dir := t.TempDir()

	// 起動前にフォルダ信頼を書いておく（production の claude.ensureFolderTrusted と同じ形）。
	// ダイアログを「出させない」のが目的である点が大事: 2.1.248 で選択肢の並びが反転し
	// 既定が **「No, exit」** になったため（実測・2.1.247 は「Yes, I trust this folder」が
	// 既定）、「出てから Enter で承認」は承認どころか終了になる。実際それで pane が消え、
	// 空フレームのまま 90 秒待って落ちていた。本番は必ず先に書くので、ここを合わせる方が
	// 契約としても正しい（このテストが見たいのはフッタであってオンボーディングではない）。
	preTrustFolder(t, dir)

	argv := append([]string{"new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "-c", dir, "claude"}, args...)
	if out, err := exec.Command("tmux", argv...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", tn).Run() })

	waitReady(t, name, tn)

	// D: 起動直後の ready prompt。実関数（capture 込み）をそのまま呼ぶ。
	// manual モードの回帰（ヒント消失で常に false）はここで落ちる。
	if !AtIdlePrompt(name) {
		t.Errorf("ready prompt なのに AtIdlePrompt=false — フッタ契約が変わった可能性\n%s", frameDump(tn))
	}
	if IsBusy(name) {
		t.Errorf("ready prompt なのに IsBusy=true\n%s", frameDump(tn))
	}

	// ターン投入。
	if out, err := exec.Command("tmux", "send-keys", "-t", tn, contractPrompt).CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	if out, err := exec.Command("tmux", "send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("send-keys Enter: %v: %s", err, out)
	}

	frames, seen := sampleTurn(t, tn)

	// A: working表示そのものを一度も観測できないなら、表示仕様が根本から変わったか
	// ターンが走っていない。2.1.220は短いターンで経過タイマーを描かず、footerの
	// "esc to interrupt"だけを出すため、両方を独立証拠として扱う。
	nGT := 0
	for _, f := range frames {
		if f.gt != "" {
			nGT++
		}
	}
	if nGT == 0 {
		t.Errorf("ターンを走らせたのに、経過タイマーまたはesc-to-interrupt表示を1度も観測できなかった"+
			"（%d フレーム）— TUI の表示仕様が根本的に変わった可能性\n観測した行:\n%s",
			len(frames), strings.Join(seen, "\n"))
		return
	}

	// B（本命）: **working表示が出ているフレームは必ず busy と判定できねばならない**。
	// 3 回の回帰はいずれも「spinnerRe が実物より狭い」形で起きた（"esc to interrupt" 必須 →
	// ローテーションで消えて破綻／"tokens" 必須 → 思考中はトークンが出ず破綻）。この差分
	// 判定はその失敗モードを直接突く: 判定ロジックとは独立のゆるい基準（括弧付き経過
	// タイマー、または2.1.220の短いターンで唯一残るesc-to-interrupt footer）で
	// workingフレームを拾い、production の spinnerActive が追随できているかを見る。
	miss := 0
	for i, f := range frames {
		if f.gt != "" && !f.busy {
			if miss++; miss == 1 {
				// idle=true なら Console は実際に「入力待ち」バッジを出す（停止ボタンが消える）
				// ところまで行っている＝ユーザーが見た症状そのもの。
				t.Errorf("スピナーが出ているのに IsBusy=false（frame#%d, AtIdlePrompt=%v）。"+
					"spinnerRe が実物に追随できていない:\n  %q", i, f.idle, f.gt)
			}
		}
	}
	if miss > 0 {
		t.Errorf("スピナー表示フレーム %d 個中 %d 個で busy 判定に失敗\n観測した行:\n%s",
			nGT, miss, strings.Join(seen, "\n"))
	}

	// C: 終了後は idle に落ち着く。ターン後要約（「✻ Worked for 13m 53s」）を busy と
	// 誤判定すると停止バーが出っぱなしになるので、その逆方向も見る。
	if IsBusy(name) {
		t.Errorf("ターン終了後も IsBusy=true — ターン後要約を busy と誤判定している可能性\n%s", frameDump(tn))
	}
	if !AtIdlePrompt(name) {
		t.Errorf("ターン終了後に AtIdlePrompt=false\n%s", frameDump(tn))
	}

	nBusy, nTokenless := 0, 0
	for _, f := range frames {
		if f.busy {
			nBusy++
		}
		if f.gt != "" && !strings.Contains(f.gt, "tokens") {
			nTokenless++
		}
	}
	t.Logf("mode=%s: %d フレーム観測 / スピナー表示 %d / busy 判定 %d / うちトークン無し %d",
		mode, len(frames), nGT, nBusy, nTokenless)
	if nTokenless == 0 {
		// 3 度目の回帰の現場はここ。踏めなかった run はその分だけ弱い（モデルが即答すると
		// 思考フェーズが出ない）。落とすほどではないが、緑を過信しないよう明示する。
		t.Logf("注意: このターンでは思考フェーズ（トークン無しスピナー）を踏めなかった。" +
			"トークン依存の退行はこの run では検出できていない（testdata/footers の" +
			" busy_thinking_no_tokens が固定で担保）")
	}
	t.Logf("観測した spinner/footer 行:\n%s", strings.Join(seen, "\n"))
}

// waitReady は入力プロンプトに到達するまで待つ。到達できない原因は「認証できていない」
// （CI で最も疑わしい）か「フッタ契約が変わった」のどちらか。区別できないので両方を出す。
func waitReady(t *testing.T, name, tn string) {
	t.Helper()
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		s := CapturePane(tn)
		// A pristine hosted runner has no saved theme. The workspace image normally
		// inherits an initialized persistent Claude config, so this one-time selector
		// is harness onboarding rather than the composer contract under test.
		if strings.Contains(s, "Syntax theme:") &&
			(strings.Contains(s, "Dark mode") || strings.Contains(s, "Light mode")) {
			_ = exec.Command("tmux", "send-keys", "-t", tn, "Enter").Run()
			time.Sleep(2 * time.Second)
			continue
		}
		// 起動時のフォルダ信頼ダイアログ（--dangerously-skip-permissions でも出る）。
		// preTrustFolder が効いていれば出ないが、上流が保存形式を変えれば出る。**盲打ちの
		// Enter は絶対にしない** — 既定の選択肢は上流の都合で入れ替わり（2.1.248 で実際に
		// 反転した）、その日から「承認」が「終了」になる。必ず Yes の行を選んでから押す。
		if strings.Contains(s, "trust this folder") || strings.Contains(s, "Do you trust the files") {
			chooseTrustYes(t, tn)
			time.Sleep(2 * time.Second)
			continue
		}
		if AtIdlePrompt(name) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s 以内に入力プロンプトへ到達できなかった。\n"+
		"考えられる原因: (1) 認証されていない（CLAUDE_CODE_OAUTH_TOKEN は対話 TUI では効かない\n"+
		"可能性がある／ANTHROPIC_API_KEY は確認ダイアログを出す）、(2) フッタ契約が変わって\n"+
		"atPromptFooter が効かなくなった、(3) 未知のオンボーディング画面。\n%s",
		readyWait, frameDump(tn))
}

// frame は 1 回の capture に対する観測。busy/idle は**同一フレーム**に対して当てる
// （IsBusy と AtIdlePrompt を続けて呼ぶと別フレームを見て競合するので、ここだけは内部の
// 純粋関数を使う。exported 版は D/C の単発判定で実行経路ごと検証している）。
type frame struct {
	busy bool
	idle bool
	gt   string // 独立基準で拾った「スピナーが出ている」証拠行（空＝出ていない）
}

// gtSpinnerRe は **production の spinnerRe とは独立の、意図的にゆるい**「スピナーが出て
// いる」検出。括弧付きの経過タイマーだけを見る（動名詞も「…」も行頭も問わない）。
// spinnerRe で拾うと「壊れている時に限って証拠が出せない」ので、必ず別基準にすること。
//
// ターン後要約「✻ Cogitated for 6s」は括弧が無いので当たらない（＝busy を要求しない）。
var gtSpinnerRe = regexp.MustCompile(`\([^)\n]*[0-9]+(?:h|m|s)\b`)

// gtSpinnerLine は「working表示が出ている」証拠行を返す（無ければ ""）。
// 2.1.220は短いターンでタイマー付きheaderを省き、footerの"esc to interrupt"だけを
// 描く。ウェルカムボックス等の枠行は除く。
func gtSpinnerLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "│") || strings.HasPrefix(t, "╭") || strings.HasPrefix(t, "╰") {
			continue
		}
		if gtSpinnerRe.MatchString(t) || strings.Contains(t, "esc to interrupt") {
			return t
		}
	}
	return ""
}

// sampleTurn は 1 フレーム 1 capture でターンを観測する。
//
// 終了検出に spinnerActive は使えない（それが壊れている時に無限待ちになる）。また
// **回答テキストのストリーミング中はスピナーが描画されない**（実測: 6 秒のターンで
// 思考 1s → スピナー消滅 → ストリーミング 5s → 要約）ので「非 busy が続いたら終了」も
// 誤判定する。そこで独立信号＝ターン後要約（過去形動詞 + " for "）の出現で終わりを見る。
func sampleTurn(t *testing.T, tn string) (frames []frame, seen []string) {
	t.Helper()
	uniq := map[string]bool{}
	deadline := time.Now().Add(turnWait)
	for time.Now().Before(deadline) {
		s := CapturePane(tn)
		f := frame{busy: spinnerActive(s), idle: atIdlePrompt(s), gt: gtSpinnerLine(s)}
		frames = append(frames, f)
		for _, ln := range spinnerishLines(s) {
			uniq[ln] = true
		}
		if ln := findLine(s, modeFooterRe.MatchString); ln != "(none in frame)" {
			uniq[ln] = true
		}
		if f.gt == "" && postTurnSummary(s) != "" {
			break // 要約が出てスピナーも消えた＝ターン終了
		}
		time.Sleep(tick)
	}
	for k := range uniq {
		seen = append(seen, "  "+k)
	}
	sort.Strings(seen)
	time.Sleep(2 * time.Second) // C の判定前に描画を落ち着かせる
	return frames, seen
}

// postTurnSummaryRe はターン終了後に残る要約行（「✻ Worked for 13m 53s」）。過去形動詞は
// スピナーの動名詞とは別系統の語彙なので、終了検出の独立信号に使える。
var postTurnSummaryRe = regexp.MustCompile(`(?m)^\S? ?(?:Baked|Brewed|Churned|Cogitated|Cooked|Crunched|Saut\x{00E9}ed|Worked) for [0-9]`)

func postTurnSummary(s string) string {
	if ln := findLine(s, postTurnSummaryRe.MatchString); ln != "(none in frame)" {
		return ln
	}
	return ""
}

// spinnerishLines は「スピナーらしき行」を**判定ロジックとは無関係な条件**で拾う。
// spinnerRe で拾うと、壊れている時に限って何も出せず診断にならない（＝一番欲しい時に
// 役に立たない）ので、ここは意図的に別基準にしてある。
//
// 該当行を全部返す: 最初の 1 本だけ返す実装だと、ウェルカムボックスの
// 「│ Opus 4.8 (1M context) with hi… · Claude Max · │」が先にマッチして本物の
// スピナー行を握り潰した（実測で踏んだ）。枠線行は除外する。
func spinnerishLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "│") || strings.HasPrefix(t, "╭") || strings.HasPrefix(t, "╰") {
			continue // ウェルカムボックス等の枠
		}
		if strings.Contains(t, "…") && strings.Contains(t, "(") && strings.Contains(t, ")") {
			out = append(out, t)
			continue
		}
		if strings.HasPrefix(t, "✻") && strings.Contains(t, " for ") {
			out = append(out, t)
		}
	}
	return out
}

func frameDump(tn string) string {
	s := CapturePane(tn)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 14 { // 末尾（フッタ周辺）だけで足りる
		lines = lines[len(lines)-14:]
	}
	return "--- 最後のフレーム(末尾) ---\n" + strings.Join(lines, "\n") + "\n---"
}

var _ = os.Getenv // 認証は env（CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY）か既存の
// .credentials.json 任せ。ここでは読まない（打鍵・ログに載せないため）。

// claudeStateFile is where claude keeps per-user state (onboarding + per-dir trust).
// Mirrors the CLI's own resolution: $CLAUDE_CONFIG_DIR/.claude.json when set (the
// Workspace sets it), else ~/.claude.json at the home ROOT — not ~/.claude/.
func claudeStateFile(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home が引けません: %v", err)
	}
	return filepath.Join(home, ".claude.json")
}

// preTrustFolder pre-accepts the folder-trust dialog for dir, the way production's
// claude.ensureFolderTrusted does before every TUI launch. Merges into the existing
// file: the harness's own onboarding seed and whatever claude has written since must
// survive.
func preTrustFolder(t *testing.T, dir string) {
	t.Helper()
	p := claudeStateFile(t)
	root := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &root)
	}
	root["hasCompletedOnboarding"] = true
	if _, ok := root["theme"]; !ok {
		root["theme"] = "dark"
	}
	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[dir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[dir] = entry
	root["projects"] = projects
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatalf("%s を組み立てられません: %v", p, err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("%s を書けません: %v", p, err)
	}
}

// chooseTrustYes moves the selection onto "Yes, I trust this folder" and only then
// presses Enter. The highlighted row is marked with ❯; the option order is NOT stable
// across CLI versions, so the row is found by its text, never by position.
func chooseTrustYes(t *testing.T, tn string) {
	t.Helper()
	const yes = "Yes, I trust"
	for i := 0; i < 4; i++ {
		var cur string
		for _, ln := range strings.Split(CapturePane(tn), "\n") {
			if strings.Contains(ln, "❯") {
				cur = ln
				break
			}
		}
		if cur == "" {
			break // 選択マーカーが見つからない — 下でフレームごと報告する
		}
		if strings.Contains(cur, yes) {
			_ = exec.Command("tmux", "send-keys", "-t", tn, "Enter").Run()
			return
		}
		_ = exec.Command("tmux", "send-keys", "-t", tn, "Down").Run()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("信頼ダイアログで %q の行を選べませんでした（選択肢の形が変わった可能性）\n%s", yes, frameDump(tn))
}
