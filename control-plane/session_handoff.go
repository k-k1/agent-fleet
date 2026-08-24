package main

// メンバーへの引き継ぎ（docs/77 / ADR 0057）の API。
//
// 所有者 A が「この続きをやってほしい」を、**既にそのセッションを共有している** B へ差し出す。
// 受け取った B は自分の Console から自分の Workspace にセッションを立てる。
//
// ⚠️ この面には A → B の「実行」が一切無い。CP が所有者 Agent を叩くのは offer を作る瞬間の
// 座標取得（GET /sessions/{name}/handoff-context）1 回だけで、そこに副作用は無い。だから
// 共有 RW 提案（session_share.go）が必要とした owner lease・冪等 ledger・二重実行防止は
// ここには存在しない ——「越境するのは文章と座標だけ」という決定（ADR 0057 決定 1/3）が
// そのまま実装量の差になっている。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// handoffPromptMaxBytes は引き継ぎ本文の上限。Agent 側の提案（handoffProposalMaxBytes）と
	// 同じ 64 KiB —— 差し出す前に手元にあった本文がそのまま通らないのは事故になる。
	handoffPromptMaxBytes = 64 << 10
	handoffTitleMaxBytes  = 512
	// handoffOfferTTL は受領待ちの寿命。共有 RW 提案の 24 時間より長いのは、宛先が
	// **人**だからである（席を外している、休みを取っている、が普通に起きる）。
	handoffOfferTTL = 7 * 24 * time.Hour
)

type sessionHandoffAPI struct {
	memberAuth
	// share は共有 API の**同じインスタンス**。在庫の同期スロットル（syncedAt）と読み取り
	// レートのバケツはそこが持っているので、別インスタンスを作ると引き継ぎ面だけが
	// 所有者 Workspace への往復を独自に増やす。
	share sessionShareAPI
}

func newSessionHandoffAPI(m *manager, share sessionShareAPI) sessionHandoffAPI {
	return sessionHandoffAPI{memberAuth: memberAuth{m}, share: share}
}

// writeHandoffErr は詳細（ゲートの理由や現在の状態）を載せたエラー。writeAPIErr は
// code/message しか返さないが、この面は「なぜ止めたか」を Console が出し分ける。
func writeHandoffErr(w http.ResponseWriter, status int, code, message string, detail map[string]any) {
	e := map[string]any{"code": code, "message": message}
	for k, v := range detail {
		e[k] = v
	}
	writeJSON(w, status, map[string]any{"error": e})
}

// handoffOfferBody は差し出しの入力。宛先はセッションではなく**人**（userKey）で、座標は
// 受け取らない —— repo / branch / HEAD は CP が所有者 Agent に聞く（ADR 0057 決定 5）。
type handoffOfferBody struct {
	SessionName      string `json:"sessionName"`
	RecipientUserKey string `json:"recipientUserKey"`
	Title            string `json:"title"`
	Prompt           string `json:"prompt"`
	// AckWarning は「未コミットの変更があるが承知の上で送る」。Blocked は覆せない。
	AckWarning bool `json:"ackWarning"`
}

func (a sessionHandoffAPI) seal(ctx context.Context, tenant string, body []byte) (string, string, error) {
	if a.mgr.custodian == nil {
		return base64.StdEncoding.EncodeToString(body), "", nil
	}
	ct, err := a.mgr.custodian.Wrap(ctx, tenant, body)
	return ct, tenant, err
}

func (a sessionHandoffAPI) open(ctx context.Context, o SessionHandoffOffer) string {
	if o.Ciphertext == "" {
		return ""
	}
	if o.KeyRef == "" {
		b, err := base64.StdEncoding.DecodeString(o.Ciphertext)
		if err != nil {
			return ""
		}
		return string(b)
	}
	b, err := a.mgr.custodian.Unwrap(ctx, o.KeyRef, o.Ciphertext)
	if err != nil {
		return ""
	}
	return string(b)
}

// handoffOfferDTO は所有者向け（本文なし）。A の手元には元の提案があるので、台帳は状態が読めれば足りる。
func handoffOfferDTO(o SessionHandoffOffer, recipientKey string) map[string]any {
	return map[string]any{
		"id": o.ID, "sessionId": o.CatalogID, "sessionName": o.SourceSessionName,
		"recipientUserKey": recipientKey, "title": o.Title, "status": o.Status,
		"branch": o.Branch, "repoRemote": o.RepoRemote, "headSha": o.HeadSha,
		"createdAt": o.CreatedAt, "expiresAt": o.ExpiresAt, "decidedAt": o.DecidedAt,
		"acceptedSessionName": o.AcceptedSessionName,
	}
}

