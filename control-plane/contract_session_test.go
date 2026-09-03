package main

// contract_session_test.go — 「契約の型化」の最小 1 本（家系: sessionWire）。
//
// 何のためか（docs/log/23 の診断「三重の手動同期」の脚②）。
// Console の `console/src/types/session.ts` は **手書きの TS 型**で、Go の `sessionWire`
// とは**何の機械的な関係も無い**。片側の json タグを直しても、もう片側は黙ったまま
// `undefined` を読むだけで、Go のテストも Console のテストも緑のまま通る。
//
// 既存の安全網との関係（**作り直していない・上に足している**）:
//   - `routes_golden_test.go` = 窓口が在るか。JSON の形は見ない。
//   - `wire_golden_test.go`   = json キー・JSON 上の型・omitempty を撮る。**Go 側だけ**で閉じており、
//     Console の型とは突き合わせない。また **Go のフィールド名を撮っていない**ので、
//     **同じ型の 2 つの json タグを入れ替えても、キー集合が変わらず緑のまま通る**（下の TestSessionWireFieldBinding が塞ぐ）。
//   - `session_wire_test.go`  = Agent → CP の往復で drop しないこと。Console 側は見ない。
//
// ⚠️ **ワイヤは 1 バイトも変えていない。**ここでやっているのは「今のワイヤを書き表す」ことだけ。

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// consoleSessionTS は Console の手書き型の在り処。
// 🔴 パターンではなく**解決結果**で存在を見る（この定数を直すときは下の Fatal も見ること）。
//
// 🔴 **手元で変異を当てるときは `go test -count=1` を付けること。**
// この検査は**モジュールの外**（Console の TS）を読む。TS だけを書き換えても
// テストバイナリは変わらないので、**`go test` のキャッシュに当たって `ok (cached)` が出る**
// ——**変異を当てたのに緑に見える。**（案 A の家系は全部この性質を持つ。先例の
// `workspace/agent/errcodes_catalog_test.go` も同じ。）**CI は毎回まっさらなので影響を受けるのは
// 手元の申告のほうで、「変異を当てたが緑だった」という報告が腐る。
const consoleSessionTS = "../console/src/types/session.ts"

// --- ① 取り違え検査: Go フィールド ↔ json キーの結び付きを固定する ---

// sessionWireBinding は sessionWire の「Go フィールド名 → json キー」。
//
// 🔴 **なぜ json キーの集合ではなく「結び付き」を撮るのか。**
// 同じ型のフィールド 2 つ（例: Branch / CurrentBranch, ExitReason / Carried）の json タグを
// 入れ替えると、**ワイヤに出るキーの集合は 1 文字も変わらない**。wire.golden も TS との
// キー突き合わせも緑のまま通り、画面には「別のブランチ名」が出る。
// 結び付きを撮ると、この入れ替えが**この表との差分**として出る。
//
// 🔴 **フィールド名を撮っても移送で偽の赤にならない。**
// wire.golden が「Go の型名は撮らない」としたのは、型名が `main` → `internal/x` の移送で
// 必ず変わるため。**フィールド名は変わらない**——json タグが効くのは公開フィールドだけで、
// 公開フィールドは移送しても改名されない（実測 2026-09-03・develop: json タグ付きの
// 非公開フィールドは両モジュール合わせて **0 件** / 公開フィールドは 3,001 件）。
var sessionWireBinding = map[string]string{
	"Name":                 "name",
	"Kind":                 "kind",
	"Driver":               "driver",
	"Dir":                  "dir",
	"Subdir":               "subdir",
	"Repo":                 "repo",
	"WorkingCopyID":        "workingCopyId",
	"Title":                "title",
	"Display":              "display",
	"Color":                "color",
	"Label":                "label",
	"Started":              "started",
	"CreatedAt":            "createdAt",
	"RemoteUrl":            "remoteUrl",
	"State":                "state",
	"Alive":                "alive",
	"Resumable":            "resumable",
	"Locked":               "locked",
	"Archived":             "archived",
	"BackgroundBusy":       "backgroundBusy",
	"BackgroundBusyReason": "backgroundBusyReason",
	"RateLimitResumeAt":    "rateLimitResumeAt",
	"Context":              "context",
	"Branch":               "branch",
	"CurrentBranch":        "currentBranch",
	"BranchDrift":          "branchDrift",
	"Worktree":             "worktree",
	"ExitReason":           "exitReason",
	"ExitCode":             "exitCode",
	"ExitSignal":           "exitSignal",
	"KeepAwakeUntil":       "keepAwakeUntil",
	"Carried":              "carried",
}

