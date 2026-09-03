package main

// control-plane/errcodes.go の値と Console の `err.<code>` カタログを突き合わせる。
// workspace/agent 側の errcodes_catalog_test.go と対で、**同じ穴の CP 側**。
//
// 🔴 CP には Console のカタログを読むヘルパが 1 つも無く、`egress_member.go` と
// `internal/mcpsrv/mcp_server.go` の「追加・改名時は errors.ts の err.<code> も同時に」
// という**コメントだけ**が規約だった。綴りを変えても Go も Console も緑のまま、画面だけが
// 開発者向けメッセージへ落ちる。
//
// agent 側と同じく**実際に送出されるコードだけ**を見る（宣言だけの定数は画面に出ようが無い）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cpConsoleCatalog concatenates one locale's domain catalogs.
// ⚠️ `locales/<locale>.ts` は import と spread しか持たない合成ファイルなので読まない
// （読むと「キーが在るのに無い」と言う検査になる）。ADR 0067 決定 4。
func cpConsoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := filepath.Join("..", "console", "src", "lib", "i18n", "locales", locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// console/ を含まない配布物でのビルドに備える（agent 側の先例と同じ）。
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	if n == 0 {
		t.Fatalf("%s に .ts が 1 つも無い（カタログの置き場所が変わった？）", dir)
	}
	return b.String()
}

func TestCPEmittedErrCodesHaveConsoleCatalogEntry(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "errcodes.go", nil, 0)
	if err != nil {
		t.Fatalf("errcodes.go を読めない: %v", err)
	}
	codeOf := map[string]string{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					codeOf[n.Name] = strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	if len(codeOf) == 0 {
		t.Fatal("errcodes.go から定数を 1 つも読めなかった＝この検査が無言化している")
	}

	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]string{}
	scanned := 0
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || n == "errcodes.go" {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		scanned++
		ast.Inspect(af, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok {
				if code, ok := codeOf[id.Name]; ok {
					emitted[code] = id.Name
				}
			}
			return true
		})
	}
	if scanned < 50 {
		t.Fatalf(".go を %d 本しか読めていない＝この検査が無言化している", scanned)
	}
	if len(emitted) == 0 {
		t.Fatal("送出されているエラーコードを 1 つも見つけられなかった（走査が壊れている）")
	}

	catalog := cpConsoleCatalog(t, "ja")
	for code, constName := range emitted {
		if !consoleCatalogHasKeyCP(catalog, "err."+code) {
			t.Errorf("%s = %q を送出しているのに、Console のカタログに \"err.%s\" が無い。"+
				"console/src/lib/i18n/locales/{ja,en}/errors.ts へ同時に足すこと",
				constName, code, code)
		}
	}
}

func consoleCatalogHasKeyCP(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}
