package main

// contract_wire_test.go — 「契約の型化」の横展開（案 A のゴールデン中継）。
//
// #339 が `sessionWire` 1 家系で示した形を、**ワイヤ確認済みの家系へ表駆動で広げる**。
// ワイヤは 1 バイトも変えていない——**いまのワイヤを書き表すだけ**。
//
// 家系の選び方（第 1 段の測定・`wirescan` で確定）: Console の手書き TS 型と Go 構造体の
// キー集合が **Jaccard 0.7 以上 1.0 未満**（＝対応しているのにズレている）で、かつ
// **その Go 型が実際に JSON として書き出されている**ことを確認済みのもの。
//
// 3 本立て（**どれか 1 本では足りない**）:
//
//	① bind  Go フィールド名 → json キー。**同じ型の 2 つのタグを入れ替える取り違え**を捕まえる。
//	        キー集合は変わらないので wire.golden も ②③ も鳴らない。
//	② scan  TS 側のキー集合を**この表に固定する**。TS 側のドリフト（キーの増減）と、
//	        **その家系の実ファイルで結果が変わる**走査の壊れを捕まえる。
//	        🔴 **走査の壊れ全般を捕まえるのは②ではなく、合成標本の対照
//	        （TestTSInterfaceFieldsParser）のほうである。**実測（この PR で確認）:
//	        走査から `;` を文の区切りから外す変異／深さ判定を外す変異を当てても、
//	        **9 家系の②は緑のまま**で、赤くなったのは合成標本の対照だけだった
//	        ——**どの家系の実 TS も 1 行 1 フィールドで、壊れた枝を通らない。**
//	        #343 でレビュワーが `sessionWire` 1 家系で見つけたのと同じことが、9 家系でも成り立つ。
//	        **だから家系を増やしても合成標本は要る**（実入力は永久に易しいままかもしれない）。
//	③ match TS ↔ Go のキー集合。免除表つき、**免除の寿命は 4 方向**（両側の「揃った」「消えた」）。
//
// ⚠️ **この仕組みはモジュールの外（Console の TS）を読む。**手元で変異を当てるときは
// **`go test -count=1`** を付けること（TS だけ書き換えてもテストバイナリが変わらず
// `ok (cached)` が出る＝**変異を当てたのに緑に見える**）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// cpContractFamilies は control-plane 側の家系。
// sessionWire は #339 で入れたもの（表は contract_session_test.go に据え置き）。
func cpContractFamilies() []contractFamily {
	return []contractFamily{
		sessionContractFamily(),

		// テナントの外部 IdP 設定。Console は管理画面のフォームで読む。
		{
			name:    "tenantIdPBody",
			goType:  reflect.TypeOf(tenantIdPBody{}),
			binding: tenantIdPBinding,
			tsPath:  "../console/src/features/settings/tenant/tenantLoginTypes.ts",
			tsName:  "TenantIdP",
			tsKeys: keySet("id", "name", "label_ja", "label_en", "kind", "issuer", "client_id",
				"client_secret", "trust", "allowed_tids", "allowed_domains", "allowed_orgs",
				"link_claim", "provider_id", "tenant_slug", "status", "has_secret",
				"approved_by", "approved_at", "usable"),
			tsOnly: map[string]string{},
			goOnly: map[string]string{
				// 監査用のタイムスタンプ。画面はどれも描いていない（`approved_at` は描いている）。
				// **足すか落とすかは設計判断**なので、ここでは書き表すだけにする。
				"created_by": "【穴】監査メタ。Console の TenantIdP に宣言が無く、画面にも出ていない。",
				"created_at": "【穴】同上。",
				"updated_at": "【穴】同上。",
			},
		},

		// SSM ホスト（起動モーダルのカード）。
		{
			name:    "ssmHostDTO",
			goType:  reflect.TypeOf(ssmHostDTO{}),
			binding: ssmHostBinding,
			tsPath:  "../console/src/types/session.ts",
			tsName:  "SsmHost",
			tsKeys:  keySet("id", "alias", "profileId", "region", "instanceId", "documentName", "accountId"),
			tsOnly: map[string]string{
				// 🔴 **実害のある穴**。`StartModal.tsx:185` の ssmCardSub が `h.accountId` を読み、
				// 真なら「アカウント <id>」をカードの副題に出す。**ssmHostDTO は accountId を出さない**ので
				// この分岐は常に偽＝**この表示は一度も出たことがない。**
				// 兄弟の `ssmProfileDTO` が `accountId` を持つ（ssm.go:44）が、**別の実体**であり、
				// 同じ関数は profile の label をわざわざ `ssmProfiles.find()` で引いている。
				// **profile から引くのか host に載せるのかは設計判断。**
				"accountId": "【穴】StartModal.tsx:185,686 が h.accountId を読んでカード副題を出すが、ssmHostDTO は出さない＝この表示は出ない。兄弟の ssmProfileDTO(ssm.go:44) が持つ",
			},
			goOnly: map[string]string{
				"createdAt": "【穴】ssmHostDTO は出しているが Console の SsmHost に宣言が無い。",
			},
		},

		// SSM プロファイル（設定の書き出し／取り込みが読む）。
		{
			name:    "ssmProfileDTO",
			goType:  reflect.TypeOf(ssmProfileDTO{}),
			binding: ssmProfileBinding,
			tsPath:  "../console/src/lib/settingsBundle.ts",
			tsName:  "SsmProfileEntry",
			tsKeys:  keySet("label", "startUrl", "ssoRegion", "accountId", "roleName", "region"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				// 設定バンドルは**移送先で作り直す**ものなので、id と作成時刻を持ち込まないのは
				// 意図された設計（[[settings-export-import]] の「取り込みは足すだけ」）。
				"id":        "意図された免除: 設定バンドルは取り込み先で id を振り直すので、書き出しに含めない。",
				"createdAt": "意図された免除: 同上。取り込み時刻が正しく、書き出し元の作成時刻は持ち込まない。",
			},
		},

		// テナントの Git OAuth アプリ設定。
		{
			name:    "gitOAuthBody",
			goType:  reflect.TypeOf(gitOAuthBody{}),
			binding: gitOAuthBinding,
			tsPath:  "../console/src/features/settings/tenant/tenantGitOAuth.tsx",
			tsName:  "GitOAuthApp",
			tsKeys:  keySet("provider", "client_id", "has_secret", "needs_secret", "updated_at", "redirect_uri"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				// 🔴 **これは「穴」ではなく、Console が読んではいけないもの。**
				// 秘密は保存時だけ受け取り、読み出しでは `has_secret` の真偽しか返さない。
				// TS 型に宣言が無いのは正しい（宣言すると「読める」と誤解させる）。
				"client_secret": "意図された免除: 秘密。書き込み専用で、読み出しは has_secret の真偽だけ。Console の型に載せない。",
				"updated_by":    "【穴】監査メタ。Console の GitOAuthApp に宣言が無く、画面にも出ていない。",
			},
		},

		// テナント MCP サーバ設定。
		// 🔴 **AST 経路**——`mcpServerBody` は internal/mcpsrv の**非公開**型で reflect では届かない。
		{
			name:    "mcpServerBody",
			goPath:  "internal/mcpsrv/mcp_server.go",
			goName:  "mcpServerBody",
			binding: mcpServerBinding,
			tsPath:  "../console/src/features/settings/mcp/mcpWire.ts",
			tsName:  "TenantServer",
			tsKeys: keySet("id", "name", "label", "transport", "url", "headers", "targets",
				"kinds", "timeoutMs", "enabled", "user_secret", "created_by", "updated_at"),
			tsOnly: map[string]string{},
			goOnly: map[string]string{
				"tenant_slug": "【穴】どのテナントの設定かを返しているが、Console の TenantServer に宣言が無い。",
				"created_at":  "【穴】監査メタ。宣言が無く、画面にも出ていない（updated_at だけ出している）。",
			},
		},
	}
}

