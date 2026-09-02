package assistants

import (
	"strings"
	"testing"
)

// 移送で main 側の 2 つ（ensureBuiltinKnowledge / preferredHeadlessAgent）が
// パッケージ変数のフックに化け、**代入 2 行を消しても全テストが緑**という穴が開いた
// （レビュー参考 1・変異試験で実測）。いまは NewDeps の引数なので渡し忘れはコンパイル
// エラーになるが、「ビルトインが実際に渡された値を使っている」ことはコンパイラには見えない。
// ここはその 1 点だけを押さえる — Agent が空文字やナレッジのパスが空で立つ形は、
// 画面上は普通に見えるので気付けない。
func TestBuiltinsUseInjectedDeps(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // uiprefs.Locale() が実ホームの ui-prefs.json を読まないように

	const knowPath = "/tmp/af-knowledge-probe"
	const agentKind = "codex-probe"
	got := Builtins(NewDeps(
		func() string { return knowPath },
		func() string { return agentKind },
	))

	if len(got) == 0 {
		t.Fatal("ビルトインが 1 つも無い")
	}
	for _, a := range got {
		if a.Agent != agentKind {
			t.Errorf("%s: Agent = %q, want %q（DefaultAgent が結線されていない）", a.ID, a.Agent, agentKind)
		}
		if len(a.Knowledge) != 1 || a.Knowledge[0] != knowPath {
			t.Errorf("%s: Knowledge = %v, want [%s]（KnowledgeDir が結線されていない）", a.ID, a.Knowledge, knowPath)
		}
		if a.Persona == "" {
			t.Errorf("%s: Persona が空", a.ID)
		}
	}
}

// ゼロ値の Deps（NewDeps を通さずに組んだもの）は、無害な既定値で走らずに落ちること。
// 「結線し忘れが緑になる」形に戻さないための番人。
func TestZeroDepsPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("ゼロ値の Deps で panic しなかった（無害な既定値で走ってはいけない）")
		}
		if !strings.Contains(fmtOf(r), "NewDeps") {
			t.Errorf("panic の文言が結線し忘れを指していない: %v", r)
		}
	}()
	_ = Builtins(Deps{})
}

func fmtOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return ""
}

func TestNewDepsRejectsNil(t *testing.T) {
	for _, c := range []struct {
		name       string
		know, agnt func() string
	}{
		{"knowledgeDir が nil", nil, func() string { return "claude" }},
		{"defaultAgent が nil", func() string { return "/k" }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("nil を渡したのに panic しなかった")
				}
			}()
			_ = NewDeps(c.know, c.agnt)
		})
	}
}