// sessionWireFields は sessionWire の実際の「Go フィールド名 → json キー」を reflect で読む。
//
// 🔴 **下の 2 本はどちらもここを originalとする。**②が `sessionWireBinding`（手書きの表）を
// 材料にしていると、**構造体を直しても②に届かない**——免除の寿命を見張る逆検査が
// 「Go 側から消えた／増えた」に鳴らなくなる（実測で緑になった）。
// 表は①が守るもので、②が読むものではない。
func sessionWireFields(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	rt := reflect.TypeOf(sessionWire{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		got[f.Name] = strings.Split(tag, ",")[0]
	}
	if len(got) == 0 {
		t.Fatal("sessionWire から json タグを 1 つも読めなかった＝この検査が無言化している")
	}
	return got
}

// TestSessionWireFieldBinding は「同じ型の 2 つを入れ替える」取り違えを捕まえる。
func TestSessionWireFieldBinding(t *testing.T) {
	got := sessionWireFields(t)
	for name, want := range sessionWireBinding {
		g, ok := got[name]
		if !ok {
			t.Errorf("sessionWire に フィールド %s が無い（消えたか改名された）", name)
			continue
		}
		if g != want {
			t.Errorf("sessionWire.%s の json タグが %q（表は %q）"+
				"——同じ型のフィールド同士でタグを入れ替えると、ワイヤのキー集合は変わらないまま値だけが入れ替わる",
				name, g, want)
		}
	}
	for name, key := range got {
		if _, ok := sessionWireBinding[name]; !ok {
			t.Errorf("sessionWire.%s (json:%q) が表に無い——足したなら表にも足すこと（Console 側の型にも要るはず）", name, key)
		}
	}
}

// --- ② 対応検査: sessionWire の json キー ↔ Console の Session 型 ---

// consoleOnlyExempt は「Console の Session に在るが、Go の sessionWire が出さない」ことが
// **意図されている**キー。🔴 増やすときは必ず理由を書くこと。
// **ここは「まだ直していない」を隠す場所ではない。**
//
// 🔴 **いま入っている 2 件は「意図された免除」ではなく、この検査が見つけた穴**である。
// どちらを正にするか（TS の宣言を消すか、Go に足すか）は**ワイヤに関わる設計判断**なので、
// 第 1 段では触らずに免除し、報告で利用者へ上げている。**塞いだ瞬間に下の逆検査が免除を外させる。**
var consoleOnlyExempt = map[string]string{
	"model": "【穴】session.ts:51 が `model?: string; // claude model` を宣言しているが、" +
		"sessionWire にも Agent の session.Session にも該当キーが無い。Console 側の実読みも見つからない＝死んだ宣言の疑い。",
	"path": "【穴】session.ts:34 が `path?: string; // absolute working dir` を宣言しているが、" +
		"sessionWire にも Agent の session.Session にも該当キーが無い（実際の作業ディレクトリは dir）。",
}

// goOnlyExempt は「sessionWire が出すが Console の Session 型が宣言していない」ことが
// **意図されている**キー。同上。
//
// 🔴 3 件とも**穴**。とくに started は、Console が
// `console/src/features/sessions/ArchivedModal.tsx:19` で
// `type ArchivedSession = Session & { started?: string };` と**局所的に継ぎ足している**
// ——共有の型が実際のワイヤに追いついていないことを、機能側が交差型で埋めている実例。
var goOnlyExempt = map[string]string{
	"started":  "【穴】ArchivedModal.tsx:19 が交差型で局所的に足している。共有の Session に載せるのが筋だが、既存の利用箇所の型が変わる。",
	"display":  "【穴】sessionWire が出しているが Session に宣言が無い。",
	"archived": "【穴】sessionWire が出しているが Session に宣言が無い。",
}

func TestSessionWireMatchesConsoleType(t *testing.T) {
	tsKeys := consoleInterfaceFields(t, consoleSessionTS, "Session")
	goKeys := map[string]bool{}
	for _, k := range sessionWireFields(t) { // 🔴 表ではなく実際の struct から
		goKeys[k] = true
	}

	var tsOnly, goOnly []string
	for k := range tsKeys {
		if !goKeys[k] {
			tsOnly = append(tsOnly, k)
		}
	}
	for k := range goKeys {
		if !tsKeys[k] {
			goOnly = append(goOnly, k)
		}
	}
	sort.Strings(tsOnly)
	sort.Strings(goOnly)

	for _, k := range tsOnly {
		if _, ok := consoleOnlyExempt[k]; ok {
			continue
		}
		t.Errorf("console/src/types/session.ts の Session が %q を宣言しているが、sessionWire は出さない"+
			"——Console は常に undefined を読む（型検査は optional なので鳴らない）", k)
	}
	for _, k := range goOnly {
		if _, ok := goOnlyExempt[k]; ok {
			continue
		}
		t.Errorf("sessionWire が %q を出すが、console/src/types/session.ts の Session に無い"+
			"——Console からは型の上で見えない", k)
	}

	// 🔴 免除の寿命の逆検査（README §4）。
	//
	// **免除が要らなくなる道は「揃う」と「消える」の 2 つある。片方だけ見ると片肺になる。**
	// consoleOnlyExempt（TS に在って Go に無い）は、**Go が出すようになった**ときだけでなく
	// **TS 側から消された**ときにも要らなくなる。後者を見ていないと、
	// **免除に書いた理由が嘘のまま黙って残る**——`model` / `path` はまさに「消す」方向で
	// 検討されているので、この向きが本命である。goOnlyExempt も対称に 2 方向を見る。
	for k, why := range consoleOnlyExempt {
		if goKeys[k] {
			t.Errorf("免除 %q (%s) はもう要らない——sessionWire が出すようになった。consoleOnlyExempt から外すこと", k, why)
		}
		if !tsKeys[k] {
			t.Errorf("免除 %q (%s) はもう要らない——console/src/types/session.ts の Session から消えた"+
				"（両側に無いキーの免除は理由ごと嘘になる）。consoleOnlyExempt から外すこと", k, why)
		}
	}
	for k, why := range goOnlyExempt {
		if !goKeys[k] {
			t.Errorf("免除 %q (%s) はもう要らない——sessionWire が出さなくなった"+
				"（両側に無いキーの免除は理由ごと嘘になる）。goOnlyExempt から外すこと", k, why)
		}
		if tsKeys[k] {
			t.Errorf("免除 %q (%s) はもう要らない——Console の Session が宣言するようになった。goOnlyExempt から外すこと", k, why)
		}
	}
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
// 合成標本（testdata/contract_ts_probe.ts）は、実際に踏んだ形だけを並べてある。
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
	// 🔴 「0 件でした」を結果として採らないための下限（走査が壊れたら Fatal）。
	// ⚠️ **この下限は「一部の行だけ複数キー」を捕まえない**——数個取りこぼしても 10 は超えるため。
	// そちらは TestTSInterfaceFieldsParser（合成標本）の担当。
	if len(out) < 10 {
		t.Fatalf("interface %s のフィールドを %d 個しか読めなかった＝TS の書き方が変わって走査が壊れている", name, len(out))
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