var mcpServerBinding = map[string]string{
	"ID": "id", "TenantSlug": "tenant_slug", "Name": "name", "Label": "label",
	"Transport": "transport", "URL": "url", "Headers": "headers", "Targets": "targets",
	"Kinds": "kinds", "TimeoutMS": "timeoutMs", "Enabled": "enabled",
	"UserSecret": "user_secret", "CreatedBy": "created_by", "CreatedAt": "created_at",
	"UpdatedAt": "updated_at",
}

var tenantIdPBinding = map[string]string{
	"ID": "id", "Name": "name", "LabelJA": "label_ja", "LabelEN": "label_en", "Kind": "kind",
	"Issuer": "issuer", "ClientID": "client_id", "ClientSecret": "client_secret", "Trust": "trust",
	"AllowedTIDs": "allowed_tids", "AllowedDomains": "allowed_domains", "AllowedOrgs": "allowed_orgs",
	"LinkClaim": "link_claim", "ProviderID": "provider_id", "TenantSlug": "tenant_slug",
	"Status": "status", "HasSecret": "has_secret", "ApprovedBy": "approved_by",
	"ApprovedAt": "approved_at", "CreatedBy": "created_by", "CreatedAt": "created_at",
	"UpdatedAt": "updated_at", "Usable": "usable",
}

var ssmHostBinding = map[string]string{
	"ID": "id", "Alias": "alias", "ProfileID": "profileId", "Region": "region",
	"InstanceID": "instanceId", "DocumentName": "documentName", "CreatedAt": "createdAt",
}

var ssmProfileBinding = map[string]string{
	"ID": "id", "Label": "label", "StartURL": "startUrl", "SSORegion": "ssoRegion",
	"AccountID": "accountId", "RoleName": "roleName", "Region": "region", "CreatedAt": "createdAt",
}

