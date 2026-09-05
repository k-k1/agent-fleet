package chatx

// Contract test for the claude route of the one-shot headless calls (title / branch name /
// reply candidates) (docs/log/46 §1-a-2).
//
// This argv exists to cut the fixed overhead paid on every call. Revert it by accident and the
// token cost silently goes back to 4x while the feature keeps working, so nobody notices; the
// shape is pinned here.
//
// The live side (AF_TITLE_LIVE=1) fires the real claude exactly once and confirms that a title
// still comes back with the trimmed argv and that the measured reduction still holds.

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

	// Replacement, not append: --append-system-prompt would keep the whole default prompt.
	if !hasFlagValue(args, "--system-prompt", "あなたは件名を付ける専用ツールです。") {
		t.Fatalf("the persona is not on --system-prompt: %q", joined)
	}
	if strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("--append-system-prompt keeps the default prompt = the main source of overhead: %q", joined)
	}
	if !hasFlagValue(args, "--model", "haiku") {
		t.Fatalf("--model was not passed: %q", joined)
	}
	// No tools are loaded at all, and with no tools there is nothing to skip permissions for.
	if n := len(args); args[n-2] != "--tools" || args[n-1] != "" {
		t.Fatalf(`--tools "" is variadic, so it must come last: %q`, joined)
	}
	if strings.Contains(joined, "--disallowedTools") || strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Fatalf("with no tools, neither disallow nor permission-skip is needed: %q", joined)
	}
	// Leave no transcript: a one-shot call is never resumed.
	if !hasArg(args, "--no-session-persistence") {
		t.Fatalf("--no-session-persistence is missing: %q", joined)
	}
	if !hasFlagValue(args, "--output-format", "json") {
		t.Fatalf("JSON output is required because usage/cost is read from it: %q", joined)
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
		t.Fatalf("an 18-character title needs no thinking tokens: %v", claudeOneShotEnv)
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

// TestTitleSuggestLive is an opt-in contract test that fires the real claude. It runs the
// Japanese and the English locale once each, measuring that the conversation log stays the same
// Japanese while the display language alone changes the output language (an English speaker
// reads an English title even for a conversation over a Japanese codebase). A unit test can
// only inspect the prompt string, so whether it really comes back in English is visible only
// here.
// Run with: AF_TITLE_LIVE=1 go test -run TestTitleSuggestLive -v .
func TestTitleSuggestLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("set AF_TITLE_LIVE=1 to enable the real-claude title suggestion contract test")
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
				t.Fatalf("empty title (preamble stripping too aggressive / system prompt trimmed too far): reply=%q", reply)
			}
			if strings.Contains(title, "\n") {
				t.Fatalf("a title must be one line: %q", title)
			}
			switch lang {
			case "en":
				if hasJapanese(title) {
					t.Fatalf("Japanese title under the English locale: %q (raw=%q)", title, reply)
				}
			case "ja":
				if !hasJapanese(title) {
					t.Fatalf("no Japanese in the title under the Japanese locale: %q (raw=%q)", title, reply)
				}
			}
			t.Logf("lang=%s title=%q (raw=%q)", lang, title, reply)
		})
	}
}

// hasJapanese (does it contain even one kana or kanji?) is shared with prompt_lang_test.go.
// It is what the locale assertions measure: ASCII inside an English title (proper nouns) is
// normal, so the decision is made on the presence of Japanese characters.

// TestUsageLedgerLive (package main) fires the real claude once and checks that a measured row
// actually lands in the ledger (docs/log/46 P1 completion criterion). A unit test only sees the
// usageCall it assembled, so a change in the CLI's output shape (modelUsage key names,
// canonicalModel, total_cost_usd) is detectable only there.
// Run with: AF_TITLE_LIVE=1 go test -run TestUsageLedgerLive -v .

// oneShotLiveTurns is the minimal conversation log fed to the three features.
func oneShotLiveTurns() []transcript.Turn {
	return []transcript.Turn{
		{Role: "user", Text: "使用量のグラフを作りたい", TS: "2026-07-25T09:00:00Z"},
		{Role: "assistant", Text: "機能別トークン計測の台帳を設計します。まず補助呼び出しの実在箇所を洗い出しました。", TS: "2026-07-25T09:01:00Z"},
	}
}

// TestBranchSuggestLive / TestReplySuggestLive (package main) cover the remaining two features
// that share oneShotHeadless: that the persona's instructions (one kebab-case word / short
// reply candidates) still hold with the trimmed argv.

