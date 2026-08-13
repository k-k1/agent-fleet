-- 受信側で「進行中 / 入力待ち / 質問中 / プラン待ち」を出すための、Agent が返す live
-- state(working | idle | question | plan | permission | blocked | compacting)。state 列は
-- running/stopped(生存)なので別列にする。停止中のセッションでは空。
ALTER TABLE shared_session_catalog ADD COLUMN activity TEXT NOT NULL DEFAULT '';
