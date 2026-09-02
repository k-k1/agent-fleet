package chatx

// chatx 単体テストでの配線（本番は main の chat_wiring.go が持つ）。
// internal/mcpx/deps_test.go と同じ形。
//
// 🔥 **凍結ワイヤの値（errCode*）は本物の文字列を書く。** Console の
// `err.<code>` カタログと対になっていて、テストがその値を突くものがあるため。
// 関数側は「本物と同じ形の最小実装」で、**無害な既定値は置かない**（未配線は
// Configure が panic で落とす）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func testDeps() Deps {
	return Deps{
		ErrCodeChatAgentUnsupported:   "chat_agent_unsupported",
		ErrCodeChatAssistantNotFound:  "chat_assistant_not_found",
		ErrCodeChatConversationNotFnd: "chat_conversation_not_found",
		ErrCodeChatMessageEmpty:       "chat_message_empty",
		ErrCodeChatNothingToCompact:   "chat_nothing_to_compact",
		ErrCodeChatPromptEmpty:        "chat_prompt_empty",
		ErrCodeChatTitleEmpty:         "chat_title_empty",
		ErrCodeLocked:                 "locked",
		ErrCodeTitleFeatureDisabled:   "title_feature_disabled",
		ErrCodeTitleNoContent:         "title_no_content",

		// 🔥 ここは**本物と同じ読み方**にする。空を返す偽物にすると
		// 「設定が効く」ことを見ている検査（TestResolveChatModelUsesAssistantPreference /
		// TestChatModelForResolvesPerActualBackend / TestChatAutoTurnLimit）が
		// **アサーションはそのままなのに通らなくなる／弱くなる**。ui-prefs の読み出しは
		// internal/uiprefs が持っているので、main を介さずに同じ経路を通せる。
		AssistantAgentOrderPref: func() []string { return DefaultHeadlessOrder },
		AssistantChatModelPref: func(kind string) (string, bool) {
			return modelPrefForTest("assistantModels", kind)
		},
		AiAssistOrderPref: func() []string { return DefaultHeadlessOrder },
		AiShortModelPref: func(kind string) (string, bool) {
			return modelPrefForTest("aiShortModels", kind)
		},
		AiProseModelPref: func(kind string) (string, bool) {
			return modelPrefForTest("aiProseModels", kind)
		},
		// main の chatAutoTurnLimit と同じ丸め: 未設定なら既定、常に [1, 上限] に収める。
		ChatAutoTurnLimit: func() int {
			v, ok := uiprefs.Read()["assistantAutoTurnLimit"].(float64)
			if !ok {
				return DefaultAutoTurns
			}
			n := int(v)
			if n < 1 {
				return 1
			}
			if n > MaxAutoTurnLimit {
				return MaxAutoTurnLimit
			}
			return n
		},
		ChatAutoTurnModel: func() string {
			v, _ := uiprefs.Read()["assistantAutoTurnModel"].(string)
			return strings.TrimSpace(v)
		},

		FilterVisibleModels: func(_ string, list []agents.ModelChoice) []agents.ModelChoice { return list },
		VisibleModel:        func(_, model string) string { return model },
		VisibleModelIDs:     func(_ string, ids []string) []string { return ids },

		AssistantDeps: func() assistants.Deps {
			return assistants.NewDeps(func() string { return "" }, func() string { return session.KindClaude })
		},
		// main の ensureBuiltinKnowledge と同じ置き場を作る（会話が Knowledge に
		// このパスを持つことを chat_verb_test.go が見ている）。埋め込み本文の実体化は
		// main 側の責務なのでディレクトリだけ用意する。
		EnsureBuiltinKnowledge: func() string {
			dir := filepath.Join(paths.HomeDir(), ".config", "agent-fleet", "knowledge", "af")
			_ = os.MkdirAll(dir, 0o700)
			return dir
		},

		CleanSuggestedTitle:      strings.TrimSpace,
		TitleModel:               func() string { return "haiku" },
		TitleSuggestFooter:       func(string) string { return "footer" },
		TitleSuggestInstructions: func(string) string { return "instructions" },
		TitleSuggestPersona:      func(string) string { return "persona" },
		TitleSuggestTimeout:      60 * time.Second,

		CleanSuggestedReplies:    func(s string) []string { return strings.Split(s, "\n") },
		ReplyCounterpartChat:     1,
		ReplySuggestEnabled:      func() bool { return true },
		ReplySuggestInstructions: func(string, int) string { return "reply-instructions" },
		ReplySuggestLogHeader:    func(string) string { return "log" },
		ReplySuggestModel:        func() string { return "haiku" },
		ReplySuggestPersona:      func(string) string { return "reply-persona" },
		ReplySuggestTimeout:      60 * time.Second,
		ReplySuggestWindow: func(b *strings.Builder, msgs []ReplyMsg) {
			for _, m := range msgs {
				b.WriteString(m.Role + ": " + m.Text + "\n")
			}
		},

		AbortResumeHolds: func(string, claude.Abort, time.Time) bool { return false },
		ChatTurnUsageTag: func(convID, seedVerb, trigger string) usagex.Tag {
			return usagex.Tag{Feature: usagex.FeatureAssistantChat, Trigger: trigger, Ref: convID, Verb: seedVerb}
		},
		CleanTitle: func(s string) (string, bool) {
			s = strings.TrimSpace(s)
			return s, s != ""
		},
		// 本物（agent.go）と同じ形: 知らない種別は claude に丸める。
		NormalizeKind: func(kind string) string {
			if _, ok := ChatProviders[kind]; ok {
				return kind
			}
			return session.KindClaude
		},
		// main の safeBrowsePath と同じ形（ブラウズ根の下へ収まる相対パスだけ通す）。
		// 素通しの偽物にすると、添付ファイルの置き場が Knowledge に入る検査
		// （chat_verb_test.go）が**根の下に解決されない**ので落ちる＝弱い偽物は
		// アサーションを変えずに検査を壊す。除外リスト（isDenied）は fs.go の責務。
		SafeBrowsePath: func(p string) (string, string, bool) {
			root := os.Getenv("AF_BROWSE_ROOT")
			if root == "" {
				root = paths.HomeDir()
			}
			p = strings.TrimSpace(p)
			clean := filepath.Clean(p)
			if filepath.IsAbs(p) {
				rel, err := filepath.Rel(root, clean)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return "", "", false
				}
				if rel == "." {
					rel = ""
				}
				return clean, rel, true
			}
			if clean == "." {
				clean = ""
			}
			if clean == ".." || strings.HasPrefix(clean, "../") {
				return "", "", false
			}
			full := filepath.Join(root, clean)
			rel, err := filepath.Rel(root, full)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", "", false
			}
			if rel == "." {
				rel = ""
			}
			return full, rel, true
		},
		MaybePushOperatorReply: func(string, string) {},
		RateLimitState:         func(string) (string, string, bool) { return "", "", false },
	}
}

