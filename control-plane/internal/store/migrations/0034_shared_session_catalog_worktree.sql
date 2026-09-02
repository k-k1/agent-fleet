-- 受信側の共有セッション一覧をプロジェクト/worktreeツリーで表示するための追加情報。
ALTER TABLE shared_session_catalog ADD COLUMN worktree INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shared_session_catalog ADD COLUMN parent TEXT NOT NULL DEFAULT '';
