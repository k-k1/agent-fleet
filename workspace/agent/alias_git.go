package main

// alias_git.go — git 家系（`internal/gitx`）の移送で開いた口を 1 枚で塞ぐ。
//
// 家系は `git.go` / `git_identity.go` / `git_oauth.go` / `git_remote.go` /
// `git_submodule.go` / `git_view.go` の 6 ファイル 3,871 行ごと `internal/gitx` へ
// 動いた。**呼び出し側は 1 行も変えていない**（routes.go / session_handlers.go /
// session_cleanup.go / cleanup_*.go / connections.go / workitems*.go / svn.go /
// fetch_loop.go / fs_git.go / locks.go / memory_*.go …）ので、動いたことに気付くのは
// このファイルだけである。剥がすのはウェーブ境界の別セッションの仕事（ADR 0067）。
//
// 依存は双方向だったので、向きごとに扱いを変えている:
//
//   - **main → gitx**（呼び出し側が使う名前 80）… 下の `x = gitx.X`。
//     公開名は**旧名の先頭 1 文字を大文字にしただけ**（唯一の例外は
//     `sshToHTTPS` → `SSHToHTTPS`）。機械置換で説明できる形にしてある。
//   - **gitx → main**（git 家系が main の各家系へ伸ばしていた手 26 本）…
//     `gitx.Configure` で関数値として配線する。gitx は main を import できないので、
//     これが唯一の方法である（詳細は internal/gitx/deps.go と git_wiring.go）。
//
// ⚠️ **`var` を var エイリアスで受けてはいけない**（写しになり、遠側の代入が届かない）。
// ここで唯一 var を受けているのは下の sentinel error 3 本で、理由はそこに書いた。

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// --- 型 -------------------------------------------------------------------
//
// 型はエイリアス（`=`）で受ける。**新しい型を定義してはいけない** —— `Repo` は
// wire_golden_test.go が `reflect.TypeOf(Repo{})` で形を撮っている上流なので、
// 別の型にすると json タグは同じでも「同じ型」ではなくなる。
type (
	Repo            = gitx.Repo
	RepoStatus      = gitx.RepoStatus
	branchInfo      = gitx.BranchInfo
	renameBranchReq = gitx.RenameBranchReq
)

// --- sentinel error --------------------------------------------------------
//
// 遠側は `var` だが、**代入されることが無い** sentinel（`errors.Is` の比較対象）
// なので写しで構わない —— 写しても指している実体は同じ 1 つである。
// 移送前に「git 家系の外から代入している箇所」が 0 件であることを確認済み。
var (
	errBBNoRepoRead          = gitx.ErrBBNoRepoRead
	errBBScopeless           = gitx.ErrBBScopeless
	errBitbucketUnauthorized = gitx.ErrBitbucketUnauthorized
)

