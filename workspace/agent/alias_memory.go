package main

// alias_memory.go — `internal/memoryx` へ移送したシンボルの別名（main → memoryx）。
//
// ADR 0067 決定 2 の**エイリアス移送**: 呼び出し側（routes.go / main.go）を 1 行も
// 触らないために、移送前と同じ綴りをここで受け直す。**回収ウェーブで丸ごと剥がす前提**の
// ファイルなので、逆向き（memoryx → main）の配線は混ぜず memory_wiring.go に置く
// （gitx が alias_git.go / git_wiring.go で辿った道と同じ形。同じファイルに置くと、
// 回収のときに「消す行」と「残す行」が混ざる）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"

// REST（routes.go が登録する 10 本）。
var (
	handleMemoryRoots          = memoryx.HandleMemoryRoots
	handleMemorySnapshots      = memoryx.HandleMemorySnapshots
	handleMemorySnapshotCreate = memoryx.HandleMemorySnapshotCreate
	handleMemoryDiff           = memoryx.HandleMemoryDiff
	handleMemoryTree           = memoryx.HandleMemoryTree
	handleMemoryRestore        = memoryx.HandleMemoryRestore
	handleMemorySettings       = memoryx.HandleMemorySettings
	handleMemoryExport         = memoryx.HandleMemoryExport
	handleMemoryImport         = memoryx.HandleMemoryImport
	handleMemoryImportApply    = memoryx.HandleMemoryImportApply
)

// 自動 snapshot のポーリングループ（main.go が起こす）。
var startMemorySnapshotLoop = memoryx.StartMemorySnapshotLoop
