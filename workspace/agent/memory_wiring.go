package main

// memory_wiring.go — `internal/memoryx` の外向き依存（memoryx → main）を 1 箇所で配線する。
//
// 逆向き（main → memoryx）は別名として alias_memory.go にある。**2 枚に分けてある**のは、
// エイリアスがウェーブ境界で丸ごと剥がれて消えるのに対し、配線は残るため
// （memoryx が errcodes.go を引く関係そのものは回収しても消えない）。
//
// 🔥 **配線に既定値を置かない。** 未配線は `memoryx.Configure` が panic で落とす。
// ここは全部が文字列なので、零値を許すと Console へ `""` というコードが届き、
// i18n が解決できずに生の developer メッセージが露出する（静かに壊れる形）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"

func init() { memoryx.Configure(memoryDeps()) }

// memoryDeps は本番の配線一式。**memoryx 側の網羅検査（internal/memoryx/deps_test.go）は
// 作り物を使う**ので、ここが唯一「本物の値」を書く場所である。
func memoryDeps() memoryx.Deps {
	return memoryx.Deps{
		ErrCodeBadRequest:     errCodeMemoryBadRequest,
		ErrCodeBadRev:         errCodeMemoryBadRev,
		ErrCodeBadPath:        errCodeMemoryBadPath,
		ErrCodeNoSnapshots:    errCodeMemoryNoSnapshots,
		ErrCodeSnapshotFailed: errCodeMemorySnapshotFailed,
		ErrCodeDiffFailed:     errCodeMemoryDiffFailed,
		ErrCodeBadScope:       errCodeMemoryBadScope,
		ErrCodeRestoreFailed:  errCodeMemoryRestoreFailed,
		ErrCodeExportFailed:   errCodeMemoryExportFailed,
		ErrCodeImportFailed:   errCodeMemoryImportFailed,
		ErrCodeBadImport:      errCodeMemoryBadImport,
		ErrCodeSecretDetected: errCodeMemorySecretDetected,
		ErrCodeTooLarge:       errCodeMemoryTooLarge,
	}
}