var gitOAuthBinding = map[string]string{
	"Provider": "provider", "ClientID": "client_id", "ClientSecret": "client_secret",
	"HasSecret": "has_secret", "NeedsSecret": "needs_secret", "UpdatedBy": "updated_by",
	"UpdatedAt": "updated_at", "RedirectURI": "redirect_uri",
}

func TestContractFamilies(t *testing.T) {
	fams := cpContractFamilies()
	// 🔴 走査の母数を見張る（#320 型）。家系が黙って消えたらここで気付く。
	if len(fams) != 6 {
		t.Fatalf("家系が %d 件しかない＝表から落ちている（足したなら本数も直すこと）", len(fams))
	}
	for _, f := range fams {
		t.Run(f.name, func(t *testing.T) { checkContractFamily(t, f) })
	}
}

// ===== 共有機構ここから（control-plane と workspace/agent で byte 一致・下の検査が見張る）=====
// contractFamily は 1 家系分の契約。
type contractFamily struct {
	name string // 家系名（エラーメッセージ用）

	// Go 側のワイヤ型。**経路は 2 つあり、選び方は機械的**（下の goStructFieldsFromSource の
	// コメント参照）: 同じパッケージから届くなら goType（reflect）、
	// 別パッケージの非公開型なら goPath + goName（go/ast）。**両方を埋めてはいけない。**
	goType  reflect.Type
	goPath  string            // reflect で届かないときだけ。宣言ファイルへの相対パス
	goName  string            // 同上。struct 名
	binding map[string]string // Go フィールド名 → json キー（①の原本）

	tsPath string          // Console の手書き型の在り処
	tsName string          // TS の interface 名
	tsKeys map[string]bool // TS 側のキー集合（②の原本）

	// 免除。🔴 **増やすときは必ず理由を書くこと。ここは「まだ直していない」を隠す場所ではない。**
	tsOnly map[string]string // TS に在って Go が出さない
	goOnly map[string]string // Go が出すが TS に無い
}

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkContractFamily は 1 家系に ①bind ②scan ③match を当てる。
//
// 🔴 **原本の取り方に意味がある**（#339 で片肺だった反省）:
//   - ③ が読む Go 側は **reflect（実際の struct）**——手書きの表を材料にすると、
//     構造体を直しても③に届かず、免除の寿命の逆検査が鳴らなくなる。
//   - ③ が読む TS 側は **実際に走査した結果**——表を材料にすると、TS を直しても③に届かない。
//   - 表（binding / tsKeys）は **①②が守るもので、③が読むものではない。**
func checkContractFamily(t *testing.T, f contractFamily) {
	t.Helper()

	// --- ① Go フィールド名 ↔ json キーの結び付き ---
	goFields := contractGoFields(t, f)
	for name, want := range f.binding {
		got, ok := goFields[name]
		if !ok {
			t.Errorf("%s に フィールド %s が無い（消えたか改名された）", f.name, name)
			continue
		}
		if got != want {
			t.Errorf("%s.%s の json タグが %q（表は %q）"+
				"——同じ型のフィールド同士でタグを入れ替えると、ワイヤのキー集合は変わらないまま値だけが入れ替わる",
				f.name, name, got, want)
		}
	}
	for name, key := range goFields {
		if _, ok := f.binding[name]; !ok {
			t.Errorf("%s.%s (json:%q) が表に無い——足したなら表にも足すこと（Console 側の型にも要るはず）", f.name, name, key)
		}
	}

	// --- ② TS 側のキー集合を表に固定する（走査が壊れたことを捕まえるのはここだけ）---
	scanned := consoleInterfaceFields(t, f.tsPath, f.tsName)
	for k := range f.tsKeys {
		if !scanned[k] {
			t.Errorf("%s: %s の %q が表に在るのに TS 側で見つからない。原因は 2 つのどちらか——"+
				"(a) キーを意図して消した → tsKeys の表と免除表も直すこと（同じ実行の下のほうに"+
				"「免除はもう要らない」が出ているはず）／(b) 走査が壊れた → 合成標本の対照"+
				"（TestTSInterfaceFieldsParser）も一緒に赤くなっているはず",
				f.name, f.tsName, k)
		}
	}
	for k := range scanned {
		if !f.tsKeys[k] {
			t.Errorf("%s: %s に %q が増えている——表にも足すこと（③の判定はここを通らない）", f.name, f.tsName, k)
		}
	}

	// --- ③ TS ↔ Go のキー集合（免除つき）---
	goKeys := map[string]bool{}
	for _, k := range goFields {
		goKeys[k] = true
	}
	var tsOnly, goOnly []string
	for k := range scanned {
		if !goKeys[k] {
			tsOnly = append(tsOnly, k)
		}
	}
	for k := range goKeys {
		if !scanned[k] {
			goOnly = append(goOnly, k)
		}
	}
	sort.Strings(tsOnly)
	sort.Strings(goOnly)
	for _, k := range tsOnly {
		if _, ok := f.tsOnly[k]; !ok {
			t.Errorf("%s: %s が %q を宣言しているが %s は出さない"+
				"——Console は常に undefined を読む（optional なので型検査は鳴らない）", f.name, f.tsName, k, f.name)
		}
	}
	for _, k := range goOnly {
		if _, ok := f.goOnly[k]; !ok {
			t.Errorf("%s: %s が %q を出すが %s に宣言が無い——Console からは型の上で見えない",
				f.name, f.name, k, f.tsName)
		}
	}

	// --- 免除の寿命（4 方向。「揃った」だけでなく「消えた」も見る）---
	for k, why := range f.tsOnly {
		if goKeys[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が出すようになった", f.name, k, why, f.name)
		}
		if !scanned[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s から消えた（両側に無いキーの免除は理由ごと嘘になる）",
				f.name, k, why, f.tsName)
		}
	}
	for k, why := range f.goOnly {
		if !goKeys[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が出さなくなった（両側に無いキーの免除は理由ごと嘘になる）",
				f.name, k, why, f.name)
		}
		if scanned[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が宣言するようになった", f.name, k, why, f.tsName)
		}
	}
}

