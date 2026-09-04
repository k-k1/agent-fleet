package memoryx

// deps.go — memoryx が呼び出し側（package main）へ伸ばしている手を 1 枚に集めたもの。
//
// この家系の外向き依存は **errcodes.go の安定エラーコード 13 本だけ**である
// （コンパイラに出させた断面の全件・`go build -gcflags=-e`）。関数も型も 1 つも要らない
// ——「メモリの版管理」は live ツリーと専用 bare repo で閉じており、他の家系を呼ばない。
//
// コードを memoryx 側で定義し直さない理由は internal/gitx/deps.go と同じ: Console の
// i18n カタログ（console/src/core/api/client.ts の ERR_TEXT）と対になっている文字列で、
// **出所が 2 つになると、片方だけ直した日に画面が生のコードを出す**。
//
// memoryx 単体のテストは main を持たないので、TestMain ではなく init が配線する
// （deps_test.go 参照）。

import (
	"fmt"
	"reflect"
	"sort"
)

// Deps は「memoryx から見た外の世界」。**型は main のものを 1 つも含まない**。
type Deps struct {
	// --- 安定エラーコード（errcodes.go）---
	ErrCodeBadRequest     string
	ErrCodeBadRev         string
	ErrCodeBadPath        string
	ErrCodeNoSnapshots    string
	ErrCodeSnapshotFailed string
	ErrCodeDiffFailed     string
	ErrCodeBadScope       string
	ErrCodeRestoreFailed  string
	ErrCodeExportFailed   string
	ErrCodeImportFailed   string
	ErrCodeBadImport      string
	ErrCodeSecretDetected string
	ErrCodeTooLarge       string
}

var deps Deps

// Configure は起動時に 1 回だけ呼ぶ（main の memory_wiring.go / memoryx のテストの init）。
//
// 🔥 **網羅は reflect で取る。手書きの一覧にしない。** ここは**全フィールドが値型（文字列）**
// なので、未配線が nil 参照で落ちてくれることが無い: 空のまま静かに走り、Console には
// `""` というコードが届いて i18n が解決できず、生の developer メッセージが露出する。
//
// 例外を作るときはフィールドに `memoryx:"optional"` と書く（一覧を別に持たない）。
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("memoryx") == "optional" {
			continue
		}
		if v.Field(i).IsZero() {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("memoryx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
	errCodeMemoryBadRequest = d.ErrCodeBadRequest
	errCodeMemoryBadRev = d.ErrCodeBadRev
	errCodeMemoryBadPath = d.ErrCodeBadPath
	errCodeMemoryNoSnapshots = d.ErrCodeNoSnapshots
	errCodeMemorySnapshotFailed = d.ErrCodeSnapshotFailed
	errCodeMemoryDiffFailed = d.ErrCodeDiffFailed
	errCodeMemoryBadScope = d.ErrCodeBadScope
	errCodeMemoryRestoreFailed = d.ErrCodeRestoreFailed
	errCodeMemoryExportFailed = d.ErrCodeExportFailed
	errCodeMemoryImportFailed = d.ErrCodeImportFailed
	errCodeMemoryBadImport = d.ErrCodeBadImport
	errCodeMemorySecretDetected = d.ErrCodeSecretDetected
	errCodeMemoryTooLarge = d.ErrCodeTooLarge
}

// Wired は現在の配線を返す。**呼び出し側が「配線が生きているか」を通しで検査する**
// ための読み出し口で、memoryx 自身は使わない。
//
// 🔥 Configure が捕まえるのは**未配線**だけで、**間違った配線**（別のコードを繋いだ）は
// 捕まえられない。
func Wired() Deps { return deps }

// 値で受け取るもの。Configure が 1 回だけ書く（以後は読むだけ）。移送してきた 2,951 行を
// 1 行も触らずに済ませるため、**綴りは移送前の const と同じ**にしてある。
var (
	errCodeMemoryBadRequest     string
	errCodeMemoryBadRev         string
	errCodeMemoryBadPath        string
	errCodeMemoryNoSnapshots    string
	errCodeMemorySnapshotFailed string
	errCodeMemoryDiffFailed     string
	errCodeMemoryBadScope       string
	errCodeMemoryRestoreFailed  string
	errCodeMemoryExportFailed   string
	errCodeMemoryImportFailed   string
	errCodeMemoryBadImport      string
	errCodeMemorySecretDetected string
	errCodeMemoryTooLarge       string
)
