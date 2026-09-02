package main

// git_wiring.go — `internal/gitx` の外向き依存（gitx → main）を 1 箇所で配線する。
//
// 逆向き（main → gitx）は**別名として alias_git.go にあったが、RECLAIM-C で回収して
// 直参照になった**。**2 枚に分けてあったのが効いた**: エイリアスはウェーブ境界で丸ごと
// 剥がして消えるが、配線は残る（gitx が main の各家系を呼ぶ関係そのものは移送で消えない）。
// 同じファイルに置いていたら、回収のときに「消す行」と「残す行」が混ざっていた。
// mcpx が alias_mcp.go → mcp_wiring.go で辿った道と同じ形。
//
// 🔥 **配線に既定値を置かない。** 未配線は `gitx.Configure` が panic で落とす。
// ここで零値を許すと、たとえば `RepoLocked` が「常に false」になり、
// **ロックしたはずの作業コピーが削除で消える**。静かに動くより落ちる方を選ぶ。

import (
	"context"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

func init() { gitx.Configure(gitDeps()) }

// gitDeps は本番の配線一式。**gitx 側の網羅検査（internal/gitx/deps_test.go）は
// 作り物を使う**ので、ここが唯一「本物の値」を書く場所である。
func gitDeps() gitx.Deps {
	return gitx.Deps{
		AbsPath:        absPath,
		RepoLocked:     repoLocked,
		LockedRepoDirs: lockedRepoDirs,

		LiveSessionsInDir:   liveSessionsInDir,
		LockedSessionsInDir: lockedSessionsInDir,
		WorktreeHasSessions: worktreeHasSessions,
		ManagedAlive:        managedAlive,

		FinalizeSessionUsage: finalizeSessionUsage,

		RepoJobActive: repoJobActive,
		StartRepoJob:  startGitRepoJob,

		IsSvnRepo:    isSvnRepo,
		SvnRepoEntry: svnRepoEntry,

		EnsureCredHelper: ensureCredHelper,
		InternalGitHost:  internalGitHost,

		FirstNonEmpty:   firstNonEmpty,
		GitConfigGlobal: gitConfigGlobal,
		GitHosts:        gitHosts,

		ScratchAutoRelocate: scratchAutoRelocate,

		ErrCodeSessionsRunning:       errCodeSessionsRunning,
		ErrCodeSessionsRunningDelete: errCodeSessionsRunningDelete,
		ErrCodeBranchInUse:           errCodeBranchInUse,
		ErrCodeWorktreeDirty:         errCodeWorktreeDirty,
		ErrCodeWorktreeRemoveFailed:  errCodeWorktreeRemoveFailed,
		ErrCodeHasWorktrees:          errCodeHasWorktrees,
		ErrCodeLocked:                errCodeLocked,
		ErrCodeLockedSessions:        errCodeLockedSessions,
	}
}

// gitRepoJobSink は `*repoJobSink` を gitx.RepoJobSink に見せる薄い adapter。
//
// 🔥 必要な理由は 1 つだけ: 末尾を取り出すメソッドが `tailString()` で**未公開**だから
// である。未公開メソッドは宣言したパッケージに紐づくので、gitx 側が同じ綴りの
// interface を書いても `*repoJobSink` はそれを満たさない。repo_jobs.go は AG-GIT の
// 所有外なので、あちらに `TailString()` を足すのではなくこちらで被せる。
type gitRepoJobSink struct{ *repoJobSink }

func (s gitRepoJobSink) Tail() string { return s.repoJobSink.tailString() }

// startGitRepoJob は startRepoJob の sink だけを詰め替えたもの。
//
// 🔥 **nil を包まない。** `gitRepoJobSink{nil}` は「中身が nil の非 nil インターフェース」
// になり、gitx 側の `if sink != nil` を**真**にしてしまう（clone が `--progress` 付きで
// 走り、`Stream` が nil の Writer へ書いて落ちる）。nil はそのまま nil で渡す。
func startGitRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink gitx.RepoJobSink) error) any {
	return startRepoJob(kind, name, dir, url, func(ctx context.Context, sink *repoJobSink) error {
		if sink == nil {
			return run(ctx, nil)
		}
		return run(ctx, gitRepoJobSink{sink})
	})
}
