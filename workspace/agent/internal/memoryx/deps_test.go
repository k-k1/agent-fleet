package memoryx

// memoryx 単体のテストは package main を持たないので、外向きの依存を自前で配線する
// （internal/gitx/deps_test.go と同じ形）。
//
// この家系の外向き依存は**安定エラーコード 13 本だけ**で、どれも errcodes.go の定数
// である。**本物の値を書き写さない** —— 書き写した瞬間に、それ自体が二つ目の出所に
// なる（本物は main の memory_wiring.go が配線する）。
// 移送前後で「捕まえられるバグの集合」は変わらない: memoryx のテストが見ているのは
// 「ハンドラが**どのコードを選んだか**」であって文字列そのものではないので、
// 応答の code とここの値を突き合わせる形は移送前（本物の定数と突き合わせる形）と等価。

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func init() { Configure(testDeps()) }

func testDeps() Deps {
	return Deps{
		ErrCodeBadRequest:     "memoryx-test-bad_request",
		ErrCodeBadRev:         "memoryx-test-bad_rev",
		ErrCodeBadPath:        "memoryx-test-bad_path",
		ErrCodeNoSnapshots:    "memoryx-test-no_snapshots",
		ErrCodeSnapshotFailed: "memoryx-test-snapshot_failed",
		ErrCodeDiffFailed:     "memoryx-test-diff_failed",
		ErrCodeBadScope:       "memoryx-test-bad_scope",
		ErrCodeRestoreFailed:  "memoryx-test-restore_failed",
		ErrCodeExportFailed:   "memoryx-test-export_failed",
		ErrCodeImportFailed:   "memoryx-test-import_failed",
		ErrCodeBadImport:      "memoryx-test-bad_import",
		ErrCodeSecretDetected: "memoryx-test-secret_detected",
		ErrCodeTooLarge:       "memoryx-test-too_large",
	}
}

// 🔥 Deps の**どのフィールドを 1 つ落としても** Configure が落ちること。
//
// 網羅の検査そのものを reflect で回すので、**フィールドが増えたら自動で対象になる**。
// この構造体は**全部が値型（文字列）**なので、未配線が nil 参照で落ちてくれることが
// 無い —— 空のまま静かに走り、Console には `""` というコードが届いて i18n が解決
// できず、生の developer メッセージが露出する。
func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	good := testDeps()
	v := reflect.ValueOf(good)
	typ := v.Type()
	if typ.NumField() != 13 {
		t.Fatalf("Deps のフィールドが %d 個（errcodes.go の memory 節は 13 本。構造体を取り違えているか、片側だけ増えた）", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			// 正しい配線へ必ず戻す（deps はパッケージ全体で共有する）。
			defer Configure(good)
			broken := reflect.New(typ).Elem()
			broken.Set(v)
			broken.Field(i).Set(reflect.Zero(f.Type))
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s を未配線にしても Configure が通った（配線漏れが静かに素通りする）", f.Name)
				}
				if !strings.Contains(fmt.Sprint(r), f.Name) {
					t.Fatalf("panic に %s の名前が出ていない: %v", f.Name, r)
				}
			}()
			Configure(broken.Interface().(Deps))
		})
	}
}
