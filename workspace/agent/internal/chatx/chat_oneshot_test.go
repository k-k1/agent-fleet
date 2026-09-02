package chatx

// 一発ヘッドレス（タイトル/ブランチ名/返信候補）の claude 経路の契約テスト（docs/log/46 §1-a-2）。
//
// この argv は「毎回払う固定オーバーヘッド」を削るためのもので、うっかり元に戻すと
// 静かにトークンだけが 4 倍に戻る（機能は動くので気づけない）。形をここで固定する。
//
// ライブ側（AF_TITLE_LIVE=1）は実 claude を1回だけ撃ち、削った状態でも実際に件名が
// 返ることと、実測の削減幅が維持されていることを確認する。

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func TestClaudeOneShotArgs(t *testing.T) {
	args := claudeOneShotArgs("あなたは件名を付ける専用ツールです。", "haiku")
	joined := strings.Join(args, " ")

	// 置換であって追記ではない（--append-system-prompt だと既定プロンプトが丸ごと残る）。
	if !hasFlagValue(args, "--system-prompt", "あなたは件名を付ける専用ツールです。") {
		t.Fatalf("--system-prompt にペルソナが乗っていない: %q", joined)
	}
	if strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("--append-system-prompt は既定プロンプトを残す＝オーバーヘッドの主因: %q", joined)
	}
	if !hasFlagValue(args, "--model", "haiku") {
		t.Fatalf("--model が渡っていない: %q", joined)
	}
	// ツールは1つも積まない。ツールが無いので権限スキップも不要。
	if n := len(args); args[n-2] != "--tools" || args[n-1] != "" {
		t.Fatalf(`--tools "" は可変長引数なので末尾でなければならない: %q`, joined)
	}
	if strings.Contains(joined, "--disallowedTools") || strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Fatalf("ツール無しなら disallow も権限スキップも要らない: %q", joined)
	}
	// 転写を残さない（一発呼び出しは resume しない）。
	if !hasArg(args, "--no-session-persistence") {
		t.Fatalf("--no-session-persistence が無い: %q", joined)
	}
	if !hasFlagValue(args, "--output-format", "json") {
		t.Fatalf("usage/コストを読むので JSON 出力は必須: %q", joined)
	}
}

func TestClaudeOneShotArgsAllowsCLIDefault(t *testing.T) {
	args := claudeOneShotArgs("persona", "")
	if got := argValue(args, "--model"); got != "" {
		t.Fatalf("model = %q, want no explicit model", got)
	}
}

func TestClaudeOneShotEnvCutsThinking(t *testing.T) {
	if !hasArg(claudeOneShotEnv, "MAX_THINKING_TOKENS=0") {
		t.Fatalf("18文字の件名に思考トークンは要らない: %v", claudeOneShotEnv)
	}
}

func hasArg(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func hasFlagValue(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1] == val
		}
	}
	return false
}

// TestTitleSuggestLive は実 claude を撃つ opt-in の契約テスト。日本語ロケールと英語
// ロケールの両方を1回ずつ通す — 会話ログは同じ日本語のまま、表示言語だけで出力言語が
// 変わる（英語話者は日本語コードベースの会話でも英語の件名を読む）ことを実測で押さえる。
// 単体テストはプロンプト文字列しか見られないので、実際に英語で返るかはここでしか分からない。
// 実行例: AF_TITLE_LIVE=1 go test -run TestTitleSuggestLive -v .
func TestTitleSuggestLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("AF_TITLE_LIVE=1 で実 claude のタイトル提案契約テストを有効化")
	}
	const log = "user: 使用量のグラフを作りたい\nassistant: 台帳を設計します"
	for _, lang := range []string{"ja", "en"} {
		t.Run(lang, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			reply, err := OneShotHeadless(ctx, OneShotShort, titleSuggestPersona(lang),
				titleSuggestInstructions(lang)+log+"\n"+titleSuggestFooter(lang), titleModel())
			if err != nil {
				t.Fatalf("oneShotHeadless: %v", err)
			}
			title := cleanSuggestedTitle(reply)
			if title == "" {
				t.Fatalf("件名が空（前置き除去が効きすぎ／システムプロンプトの削りすぎ）: reply=%q", reply)
			}
			if strings.Contains(title, "\n") {
				t.Fatalf("件名は1行のはず: %q", title)
			}
			switch lang {
			case "en":
				if hasJapanese(title) {
					t.Fatalf("英語ロケールなのに日本語の件名: %q (raw=%q)", title, reply)
				}
			case "ja":
				if !hasJapanese(title) {
					t.Fatalf("日本語ロケールなのに日本語が無い件名: %q (raw=%q)", title, reply)
				}
			}
			t.Logf("lang=%s title=%q (raw=%q)", lang, title, reply)
		})
	}
}

