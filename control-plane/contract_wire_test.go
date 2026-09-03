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
	"reflect"
	"sort"
	"testing"
)

// contractFamily は 1 家系分の契約。
type contractFamily struct {
	name string // 家系名（エラーメッセージ用）

	goType  reflect.Type      // Go 側のワイヤ型
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
	}
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
	goFields := map[string]string{}
	for i := 0; i < f.goType.NumField(); i++ {
		fl := f.goType.Field(i)
		tag := fl.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		goFields[fl.Name] = splitJSONName(tag)
	}
	if len(goFields) == 0 {
		t.Fatalf("%s から json タグを 1 つも読めなかった＝この検査が無言化している", f.goType)
	}
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
	scanned := consoleInterfaceFields(t, f.tsPath, f.tsName, len(f.tsKeys))
	for k := range f.tsKeys {
		if !scanned[k] {
			t.Errorf("%s: %s の %q を走査が拾えていない"+
				"——TS の書き方が変わったか、走査が壊れている（走査の壊れ全般は合成標本の対照が見る）",
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
				"——Console は常に undefined を読む（optional なので型検査は鳴らない）", f.name, f.tsName, k, f.goType)
		}
	}
	for _, k := range goOnly {
		if _, ok := f.goOnly[k]; !ok {
			t.Errorf("%s: %s が %q を出すが %s に宣言が無い——Console からは型の上で見えない",
				f.name, f.goType, k, f.tsName)
		}
	}

	// --- 免除の寿命（4 方向。「揃った」だけでなく「消えた」も見る）---
	for k, why := range f.tsOnly {
		if goKeys[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が出すようになった", f.name, k, why, f.goType)
		}
		if !scanned[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s から消えた（両側に無いキーの免除は理由ごと嘘になる）",
				f.name, k, why, f.tsName)
		}
	}
	for k, why := range f.goOnly {
		if !goKeys[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が出さなくなった（両側に無いキーの免除は理由ごと嘘になる）",
				f.name, k, why, f.goType)
		}
		if scanned[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が宣言するようになった", f.name, k, why, f.tsName)
		}
	}
}

func splitJSONName(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}

func TestContractFamilies(t *testing.T) {
	fams := cpContractFamilies()
	// 🔴 走査の母数を見張る（#320 型）。家系が黙って消えたらここで気付く。
	if len(fams) != 5 {
		t.Fatalf("家系が %d 件しかない＝表から落ちている（足したなら本数も直すこと）", len(fams))
	}
	for _, f := range fams {
		t.Run(f.name, func(t *testing.T) { checkContractFamily(t, f) })
	}
}