// contractGoFields は家系の経路に従って「Go フィールド名 → json キー」を取る。
func contractGoFields(t *testing.T, f contractFamily) map[string]string {
	t.Helper()
	if (f.goType == nil) == (f.goPath == "") {
		t.Fatalf("%s: goType と goPath はどちらか一方だけを埋めること（両方 or どちらも空）", f.name)
	}
	if f.goPath != "" {
		return goStructFieldsFromSource(t, f.goPath, f.goName)
	}
	out := map[string]string{}
	for i := 0; i < f.goType.NumField(); i++ {
		fl := f.goType.Field(i)
		tag := fl.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out[fl.Name] = splitJSONName(tag)
	}
	if len(out) == 0 {
		t.Fatalf("%s から json タグを 1 つも読めなかった＝この検査が無言化している", f.goType)
	}
	return out
}

// --- 別パッケージの非公開型を読む経路（go/ast）---
//
// 🔴 **どちらの経路を使うかは機械的に決まる。迷ったら分岐条件を読むこと。**
//
//	同じパッケージから届く（package main の型・別パッケージの公開型） → reflect（goType）
//	別パッケージの**非公開**型                                        → go/ast（goPath + goName）
//
// reflect で届かない型のためだけの経路である。**届くなら必ず reflect を使う**——
// reflect は「実際の型」を見るが、AST は**ソースの見た目**しか見ないので、
// 埋め込み・型別名・生成コードのぶんだけ弱い。
//
// 🔴 **AST が追えない構文に出会ったら、浅い結果を返さずに落ちること。**
// 「今日の入力には埋め込みが 0 件だから AST と reflect は等価」という実測は
// **今日の入力に対してだけ**成立する。**次に誰かが埋め込みを足した日に黙って浅く読む**ので、
// 埋め込みを見つけたら Fatal にしてある。パスが移送で変わったときも同じ（Skip で黙らせない）。

// goStructFieldsFromSource は parseGoStructFields の薄い包み。読めなければ **Fatal**。
// （Skip で黙らせない。移送でパスが変わったら家系表の goPath を直すこと。）
func goStructFieldsFromSource(t *testing.T, path, name string) map[string]string {
	t.Helper()
	out, err := parseGoStructFields(path, name)
	if err != nil {
		t.Fatalf("%v——移送でパスが変わったなら家系表の goPath を直すこと（Skip で黙らせない）", err)
	}
	return out
}

// parseGoStructFields は <path> の `type <name> struct` を読み、
// 「Go フィールド名 → json キー」を返す。**追えない構文に出会ったら浅い結果ではなく error。**
//
// 📌 **error を返す形にしてあるのは、落ちること自体を対照で確かめられるようにするため**
// （TestGoStructFieldsFromSourceGuards）。Fatal のままだと「落ちるはず」を検査できない。
func parseGoStructFields(path, name string) (map[string]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("%s を読めない: %v", path, err)
	}
	var st *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != name {
			return true
		}
		if s, ok := ts.Type.(*ast.StructType); ok {
			st = s
		}
		return false
	})
	if st == nil {
		return nil, fmt.Errorf("%s に type %s struct が見つからない＝この検査が無言化している", path, name)
	}
	out := map[string]string{}
	for _, fl := range st.Fields.List {
		// 🔴 埋め込み（無名フィールド）。AST では中身を追えないので、
		// **浅い結果を返さずに落ちる**。reflect なら見える差なので、
		// 埋め込みが要るようになったらこの家系は reflect 経路へ移すこと。
		if len(fl.Names) == 0 {
			return nil, fmt.Errorf("%s の %s に埋め込みフィールドが在る（%s）"+
				"——AST では埋め込み先の json タグを追えない。浅く読むと「TS のみ」の見落としと"+
				"「Go のみ」の偽の赤が同時に出るので、この家系は reflect 経路へ移すこと",
				path, name, exprString(fl.Type))
		}
		if fl.Tag == nil {
			continue
		}
		tv, err := strconv.Unquote(fl.Tag.Value)
		if err != nil {
			return nil, fmt.Errorf("%s の %s: タグを読めない (%s): %v", path, name, fl.Tag.Value, err)
		}
		jt := reflect.StructTag(tv).Get("json")
		if jt == "" || jt == "-" {
			continue
		}
		key := splitJSONName(jt)
		if key == "" {
			continue
		}
		for _, id := range fl.Names {
			if id.IsExported() {
				out[id.Name] = key
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s の %s から json タグを 1 つも読めなかった＝この検査が無言化している", path, name)
	}
	return out, nil
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	}
	return "?"
}