// --- 関数 -----------------------------------------------------------------
//
// 遠側は全部 `func`（＝再代入されないので写しで安全）。出所のファイルごとに並べてある。
var (

	// --- git.go ---
	branchNameStatus               = gitx.BranchNameStatus
	ensureBranchRef                = gitx.EnsureBranchRef
	ensureRepo                     = gitx.EnsureRepo
	ensureWorktree                 = gitx.EnsureWorktree
	fastForwardNewWorktreeToOrigin = gitx.FastForwardNewWorktreeToOrigin
	fastForwardWorktree            = gitx.FastForwardWorktree
	gitBranchExists                = gitx.GitBranchExists
	gitBranchSHA                   = gitx.GitBranchSHA
	gitCreateBranch                = gitx.GitCreateBranch
	gitCurrentBranch               = gitx.GitCurrentBranch
	gitDirInfo                     = gitx.GitDirInfo
	gitOriginURL                   = gitx.GitOriginURL
	gitStatus                      = gitx.GitStatus
	gitWorktreeIntegration         = gitx.GitWorktreeIntegration
	handleCloneRepo                = gitx.HandleCloneRepo
	handleDeleteRepo               = gitx.HandleDeleteRepo
	handleInitRepo                 = gitx.HandleInitRepo
	handleListRepos                = gitx.HandleListRepos
	handleRepoBranches             = gitx.HandleRepoBranches
	handleRepoCheckout             = gitx.HandleRepoCheckout
	handleRepoFF                   = gitx.HandleRepoFF
	handleRepoFetch                = gitx.HandleRepoFetch
	handleRepoParentFF             = gitx.HandleRepoParentFF
	handleRepoStatus               = gitx.HandleRepoStatus
	isGitRepo                      = gitx.IsGitRepo
	isLinkedWorktree               = gitx.IsLinkedWorktree
	linkedWorktreeCount            = gitx.LinkedWorktreeCount
	maybePruneWorktree             = gitx.MaybePruneWorktree
	mergedLocalBranches            = gitx.MergedLocalBranches
	readWorkingCopyID              = gitx.ReadWorkingCopyID
	repoAnyDirFromPath             = gitx.RepoAnyDirFromPath
	repoDirFromPath                = gitx.RepoDirFromPath
	reposRoot                      = gitx.ReposRoot
	resolveRepoDir                 = gitx.ResolveRepoDir
	sshToHTTPS                     = gitx.SSHToHTTPS
	workingCopyID                  = gitx.WorkingCopyID
	worktreeBranches               = gitx.WorktreeBranches
	worktreeParent                 = gitx.WorktreeParent
	writeBranchInUse               = gitx.WriteBranchInUse

	// --- git_view.go ---
	gitChanges           = gitx.GitChanges
	handleRepoChanges    = gitx.HandleRepoChanges
	handleRepoCommit     = gitx.HandleRepoCommit
	handleRepoDiff       = gitx.HandleRepoDiff
	handleRepoDiscard    = gitx.HandleRepoDiscard
	handleRepoGraph      = gitx.HandleRepoGraph
	handleRepoLog        = gitx.HandleRepoLog
	handleRepoShow       = gitx.HandleRepoShow
	handleRepoStage      = gitx.HandleRepoStage
	handleRepoSubmodules = gitx.HandleRepoSubmodules
	handleRepoUnstage    = gitx.HandleRepoUnstage

	// --- git_identity.go ---
	handleGitProviderIdentityPut = gitx.HandleGitProviderIdentityPut
	handleGlobalIdentityGet      = gitx.HandleGlobalIdentityGet
	handleGlobalIdentityPut      = gitx.HandleGlobalIdentityPut
	handleRepoIdentityGet        = gitx.HandleRepoIdentityGet
	handleRepoIdentityPut        = gitx.HandleRepoIdentityPut

	// --- git_remote.go ---
	bitbucketAccount         = gitx.BitbucketAccount
	bitbucketAuthHeader      = gitx.BitbucketAuthHeader
	bitbucketConnectCheck    = gitx.BitbucketConnectCheck
	bitbucketErrText         = gitx.BitbucketErrText
	bitbucketGetStatus       = gitx.BitbucketGetStatus
	escapeRepoPath           = gitx.EscapeRepoPath
	firstLine                = gitx.FirstLine
	githubAccount            = gitx.GithubAccount
	githubHeaders            = gitx.GithubHeaders
	handleListRemoteBranches = gitx.HandleListRemoteBranches
	handleListRemoteRepos    = gitx.HandleListRemoteRepos
	refreshBitbucketAndRetry = gitx.RefreshBitbucketAndRetry
	validRemoteRepo          = gitx.ValidRemoteRepo

	// --- git_oauth.go ---
	handleBitbucketStore = gitx.HandleBitbucketStore
	loadGitOAuthBridge   = gitx.LoadGitOAuthBridge
	refreshBitbucket     = gitx.RefreshBitbucket
	refreshOAuthViaCP    = gitx.RefreshOAuthViaCP
	removeBitbucketOAuth = gitx.RemoveBitbucketOAuth
)
