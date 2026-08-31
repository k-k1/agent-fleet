-- メンバーへの引き継ぎ（docs/log/77 / ADR 0057）。
--
-- 所有者 A が「この続きをやってほしい」を、既にそのセッションを共有している B へ差し出す。
-- 実体は動かず、ここに載るのは**文章と git の座標**だけ。起動は B が自分の権限で自分の
-- Workspace に対して行うので、この表に「実行」の痕跡（lease / operation id）は無い。
--
-- ⚠️ session_share_proposal と混同しないこと。あちらは **B → A**（共有先が操作を提案し所有者が
-- 承認する）で、こちらは **A → B**（所有者が差し出し共有先が承認する）＝**方向が逆**である。
-- owner / proposer の意味が入れ替わるので同じ表には載せない（ADR 0057 決定 9）。
--
-- catalog_id に紐付けるのが ACL 連動の要: 共有解除・RO 降格・アーカイブ・セッション削除は
-- すべて shared_session_catalog / session_share 側の既存経路で起き、この表はそこへ従属する。
--
-- 本文（title / prompt）は共有 RW 提案と同格に扱う（他人の作業内容）。tenant key custodian が
-- ある環境では暗号化して置き、辞退・失効・撤回時に ciphertext を空にして消す。
--
-- repo_remote / branch / head_sha は Agent が git に聞いた事実で、モデルにも Console にも
-- 書かせない（ADR 0057 決定 5）。B 側はこの remote で自分の作業コピーを同定する。
CREATE TABLE session_handoff_offer (
    id                      TEXT PRIMARY KEY,
    tenant_id               TEXT NOT NULL REFERENCES tenant(id),
    catalog_id              TEXT NOT NULL REFERENCES shared_session_catalog(id) ON DELETE CASCADE,
    owner_membership_id     TEXT NOT NULL REFERENCES membership(id),
    recipient_membership_id TEXT NOT NULL REFERENCES membership(id),
    title                   TEXT NOT NULL DEFAULT '',
    ciphertext              TEXT NOT NULL DEFAULT '',
    key_ref                 TEXT NOT NULL DEFAULT '',
    repo_remote             TEXT NOT NULL DEFAULT '',
    branch                  TEXT NOT NULL DEFAULT '',
    head_sha                TEXT NOT NULL DEFAULT '',
    source_session_name     TEXT NOT NULL DEFAULT '',
    source_session_kind     TEXT NOT NULL DEFAULT '',
    status                  TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending','accepted','declined','withdrawn','expired')),
    created_at              TEXT NOT NULL,
    expires_at              TEXT NOT NULL,
    decided_at              TEXT NOT NULL DEFAULT '',
    accepted_session_name   TEXT NOT NULL DEFAULT '',
    CHECK (owner_membership_id <> recipient_membership_id)
);
CREATE INDEX idx_handoff_offer_recipient ON session_handoff_offer(recipient_membership_id, status);
CREATE INDEX idx_handoff_offer_owner ON session_handoff_offer(owner_membership_id, status);
-- 未処理は 1 セッションにつき 1 件・宛先 1 人（ADR 0057 決定 10）。複数人へ同時に投げられると
-- 早い者勝ち＋二重作業になる。件数を数えて弾くのではなく**部分ユニーク索引**で不可能にする
-- ——数えて弾く形は 2 つの要求が同時に来ると両方通る。
CREATE UNIQUE INDEX idx_handoff_offer_one_pending ON session_handoff_offer(catalog_id) WHERE status='pending';