// catalogForOwnedSession は所有者自身の catalog 行を名前で引く。ok=false は「その行が無い」で、
// **エラーではない**（呼び出し側が意味を決める。読みは「まだ誰にも共有していない」、
// 書きは 404）。
//
// ⚠️ sync の頻度で性格が変わる。`syncCatalogLocked` は所有者 Agent への往復 2 本
// （`/sessions/catalog` と `/repos`）で、`/repos` は作業コピーごとに git を回すため
// **worktree が増えるほど重い**。共有の読み取りが `freshCatalog` で間引いているのはこれが
// 理由（session_share.go の注記）で、宛先一覧のような読みで毎回走らせるとモーダルが
// 「読み込み中」のまま止まって見える。だから読みは**同じスロットルに乗せる**。
//
// ⚠️ 逆に、同期を丸ごと省くことはできない。repo/worktree スコープの共有は**後から作られた
// セッションにも動的に効く**（docs/59 §1）ので、catalog に行が無いことは「共有されていない」
// の証明にならない —— 省くと、共有済みの新しいセッションに「まだ誰にも共有していません」と
// 表示してしまう。
func (a sessionHandoffAPI) catalogForOwnedSession(r *http.Request, res *resolved, name string, exact bool) (SharedSessionCatalog, bool, *apiError) {
	if exact || !a.share.freshCatalog(res.mv.MembershipID, shareCatalogTTL) {
		lock := a.mgr.shareLockFor(res.mv.MembershipID)
		lock.Lock()
		_, err := a.share.syncCatalogLocked(r.Context(), res)
		lock.Unlock()
		if err != nil {
			a.share.invalidateCatalog(res.mv.MembershipID) // 失敗した同期を「新鮮」に数えない
			return SharedSessionCatalog{}, false, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"}
		}
	}
	rows, err := a.mgr.store.ListSharedSessionCatalogByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		return SharedSessionCatalog{}, false, internalErr(err)
	}
	for _, c := range rows {
		if c.Name == name && !c.Archived {
			return c, true, nil
		}
	}
	return SharedSessionCatalog{}, false, nil
}

// recipientsFor は「このセッションを見られる人」＝宛先候補。**共有 ACL の逆引き**であり、
// テナント名簿ではない（ADR 0057 決定 2: 名簿は誰の文脈にも入れない）。
func (a sessionHandoffAPI) recipientsFor(ctx context.Context, mv MembershipView, c SharedSessionCatalog) ([]map[string]string, error) {
	shares, err := a.mgr.store.ListSessionSharesByOwner(ctx, mv.MembershipID)
	if err != nil {
		return nil, err
	}
	members, err := a.mgr.store.ListMembersByTenant(ctx, mv.TenantID)
	if err != nil {
		return nil, err
	}
	byID := map[string]MemberInfo{}
	for _, m := range members {
		byID[m.MembershipID] = m
	}
	byRecipient := map[string][]SessionShare{}
	for _, s := range shares {
		byRecipient[s.RecipientMembershipID] = append(byRecipient[s.RecipientMembershipID], s)
	}
	out := []map[string]string{}
	for id, rules := range byRecipient {
		// ro でも引き継げる。引き継ぎに要るのは会話が読めることで、操作を提案できることではない。
		if effectivePermission(rules, c) == "" {
			continue
		}
		m, ok := byID[id]
		if !ok {
			continue // 除名済み。ListMembersByTenant は active しか返さない
		}
		out = append(out, map[string]string{"userKey": m.UserKey, "email": m.Email})
	}
	return out, nil
}

