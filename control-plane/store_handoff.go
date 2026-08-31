package main

// メンバーへの引き継ぎ（docs/log/77 / ADR 0057）のストア。
//
// 共有 ACL の派生物なので、失効は `invalidateUnauthorizedShareDerivatives`（store_share.go）が
// 共有変更と同じトランザクションで行う。ここが持つのは作成・読み出し・状態遷移・期限切れだけで、
// **Agent への副作用が無い**のがこの機能の特徴 —— 起動は受け手が自分の Workspace に対して行うので、
// 共有 RW 提案が必要とした owner lease / 冪等 ledger はここには無い（ADR 0057 決定 3）。

import (
	"context"
	"database/sql"
	"strings"
)

const handoffOfferCols = `id,tenant_id,catalog_id,owner_membership_id,recipient_membership_id,title,
	ciphertext,key_ref,repo_remote,branch,head_sha,source_session_name,source_session_kind,
	status,created_at,expires_at,decided_at,accepted_session_name`

func scanHandoffOffer(row interface{ Scan(...any) error }) (SessionHandoffOffer, bool, error) {
	var r SessionHandoffOffer
	err := row.Scan(&r.ID, &r.TenantID, &r.CatalogID, &r.OwnerMembershipID, &r.RecipientMembershipID,
		&r.Title, &r.Ciphertext, &r.KeyRef, &r.RepoRemote, &r.Branch, &r.HeadSha,
		&r.SourceSessionName, &r.SourceSessionKind, &r.Status, &r.CreatedAt, &r.ExpiresAt,
		&r.DecidedAt, &r.AcceptedSessionName)
	if err == sql.ErrNoRows {
		return SessionHandoffOffer{}, false, nil
	}
	return r, err == nil, err
}

// CreateSessionHandoffOffer は offer を 1 件作る。created=false は「そのセッションには既に
// 未処理の引き継ぎがある」（ADR 0057 決定 10）。
//
// ⚠️ 件数を数えてから INSERT する形にはしていない。同時に 2 要求が来ると両方が「0 件」を見て
// 両方通る。**部分ユニーク索引に落とさせて**、その違反だけを created=false に翻訳する。
func (s *sqlStore) CreateSessionHandoffOffer(ctx context.Context, r SessionHandoffOffer) (bool, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_handoff_offer
		(id,tenant_id,catalog_id,owner_membership_id,recipient_membership_id,title,ciphertext,key_ref,
		 repo_remote,branch,head_sha,source_session_name,source_session_kind,status,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.TenantID, r.CatalogID, r.OwnerMembershipID, r.RecipientMembershipID, r.Title,
		r.Ciphertext, r.KeyRef, r.RepoRemote, r.Branch, r.HeadSha, r.SourceSessionName,
		r.SourceSessionKind, r.Status, r.CreatedAt, r.ExpiresAt)
	if err != nil && isUniqueViolation(err) {
		return false, nil
	}
	return err == nil, err
}

// isUniqueViolation は「一意制約に当たった」を両方言で判定する。ドライバの型に依存させると
// 片方の系列でだけ 500 になる（[[schema-dialect-parity]] と同じ壊れ方）ので、両方のメッセージを見る。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique constraint") || // sqlite (modernc) / postgres の一部
		strings.Contains(m, "duplicate key value") || // postgres
		strings.Contains(m, "constraint failed: unique") // sqlite の別表現
}

func (s *sqlStore) GetSessionHandoffOffer(ctx context.Context, id string) (SessionHandoffOffer, bool, error) {
	return scanHandoffOffer(s.db.QueryRowContext(ctx,
		`SELECT `+handoffOfferCols+` FROM session_handoff_offer WHERE id=?`, id))
}

func (s *sqlStore) listHandoffOffers(ctx context.Context, where string, arg string) ([]SessionHandoffOffer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+handoffOfferCols+` FROM session_handoff_offer WHERE `+where+` ORDER BY created_at DESC`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionHandoffOffer{}
	for rows.Next() {
		r, _, err := scanHandoffOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSessionHandoffOffersByOwner は A の台帳（出した引き継ぎの履歴）。通知を流れ物と決めた
// 以上、後から辿れるのはここだけなので、決着済みも返す（docs/log/77 §77.10）。
func (s *sqlStore) ListSessionHandoffOffersByOwner(ctx context.Context, membershipID string) ([]SessionHandoffOffer, error) {
	return s.listHandoffOffers(ctx, `owner_membership_id=?`, membershipID)
}

// ListSessionHandoffOffersByRecipient は B の受信箱。**未処理だけ**を返し、所有者がアーカイブ
// したセッションのものは外す —— 共有の一覧と同じ規律（docs/log/59 §1: 畳んだ会話は共有先に出さない）。
// 決着済みを返さないのは、受け取ったなら新しいセッションが証拠で、辞退したなら消えてよいため。
func (s *sqlStore) ListSessionHandoffOffersByRecipient(ctx context.Context, membershipID string) ([]SessionHandoffOffer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffOfferCols+` FROM session_handoff_offer
		WHERE recipient_membership_id=? AND status='pending'
		  AND EXISTS (SELECT 1 FROM shared_session_catalog c
		              WHERE c.id=session_handoff_offer.catalog_id AND c.archived=0)
		ORDER BY created_at DESC`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionHandoffOffer{}
	for rows.Next() {
		r, _, err := scanHandoffOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TransitionSessionHandoffOffer は from → to の条件付き更新。changed=false は「誰かが先に
// 決着させた」。accepted 以外は本文を消す —— 決着した引き継ぎの本文を CP に残す理由が無い
// （accepted は受け手が起動し直せるよう残す）。
func (s *sqlStore) TransitionSessionHandoffOffer(ctx context.Context, id, from, to, decidedAt, acceptedSessionName string) (bool, error) {
	body := `''`
	if to == "accepted" {
		body = `ciphertext`
	}
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer
		SET status=?,decided_at=?,accepted_session_name=?,ciphertext=`+body+`
		WHERE id=? AND status=?`, to, decidedAt, acceptedSessionName, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireSessionHandoffOffers は期限切れを失効させ、**失効した行を返す**。返すのは、docs/log/77 §77.9 の
// 「失効の直前に所有者へ 1 回だけ知らせる」を呼び出し側が組み立てるため —— 失効させてから
// 誰が対象だったかを問い直すと、その問い合わせは既に空になっている。
func (s *sqlStore) ExpireSessionHandoffOffers(ctx context.Context, now string) ([]SessionHandoffOffer, error) {
	due, err := s.listHandoffOffers(ctx, `status='pending' AND expires_at<=?`, now)
	if err != nil || len(due) == 0 {
		return nil, err
	}
	out := make([]SessionHandoffOffer, 0, len(due))
	for _, o := range due {
		changed, err := s.TransitionSessionHandoffOffer(ctx, o.ID, "pending", "expired", now, "")
		if err != nil {
			return out, err
		}
		if changed {
			out = append(out, o)
		}
	}
	return out, nil
}
