package main

// contract_wire_test.go — table-driven contract checks across the families whose wire
// format is already confirmed. Not one byte of the wire changes here; this only writes
// down what the wire already is.
//
// How a family qualifies (settled by the `wirescan` measurement): the Console's
// hand-written TS type and the Go struct have a key-set Jaccard of at least 0.7 and below
// 1.0 (they correspond, yet have drifted), and that Go type is confirmed to actually be
// written out as JSON.
//
// Three checks, and no single one of them is enough:
//
//	① bind  Go field name → json key. Catches two tags of the same type being swapped:
//	        the key set does not change, so neither wire.golden nor ②③ says anything.
//	② scan  Pins the TS-side key set to this table. Catches TS drift (keys added or
//	        removed) and a scanner breakage whose result changes for that family's real
//	        file. What catches scanner breakage in general is not ② but the synthetic
//	        fixture control (TestTSInterfaceFieldsParser). Measured: mutating the scanner
//	        (dropping `;` as a statement separator, dropping the depth test) left ② green
//	        for all 9 families and reddened only the synthetic control — every family's
//	        real TS is one field per line and never reaches the broken branch. So a family
//	        added here still needs its synthetic fixture: real input may stay easy forever.
//	③ match TS ↔ Go key sets, with an exemption table whose lifetime is checked in four
//	        directions ("now aligned" and "now gone", on both sides).
//
// This machinery reads outside the module (the Console's TS), so pass `go test -count=1`
// when mutating it by hand: editing only TS leaves the test binary unchanged and a cached
// `ok` makes the mutation look green.

import (
	"encoding/json"
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

// cpContractFamilies returns the control-plane families. The sessionWire table stays in
// contract_session_test.go.
func cpContractFamilies() []contractFamily {
	return []contractFamily{
		sessionContractFamily(),

		// Tenant external IdP settings; the Console reads them in the admin form.
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
				// Audit timestamps; the screen draws none of them (`approved_at` it does
				// draw). Adding or dropping them is a design decision, so this only
				// writes down the current state.
				"created_by": "【穴】監査メタ。Console の TenantIdP に宣言が無く、画面にも出ていない。",
				"created_at": "【穴】同上。",
				"updated_at": "【穴】同上。",
			},
		},

		// SSM hosts (the cards in the start modal).
		{
			name:    "ssmHostDTO",
			goType:  reflect.TypeOf(ssmHostDTO{}),
			binding: ssmHostBinding,
			tsPath:  "../console/src/types/session.ts",
			tsName:  "SsmHost",
			tsKeys:  keySet("id", "alias", "profileId", "region", "instanceId", "documentName"),
			// `accountId` is deliberately absent from SsmHost: an account id is an
			// attribute of the profile, not of the host, so the Console reads it from
			// `ssmProfileDTO` (ssmAcctLabel in StartModal.tsx). Declaring it here again
			// would make the Console read undefined forever without the type check ever
			// complaining — which is exactly the bug this family caught.
			tsOnly: map[string]string{},
			goOnly: map[string]string{
				"createdAt": "【穴】ssmHostDTO は出しているが Console の SsmHost に宣言が無い。",
			},
		},

		// SSM profiles (read by settings export / import).
		{
			name:    "ssmProfileDTO",
			goType:  reflect.TypeOf(ssmProfileDTO{}),
			binding: ssmProfileBinding,
			tsPath:  "../console/src/lib/settingsBundle.ts",
			tsName:  "SsmProfileEntry",
			tsKeys:  keySet("label", "startUrl", "ssoRegion", "accountId", "roleName", "region"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				// A settings bundle is rebuilt at its destination, so leaving out the id
				// and the creation time is the intended design ([[settings-export-import]]:
				// import only ever adds).
				"id":        "意図された免除: 設定バンドルは取り込み先で id を振り直すので、書き出しに含めない。",
				"createdAt": "意図された免除: 同上。取り込み時刻が正しく、書き出し元の作成時刻は持ち込まない。",
			},
		},

		// Tenant Git OAuth app settings.
		{
			name:    "gitOAuthBody",
			goType:  reflect.TypeOf(gitOAuthBody{}),
			binding: gitOAuthBinding,
			tsPath:  "../console/src/features/settings/tenant/tenantGitOAuth.tsx",
			tsName:  "GitOAuthApp",
			tsKeys:  keySet("provider", "client_id", "has_secret", "needs_secret", "updated_at", "redirect_uri"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				// Not a gap: the Console must not read this. The secret is accepted on
				// write only, and a read returns nothing but the `has_secret` boolean.
				// Declaring it in the TS type would suggest it is readable.
				"client_secret": "意図された免除: 秘密。書き込み専用で、読み出しは has_secret の真偽だけ。Console の型に載せない。",
				"updated_by":    "【穴】監査メタ。Console の GitOAuthApp に宣言が無く、画面にも出ていない。",
			},
		},

		// Tenant MCP server settings. AST route: `mcpServerBody` is an unexported type in
		// internal/mcpsrv, so reflect cannot reach it.
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

		// One cell of the uptime heatmap (GET /api/usage/me/hourly and friends). The only
		// family whose Go side embeds: usageHourPoint embeds store.UsageHourCounters, so
		// 7 of its 8 keys arrive by promotion. A scanner naively written as "skip when the
		// json tag is empty" loses all 7 and turns them into 7 "TS only" findings (see the
		// reflectJSONFields comment and its control).
		{
			name:    "usageHourPoint",
			goType:  reflect.TypeOf(usageHourPoint{}),
			binding: usageHourPointBinding,
			tsPath:  "../console/src/features/usage/uptime.ts",
			tsName:  "UptimePoint",
			tsKeys: keySet("hour", "samples", "running_secs", "measured_secs",
				"session_secs", "busy_secs", "max_sessions", "max_busy"),
			tsOnly: map[string]string{},
			goOnly: map[string]string{},
		},
	}
}

