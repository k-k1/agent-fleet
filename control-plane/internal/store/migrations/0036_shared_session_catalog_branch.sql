-- 受信側ツリーで作業コピーをブランチ名で見分けられるようにする(所有者側の repo 行と
-- 同じ表記)。worktree のフォルダ名は "<base>@<ランダム slug>" なので、ブランチが無いと
-- どの作業なのか共有先には分からない。同期のたびに現在のブランチで上書きされる。
ALTER TABLE shared_session_catalog ADD COLUMN branch TEXT NOT NULL DEFAULT '';