func splitJSONName(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}

// tsProbeFixture は走査の陽性対照に使う合成標本。**実際に踏んだ／踏みかけた形だけ**を並べてある。
//
// 📌 別ファイル（testdata/*.ts）ではなく定数に畳んである理由: この 1 枚は検査と一体で、
// 分けると移送で孤児になるうえ、所有権の単位も分かれる（`console/src/types/*` とは別物）。
const tsProbeFixture = `
// ① 1 行 1 フィールド（Session が実際にこの形。ここだけ通っても意味がない）
export interface OnePerLine {
  a1: string;
  a2?: number;
  a3: boolean;
}

// ② 一部の行だけ複数キー。🔴 これが最も危ない —— 行単位の走査は b11 を落とすが、
// 総数は 10 を超えるので「フィールドが少なすぎる」Fatal に落ちず、黙って穴が開く。
export interface Mixed {
  b01: string;
  b02: string;
  b03: string;
  b04: string;
  b05: string;
  b06: string;
  b07: string;
  b08: string;
  b09: string;
  b10: string; b11?: number;
}

// ③ 入れ子の 1 行オブジェクト。🔴 行を「;」で割る直し方をすると、
// nested の中の name / display をこの型の直下のキーとして数えてしまう（測定器で実際に踏んだ）。
export interface Nested {
  n1: string;
  n2?: { name: string; display?: string }[];
  n3: boolean;
}

// ④ コメント・文字列リテラルに 「:」「;」「{」「}」が入る形（深さと文頭の判定を狂わせにくる）
export interface Tricky {
  // これはコメント: セミコロン; と波括弧 { } を含む
  t1: "a;b" | "c:{d}" | string;
  /* ブロックコメント: t9: string; ← これは拾ってはいけない */
  t2?: string;
  t3: string;
}

// ⑤ 名前が前方一致する別の型（Session と SessionContextUsage の関係）
export interface Pre {
  p1: string;
  p2: string;
  p3: string;
}

export interface PreExtra {
  x1: string;
  x2: string;
  x3: string;
}
`

// TestTSInterfaceFieldsParser は**走査そのものの陽性対照**。
//
// 🔴 この検査が要る理由: `Session` は 1 行 1 フィールドなので、**走査が壊れていても
// Session だけは通ってしまう。**案 A を他の家系へ写したとき、`a: string; b?: number;` の形が
// 1 つでもあると、**取りこぼしたキーが「TS に無い」に化けて偽の赤／穴の見落としになる。**
// 合成標本（上の tsProbeFixture）は、実際に踏んだ形だけを並べてある。
//
// 🔥 **レビュワーが #343 で実測**: 走査を壊す変異（`;` を文の区切りから外す／深さ判定を外す）を
// 当てても、①`TestSessionWireFieldBinding` と ②`TestSessionWireMatchesConsoleType` は
// **どちらも PASS のまま**だった——**実入力の `Session` が 1 行 1 フィールドなので、
// 走査が壊れても本番の突き合わせは何も言わない。**この対照だけが鳴る。
// **横展開する家系にも必ず合成標本を付けること**（実入力は易しすぎて対照にならない）。
func TestTSInterfaceFieldsParser(t *testing.T) {
	src := tsProbeFixture
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"OnePerLine", []string{"a1", "a2", "a3"}},
		// 同じ行に 2 キー。行単位の走査は b11 を落とす（総数 11 なので Fatal には落ちない）。
		{"Mixed", []string{"b01", "b02", "b03", "b04", "b05", "b06", "b07", "b08", "b09", "b10", "b11"}},
		// 入れ子の name / display を拾ってはいけない。
		{"Nested", []string{"n1", "n2", "n3"}},
		// コメント／文字列の中の `t9` を拾ってはいけない。
		{"Tricky", []string{"t1", "t2", "t3"}},
		// 前方一致する別の型を掴んではいけない（Pre が PreExtra を拾わない）。
		{"Pre", []string{"p1", "p2", "p3"}},
		{"PreExtra", []string{"x1", "x2", "x3"}},
	} {
		got, err := tsInterfaceFields(src, tc.name)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		want := map[string]bool{}
		for _, k := range tc.want {
			want[k] = true
		}
		for k := range want {
			if !got[k] {
				t.Errorf("%s: %q を落としている（走査が壊れている）", tc.name, k)
			}
		}
		for k := range got {
			if !want[k] {
				t.Errorf("%s: %q を余計に拾っている（入れ子・コメント・文字列を巻き込んでいる）", tc.name, k)
			}
		}
	}

	// TS のテンプレートリテラル型（バッククォート）でも深さを見失わないこと。
	// 上の標本は raw string なので、この 1 例だけ通常の文字列で組む。
	tmpl := "export interface Tmpl {\n  m1: `a;b{c}`;\n  m2: string;\n  m3: string;\n}\n"
	if got, err := tsInterfaceFields(tmpl, "Tmpl"); err != nil {
		t.Errorf("Tmpl: %v", err)
	} else if len(got) != 3 || !got["m1"] || !got["m2"] || !got["m3"] {
		t.Errorf("Tmpl: テンプレートリテラルで深さを見失っている: %v", got)
	}

	// 無いものを探したら Fatal 相当のエラーになること（Skip や空返しで黙らない）。
	if _, err := tsInterfaceFields(src, "NoSuchInterface"); err == nil {
		t.Error("存在しない interface でエラーにならない＝この検査が無言化しうる")
	}
}

