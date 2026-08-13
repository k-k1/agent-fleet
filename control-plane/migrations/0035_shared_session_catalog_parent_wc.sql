-- repo 共有をプロジェクト全体(ベース作業コピー＋その配下 worktree)へ広げるため、
-- 各行に「親(ベース)作業コピーの workingCopyId」を持たせる。parent(フォルダ名)は
-- 表示用で、名前は付け替わりうるので ACL 判定には使わない。ベース直下のセッションは空。
ALTER TABLE shared_session_catalog ADD COLUMN parent_working_copy_id TEXT NOT NULL DEFAULT '';