// recipients — GET /api/sessions/{name}/handoff-recipients。差し出す前に A が宛先を選ぶための
// 一覧と、push ゲートの判定（docs/77 §77.5）を同時に返す。ゲートを送信時だけに置くと、A は
// 長い本文を書き終えてから初めて弾かれる。
func (a sessionHandoffAPI) recipients(w http.ResponseWriter, r *http.Request, res *resolved) {
	name := r.PathValue("name")
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"})
		return
	}
	c, found, e := a.catalogForOwnedSession(r, res, name, false)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	// 「まだ誰にも共有していない」は**正常な状態**なのでエラーにしない。エラーで返すと
	// 画面はそれを「取得に失敗した」としか言えず、利用者は次に何をすればよいか分からない
	// （実利用で最初に踏まれた）。空の候補で答え、先に共有する導線は Console 側が出す。
	members := []map[string]string{}
	if found {
		var err error
		if members, err = a.recipientsFor(r.Context(), res.mv, c); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	ctx, _, e := a.handoffContext(r.Context(), res, name)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	pending := ""
	if found {
		if rows, err := a.mgr.store.ListSessionHandoffOffersByOwner(r.Context(), res.mv.MembershipID); err == nil {
			for _, o := range rows {
				if o.CatalogID == c.ID && o.Status == "pending" {
					pending = o.ID
					break
				}
			}
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"members": members, "context": ctx, "pendingOfferId": pending})
}

// handoffContext は所有者 Agent に作業コピーの状態を聞く。返り値の 2 つ目は生の map で、
// Console にはそのまま渡す（blocked / warning のトークンは Agent が組み立てる — 条件が
// 2 か所に分かれると必ずずれる）。
func (a sessionHandoffAPI) handoffContext(ctx context.Context, res *resolved, name string) (map[string]any, string, *apiError) {
	payload, status, e := a.share.ownerGET(ctx, res, "/sessions/"+url.PathEscape(name)+"/handoff-context")
	if e != nil {
		return nil, "", e
	}
	// ⚠️ 上流の 404 と 409 を混ぜない。「そんなセッションは無い」と「作業コピーが無いので
	// 引き継げない」は利用者の次の一手が違う（前者は名前が違う／消えた、後者は共有や
	// push の話ですらない）。混ぜると、消えたセッションに対して「作業コピーがありません」と
	// 出て原因が追えなくなる。
	switch {
	case status == http.StatusNotFound:
		return nil, "", &apiError{http.StatusNotFound, "not_found", "no such session"}
	case status >= 400:
		return nil, "", &apiError{http.StatusConflict, "handoff_no_working_copy", "session has no working copy to hand off"}
	}
	blocked, _ := payload["blocked"].(string)
	return payload, blocked, nil
}

// create — POST /api/session-handoff-offers。
func (a sessionHandoffAPI) create(w http.ResponseWriter, r *http.Request, res *resolved) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, handoffPromptMaxBytes+handoffTitleMaxBytes+4096))
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "handoff_too_large", "handoff is too large"})
		return
	}
	var in handoffOfferBody
	if json.Unmarshal(raw, &in) != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_handoff", "invalid handoff request"})
		return
	}
	in.Title, in.Prompt = strings.TrimSpace(in.Title), strings.TrimSpace(in.Prompt)
	switch {
	case in.Title == "" || in.Prompt == "":
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_handoff", "title and prompt are required"})
		return
	case len(in.Title) > handoffTitleMaxBytes || len(in.Prompt) > handoffPromptMaxBytes:
		writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "handoff_too_large", "handoff is too large"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"})
		return
	}
	c, found, e := a.catalogForOwnedSession(r, res, strings.TrimSpace(in.SessionName), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "handoff_session_not_shared", "session is not shared with anyone"})
		return
	}
	// 宛先は共有先に限る。名簿から引くのではなく ACL の逆引きに含まれるかで判定する。
	members, err := a.recipientsFor(r.Context(), res.mv, c)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	recipientKey := strings.TrimSpace(in.RecipientUserKey)
	allowed := false
	for _, m := range members {
		if m["userKey"] == recipientKey {
			allowed = true
			break
		}
	}
	if !allowed {
		writeAPIErr(w, &apiError{http.StatusNotFound, "handoff_recipient_not_shared", "recipient does not have this session shared"})
		return
	}
	recipient, ok, err := memberByUserKey(r.Context(), a.mgr.store, res.mv.TenantID, recipientKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || recipient.MembershipID == res.mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "handoff_recipient_not_shared", "recipient does not have this session shared"})
		return
	}
	// push ゲート。⚠️ CP が Agent に聞いた事実だけを見る。要求ボディの座標は受け取らない。
	hctx, blocked, e := a.handoffContext(r.Context(), res, c.Name)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if blocked != "" {
		writeHandoffErr(w, http.StatusConflict, "handoff_blocked", "the working copy is not in a handoff-able state", map[string]any{"reason": blocked, "context": hctx})
		return
	}
	if warn, _ := hctx["warning"].(string); warn != "" && !in.AckWarning {
		writeHandoffErr(w, http.StatusConflict, "handoff_warning", "the working copy has uncommitted changes", map[string]any{"reason": warn, "context": hctx})
		return
	}
	ct, kr, err := a.seal(r.Context(), res.mv.TenantID, []byte(in.Prompt))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	now := time.Now().UTC()
	str := func(k string) string { s, _ := hctx[k].(string); return s }
	o := SessionHandoffOffer{
		ID: newID(), TenantID: res.mv.TenantID, CatalogID: c.ID,
		OwnerMembershipID: res.mv.MembershipID, RecipientMembershipID: recipient.MembershipID,
		Title: in.Title, Ciphertext: ct, KeyRef: kr,
		RepoRemote: str("remote"), Branch: str("branch"), HeadSha: str("headSha"),
		SourceSessionName: c.Name, SourceSessionKind: c.Kind, Status: "pending",
		CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(handoffOfferTTL).Format(time.RFC3339),
	}
	created, err := a.mgr.store.CreateSessionHandoffOffer(r.Context(), o)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !created {
		writeAPIErr(w, &apiError{http.StatusConflict, "handoff_already_pending", "this session already has a pending handoff"})
		return
	}
	a.notifyOffered(o, res.mv.TenantSlug, c)
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: res.mv.TenantID,
		ActorKind: "user", ActorID: res.ident.ID, Action: "session.handoff.offer", Target: c.Name,
		Detail: "recipient=" + recipient.UserKey, HTTPStatus: http.StatusCreated, At: nowTS()})
	writeJSON(w, http.StatusCreated, handoffOfferDTO(o, recipient.UserKey))
}