// hasJapanese（仮名・漢字を1文字でも含むか）は prompt_lang_test.go のものを共用する。
// ロケール判定の実測用: 英語件名に固有名詞としての ASCII が混ざるのは正常なので、判定は
// 「日本語文字の有無」で行う。

// TestUsageLedgerLive は実 claude を1回撃ち、台帳に「実測の1行」が実際に落ちることを
// 見る opt-in テスト（docs/log/46 P1 完了条件）。単体テストは組み立てた usageCall しか通らない
// ので、CLI の出力形が変わったこと（modelUsage のキー名・canonicalModel・total_cost_usd）は
// ここでしか検知できない。
// 実行例: AF_TITLE_LIVE=1 go test -run TestUsageLedgerLive -v .
// liveTurns は3機能に食わせる最小の会話ログ。
func oneShotLiveTurns() []transcript.Turn {
	return []transcript.Turn{
		{Role: "user", Text: "使用量のグラフを作りたい", TS: "2026-07-25T09:00:00Z"},
		{Role: "assistant", Text: "機能別トークン計測の台帳を設計します。まず補助呼び出しの実在箇所を洗い出しました。", TS: "2026-07-25T09:01:00Z"},
	}
}

// TestBranchSuggestLive / TestReplySuggestLive — oneShotHeadless を共有する残り2機能。
// 削った argv でもペルソナの指示（kebab-case 1語 / 短い返信候補）が守られることを見る。
func TestCheapOneShotModel(t *testing.T) {
	// 実カタログ（codex debug models 2026-07-25）: mini だけが小型。
	got := cheapOneShotModel([]string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"})
	if got != "gpt-5.4-mini" {
		t.Fatalf("小型モデルを選べていない: %q", got)
	}
	// 小型が無いカタログでは "" を返し、呼び出し側は -m を渡さない（従来動作へ縮退）。
	// 存在しない名前を推測するより、今と同じコストを払う方が安全。
	if got := cheapOneShotModel([]string{"gpt-5.6-sol", "gpt-5.5"}); got != "" {
		t.Fatalf("小型が無ければ空を返すべき: %q", got)
	}
	if got := cheapOneShotModel(nil); got != "" {
		t.Fatalf("カタログ取得失敗（未ログイン等）でも空: %q", got)
	}
	// 大文字混じり・他ベンダの語彙でも拾う（カタログのドリフト耐性）。
	if got := cheapOneShotModel([]string{"Gemini-3-FLASH"}); got != "Gemini-3-FLASH" {
		t.Fatalf("marker は大小文字を無視して照合する: %q", got)
	}
}

func TestRecommendedUtilityModelStableBackends(t *testing.T) {
	if got := recommendedUtilityModel(session.KindClaude); got != "haiku" {
		t.Errorf("claude recommendation = %q", got)
	}
	if got := recommendedUtilityModel(session.KindCursor); got != "" {
		t.Errorf("cursor recommendation = %q, want Auto", got)
	}
	if got := recommendedUtilityModel(session.KindAgy); got != defaultAgyChatModel {
		t.Errorf("agy recommendation = %q", got)
	}
}

func TestCodexOneShotArgs(t *testing.T) {
	t.Setenv("AF_TITLE_MODEL_CODEX", "gpt-5.4-mini") // カタログ取得（実 CLI）に依存させない
	args, _ := codexOneShotArgs()
	joined := strings.Join(args, " ")

	if !hasFlagValue(args, "-m", "gpt-5.4-mini") {
		t.Fatalf("AF_TITLE_MODEL_CODEX が効いていない: %q", joined)
	}
	if !hasFlagValue(args, "-c", `model_reasoning_effort="low"`) {
		t.Fatalf("一発呼び出しに利用者の high 設定を効かせてはいけない: %q", joined)
	}
	if !hasArg(args, "--ephemeral") {
		t.Fatalf("一発呼び出しはスレッドを残さない: %q", joined)
	}
	if args[len(args)-1] != "-" {
		t.Fatalf("stdin 読みの \"-\" は末尾でなければならない: %q", joined)
	}
}

func TestCodexOneShotArgsForSelectedModel(t *testing.T) {
	t.Setenv("AF_TITLE_MODEL_CODEX", "env-model")
	args, auto := codexOneShotArgsFor("ui-model")
	if auto {
		t.Fatal("an explicit UI model must not be treated as an automatic cheap-model pick")
	}
	if got := argValue(args, "-m"); got != "ui-model" {
		t.Fatalf("model = %q, want ui-model", got)
	}
}

// TestCodexOneShotLive — 実 codex を1回撃つ opt-in の契約テスト。-c の TOML 構文と、
// カタログから選んだ小型モデル名が実在することを、実 CLI でしか確かめられない。
// 実行例: AF_TITLE_LIVE_CODEX=1 go test -run TestCodexOneShotLive -v .
func TestCodexOneShotLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE_CODEX") != "1" {
		t.Skip("AF_TITLE_LIVE_CODEX=1 で実 codex の一発呼び出し契約テストを有効化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	args, _ := codexOneShotArgs()
	t.Logf("argv: codex %s", strings.Join(args, " "))
	cmd := chatCodexCmd(ctx, nil, args...)
	cmd.Stdin = strings.NewReader(headlessPrompt(titleSuggestPersona("ja"), nil,
		"以下の会話に件名を付けてください。\nuser: 使用量のグラフを作りたい\nassistant: 台帳を設計します"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("codex 実行失敗（-c 構文かモデル名が実在しない疑い）: %s", cliErr(err))
	}
	reply, _, execErr, _ := parseCodexExecEvents(out)
	if execErr != "" {
		t.Fatalf("codex がエラーを返した: %s", execErr)
	}
	if title := cleanSuggestedTitle(reply); title == "" {
		t.Fatalf("件名が空: reply=%q", reply)
	} else {
		t.Logf("title=%q", title)
	}
}

// TestOpencodeOneShotLive — opencode は「カタログに載っている＝そのアカウントで使える」
// ではない、を固定するテスト（実測 2026-07-25: opencode/claude-haiku-4-5 は models に
// 載っているのに実行すると "Unexpected server error"、--model 無しの既定は正常応答）。
// なので opencode 側では安価モデルの自動ピンをしない。ここでは既定経路が生きていること
// だけを実 CLI で確かめる。実行例: AF_TITLE_LIVE_OPENCODE=1 go test -run TestOpencodeOneShotLive -v .
func TestOpencodeOneShotLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE_OPENCODE") != "1" {
		t.Skip("AF_TITLE_LIVE_OPENCODE=1 で有効化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	args := []string{"run", "--format", "json", "--dir", chatWorkdir(),
		headlessPrompt(titleSuggestPersona("ja"), nil,
			"以下の会話に件名を付けてください。\nuser: 使用量のグラフを作りたい\nassistant: 台帳を設計します")}
	cmd := exec.CommandContext(ctx, "opencode", args...)
	cmd.Dir = chatWorkdir()
	cmd.Env = envWith(opencode.Env()...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode 実行失敗: %s", cliErr(err))
	}
	reply, _, _, _, _ := parseOpencodeRunEvents(out)
	if title := cleanSuggestedTitle(reply); title == "" {
		t.Fatalf("件名が空: reply=%q", reply)
	} else {
		t.Logf("title=%q", title)
	}
}

func TestCodexOneShotFallsBackWhenPickIsOurs(t *testing.T) {
	// 利用者が明示したモデルは尊重（勝手に外さない）。
	t.Setenv("AF_TITLE_MODEL_CODEX", "gpt-5.4-mini")
	if _, auto := codexOneShotArgs(); auto {
		t.Fatal("環境変数で明示されたモデルを自前ピク扱いにしてはいけない")
	}
	// 自前ピクを外した argv は -m とその値だけが消え、他は不変。
	got := codexOneShotArgsNoModel([]string{"exec", "-m", "gpt-5.4-mini", "-c", `model_reasoning_effort="low"`, "-"})
	want := []string{"exec", "-c", `model_reasoning_effort="low"`, "-"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("-m の対だけを落とすはず: got=%q", got)
	}
}
