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
	"bufio"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// consoleSessionTS は Console の手書き型の在り処。
// 🔴 パターンではなく**解決結果**で存在を見る（この定数を直すときは下の Fatal も見ること）。
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

// TestSessionWireFieldBinding は「同じ型の 2 つを入れ替える」取り違えを捕まえる。
func TestSessionWireFieldBinding(t *testing.T) {
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
	for _, k := range sessionWireBinding {
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

	// 🔴 免除の寿命の逆検査（README §4）。両側が揃った免除は、その場で外させる。
	for k, why := range consoleOnlyExempt {
		if goKeys[k] {
			t.Errorf("免除 %q (%s) はもう要らない——sessionWire が出すようになった。consoleOnlyExempt から外すこと", k, why)
		}
	}
	for k, why := range goOnlyExempt {
		if tsKeys[k] {
			t.Errorf("免除 %q (%s) はもう要らない——Console の Session が宣言するようになった。goOnlyExempt から外すこと", k, why)
		}
	}
}

// consoleInterfaceFields は TS の `interface <name> { ... }` の**深さ 1 の**フィールド名を返す。
// 入れ子のオブジェクト型・コメント・ユニオンは読み飛ばす。
func consoleInterfaceFields(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Console の型を読めない (%s): %v"+
			"——移送でパスが変わったなら consoleSessionTS を直すこと（Skip で黙らせない）", path, err)
	}
	defer f.Close()

	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	depth, inside := 0, false
	for sc.Scan() {
		line := sc.Text()
		if !inside {
			if strings.HasPrefix(strings.TrimSpace(line), "export interface "+name+" ") ||
				strings.HasPrefix(strings.TrimSpace(line), "interface "+name+" ") {
				inside = true
				depth = strings.Count(line, "{") - strings.Count(line, "}")
			}
			continue
		}
		if depth == 1 {
			if k := tsFieldName(line); k != "" {
				out[k] = true
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	if !inside {
		t.Fatalf("%s に interface %s が見つからない＝この検査が無言化している", path, name)
	}
	// 🔴 「0 件でした」を結果として採らないための下限（走査が壊れたら Fatal）。
	if len(out) < 10 {
		t.Fatalf("interface %s のフィールドを %d 個しか読めなかった＝TS の書き方が変わって走査が壊れている", name, len(out))
	}
	return out
}

// tsFieldName は `  foo?: string; // …` の "foo" を返す。フィールド行でなければ ""。
func tsFieldName(line string) string {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "*") || strings.HasPrefix(s, "/*") {
		return ""
	}
	i := strings.IndexAny(s, ":?")
	if i <= 0 {
		return ""
	}
	name := strings.TrimSuffix(s[:i], "?")
	for _, r := range name {
		if !(r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	// `foo?: T` / `foo: T` の形だけ。`foo` の直後が ? か : であることを確かめる。
	rest := s[i:]
	if !strings.HasPrefix(rest, ":") && !strings.HasPrefix(rest, "?:") {
		return ""
	}
	return name
}