// modelPrefForTest は main の assistantModelPref と同じ読み方（除外リストの判定は
// model_deny.go の責務なので、ここでは設定の読み出しだけを写す）。
func modelPrefForTest(key, kind string) (string, bool) {
	raw, ok := uiprefs.Read()[key].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := raw[kind].(string)
	return v, ok
}

func init() { Configure(testDeps()) }

// TestConfigureRejectsEveryMissingField は **フィールドを 1 つずつ落として panic することを
// 全フィールドについて**確かめる。手書きの検査だと**フィールドを足したときに検査へ足し忘れて
// 穴が開く**ので、reflect で回す（ADR 決定 5 の但し書き・#317）。
func TestConfigureRejectsEveryMissingField(t *testing.T) {
	full := testDeps()
	rt := reflect.TypeOf(full)
	t.Cleanup(func() { Configure(testDeps()) })
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		t.Run(name, func(t *testing.T) {
			d := testDeps()
			reflect.ValueOf(&d).Elem().Field(i).SetZero()
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s を落としても panic しなかった（配線漏れが緑になる）", name)
				}
				if msg, _ := r.(string); !strings.Contains(msg, name) {
					t.Fatalf("panic の文言に %s が出ていない: %v", name, r)
				}
			}()
			Configure(d)
		})
	}
}

// TestInstallReconcilerForTestHasNoProductionCaller は、**テスト専用に公開した据え付け口が
// 本番コードから呼ばれていない**ことを固定する（レビュー参考 3）。
//
// `InstallReconcilerForTest` は `TestSessionReport*` 3 本の end-to-end が「本物の
// reconciler が回っていること」を前提にしているために公開している（`reportRec` は var で、
// 別名で受けると写しになり据え替えが届かない）。**本番から呼べる形なのは事実**なので、
// 呼ばれ始めたら赤で気付けるようにしておく。
//
// 🔥 **テキスト検索ではなく AST で「呼び出し」だけを見る。** 素の文字列一致だと
// **宣言している当のファイルと doc コメント**を拾って常に赤になる（最初にそう書いた）。
// 🔥 **走査した本数を必ず確かめる**（#320 の「1 件も見つからなければ何も検査しない」対策）。
// パスの前提が崩れて 0 ファイルしか読まなければ、この検査は「呼び出し 0 件」を無条件に
// 報告する＝存在しないのと同じになる。
func TestInstallReconcilerForTestHasNoProductionCaller(t *testing.T) {
	roots := []string{".", "../.."} // internal/chatx と workspace/agent（package main）
	scanned := 0
	for _, root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("走査できない %s: %v", root, err)
		}
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			path := filepath.Join(root, n)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			scanned++
			ast.Inspect(f, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name == "InstallReconcilerForTest" {
					t.Errorf("%s が InstallReconcilerForTest を呼んでいる"+
						"（テスト専用の据え付け口。本番から回すなら設計を見直すこと）", path)
				}
				return true
			})
		}
	}
	if scanned < 50 {
		t.Fatalf("非テストの .go を %d 本しか読めていない＝この検査が無言化している"+
			"（置き場が変わった？ roots=%v）", scanned, roots)
	}
}
