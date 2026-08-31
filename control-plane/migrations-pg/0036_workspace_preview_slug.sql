-- プレビュー用サブドメインの slug（docs/81 §4・ADR 0062 決定 3）。
-- Workspace の起動ごとに引き直し、停止で空に戻す。URL は {slug}-{port}.{PreviewDomain}。
--
-- なぜ列が要るか: プレビューのリクエストは Host しか手がかりを持たない（Console の
-- セッションも ?tenant= も付いてこない別オリジンからの生アクセス）ので、slug から
-- Workspace を引けることが経路の前提そのものになる。
--
-- 一意制約は「空でない slug」にだけ張る。停止中の Workspace は全部 '' を持つので、
-- 素の UNIQUE では 2 つ目の停止で衝突する。
ALTER TABLE workspace ADD COLUMN preview_slug TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_preview_slug
  ON workspace(preview_slug) WHERE preview_slug <> '';
