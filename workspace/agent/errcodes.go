package main

// Console 側で和文ローカライズされるエラーコード（console/src/core/api/client.ts の
// ERR_TEXT と対、docs/23 P0-3）。ここの文字列を変えると Console の文言解決が落ちて
// developer メッセージへフォールバックする — 変更は必ず両側同時に。CP 側の対は
// control-plane/errcodes.go（quota_sessions）。
const (
	errCodeSessionsRunning       = "sessions_running"
	errCodeSessionsRunningDelete = "sessions_running_delete"
	errCodeWorktreeDirty         = "worktree_dirty"
	errCodeWorktreeRemoveFailed  = "worktree_remove_failed"
	errCodeHasWorktrees          = "has_worktrees"
)
