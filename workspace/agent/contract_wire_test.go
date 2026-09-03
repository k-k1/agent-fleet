package main

// contract_wire_test.go — 「契約の型化」の横展開（案 A のゴールデン中継）・agent 側。
//
// control-plane/contract_wire_test.go と**同じ仕組みの写し**。
// 🔴 **写しである理由**: control-plane と workspace/agent は**別の Go モジュール**なので、
// テストヘルパを共有できない（`wire_golden_test.go` / `routes_golden_test.go` が両側に在るのと同じ）。
// **片方を直したらもう片方も直すこと。**仕組みの説明は control-plane 側の冒頭コメントにある。
//
// 3 本立て: ①bind（Go フィールド名 ↔ json キー・同型の取り違えを捕まえる）
//           ②scan（TS 側のキー集合を表に固定する・**ドリフトと、実ファイルで結果が変わる走査の壊れ**）
//           ③match（TS ↔ Go・免除つき・**免除の寿命は 4 方向**）
// 🔴 **走査の壊れ全般を捕まえるのは②ではなく合成標本の対照**（TestTSInterfaceFieldsParser）。
// 実測: 走査を壊す変異を当てても実 TS が 1 行 1 フィールドだと②は緑のまま。詳細は CP 側の冒頭に。
//
// ⚠️ モジュールの外（Console の TS）を読むので、**手元で変異を当てるときは `go test -count=1`**。

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// agentContractFamilies は workspace/agent 側の家系。
func agentContractFamilies() []contractFamily {
	return []contractFamily{
		// チャットの 1 メッセージ。Console のチャット面が読む中心の型。
		{
			name:    "chatx.ChatMessage",
			goType:  reflect.TypeOf(chatx.ChatMessage{}),
			binding: chatMessageBinding,
			tsPath:  "../../console/src/types/chat.ts",
			tsName:  "ChatMessage",
			tsKeys: keySet("role", "content", "ts", "agent", "model", "steps", "session",
				"delivered", "notice_key", "notice_args", "report_kind", "report_reason"),
			tsOnly: map[string]string{},
			goOnly: map[string]string{
				"instr": "【穴】chatx.ChatMessage が出しているが Console の ChatMessage に宣言が無い。",
			},
		},

		// ブランチ一覧の 1 行。
		// 🔴 **この家系だけ TS 1 型 ↔ Go 2 型**——Console の `Branch` は
		// ローカル（gitx.BranchInfo）とリモート（gitx.remoteBranch）の**両方の窓口**が返す形を
		// 1 つの型で受けている。`default` はリモート側にしか無い。
		{
			name:    "gitx.BranchInfo",
			goType:  reflect.TypeOf(gitx.BranchInfo{}),
			binding: branchInfoBinding,
			tsPath:  "../../console/src/features/repos/BranchList.tsx",
			tsName:  "Branch",
			tsKeys:  keySet("name", "unix", "date", "subject", "default", "remote", "current", "worktree_path"),
			tsOnly: map[string]string{
				// **穴ではない。**兄弟の `remoteBranch`（internal/gitx/git_remote.go:42）が
				// `json:"default"` を出しており、BranchList はローカルとリモートの両方を同じ型で描く。
				// ⚠️ **remoteBranch は非公開**なので、この家系からは reflect で触れない。
				// 12 本目以降で internal/gitx に検査を置くなら、そちらで固定すること。
				"default": "穴ではない: 兄弟の remoteBranch(internal/gitx/git_remote.go:42) が json:\"default\" を出す。BranchList はローカルとリモートを同じ TS 型で描いている",
			},
			goOnly: map[string]string{},
		},

		// 掃除の控え（アーカイブ）。
		{
			name:    "cleanupManifest",
			goType:  reflect.TypeOf(cleanupManifest{}),
			binding: cleanupManifestBinding,
			tsPath:  "../../console/src/features/sessions/CleanupModal.tsx",
			tsName:  "CleanupArchive",
			tsKeys:  keySet("id", "at", "reason", "sessions", "branches"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				"worktrees": "【穴】cleanupManifest が出しているが Console の CleanupArchive に宣言が無い（掃除で消したワークツリーの一覧が画面に出ない）。",
			},
		},

		// ブラウザ添付の状態。
		{
			name:    "browserx.BrowserAttachmentResponse",
			goType:  reflect.TypeOf(browserx.BrowserAttachmentResponse{}),
			binding: browserAttachmentBinding,
			tsPath:  "../../console/src/features/browser/attachmentController.ts",
			tsName:  "BrowserAttachmentStatus",
			tsKeys:  keySet("id", "state", "title", "url", "expiresAt", "controlMode", "handoff"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				"openUrl": "【穴】添付を開く URL。Console の BrowserAttachmentStatus に宣言が無い。",
				"viewer":  "【穴】同上。",
			},
		},
	}
}

var chatMessageBinding = map[string]string{
	"Role": "role", "Content": "content", "TS": "ts", "Agent": "agent", "Model": "model",
	"Steps": "steps", "Session": "session", "Instr": "instr", "Delivered": "delivered",
	"NoticeKey": "notice_key", "NoticeArgs": "notice_args", "ReportKind": "report_kind",
	"ReportReason": "report_reason",
}

var branchInfoBinding = map[string]string{
	"Name": "name", "Remote": "remote", "Unix": "unix", "Date": "date",
	"Subject": "subject", "Current": "current", "WorktreePath": "worktree_path",
}

var cleanupManifestBinding = map[string]string{
	"ID": "id", "At": "at", "Reason": "reason", "Sessions": "sessions",
	"Branches": "branches", "Worktrees": "worktrees",
}

var browserAttachmentBinding = map[string]string{
	"ID": "id", "State": "state", "Title": "title", "URL": "url", "OpenURL": "openUrl",
	"ExpiresAt": "expiresAt", "Viewer": "viewer", "ControlMode": "controlMode", "Handoff": "handoff",
}

func TestContractFamilies(t *testing.T) {
	fams := agentContractFamilies()
	// 🔴 走査の母数を見張る（#320 型）。家系が黙って消えたらここで気付く。
	if len(fams) != 4 {
		t.Fatalf("家系が %d 件しかない＝表から落ちている（足したなら本数も直すこと）", len(fams))
	}
	for _, f := range fams {
		t.Run(f.name, func(t *testing.T) { checkContractFamily(t, f) })
	}
}

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
func consoleInterfaceFields(t *testing.T, path, name string, wantAtLeast int) map[string]bool {
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
	// 🔴 「0 件でした」を結果として採らないための下限。
	// **下限は家系ごとに、固定したキー数そのもの**にしてある（定数の 10 にすると、
	// キーが 6〜7 個の小さい家系で必ず落ち、逆に大きい家系では数個の取りこぼしを見逃す）。
	// ⚠️ **この下限は「一部の行だけ複数キー」を捕まえない**——どの家系でも数個の取りこぼしは
	// 素通りしうる。そちらは TestTSInterfaceFieldsParser（合成標本）の担当。
	if len(out) < wantAtLeast {
		t.Fatalf("interface %s のフィールドを %d 個しか読めなかった（表は %d 個）＝TS の書き方が変わって走査が壊れている",
			name, len(out), wantAtLeast)
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
