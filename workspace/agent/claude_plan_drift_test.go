//go:build drift

// プラン決着（ExitPlanMode の tool_result）の文言ドリフト検知 — Tier 1。
// build tag `drift` で通常の `go test ./...` から除外される（実 CLI か実転写が要る）。
//
// なぜ要るか: バッジ（承認 / 却下）は claude が返す**非契約の文言**を読んで出している。
// 2026-08-31、承認結果に承認された計画本文が丸ごと付くようになり（`## Approved Plan:`
// 以下）、キーワード照合が計画本文の「却下」を拾って**承認したプランに 却下 バッジ**が
// 付いた。単体テストは既知の形を固定しているだけなので、この変更を 1 つも検知できない。
//
// ここは 2 本立て:
//   - TestDriftClaudePlanResultLiterals — 実 CLI バイナリに「我々が読んでいる文言」が
//     まだ在るか。クレデンシャル不要・実ターン不要なので毎日回せる（cli-drift.yml）。
//     ⚠ 文字列が在ることは「今もその場所で使われている」証明ではない（false green は
//     あり得る）。経路まで通す証明は claude_plan_contract_test.go（実 TUI）の役目。
//   - TestDriftClaudePlanResultsInRealTranscripts — この機械の**実際の転写**に残る決着を
//     production の読み出しで分類し、承認とも却下とも読めない結果が無いか見る。CI が
//     踏めない承認肢（"Yes, and manually approve edits" 等）も、実フリートで使われた分は
//     ここに出る。Workspace 内で回す用。
package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// planResultLiterals は製品コードが依存している文言。ここが消えたら、バッジの判定か
// 埋め込み計画本文の切り落としのどちらかが黙って壊れる。
var planResultLiterals = []string{
	// 承認側のヘッダ（planDecision.ts isApproved / plan_verdict.go planApprovedRe）。
	"User has approved your plan",
	// 承認結果に埋め込まれる計画本文の始まり（planAnswerHead / PLAN_BODY_MARKER）。
	// これを見失うと計画本文がバッジ判定に流れ込み、2026-08-31 の症状が再発する。
	"## Approved Plan:",
	// 却下側。Console の 却下 ボタンは中断（Escape）なので、決着はこの形で残る。
	"Request interrupted by user for tool use",
}

func TestDriftClaudePlanResultLiterals(t *testing.T) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		needBin(t, "claude") // E2E_REQUIRE=1 なら fail、そうでなければ skip
		return
	}
	if p, err := filepath.EvalSymlinks(bin); err == nil {
		bin = p
	}
	if v, err := exec.Command("claude", "--version").Output(); err == nil {
		t.Logf("claude version: %s (%s)", strings.TrimSpace(string(v)), bin)
	}
	// 実体は単一バイナリ（2.1.251 は 214MB の claude.exe）だが、以前は JS バンドル
	// だった。どちらでも見つかるよう、バイナリ本体とパッケージ配下のスクリプトを見る。
	files := []string{bin}
	if dir := filepath.Dir(filepath.Dir(bin)); dir != "" {
		for _, pat := range []string{"*.js", "*.cjs", "*.mjs"} {
			if m, _ := filepath.Glob(filepath.Join(dir, pat)); m != nil {
				files = append(files, m...)
			}
		}
	}
	for _, lit := range planResultLiterals {
		found := false
		for _, f := range files {
			if ok, err := fileContains(f, lit); err == nil && ok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CLI から %q が消えている — プラン決着の文言が変わった可能性。\n"+
				"実物を 1 件採取して console/src/features/mirror/planDecision.ts と\n"+
				"workspace/agent/internal/agents/claude/plan_verdict.go を合わせ直すこと\n"+
				"（放置すると承認/却下のバッジが黙って逆になる = 2026-08-31 の再発）。", lit)
		}
	}
}

// fileContains streams f looking for needle, so a 200MB+ binary is not read into memory.
func fileContains(path, needle string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	const chunk = 4 << 20
	pat := []byte(needle)
	buf := make([]byte, chunk+len(pat))
	keep := 0 // 直前チャンクの末尾を残して、境界にまたがる一致を落とさない
	for {
		n, err := f.Read(buf[keep:])
		if n > 0 && bytes.Contains(buf[:keep+n], pat) {
			return true, nil
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if keep+n >= len(pat) {
			keep = copy(buf, buf[keep+n-len(pat)+1:keep+n])
		} else {
			keep += n
		}
	}
}

func TestDriftClaudePlanResultsInRealTranscripts(t *testing.T) {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	logs, _ := filepath.Glob(filepath.Join(root, "projects", "*", "*.jsonl"))
	if len(logs) == 0 {
		t.Skipf("転写が無い (%s) — 実 Workspace 内で回すテスト", root)
	}
	total, unknown := 0, 0
	for _, p := range logs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var lines [][]byte
		for _, ln := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(ln)) > 0 {
				lines = append(lines, ln)
			}
		}
		// production の読み出しをそのまま通す（Answer は planAnswerHead 済み）。
		for _, turn := range claude.CollectTurns(lines, 0, len(lines)) {
			for _, part := range turn.Parts {
				if part.Kind != "plan" || part.Answer == "" {
					continue
				}
				total++
				if claude.PlanVerdict(part.Answer) == claude.PlanUnknown {
					unknown++
					t.Errorf("%s: 承認とも却下とも読めない決着 = 文言ドリフト:\n  %q",
						filepath.Base(p), transcript.CapOutput(part.Answer))
				}
				// 承認結果に計画本文が残っていたら切り落としが効いていない。
				if part.Plan != "" && strings.Contains(part.Answer, strings.TrimSpace(part.Plan)) {
					t.Errorf("%s: 決着に計画本文が丸ごと残っている = planAnswerHead の目印が変わった",
						filepath.Base(p))
				}
			}
		}
	}
	t.Logf("実転写のプラン決着: %d 件（判定不能 %d）/ %d ファイル", total, unknown, len(logs))
	if total == 0 {
		t.Skip("決着済みのプランが 1 件も無い — このホストではドリフトを判定できない")
	}
}