// consoleInterfaceFields は TS の `interface <name> { ... }` の**深さ 1 の**フィールド名を返す。
func consoleInterfaceFields(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Console の型を読めない (%s): %v"+
			"——移送でパスが変わったなら consoleSessionTS を直すこと（Skip で黙らせない）", path, err)
	}
	out, err := tsInterfaceFields(string(b), name)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	// 🔴 **件数の下限は「0 件」しか見ない。これは抜けではなく、意図してこうしてある。**
	//
	// 以前は「表に固定したキー数」を下限にして Fatal していたが、**診断が誤った方向を指した**——
	// TS からキーが 1 つ消えると必ず Fatal し、文言は「走査が壊れている」。**実際の原因は
	// 「キーが意図して消された」で走査は無傷**であり、しかも Fatal が後続を止めるので
	// **「免除を外せ」という正しい指示が出なかった**（死んだ TS 宣言を消す作業がこの経路を通る）。
	//
	// 件数ガードが要らない理由: **呼び出し側の②（キー集合を表と突き合わせる）が、同じ面を
	// 「どのキーが」まで含めて見ている。**走査が痩せれば、読めなかったキーが②で名指しで赤くなり、
	// ③でも「Go のみ」として出る。**件数は情報を足していない**どころか、キーが 6〜7 個の
	// 小さい家系（SsmHost / SsmProfileEntry / GitOAuthApp）を誤って Fatal させていた。
	// **下限が無いのを見て足しに来ないこと。**
	//
	// ⚠️ 走査の壊れ全般を捕まえるのは②でも③でもなく **TestTSInterfaceFieldsParser（合成標本）**。
	// 実入力はどの家系も 1 行 1 フィールドで、壊れた枝を通らない（実測）。
	if len(out) == 0 {
		t.Fatalf("interface %s のフィールドを 1 つも読めなかった＝走査が無言化している", name)
	}
	return out
}

