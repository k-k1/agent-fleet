package memoryx

// エージェントメモリの版管理（docs/log/39 / ADR 0022）— snapshot 本体。
//
//	live ──① allowlist copy──▶ staging ──② git commit──▶ af-memory.git（bare）
//
// 無変更なら commit しない（空コミットで履歴を汚さない）。commit メッセージの trailer に
// 契機（AF-Trigger）と変更 slug を刻むので、一覧 API は git log だけで組み立てられる。

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// memorySnapshotMu は snapshot / 将来の restore・import を直列化する。staging と bare
// repo の index を共有するため、同時実行は許さない（トリガーループと手動 API が競合する）。
var memorySnapshotMu sync.Mutex

// snapshot の契機（commit trailer AF-Trigger の値）。restore / import は P2/P3 で使う。
const (
	memoryTriggerAuto       = "auto"
	memoryTriggerManual     = "manual"
	memoryTriggerPreRestore = "pre-restore"
	memoryTriggerRestore    = "restore"
	memoryTriggerImport     = "import"
)

// memorySnapshotResult は 1 回の snapshot 実行の結果。Committed=false は「変更が無くて
// 積まなかった」— エラーではない正常系。
type memorySnapshotResult struct {
	Committed bool               `json:"committed"`
	Rev       string             `json:"rev,omitempty"`
	Trigger   string             `json:"trigger"`
	Files     int                `json:"files"`    // repo に載っている対象ファイル総数
	Changed   []string           `json:"changed"`  // 変更のあった repo 内パス
	Projects  []memoryProjectRef `json:"projects"` // 変更のあった claude プロジェクト
	Kinds     []string           `json:"kinds"`    // 対象になったルートの kind
}

// memorySnapshot は live → staging → commit を 1 往復する。now は呼び出し側から渡す
// （テストが決定的に検証できるよう time.Now() を内部で呼ばない — 既存 cleanup_archive と同じ流儀）。
func memorySnapshot(trigger string, now time.Time) (memorySnapshotResult, error) {
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	return memorySnapshotLocked(trigger, now)
}

// memorySnapshotLocked は memorySnapshotMu を握った状態で 1 往復する。trailers は
// AF-Trigger の後ろに足す追加の trailer 行（restore が戻し元 rev と scope を刻む）。
func memorySnapshotLocked(trigger string, now time.Time, trailers ...string) (memorySnapshotResult, error) {
	res := memorySnapshotResult{Trigger: trigger, Changed: []string{}, Projects: []memoryProjectRef{}, Kinds: []string{}}
	roots := memoryRoots()
	if len(roots) == 0 {
		return res, nil
	}
	if err := memoryEnsureRepo(); err != nil {
		return res, err
	}
	staging := memoryStagingDir()
	for _, r := range roots {
		n, err := memorySyncToStaging(r, staging)
		if err != nil {
			return res, err
		}
		res.Files += n
		res.Kinds = append(res.Kinds, r.Kind)
	}
	if _, err := memoryGitRun("add", "-A"); err != nil {
		return res, fmt.Errorf("stage memory: %w", err)
	}
	// 差分ゼロなら何も積まない（★8 repo 肥大の抑制でもある）。
	changed, err := memoryGitRun("diff", "--cached", "--name-only")
	if err != nil {
		return res, fmt.Errorf("inspect staged memory: %w", err)
	}
	if strings.TrimSpace(changed) == "" {
		return res, nil
	}
	for _, p := range strings.Split(changed, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			res.Changed = append(res.Changed, p)
		}
	}
	sort.Strings(res.Changed)
	_, res.Projects, _ = memorySummarizePaths(res.Changed)

	msg := memoryCommitMessage(trigger, now, res.Changed, res.Projects, trailers)
	if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "-m", msg); err != nil {
		return res, fmt.Errorf("commit memory snapshot: %w", err)
	}
	rev, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		return res, err
	}
	res.Committed, res.Rev = true, rev
	// ★8 repo 肥大: 判断は git に任せる（--auto は閾値を超えたときだけ働く）。失敗しても
	// snapshot は成立しているので握り潰す — ここで返すと「積めたのに失敗」に見えてしまう。
	_, _ = memoryGitRun("gc", "--auto", "--quiet")
	return res, nil
}

// memoryCommitMessage は 1 行目のサマリと trailer（AF-Trigger / AF-Changed）を組む。
// trailer は最終段落に固めて置く — `git log --pretty=%(trailers:...)` が拾える形。
func memoryCommitMessage(trigger string, now time.Time, changed []string, projects []memoryProjectRef, trailers []string) string {
	// 1 行目の動詞で「積んだ理由」が一覧の先頭から読めるようにする（詳細は trailer）。
	verb := "snapshot"
	switch trigger {
	case memoryTriggerRestore:
		verb = "restore"
	case memoryTriggerImport:
		verb = "import"
	}
	var subject string
	switch {
	case len(projects) == 1:
		subject = fmt.Sprintf("%s: %s (%s)", verb, now.Format(time.RFC3339), projects[0].Display)
	case len(projects) > 1:
		subject = fmt.Sprintf("%s: %s (%d projects changed)", verb, now.Format(time.RFC3339), len(projects))
	default:
		subject = fmt.Sprintf("%s: %s (%d files changed)", verb, now.Format(time.RFC3339), len(changed))
	}
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
	b.WriteString("AF-Trigger: " + trigger + "\n")
	for _, t := range trailers {
		if t = strings.TrimSpace(t); t != "" {
			b.WriteString(t + "\n")
		}
	}
	for _, p := range projects {
		b.WriteString("AF-Changed: " + p.Slug + "\n")
	}
	return b.String()
}