// TestCheapOneShotModel checks the small-model pick against a real catalogue.
func TestCheapOneShotModel(t *testing.T) {
	// Real catalogue (codex debug models, 2026-07-25): only mini is a small model.
	got := cheapOneShotModel([]string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"})
	if got != "gpt-5.4-mini" {
		t.Fatalf("no small model was picked: %q", got)
	}
	// A catalogue with no small model returns "" and the caller passes no -m, degrading to the
	// previous behaviour: paying today's cost is safer than guessing a name that may not exist.
	if got := cheapOneShotModel([]string{"gpt-5.6-sol", "gpt-5.5"}); got != "" {
		t.Fatalf("want empty when there is no small model: %q", got)
	}
	if got := cheapOneShotModel(nil); got != "" {
		t.Fatalf("want empty when the catalogue cannot be fetched (not logged in, ...): %q", got)
	}
	// Mixed case and another vendor's vocabulary are still picked up (catalogue drift tolerance).
	if got := cheapOneShotModel([]string{"Gemini-3-FLASH"}); got != "Gemini-3-FLASH" {
		t.Fatalf("the marker must match case-insensitively: %q", got)
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
	t.Setenv("AF_TITLE_MODEL_CODEX", "gpt-5.4-mini") // do not depend on a catalogue fetch (real CLI)
	args, _ := codexOneShotArgs()
	joined := strings.Join(args, " ")

	if !hasFlagValue(args, "-m", "gpt-5.4-mini") {
		t.Fatalf("AF_TITLE_MODEL_CODEX has no effect: %q", joined)
	}
	if !hasFlagValue(args, "-c", `model_reasoning_effort="low"`) {
		t.Fatalf("the user's high setting must not apply to a one-shot call: %q", joined)
	}
	if !hasArg(args, "--ephemeral") {
		t.Fatalf("a one-shot call must leave no thread behind: %q", joined)
	}
	if args[len(args)-1] != "-" {
		t.Fatalf("the \"-\" that reads stdin must come last: %q", joined)
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

// TestCodexOneShotLive is an opt-in contract test that fires the real codex once: the TOML
// syntax of -c, and that the small model name picked from the catalogue actually exists, can
// only be confirmed against the real CLI.
// Run with: AF_TITLE_LIVE_CODEX=1 go test -run TestCodexOneShotLive -v .
func TestCodexOneShotLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE_CODEX") != "1" {
		t.Skip("set AF_TITLE_LIVE_CODEX=1 to enable the real-codex one-shot contract test")
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
		t.Fatalf("codex failed (suspect the -c syntax, or a model name that does not exist): %s", cliErr(err))
	}
	reply, _, execErr, _ := parseCodexExecEvents(out)
	if execErr != "" {
		t.Fatalf("codex returned an error: %s", execErr)
	}
	if title := cleanSuggestedTitle(reply); title == "" {
		t.Fatalf("empty title: reply=%q", reply)
	} else {
		t.Logf("title=%q", title)
	}
}

// TestOpencodeOneShotLive pins that with opencode "listed in the catalogue" does NOT mean
// "usable on that account" (measured 2026-07-25: opencode/claude-haiku-4-5 is listed in models
// yet running it returns "Unexpected server error", while the default with no --model answers
// normally). That is why no cheap model is auto-pinned on the opencode side; this only confirms
// against the real CLI that the default route is alive.
// Run with: AF_TITLE_LIVE_OPENCODE=1 go test -run TestOpencodeOneShotLive -v .
func TestOpencodeOneShotLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE_OPENCODE") != "1" {
		t.Skip("set AF_TITLE_LIVE_OPENCODE=1 to enable")
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
		t.Fatalf("opencode failed: %s", cliErr(err))
	}
	reply, _, _, _, _ := parseOpencodeRunEvents(out)
	if title := cleanSuggestedTitle(reply); title == "" {
		t.Fatalf("empty title: reply=%q", reply)
	} else {
		t.Logf("title=%q", title)
	}
}

func TestCodexOneShotFallsBackWhenPickIsOurs(t *testing.T) {
	// A model the user named explicitly is respected (never dropped behind their back).
	t.Setenv("AF_TITLE_MODEL_CODEX", "gpt-5.4-mini")
	if _, auto := codexOneShotArgs(); auto {
		t.Fatal("a model named explicitly through the environment must not count as our own pick")
	}
	// Dropping our own pick removes only -m and its value; everything else is unchanged.
	got := codexOneShotArgsNoModel([]string{"exec", "-m", "gpt-5.4-mini", "-c", `model_reasoning_effort="low"`, "-"})
	want := []string{"exec", "-c", `model_reasoning_effort="low"`, "-"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("only the -m pair should be dropped: got=%q", got)
	}
}