// tsInterfaceFields は TS の interface 本体を 1 文字ずつ辿り、**深さ 1 の**フィールド名を返す。
//
// 🔴 **行単位で「1 行 1 キー」を取ってはいけない。**`a: string; b?: number;` のように
// 同じ行にキーが並ぶ形を取りこぼす。取りこぼしても総数は 10 を超えるので上の Fatal に落ちず、
// **「TS のみ」の検出漏れ（＝穴の見落とし）と「Go のみ」の誤検出（＝偽の赤）を同時に起こす。**
//
// 🔴 **だからといって行を `;` で割るのも誤り。**入れ子の 1 行オブジェクトを巻き込む——
// 実例 `sessions?: { name: string; display?: string }[];` を `;` で割ると
// `name` / `display` を**この型の直下のキーとして数えてしまう**（測定器で実際に踏んだ）。
// **深さを見るしかない。**
func tsInterfaceFields(src, name string) (map[string]bool, error) {
	start := -1
	for _, pre := range []string{"export interface " + name, "interface " + name} {
		for i := 0; i+len(pre) <= len(src); i++ {
			if !strings.HasPrefix(src[i:], pre) {
				continue
			}
			if i > 0 && isTSIdentRune(rune(src[i-1])) {
				continue // 別の名前の末尾に一致しただけ（SessionFoo など）
			}
			// 宣言名の直後は識別子の続きであってはならない（Session と SessionContextUsage）
			if j := i + len(pre); j < len(src) && isTSIdentRune(rune(src[j])) {
				continue
			}
			if k := strings.IndexByte(src[i:], '{'); k >= 0 {
				start = i + k
			}
			break
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("interface %s が見つからない＝この検査が無言化している", name)
	}

	out := map[string]bool{}
	depth := 0
	stmt := true // 「文の頭」＝ここから始まる識別子だけがフィールド名になりうる
	for i := start; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			stmt = true
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			if k := strings.Index(src[i+2:], "*/"); k >= 0 {
				i += 2 + k + 1
			} else {
				i = len(src)
			}
			continue
		case c == '"' || c == '\'' || c == '`':
			q := c
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == q {
					break
				}
			}
			stmt = false
			continue
		case c == '{':
			depth++
			stmt = true
			continue
		case c == '}':
			depth--
			stmt = true
			if depth == 0 {
				return out, nil
			}
			continue
		case c == ';' || c == ',' || c == '\n':
			stmt = true
			continue
		case c == ' ' || c == '\t' || c == '\r':
			continue
		}
		if depth != 1 || !stmt {
			stmt = false
			continue
		}
		// 深さ 1 の文頭。識別子を読み、`?` を挟んで `:` が続けばフィールド名。
		j := i
		for j < len(src) && isTSIdentRune(rune(src[j])) {
			j++
		}
		if j > i {
			k := j
			for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
				k++
			}
			if k < len(src) && src[k] == '?' {
				k++
				for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
					k++
				}
			}
			if k < len(src) && src[k] == ':' {
				out[src[i:j]] = true
			}
		}
		if j > i {
			i = j - 1
		}
		stmt = false
	}
	return nil, fmt.Errorf("interface %s の本体が閉じていない＝走査が壊れている", name)
}

