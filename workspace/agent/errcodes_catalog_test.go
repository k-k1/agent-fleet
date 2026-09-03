package main

// errcodes.go の値と Console の `err.<code>` カタログを突き合わせる。
//
// 🔴 **この突き合わせはリポジトリに 1 つも無かった**（develop からある穴で、memory 家系に
// 限らない）。errcodes.go の先頭にも egress_member.go にも「変更は両側同時に」と**コメントでは
// 書いてある**が、守っているのは注意書きだけだった。**Go 側で綴りを 1 文字変えると、Console は
// キーを引けずに developer 向けの英語メッセージへ黙ってフォールバックする**——
// HTTP は 200/4xx のまま、Go のテストも Console のテストも緑。
//
// **見るのは「実際に送出されるコード」だけ**にしてある。errcodes.go には宣言だけあって
// どこからも参照されていない定数が在り（実測 3 本: conn_jira_fields_required /
// conn_jira_rejected / write_cancelled）、**送出されないコードは画面に出ようが無い**ので
// カタログを要求しても雑音にしかならない。参照された日に、この検査がカタログを要求する。
//
// 📌 ja が正本（`../../console/src/lib/i18n/locales/ja`）。en の欠けは i18n 側の検査の担当。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// emittedErrCodes returns the string values of errcodes.go's constants that some
// other file in package main actually references. The map is code -> const name.
func emittedErrCodes(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "errcodes.go", nil, 0)
	if err != nil {
		t.Fatalf("errcodes.go を読めない: %v", err)
	}
	codeOf := map[string]string{} // const name -> code value
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
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				codeOf[n.Name] = strings.Trim(lit.Value, `"`)
			}
		}
	}
	if len(codeOf) == 0 {
		t.Fatal("errcodes.go から定数を 1 つも読めなかった＝この検査が無言化している")
	}

	// どの定数が実際に参照されているかを、errcodes.go 以外の package main で数える。
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
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
			id, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if code, ok := codeOf[id.Name]; ok {
				out[code] = id.Name
			}
			return true
		})
	}
	if scanned < 50 {
		t.Fatalf(".go を %d 本しか読めていない＝この検査が無言化している", scanned)
	}
	if len(out) == 0 {
		t.Fatal("送出されているエラーコードを 1 つも見つけられなかった（走査が壊れている）")
	}
	return out
}

// catalogExempt はカタログを持たないことが**意図されている**コード。
// 🔴 増やすときは必ず理由を書くこと。ここは「まだ直していない」を隠す場所ではない。
var catalogExempt = map[string]string{
	// errcodes.go の宣言直上に理由が書いてある: クライアントが既に諦めた後にだけ返るので
	// （タイムアウト / 切断を mutex 取得時に検出）、生きた画面がこれを描くことは無い。
	"write_cancelled": "生きたクライアントに届かない（errcodes.go の宣言に明記）",

	// ⚠️ 以下 2 件は**意図された免除ではなく、この検査が見つけた実際の穴**。
	// 免除に入れているのは「塞ぎ方に判断が要る」ため（司令塔へ確認中）:
	// errText は `err.<code>` が在るとサーバの message を**捨てて**カタログ文言を使う
	// （client.ts:159）。conn_jira_rejected は message に Jira 側の生の理由を載せているので、
	// 一般化した和文を足すと**いま出ている具体的な理由が消える**（＝情報が減る）。
	// いまの実害は「和文にならず英語の message が出る」で、[object Object] にはならない。
	"conn_jira_fields_required": "未カタログ（穴）。和文を足すか message 優先にするか要判断",
	"conn_jira_rejected":        "未カタログ（穴）。カタログ化すると Jira の生の理由を捨てる",
}

func TestEmittedErrCodesHaveConsoleCatalogEntry(t *testing.T) {
	catalog := consoleCatalog(t, "ja")
	for code, constName := range emittedErrCodes(t) {
		if _, ok := catalogExempt[code]; ok {
			continue
		}
		if !consoleCatalogHasKey(catalog, "err."+code) {
			t.Errorf("%s = %q を送出しているのに、Console のカタログに \"err.%s\" が無い。"+
				"console/src/lib/i18n/locales/{ja,en}/errors.ts へ同時に足すこと"+
				"（無いと画面は開発者向けメッセージへ黙ってフォールバックする）",
				constName, code, code)
		}
	}
	// 免除表が腐るのを防ぐ: カタログが後から足されたら免除を外させる。
	// 放置すると「免除だから見ない」が積み上がり、この検査は名前だけになる。
	for code, why := range catalogExempt {
		if consoleCatalogHasKey(catalog, "err."+code) {
			t.Errorf("err.%s はカタログに在るのに免除表に残っている（理由: %s）。免除表から外すこと", code, why)
		}
	}
}