// listOwned — GET /api/session-handoff-offers。A の台帳。通知は流れ物なので、後から辿れる
// 唯一の場所（docs/77 §77.10）。
func (a sessionHandoffAPI) listOwned(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	a.expireDue(r.Context())
	rows, err := a.mgr.store.ListSessionHandoffOffersByOwner(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	keys := a.userKeys(r.Context(), mv.TenantID)
	out := []any{}
	for _, o := range rows {
		out = append(out, handoffOfferDTO(o, keys[o.RecipientMembershipID]))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// listReceived — GET /api/session-handoff-offers/received。B の受信箱。**本文を返す**のは、
// 受け取るかどうかを決めるのに本文を読む必要があるからで、ここが唯一の本文の出口。
func (a sessionHandoffAPI) listReceived(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	a.expireDue(r.Context())
	rows, err := a.mgr.store.ListSessionHandoffOffersByRecipient(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	keys := a.userKeys(r.Context(), mv.TenantID)
	out := []any{}
	for _, o := range rows {
		d := handoffOfferDTO(o, keys[o.RecipientMembershipID])
		d["ownerUserKey"] = keys[o.OwnerMembershipID]
		d["prompt"] = a.open(r.Context(), o)
		d["sourceSessionKind"] = o.SourceSessionKind
		out = append(out, d)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

func (a sessionHandoffAPI) userKeys(ctx context.Context, tenantID string) map[string]string {
	out := map[string]string{}
	rows, err := a.mgr.store.ListMembersByTenant(ctx, tenantID)
	if err != nil {
		return out
	}
	for _, m := range rows {
		out[m.MembershipID] = m.UserKey
	}
	return out
}

// withdraw — DELETE /api/session-handoff-offers/{id}。所有者の撤回。
//
// A が「待ちきれず自分で起動する」ときも Console はこれを呼ぶ。⚠️ 起動と撤回の競合
// （同じ仕事が 2 つ走る）を閉じるのは pending → withdrawn の条件付き更新で、負けた側は
// 409 を受けて起動をやめる（ADR 0057 決定 6）。
func (a sessionHandoffAPI) withdraw(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || o.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "handoff offer not found"})
		return
	}
	changed, err := a.mgr.store.TransitionSessionHandoffOffer(r.Context(), o.ID, "pending", "withdrawn", nowTS(), "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		writeHandoffErr(w, http.StatusConflict, "handoff_already_decided", "the handoff was already decided", map[string]any{"status": a.statusOf(r.Context(), o.ID)})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff.withdraw", Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: nowTS()})
	writeJSON(w, http.StatusOK, map[string]any{"status": "withdrawn"})
}

func (a sessionHandoffAPI) statusOf(ctx context.Context, id string) string {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(ctx, id)
	if err != nil || !ok {
		return ""
	}
	return o.Status
}

// accept — POST /api/session-handoff-offers/{id}/accept。受け手が**起動できたあと**の事後申告。
//
// 起動そのものは B の Console が既存の POST /sessions で行う（ADR 0057 決定 3）。ここで
// 起動を代行しないのは、そうすると CP が他人の Workspace を操作することになり、この機能が
// 避けた構造そのものになるため。
func (a sessionHandoffAPI) accept(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	var in struct {
		SessionName string `json:"sessionName"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	a.decide(w, r, ident, mv, "accepted", strings.TrimSpace(in.SessionName))
}

// decline — POST /api/session-handoff-offers/{id}/decline。理由は求めない（ADR 0057 決定 8）。
func (a sessionHandoffAPI) decline(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	a.decide(w, r, ident, mv, "declined", "")
}

func (a sessionHandoffAPI) decide(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView, to, sessionName string) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// 権限が無い相手には 404。存在の有無すら答えないのは共有と同じ作法。
	if !ok || o.RecipientMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "handoff offer not found"})
		return
	}
	changed, err := a.mgr.store.TransitionSessionHandoffOffer(r.Context(), o.ID, "pending", to, nowTS(), sessionName)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		writeHandoffErr(w, http.StatusConflict, "handoff_already_decided", "the handoff was already decided", map[string]any{"status": a.statusOf(r.Context(), o.ID)})
		return
	}
	if to == "accepted" {
		a.notifyAccepted(o, mv.TenantID, sessionName)
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff." + to, Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: nowTS()})
	writeJSON(w, http.StatusOK, map[string]any{"status": to})
}

// expireDue は期限切れを失効させ、所有者へ 1 回だけ知らせる。一覧を読むついでに回すのは、
// この機能に専用のワーカーを増やさないため（失効が数分遅れても実害が無い）。
func (a sessionHandoffAPI) expireDue(ctx context.Context) {
	expired, err := a.mgr.store.ExpireSessionHandoffOffers(ctx, nowTS())
	if err != nil {
		return
	}
	for _, o := range expired {
		a.notify(o.OwnerMembershipID, "handoff-expired", "handoff-expired-"+o.ID+"-"+o.OwnerMembershipID,
			"session", o.SourceSessionName, o.SourceSessionKind, o.Title,
			map[string]any{"offerId": o.ID, "sessionName": o.SourceSessionName})
	}
}

// notifyOffered / notifyAccepted — docs/77 §77.9。
//
// ⚠️ CP から直接 InsertNotification する。既存のセッション通知は Agent のアウトボックスを
// drain する経路だが、引き継ぎは**送る側も受け取る側も Workspace が止まっている**場面が
// 主戦場なので、その経路では届かない。
func (a sessionHandoffAPI) notifyOffered(o SessionHandoffOffer, _ string, c SharedSessionCatalog) {
	title := o.Title
	if title == "" {
		title = c.Name
	}
	a.notify(o.RecipientMembershipID, "handoff-offer", "handoff-offer-"+o.ID+"-"+o.RecipientMembershipID,
		"shared-session", o.CatalogID, o.SourceSessionKind, title,
		map[string]any{"offerId": o.ID, "catalogId": o.CatalogID, "sessionName": o.SourceSessionName})
}

func (a sessionHandoffAPI) notifyAccepted(o SessionHandoffOffer, _ string, sessionName string) {
	a.notify(o.OwnerMembershipID, "handoff-accepted", "handoff-accepted-"+o.ID+"-"+o.OwnerMembershipID,
		"session", o.SourceSessionName, o.SourceSessionKind, o.Title,
		map[string]any{"offerId": o.ID, "sessionName": o.SourceSessionName, "acceptedSessionName": sessionName})
}

// notify は 1 通入れる。⚠️ InsertNotification の冪等は ON CONFLICT(event_id) で
// **membership を含まない**ので、eventID には必ず受信者を混ぜること（同じ id を 2 通に使うと
// 片方が黙って消える）。
func (a sessionHandoffAPI) notify(membershipID, kind, eventID, targetType, targetID, targetKind, displayName string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	_ = a.mgr.store.InsertNotification(context.Background(), Notification{
		EventID: eventID, MembershipID: membershipID, Kind: kind,
		TargetType: targetType, TargetID: targetID, TargetKind: targetKind,
		DisplayName: displayName, Payload: string(b), CreatedAt: nowTS(),
	})
}