var usageHourPointBinding = map[string]string{
	// Hour is declared on the outer struct; the other seven are promoted from
	// store.UsageHourCounters.
	"Hour": "hour", "Samples": "samples", "RunningSecs": "running_secs",
	"MeasuredSecs": "measured_secs", "SessionSecs": "session_secs", "BusySecs": "busy_secs",
	"MaxSessions": "max_sessions", "MaxBusy": "max_busy",
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
	// Watch the population: a family silently dropped from the table is noticed here.
	if len(fams) != 7 {
		t.Fatalf("家系が %d 件しかない＝表から落ちている（足したなら本数も直すこと）", len(fams))
	}
	for _, f := range fams {
		t.Run(f.name, func(t *testing.T) { checkContractFamily(t, f) })
	}
}

// ===== shared machinery starts here (byte-identical in both modules) =====
// Anything shared by both modules must live inside this region. What the check below
// protects is the region, not the file: a shared helper added outside it can exist on only
// one side and still pass green (measured). Since there is a place that is not looked at,
// the text has to tell the reader which place is.
// contractFamily is the contract for one family.
type contractFamily struct {
	name string // family name, for error messages

	// The Go-side wire type. There are two routes and the choice is mechanical (see the
	// goStructFieldsFromSource comment): goType (reflect) when the type is reachable from
	// this package, goPath + goName (go/ast) for an unexported type in another package.
	// Never fill in both.
	goType  reflect.Type
	goPath  string            // only when reflect cannot reach it: path to the declaring file
	goName  string            // ditto; struct name
	binding map[string]string // Go field name → json key (the source for ①)

	tsPath string          // where the Console's hand-written type lives
	tsName string          // TS interface name
	tsKeys map[string]bool // TS-side key set (the source for ②)

	// Exemptions. Always write down the reason when adding one: this is not a place to
	// hide "not fixed yet".
	tsOnly map[string]string // declared in TS, not emitted by Go
	goOnly map[string]string // emitted by Go, not declared in TS
}

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkContractFamily applies ①bind ②scan ③match to one family.
//
// Where each check takes its source from matters:
//   - The Go side ③ reads is reflect (the actual struct). Feed it the hand-written table
//     instead and a fix to the struct never reaches ③, so the reverse check on an
//     exemption's lifetime stops firing.
//   - The TS side ③ reads is the actual scan result; feed it the table instead and a fix
//     to the TS never reaches ③.
//   - The tables (binding / tsKeys) are what ①② protect, not what ③ reads.
func checkContractFamily(t *testing.T, f contractFamily) {
	t.Helper()

	// --- ① the binding between Go field name and json key ---
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

	// --- ② pin the TS-side key set to the table ---
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

	// --- ③ TS ↔ Go key sets, with exemptions ---
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

	// --- exemption lifetime: four directions, "now aligned" and also "now gone" ---
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

// contractGoFields collects "Go field name → json key" along the family's route.
func contractGoFields(t *testing.T, f contractFamily) map[string]string {
	t.Helper()
	if (f.goType == nil) == (f.goPath == "") {
		t.Fatalf("%s: goType と goPath はどちらか一方だけを埋めること（両方 or どちらも空）", f.name)
	}
	if f.goPath != "" {
		return goStructFieldsFromSource(t, f.goPath, f.goName)
	}
	out, err := reflectJSONFields(f.goType, 0)
	if err != nil {
		t.Fatalf("%s: %v", f.name, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s から json タグを 1 つも読めなかった＝この検査が無言化している", f.goType)
	}
	return out
}

// reflectJSONFields returns a struct's "Go field name → json key".
//
// Embedded (anonymous) fields are promoted, exactly as encoding/json promotes them.
// Written naively as "skip when `Tag.Get("json")` is empty", the untagged embedded field
// is skipped along with every key it promotes: `usageHourPoint`
// (control-plane/usage_hourly.go:55) embeds `store.UsageHourCounters`, and skipping it
// drops 7 of its 8 keys, which then surface as 7 "TS only" findings. Reading shallowly and
// producing a false red is the worst way this side can break.
//
// A shape that cannot be followed fails instead of returning a shallow result (the same
// discipline as the AST route): embedding two or more levels deep, and a promotion whose
// keys collide, are errors. json's promotion rules are more intricate than that
// (depth-first, a same-depth collision drops both), so nothing passes on an approximation
// — everything unexpected fails.
func reflectJSONFields(rt reflect.Type, depth int) (map[string]string, error) {
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%s は struct ではない", rt)
	}
	out := map[string]string{}
	for i := 0; i < rt.NumField(); i++ {
		fl := rt.Field(i)
		tag := fl.Tag.Get("json")
		if fl.Anonymous && tag == "" {
			// An untagged embedded field is a promotion. Only one level is allowed.
			et := fl.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() != reflect.Struct {
				// json emits an exported non-struct embedded field under its type name
				// (embedding `MyDur int64` yields `{"MyDur":0}`), so skipping it would
				// lose a key. An unexported one json does not emit, but deciding
				// emitted-or-not from the exportedness of a type name is not this
				// scanner's job, so both fail here.
				if fl.IsExported() {
					return nil, fmt.Errorf("%s に公開された非 struct の埋め込みが在る（%s %s）"+
						"——json は型名をキーにして出すが、この走査は追わない。"+
						"この家系は埋め込みを解いた型を指すこと", rt, fl.Name, et)
				}
				continue
			}
			if depth > 0 {
				return nil, fmt.Errorf("%s に 2 段以上の埋め込みが在る（%s）"+
					"——json の昇格規則は深さ優先で衝突の扱いも複雑なので、近似で通さない。"+
					"この家系は埋め込みを解いた型を指すか、経路を作り直すこと", rt, et)
			}
			sub, err := reflectJSONFields(et, depth+1)
			if err != nil {
				return nil, err
			}
			for k, v := range sub {
				if err := putJSONField(out, rt, k, v); err != nil {
					return nil, err
				}
			}
			continue
		}
		if tag == "-" || (tag == "" && !fl.IsExported()) {
			continue // json:"-" and unexported fields never reach the wire
		}
		// json emits an exported field with no tag under its Go field name; "skip when the
		// tag is empty" loses it (measured by differential test).
		name := splitJSONName(tag)
		if name == "" {
			name = fl.Name
		}
		if err := putJSONField(out, rt, fl.Name, name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- Route for reading an unexported type in another package (go/ast) ---
//
// Which route to use is decided mechanically; when in doubt, read the branch condition:
//
//	from this package (package main, or an exported type) → reflect (goType)
//	an unexported type in another package                 → go/ast (goPath + goName)
//
// This route exists only for types reflect cannot reach; always use reflect when it can.
// reflect sees the actual type, the AST sees only what the source looks like, so it is
// weaker by exactly the embedding, type aliases and generated code it cannot follow.
//
// Syntax the AST cannot follow fails rather than returning a shallow result. The
// measurement "today's input has zero embedded fields, so AST and reflect are equivalent"
// holds for today's input only, and the day someone adds an embedded field it would
// silently read shallow — hence Fatal on an embedded field, and likewise when a move
// changes the path (never silence it with Skip).

// goStructFieldsFromSource is a thin wrapper over parseGoStructFields; Fatal when it
// cannot read. Never Skip — when a move changes the path, fix goPath in the family table.
func goStructFieldsFromSource(t *testing.T, path, name string) map[string]string {
	t.Helper()
	out, err := parseGoStructFields(path, name)
	if err != nil {
		t.Fatalf("%v——移送でパスが変わったなら家系表の goPath を直すこと（Skip で黙らせない）", err)
	}
	return out
}

// parseGoStructFields reads `type <name> struct` in <path> and returns
// "Go field name → json key". Syntax it cannot follow is an error, not a shallow result.
//
// It returns an error rather than calling Fatal so that the failing itself can be checked
// by a control (TestGoStructFieldsFromSourceGuards); with Fatal, "it should fail" is not
// testable.
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
		// An embedded (anonymous) field: the AST cannot follow its contents, so fail
		// instead of returning a shallow result. reflect can see the difference, so move
		// the family to the reflect route once it needs embedding.
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

// tsProbeFixture is the synthetic fixture used as the scanner's positive control. It holds
// only shapes that were actually hit, or nearly hit.
//
// It is folded into a constant rather than a separate file (testdata/*.ts) because this one
// page is of a piece with the check: split off, it is orphaned by a move, and its ownership
// unit differs from `console/src/types/*`.
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

// ⑤-a type 別名（interface と同じだけ普通に使われる。UptimePoint が実際にこの形）
export type AliasShape = {
  al1: string;
  al2?: number;
  al3: boolean;
};

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

// TestTSInterfaceFieldsParser is the positive control for the scanner itself.
//
// Why it is needed: `Session` is one field per line, so Session alone passes even with a
// broken scanner. Carried over to other families, a single `a: string; b?: number;` turns a
// missed key into "absent from TS" — a false red, or a real gap gone unnoticed. The
// synthetic fixture above holds only shapes that were actually hit.
//
// Measured: with the scanner mutated (dropping `;` as a statement separator, dropping the
// depth test), both ①`TestSessionWireFieldBinding` and
// ②`TestSessionWireMatchesConsoleType` stayed PASS — the real `Session` is one field per
// line, so a broken scanner leaves the production comparison silent and only this control
// fires. Every family added here needs its own synthetic fixture: real input is too easy
// to serve as a control.
func TestTSInterfaceFieldsParser(t *testing.T) {
	src := tsProbeFixture
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"OnePerLine", []string{"a1", "a2", "a3"}},
		// Two keys on one line: a line-based scan drops b11, and with 11 in total it
		// does not trip the Fatal.
		{"Mixed", []string{"b01", "b02", "b03", "b04", "b05", "b06", "b07", "b08", "b09", "b10", "b11"}},
		// The nested name / display must not be picked up.
		{"Nested", []string{"n1", "n2", "n3"}},
		// A `t9` inside a comment or a string must not be picked up.
		{"Tricky", []string{"t1", "t2", "t3"}},
		// A different type with a matching prefix must not be grabbed (Pre must not
		// pick up PreExtra). A type alias must read like an interface; otherwise the
		// whole family Fatals.
		{"AliasShape", []string{"al1", "al2", "al3"}},
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

	// A TS template literal type (backticks) must not make the depth count go astray.
	// The fixture above is a raw string, so this one case is built as an ordinary string.
	tmpl := "export interface Tmpl {\n  m1: `a;b{c}`;\n  m2: string;\n  m3: string;\n}\n"
	if got, err := tsInterfaceFields(tmpl, "Tmpl"); err != nil {
		t.Errorf("Tmpl: %v", err)
	} else if len(got) != 3 || !got["m1"] || !got["m2"] || !got["m3"] {
		t.Errorf("Tmpl: テンプレートリテラルで深さを見失っている: %v", got)
	}

	// Looking for something absent must be an error — no Skip, no silent empty result.
	if _, err := tsInterfaceFields(src, "NoSuchInterface"); err == nil {
		t.Error("存在しない interface でエラーにならない＝この検査が無言化しうる")
	}
}

// consoleInterfaceFields returns the depth-1 field names of the TS
// `interface <name> { ... }`.
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
	// The only lower bound on the count is "zero". That is deliberate, not an omission.
	//
	// A bound of "the number of keys pinned in the table" pointed the diagnosis the wrong
	// way: removing one key from TS always Fataled with the wording "the scanner is
	// broken", when the real cause was a key removed on purpose and the scanner was
	// intact — and since the Fatal stopped everything after it, the correct instruction
	// ("drop the exemption") never appeared. Deleting a dead TS declaration goes through
	// this path.
	//
	// The count guard is unnecessary because the caller's ② (matching the key set against
	// the table) covers the same surface down to which key it is: if the scan goes thin,
	// the unreadable key reddens by name in ②, and shows up as "Go only" in ③. The count
	// adds no information, and it wrongly Fataled the small families of 6-7 keys (SsmHost
	// / SsmProfileEntry / GitOAuthApp). Do not add a lower bound back on seeing none here.
	//
	// What catches scanner breakage in general is neither ② nor ③ but
	// TestTSInterfaceFieldsParser (the synthetic fixture): every family's real input is one
	// field per line and never reaches the broken branch (measured).
	if len(out) == 0 {
		t.Fatalf("interface %s のフィールドを 1 つも読めなかった＝走査が無言化している", name)
	}
	return out
}

// tsInterfaceFields walks a TS interface body one character at a time and returns the
// depth-1 field names.
//
// Never take "one key per line" line by line: a shape like `a: string; b?: number;` puts
// several keys on one line and is missed, and since the total still exceeds 10 it does not
// trip the Fatal above — it produces a missed "TS only" (a gap gone unnoticed) and a false
// "Go only" (a false red) at the same time.
//
// Splitting the line on `;` is wrong too: it pulls in a one-line nested object. Splitting
// `sessions?: { name: string; display?: string }[];` on `;` counts `name` / `display` as
// keys directly under this type (hit for real). Depth is the only way.
func tsInterfaceFields(src, name string) (map[string]bool, error) {
	start := -1
	// Both `interface X { … }` and `type X = { … }` are considered. Without the `type`
	// alias, that family alone Fatals with "not found": `UptimePoint`
	// (console/src/features/usage/uptime.ts:11) is `export type … = { … }`. Both spellings
	// are equally ordinary in TS, so a scanner that sees only one cannot choose families.
	for _, pre := range []string{
		"export interface " + name, "interface " + name,
		"export type " + name, "type " + name,
	} {
		for i := 0; i+len(pre) <= len(src); i++ {
			if !strings.HasPrefix(src[i:], pre) {
				continue
			}
			if i > 0 && isTSIdentRune(rune(src[i-1])) {
				continue // only matched the tail of another name (SessionFoo and the like)
			}
			// The declared name must not be followed by more identifier runes
			// (Session vs SessionContextUsage).
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
		return nil, fmt.Errorf("interface / type %s が見つからない＝この検査が無言化している", name)
	}

	out := map[string]bool{}
	depth := 0
	stmt := true // at a statement head: only an identifier starting here can be a field name
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
		// Statement head at depth 1: read an identifier; with an optional `?` before a
		// `:`, it is a field name.
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

// putJSONField adds one entry. Duplicates are judged by json name, not by Go field name:
// `encoding/json`'s collision rule is decided on the json name, so two different Go names
// sharing one json name are both dropped by json (at the same depth). Judging by Go name
// gives the worst direction of disagreement — "json does not emit it but we do" (measured
// by differential test against json.Marshal).
func putJSONField(out map[string]string, rt reflect.Type, field, jsonName string) error {
	for f, j := range out {
		if j == jsonName {
			return fmt.Errorf("%s: json 名 %q が %s と %s で重なる"+
				"——encoding/json は同深さの衝突でどちらも出さない。近似で通さない", rt, jsonName, f, field)
		}
	}
	if _, dup := out[field]; dup {
		return fmt.Errorf("%s: フィールド名 %s が重複している", rt, field)
	}
	out[field] = jsonName
	return nil
}

// TestReflectJSONFieldsMatchesEncodingJSON differential-tests against the implementation of
// the spec itself.
//
// The goal of `reflectJSONFields` is to match `encoding/json`'s promotion rules, and that
// goal is executable: running a synthetic type through both and comparing the output is
// faster and stronger than reading the rules on paper. Three disagreements were found this
// way.
//
// Only two expectations can be written: the same key set as json, or an error (erring on
// the safe side). "json does not emit it but we do" is not allowed — that puts a key which
// does not exist into the contract, and grows a false gap in the exemption table.
func TestReflectJSONFieldsMatchesEncodingJSON(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	type innerDup struct {
		P string `json:"p"`
	}
	type innerDup2 struct {
		P2 string `json:"p"` // different Go name, same json name as innerDup
	}
	type deep2 struct{ inner }
	type MyDur int64

	for _, tc := range []struct {
		name    string
		v       any
		wantErr bool // true = a shape we cannot follow, so err out (narrower than json is fine)
	}{
		{"① 素の 1 段埋め込み", struct {
			Hour string `json:"hour"`
			inner
		}{}, false},
		{"② タグ付き埋め込み（入れ子になる）", struct {
			Hour  string `json:"hour"`
			Inner inner  `json:"inner"`
		}{}, false},
		// ③ is a json-name collision within one struct. Written in source it is rejected
		// by `go vet` (structtag), so it is built and passed at run time.
		//
		// Division of labour with vet, measured:
		//   vet sees … ③ within one struct, and ⑤ between embedded fields (it checks
		//   duplicates among promoted tags too).
		//   vet does not see … ④ an outer field against an embedded one, ⑥ two-level
		//   embedding, ⑨ an exported non-struct embedded field, ⑩ an exported field with
		//   no tag.
		// So the shapes only this scanner can protect are ④⑥⑨⑩.
		{"③ 別の Go 名・同じ json 名（実行時に組む）", reflect.New(reflect.StructOf([]reflect.StructField{
			{Name: "X", Type: reflect.TypeOf(""), Tag: `json:"a"`},
			{Name: "Y", Type: reflect.TypeOf(""), Tag: `json:"a"`},
		})).Elem().Interface(), true},
		{"④ 同じ json 名が外側と昇格でぶつかる", struct {
			A string `json:"a"`
			inner
		}{}, true},
		// ⑤-a is the control for the non-colliding side: one embedded field, and json:"-"
		// is not emitted. The label is kept consistent with the body — in a template, a
		// label that disagrees with its body propagates as far as an error in the
		// implementation does.
		{"⑤-a 埋め込み 1 つ・衝突なし（json:\"-\" は出ない）", struct {
			innerDup
			Other struct{} `json:"-"`
		}{}, false},
		// ⑤-b is the real "two embedded fields at the same depth with the same json name".
		// encoding/json emits neither, so the scanner errs out. The implementation was
		// already right, but no permanent control covered this shape, so a regression from
		// touching `putJSONField`'s condition or the scan order would have gone unnoticed.
		// Like ③, written in source it is rejected by `go vet` (structtag), so it is built
		// at run time.
		{"⑤-b 同深さの 2 つの埋め込みが同じ json 名（実行時に組む）",
			reflect.New(reflect.StructOf([]reflect.StructField{
				{Name: "InnerDupA", Type: reflect.TypeOf(innerDup{}), Anonymous: true},
				{Name: "InnerDupB", Type: reflect.TypeOf(innerDup2{}), Anonymous: true},
			})).Elem().Interface(), true},
		{"⑥ 2 段の埋め込み", struct {
			Z string `json:"z"`
			deep2
		}{}, true},
		// A nil pointer embed makes json omit the fields, so compare with a non-nil value.
		// That is a property of how the control is built, not a difference in the rules.
		{"⑦ ポインタ埋め込み（非 nil）", struct {
			Hour string `json:"hour"`
			*inner
		}{Hour: "h", inner: &inner{}}, false},
		{"⑨ 公開された非 struct の埋め込み", struct {
			MyDur
			C string `json:"c"`
		}{}, true},
		{"⑩ タグ無しの公開フィールド（json は Go 名で出す）", struct {
			Plain string
			C     string `json:"c"`
		}{}, false},
	} {
		got, err := reflectJSONFields(reflect.TypeOf(tc.v), 0)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: error になるはずが %v を返した（json との食い違いを安全側に倒せていない）", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 追えるはずの形で error: %v", tc.name, err)
			continue
		}
		// Compare against the key set json.Marshal actually emits.
		b, mErr := json.Marshal(tc.v)
		if mErr != nil {
			t.Errorf("%s: marshal: %v", tc.name, mErr)
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Errorf("%s: unmarshal: %v", tc.name, err)
			continue
		}
		mine := map[string]bool{}
		for _, jn := range got {
			mine[jn] = true
		}
		for k := range raw {
			if !mine[k] {
				t.Errorf("%s: json は %q を出すのに走査が落としている（取りこぼし）", tc.name, k)
			}
		}
		for k := range mine {
			if _, ok := raw[k]; !ok {
				t.Errorf("%s: 走査が %q を出すのに json は出さない"+
					"——契約に実在しないキーが載り、免除表に偽の穴が生える", tc.name, k)
			}
		}
	}
}

// TestReflectJSONFieldsEmbedding is the positive control for how the reflect route handles
// embedding.
//
// Written naively as "skip when the json tag is empty", the untagged embedded field is
// skipped along with every key it promotes. The real input (usageHourPoint) embeds one
// level, so a broken promotion only shows up as 7 "TS only" findings and the embedding
// cannot be read as the cause. Hence a synthetic type pins both that promotion happens and
// that unfollowable shapes fail.
func TestReflectJSONFieldsEmbedding(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B int    `json:"b,omitempty"`
	}
	type deeper struct{ inner }
	type flat struct {
		X string `json:"x"`
		Y string // exported with no tag: json emits it as "Y"
		z string // unexported: not emitted
	}

	// ① One level of embedding is promoted.
	type promoted struct {
		Hour string `json:"hour"`
		inner
	}
	got, err := reflectJSONFields(reflect.TypeOf(promoted{}), 0)
	if err != nil {
		t.Fatalf("1 段の埋め込みを読めない: %v", err)
	}
	if len(got) != 3 || got["Hour"] != "hour" || got["A"] != "a" || got["B"] != "b" {
		t.Fatalf("昇格の結果が違う: %v（外側 1 ＋ 昇格 2 のはず）", got)
	}

	// ② An exported field with no tag is emitted by json under its Go name, so it is
	// emitted here too; an unexported one never reaches the wire and is dropped. The
	// differential test against json.Marshal (TestReflectJSONFieldsMatchesEncodingJSON) is
	// the authority: a hand-written expectation of "drop untagged fields" was simply wrong.
	// On the rules, the standard library is always the authority.
	got, err = reflectJSONFields(reflect.TypeOf(flat{}), 0)
	if err != nil || len(got) != 2 || got["X"] != "x" || got["Y"] != "Y" {
		t.Fatalf("タグ無しフィールドの扱いが違う: %v (%v)", got, err)
	}

	// ③ Two or more levels of embedding is an error, not a shallow result.
	type twoLevel struct {
		Z string `json:"z"`
		deeper
	}
	if _, err := reflectJSONFields(reflect.TypeOf(twoLevel{}), 0); err == nil {
		t.Error("2 段の埋め込みで error にならない" +
			"——json の昇格規則は深さ優先で衝突の扱いも複雑なので、近似で通してはいけない")
	}

	// ④ A promotion colliding with the outer field is an error (json drops both on a
	// same-depth collision).
	type clash struct {
		A string `json:"outerA"`
		inner
	}
	if _, err := reflectJSONFields(reflect.TypeOf(clash{}), 0); err == nil {
		t.Error("昇格したフィールド名が外側とぶつかっているのに error にならない")
	}

	// ⑤ A non-struct argument is an error — never a silent empty result.
	if _, err := reflectJSONFields(reflect.TypeOf(""), 0); err == nil {
		t.Error("struct でない型で error にならない＝この経路が無言化しうる")
	}
}

// TestGoStructFieldsFromSourceGuards is the positive control for the AST route itself.
//
// This route rides on the measurement "today's input has zero embedded fields". Its worst
// failure mode is reading shallow, silently, the day someone adds an embedded field, so a
// synthetic fixture pins that it fails. (Same reason the TS scanner has one: while the real
// input stays easy, a breakage leaves the production comparison silent.)
func TestGoStructFieldsFromSourceGuards(t *testing.T) {
	dir := t.TempDir()
	write := func(base, body string) string {
		p := filepath.Join(dir, base)
		if err := os.WriteFile(p, []byte("package x\n\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A plain shape must read correctly — the check that this control is not measuring
	// nothing.
	ok := write("ok.go", "type T struct {\n\tA string `json:\"a\"`\n\tB int    `json:\"b,omitempty\"`\n\tC string `json:\"-\"`\n\tD string\n}\n")
	got, err := parseGoStructFields(ok, "T")
	if err != nil {
		t.Fatalf("素の struct を読めない: %v", err)
	}
	if len(got) != 2 || got["A"] != "a" || got["B"] != "b" {
		t.Fatalf("素の struct の読み取りが違う: %v（json:\"-\" とタグ無しは落とす）", got)
	}

	// An embedded (anonymous) field is an error, not a shallow result.
	emb := write("emb.go", "type Base struct {\n\tX string `json:\"x\"`\n}\n\ntype T struct {\n\tBase\n\tA string `json:\"a\"`\n}\n")
	if _, err := parseGoStructFields(emb, "T"); err == nil {
		t.Error("埋め込みフィールドが在るのに error にならない" +
			"——AST は埋め込み先の json タグを追えないので、浅く読むと穴の見落としと偽の赤が同時に出る")
	}

	// A missing type is an error; this route must not go silent.
	if _, err := parseGoStructFields(ok, "NoSuchType"); err == nil {
		t.Error("存在しない型で error にならない＝この経路が無言化しうる")
	}

	// A missing path is an error (the case where a move changed the path).
	if _, err := parseGoStructFields(filepath.Join(dir, "nope.go"), "T"); err == nil {
		t.Error("存在しないパスで error にならない＝移送のパス変更を黙って通す")
	}

	// Not a single json tag is an error: "zero found" is never taken as a result.
	none := write("none.go", "type T struct {\n\tA string\n\tB int\n}\n")
	if _, err := parseGoStructFields(none, "T"); err == nil {
		t.Error("json タグ 0 件で error にならない＝この経路が無言化しうる")
	}
}

// ===== shared machinery ends here =====

// TestSharedContractMachineryIsIdentical checks that the shared machinery is byte-identical
// in both modules.
//
// Go cannot share test helpers across modules, so these ~380 lines exist twice, in
// control-plane and in workspace/agent (the same situation as `wire_golden_test.go` /
// `routes_golden_test.go`). If the two drift apart, both modules' tests still pass
// independently — the day one side is fixed, the other keeps measuring the contract with
// the old scanner.
//
// Comparing the whole sentinel-delimited region is the point of this check. Collecting
// blocks by name instead would silently compare a block it failed to collect as zero lines
// and go green (a thin non-zero result). A region that is not found is Fatal.
func TestSharedContractMachineryIsIdentical(t *testing.T) {
	mine := sharedContractRegion(t, "contract_wire_test.go")
	theirs := sharedContractRegion(t, "../workspace/agent/contract_wire_test.go")
	if mine == theirs {
		return
	}
	// Point at the first line where they diverge rather than pasting a whole diff.
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
	sharedRegionStart = "// ===== shared machinery starts here"
	sharedRegionEnd   = "// ===== shared machinery ends here ====="
)

// sharedContractRegion returns the region between the sentinels; Fatal when it is not
// found (never silenced with Skip).
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

// extractSharedRegion returns the region between the sentinels. It returns an error so
// that "it fails when the region is not found" can itself be checked by a control.
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

// TestSharedContractRegionExtractor is the positive control for the comparator itself.
//
// "They are byte-identical today, so it is green" looks exactly the same as a comparator
// that is not working. Accept the green only after a synthetic input differing by one
// character has actually shown a difference.
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
	// One character of difference must show up as a difference.
	b, err := extractSharedRegion(mk("func g() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("中身を変えても区間が同じに見える＝比較器が中身を見ていない")
	}
	// A missing sentinel is an error — no Skip, no silent empty result.
	if _, err := extractSharedRegion("package x\n\nfunc f() {}\n"); err == nil {
		t.Error("番兵が無いのに error にならない＝この検査が無言化しうる")
	}
	if _, err := extractSharedRegion("package x\n\n" + sharedRegionStart + "=====\nfunc f() {}\n"); err == nil {
		t.Error("終了の番兵が無いのに error にならない")
	}
	// An empty region is an error: stop short of a thin non-zero result.
	if _, err := extractSharedRegion(mk("\n")); err == nil {
		t.Error("区間が空なのに error にならない")
	}
}