func isTSIdentRune(r rune) bool {
	return r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// TestGoStructFieldsFromSourceGuards は **AST 経路そのものの陽性対照**。
//
// 🔴 この経路は「今日の入力に埋め込みが 0 件」という実測に乗っている。
// **次に誰かが埋め込みを足した日に黙って浅く読む**のが最も怖い壊れ方なので、
// 合成標本で「落ちること」を固定する。（TS 走査に合成標本を付けたのと同じ理由——
// 実入力が易しいままだと、壊れても本番の突き合わせは何も言わない。）
func TestGoStructFieldsFromSourceGuards(t *testing.T) {
	dir := t.TempDir()
	write := func(base, body string) string {
		p := filepath.Join(dir, base)
		if err := os.WriteFile(p, []byte("package x\n\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 素の形は正しく読めること（この対照が空を測っていないことの確認）。
	ok := write("ok.go", "type T struct {\n\tA string `json:\"a\"`\n\tB int    `json:\"b,omitempty\"`\n\tC string `json:\"-\"`\n\tD string\n}\n")
	got, err := parseGoStructFields(ok, "T")
	if err != nil {
		t.Fatalf("素の struct を読めない: %v", err)
	}
	if len(got) != 2 || got["A"] != "a" || got["B"] != "b" {
		t.Fatalf("素の struct の読み取りが違う: %v（json:\"-\" とタグ無しは落とす）", got)
	}

	// 🔴 埋め込み（無名フィールド）→ 浅い結果ではなく error。
	emb := write("emb.go", "type Base struct {\n\tX string `json:\"x\"`\n}\n\ntype T struct {\n\tBase\n\tA string `json:\"a\"`\n}\n")
	if _, err := parseGoStructFields(emb, "T"); err == nil {
		t.Error("埋め込みフィールドが在るのに error にならない" +
			"——AST は埋め込み先の json タグを追えないので、浅く読むと穴の見落としと偽の赤が同時に出る")
	}

	// 型が無い → error（無言化しない）。
	if _, err := parseGoStructFields(ok, "NoSuchType"); err == nil {
		t.Error("存在しない型で error にならない＝この経路が無言化しうる")
	}

	// パスが無い → error（移送でパスが変わった場合）。
	if _, err := parseGoStructFields(filepath.Join(dir, "nope.go"), "T"); err == nil {
		t.Error("存在しないパスで error にならない＝移送のパス変更を黙って通す")
	}

	// json タグが 1 つも無い → error（「0 件」を結果として採らない）。
	none := write("none.go", "type T struct {\n\tA string\n\tB int\n}\n")
	if _, err := parseGoStructFields(none, "T"); err == nil {
		t.Error("json タグ 0 件で error にならない＝この経路が無言化しうる")
	}
}

// ===== 共有機構ここまで =====

// TestSharedContractMachineryIsIdentical は、**両モジュールの共有機構が byte 一致**であることを見る。
//
// Go はモジュールを跨いでテストヘルパを共有できないので、この約 380 行は
// control-plane と workspace/agent に**同じものが 2 つ在る**（`wire_golden_test.go` /
// `routes_golden_test.go` と同じ事情）。🔴 **割れても両モジュールのテストは独立に緑のまま通る**
// ——片方だけ直した日に、もう片方は古い走査で契約を測り続ける。
// #335 参考 3（RECLAIM-D の「割れたら赤くする byte 比較」）と同じ形をここにも置く。
//
// 📌 **番兵で囲んだ区間まるごとを比べる**のがこの検査の肝。ブロック名で拾い集める方式だと、
// **拾えなかったブロックを黙って 0 件として比べて緑になる**（＝痩せた非 0 件）。
// 区間が見つからなければ Fatal にしてある。
func TestSharedContractMachineryIsIdentical(t *testing.T) {
	mine := sharedContractRegion(t, "contract_wire_test.go")
	theirs := sharedContractRegion(t, "../workspace/agent/contract_wire_test.go")
	if mine == theirs {
		return
	}
	// どこで割れたかを 1 行目で示す（全文 diff を貼らない）。
	ml, tl := strings.Split(mine, "\n"), strings.Split(theirs, "\n")
	for i := 0; i < len(ml) || i < len(tl); i++ {
		var a, b string
		if i < len(ml) {
			a = ml[i]
		}
		if i < len(tl) {
			b = tl[i]
		}
		if a != b {
			t.Fatalf("共有機構が割れている（区間 %d 行目）:\n  control-plane: %q\n  workspace/agent: %q\n"+
				"——片方だけ直すと、もう片方は古い走査で契約を測り続ける。両方に同じ変更を入れること", i+1, a, b)
		}
	}
	t.Fatalf("共有機構の長さが違う（control-plane %d 行 / workspace/agent %d 行）", len(ml), len(tl))
}

const (
	sharedRegionStart = "// ===== 共有機構ここから"
	sharedRegionEnd   = "// ===== 共有機構ここまで ====="
)

// sharedContractRegion は番兵に挟まれた区間を返す。見つからなければ **Fatal**（Skip で黙らせない）。
func sharedContractRegion(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s を読めない: %v——移送でパスが変わったならこの検査を直すこと（Skip で黙らせない）", path, err)
	}
	got, err := extractSharedRegion(string(b))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return got
}

// extractSharedRegion は番兵に挟まれた区間を返す。**error を返すのは、
// 「区間が見つからないときに落ちること」自体を対照で確かめられるようにするため。**
func extractSharedRegion(src string) (string, error) {
	i := strings.Index(src, sharedRegionStart)
	if i < 0 {
		return "", fmt.Errorf("開始の番兵 %q が無い＝この検査が無言化している", sharedRegionStart)
	}
	i = strings.Index(src[i:], "\n")
	if i < 0 {
		return "", fmt.Errorf("開始の番兵の行が閉じていない")
	}
	rest := src[strings.Index(src, sharedRegionStart)+i+1:]
	j := strings.Index(rest, sharedRegionEnd)
	if j < 0 {
		return "", fmt.Errorf("終了の番兵 %q が無い＝この検査が無言化している", sharedRegionEnd)
	}
	out := rest[:j]
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("番兵の間が空＝区間の取り出しが壊れている")
	}
	return out, nil
}

// TestSharedContractRegionExtractor は **比較器そのものの陽性対照**。
//
// 🔴 「いま byte 一致だから緑」は、**比較器が動いていなくても同じ顔で出る。**
// 片方を 1 文字変えた合成入力で、実際に差が出ることを見てから緑を採る。
func TestSharedContractRegionExtractor(t *testing.T) {
	mk := func(body string) string {
		return "package x\n\n" + sharedRegionStart + "（説明）=====\n" + body + sharedRegionEnd + "\nfunc after() {}\n"
	}
	a, err := extractSharedRegion(mk("func f() {}\n"))
	if err != nil {
		t.Fatalf("素の入力から区間を取り出せない: %v", err)
	}
	if a != "func f() {}\n" {
		t.Fatalf("区間の取り出しが違う: %q（番兵の行と外側を含めてはいけない）", a)
	}
	// 🔴 1 文字違えば差が出ること。
	b, err := extractSharedRegion(mk("func g() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("中身を変えても区間が同じに見える＝比較器が中身を見ていない")
	}
	// 番兵が欠けたら error（Skip や空返しで黙らない）。
	if _, err := extractSharedRegion("package x\n\nfunc f() {}\n"); err == nil {
		t.Error("番兵が無いのに error にならない＝この検査が無言化しうる")
	}
	if _, err := extractSharedRegion("package x\n\n" + sharedRegionStart + "=====\nfunc f() {}\n"); err == nil {
		t.Error("終了の番兵が無いのに error にならない")
	}
	// 区間が空なら error（「痩せた非 0 件」の手前で止める）。
	if _, err := extractSharedRegion(mk("\n")); err == nil {
		t.Error("区間が空なのに error にならない")
	}
}
