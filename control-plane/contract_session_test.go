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
	"reflect"
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

// --- ② 対応検査に使う免除表（家系の定義は contract_wire_test.go の cpContractFamilies）---

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

// sessionContractFamily は sessionWire 家系。**検査の中身は contract_wire_test.go の
// checkContractFamily が持つ**（#343 以降、横展開のため表駆動に畳んだ）。
// アサーションは #339 のときと同じ——①結び付き ②TS 走査の固定 ③両方向の突き合わせと 4 方向の寿命。
func sessionContractFamily() contractFamily {
	return contractFamily{
		name:    "sessionWire",
		goType:  reflect.TypeOf(sessionWire{}),
		binding: sessionWireBinding,
		tsPath:  consoleSessionTS,
		tsName:  "Session",
		tsKeys: keySet("name", "kind", "driver", "title", "color", "label", "repo", "workingCopyId",
			"path", "dir", "subdir", "remoteUrl", "state", "alive", "resumable", "backgroundBusy",
			"backgroundBusyReason", "rateLimitResumeAt", "createdAt", "model", "context", "branch",
			"currentBranch", "branchDrift", "worktree", "exitReason", "exitCode", "exitSignal",
			"carried", "locked", "keepAwakeUntil"),
		tsOnly: consoleOnlyExempt,
		goOnly: goOnlyExempt,
	}
}
