package chatx

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The chat backends' kind switches move onto methods of the provider types (chat_backends.go,
// #2870). Until the switches are deleted, each method must do what its switch arm does. The
// arms shell out to vendor CLIs, so they are compared as code (printed statements) rather than
// run — never start a real CLI here (see headlessAvailInFlight).

var kindConst = map[string]string{
	"session.KindClaude": session.KindClaude, "session.KindCodex": session.KindCodex,
	"session.KindOpencode": session.KindOpencode, "session.KindAgy": session.KindAgy,
	"session.KindCursor": session.KindCursor, "session.KindMuse": session.KindMuse, "lcppKind": lcppKind,
}

func parseChatx(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

func printStmts(fset *token.FileSet, list []ast.Stmt) string {
	var b bytes.Buffer
	for _, s := range list {
		printer.Fprint(&b, fset, s)
		b.WriteString("\n")
	}
	return b.String()
}

// switchArms returns, for the first `switch kind` in function fn, each case's kind and its
// printed body, with the identifier kind replaced by the case's constant.
func switchArms(t *testing.T, fset *token.FileSet, f *ast.File, fn string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range f.Decls {
		var body *ast.BlockStmt
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == fn {
				body = d.Body
			}
		case *ast.GenDecl: // var headlessAvailCheck = func(kind string) bool {…}
			for _, sp := range d.Specs {
				if vs, ok := sp.(*ast.ValueSpec); ok && vs.Names[0].Name == fn {
					body = vs.Values[0].(*ast.FuncLit).Body
				}
			}
		}
		if body == nil {
			continue
		}
		ast.Inspect(body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || len(out) > 0 {
				return len(out) == 0
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "kind" {
				return true
			}
			for _, c := range sw.Body.List {
				cc := c.(*ast.CaseClause)
				for _, e := range cc.List {
					var b bytes.Buffer
					printer.Fprint(&b, fset, e)
					ast.Inspect(&ast.BlockStmt{List: cc.Body}, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok && id.Name == "kind" {
							id.Name = b.String()
						}
						return true
					})
					out[kindConst[b.String()]] = printStmts(fset, cc.Body)
				}
			}
			return false
		})
	}
	if len(out) == 0 {
		t.Fatalf("no switch on kind found in %s", fn)
	}
	return out
}

// methodBodies returns, per kind, the printed body of method name on the backend types.
func methodBodies(t *testing.T, fset *token.FileSet, f *ast.File, name string) map[string]string {
	t.Helper()
	recvKind := map[string]string{}
	for k, p := range chatBackends {
		recvKind[reflect.TypeOf(p).Name()] = k
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != name {
			continue
		}
		k, ok := recvKind[fd.Recv.List[0].Type.(*ast.Ident).Name]
		if !ok {
			t.Fatalf("%s on a type that is no backend", name)
		}
		out[k] = printStmts(fset, fd.Body.List)
	}
	return out
}

func TestChatBackendsAreTheProviders(t *testing.T) {
	if len(chatBackends) != len(ChatProviders) {
		t.Fatalf("chatBackends has %d kinds, ChatProviders %d", len(chatBackends), len(ChatProviders))
	}
	for k, p := range ChatProviders {
		b := backend(k)
		if reflect.TypeOf(b) != reflect.TypeOf(p) {
			t.Errorf("%s: backend %T, ChatProviders %T", k, b, p)
		}
		if got, want := b.(kindedBackend).kind(), ChatProviderKind(&ChatConversation{}, p); got != want {
			t.Errorf("%s: kind() = %q, ChatProviderKind = %q", k, got, want)
		}
	}
}

func TestHeadlessAvailableEqualsTheSwitch(t *testing.T) {
	fs1, sw := parseChatx(t, "chat_providers.go")
	fs2, caps := parseChatx(t, "chat_backends.go")
	arms, methods := switchArms(t, fs1, sw, "headlessAvailCheck"), methodBodies(t, fs2, caps, "headlessAvailable")
	if len(arms) != 7 || !reflect.DeepEqual(arms, methods) {
		t.Fatalf("headlessAvailable differs from the switch:\nswitch  %q\nmethods %q", arms, methods)
	}
}

func TestRecommendedUtilityEqualsTheSwitch(t *testing.T) {
	fs1, sw := parseChatx(t, "chat_providers.go")
	fs2, caps := parseChatx(t, "chat_backends.go")
	arms, methods := switchArms(t, fs1, sw, "recommendedUtilityModelV"), methodBodies(t, fs2, caps, "recommendedUtility")
	if len(arms) != 4 || !reflect.DeepEqual(arms, methods) {
		t.Fatalf("recommendedUtility differs from the switch:\nswitch  %q\nmethods %q", arms, methods)
	}
}

// The runner arms call oneShotOn<Kind>(ctx, &call, run); each method must call the same one.
func TestRunOneShotEqualsTheSwitch(t *testing.T) {
	callee := func(body string) string { return strings.SplitN(body, "(", 2)[0] }
	fs1, sw := parseChatx(t, "chat_providers.go")
	fs2, caps := parseChatx(t, "chat_backends.go")
	arms, methods := switchArms(t, fs1, sw, "oneShotRunOn"), methodBodies(t, fs2, caps, "runOneShot")
	delete(arms, "") // default
	if len(arms) != 6 || len(methods) != len(arms) {
		t.Fatalf("switch arms %v, methods %v", arms, methods)
	}
	for k, a := range arms {
		want := callee(strings.TrimPrefix(a, "out, e := "))
		if got := callee(strings.TrimPrefix(methods[k], "return ")); got != want {
			t.Errorf("%s: runOneShot calls %q, the switch %q", k, got, want)
		}
	}
	var runners []string
	for k, b := range chatBackends {
		if _, ok := b.(oneShotRunner); ok {
			runners = append(runners, k)
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(runners)), slices.Sorted(slices.Values(oneShotKinds))) {
		t.Errorf("one-shot runners %v, oneShotKinds %v", runners, oneShotKinds)
	}
}

// The three single-kind conditions (agy's display-name override, muse's safe default, claude's
// caller model) become marker capabilities held by exactly that kind.
func TestSingleKindCapabilities(t *testing.T) {
	has := func(want string, ok func(ChatProvider) bool) {
		t.Helper()
		for k, b := range chatBackends {
			if ok(b) != (k == want) {
				t.Errorf("%s: capability for %s only, has it = %v", k, want, ok(b))
			}
		}
	}
	has(session.KindAgy, func(b ChatProvider) bool { _, ok := b.(envModelNamer); return ok })
	has(session.KindMuse, func(b ChatProvider) bool { _, ok := b.(safeOneShotModeler); return ok })
	has(session.KindClaude, func(b ChatProvider) bool { _, ok := b.(callerModeler); return ok })
}
