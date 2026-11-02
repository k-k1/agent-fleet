package opencode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

// bilingual is the "ja / en" shape: the Agent does not know the member's language, so a message
// that reaches the Console as free text carries both, Japanese first.
func bilingual(s string) bool {
	ja, en, ok := strings.Cut(s, " / ")
	return ok && hasCJK(ja) && en != "" && !hasCJK(en)
}

// Every error text the package builds with a Japanese literal must be a "ja / en" pair; a
// Japanese-only one reaches an English-UI member untranslated (the Agent has no language setting).
func TestErrorLiteralsAreBilingual(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	seen := 0
	for _, f := range files {
		// The files whose errors reach the Console as free text.
		switch f {
		case "opencode.go", "v2form.go", "v2state.go", "oauth.go", "driver.go", "serve.go":
		default:
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, _ := sel.X.(*ast.Ident); id == nil ||
				!(id.Name == "errors" && sel.Sel.Name == "New" || id.Name == "fmt" && sel.Sel.Name == "Errorf") {
				return true
			}
			// The whole format string, with "a" + "b" concatenations joined.
			var sb strings.Builder
			ast.Inspect(call.Args[0], func(m ast.Node) bool {
				if bl, ok := m.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					s, _ := strconv.Unquote(bl.Value)
					sb.WriteString(s)
				}
				return true
			})
			if msg := sb.String(); hasCJK(msg) {
				seen++
				if !bilingual(msg) {
					t.Errorf("%s: Japanese-only error text %q (want \"ja / en\")", f, msg)
				}
			}
			return true
		})
	}
	if seen < 10 {
		t.Fatalf("only %d Japanese error literals scanned: this check has gone silent", seen)
	}
}

func TestSentinelAndFormErrorsAreBilingual(t *testing.T) {
	if !bilingual(ErrNotConnected.Error()) {
		t.Errorf("ErrNotConnected = %q", ErrNotConnected)
	}
	f := v2Form{Fields: []v2FormField{{Key: "n", Type: "number", Required: true}}}
	for name, answers := range map[string][]agents.InteractionAnswer{
		"count":  nil,
		"number": {{Text: "x"}},
		"empty":  {{}},
	} {
		_, err := f.answer(answers)
		if err == nil || !bilingual(err.Error()) {
			t.Errorf("%s: err = %v, want a bilingual error", name, err)
		}
	}
	if err := waitOAuthMethod("http://127.0.0.1:1", 0); err == nil || !bilingual(err.Error()) {
		t.Errorf("waitOAuthMethod timeout = %v, want a bilingual error", err)
	}
	_, _, err := (agentImpl{}).ResolveForkAtPinned(session.Meta{}, agents.ForkPoint{})
	if err == nil || !bilingual(err.Error()) {
		t.Errorf("empty fork point = %v", err)
	}
}
