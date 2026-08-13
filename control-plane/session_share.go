package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	shareProposalMaxBytes = 32 << 10
	shareProposalMaxOpen  = 20
	shareOwnerLease       = 2 * time.Minute
	// How long a reconciled owner inventory is trusted before authorizeCatalog syncs
	// again. Bounds only deletion-detection lag, never ACL evaluation — see freshCatalog.
	shareCatalogTTL = 10 * time.Second
)

type shareReadWindow struct {
	at time.Time
	n  int
}

type sessionShareAPI struct {
	memberAuth
	readMu *sync.Mutex
	reads  map[string]shareReadWindow
	// syncedAt: when this owner's live inventory was last reconciled. See freshCatalog.
	syncedAt map[string]time.Time
}

func newSessionShareAPI(m *manager) sessionShareAPI {
	return sessionShareAPI{memberAuth: memberAuth{m}, readMu: &sync.Mutex{},
		reads: map[string]shareReadWindow{}, syncedAt: map[string]time.Time{}}
}

// freshCatalog reports whether this owner's inventory was reconciled recently enough to
// skip another round trip, and stamps it when it isn't.
//
// syncCatalogLocked costs two HTTP calls into the owner's Workspace Agent
// (/sessions/catalog + /repos) plus a full ReplaceSharedSessionCatalog, under a per-owner
// mutex. Running that on EVERY transcript poll — once per recipient every couple of
// seconds — was the dominant cost of reading a shared session, and the reason the first
// screenful took so long to appear.
//
// This throttles ONLY the inventory reconciliation. Authorization is unaffected:
// authorizeCatalog still evaluates the share rules against the database on every single
// request, so revoking a share still takes effect immediately. What can now lag, by at
// most shareCatalogTTL, is noticing that the owner deleted the session or working copy
// upstream — the same staleness the periodic list refresh already lives with.
func (a sessionShareAPI) freshCatalog(owner string) bool {
	a.readMu.Lock()
	defer a.readMu.Unlock()
	now := time.Now()
	if at, ok := a.syncedAt[owner]; ok && now.Sub(at) < shareCatalogTTL {
		return true
	}
	if len(a.syncedAt) > 10_000 {
		for k, at := range a.syncedAt {
			if now.Sub(at) >= shareCatalogTTL {
				delete(a.syncedAt, k)
			}
		}
	}
	a.syncedAt[owner] = now
	return false
}

// invalidateCatalog forces the next authorizeCatalog to reconcile, for the paths that
// just changed the inventory themselves and must not read their own stale stamp.
func (a sessionShareAPI) invalidateCatalog(owner string) {
	a.readMu.Lock()
	defer a.readMu.Unlock()
	delete(a.syncedAt, owner)
}

func (a sessionShareAPI) allowRead(key string) bool {
	a.readMu.Lock()
	defer a.readMu.Unlock()
	now := time.Now()
	if len(a.reads) > 10_000 {
		for k, candidate := range a.reads {
			if now.Sub(candidate.at) >= time.Minute {
				delete(a.reads, k)
			}
		}
	}
	win := a.reads[key]
	if win.at.IsZero() || now.Sub(win.at) >= time.Minute {
		win = shareReadWindow{at: now}
	}
	if win.n >= 120 {
		a.reads[key] = win
		return false
	}
	win.n++
	a.reads[key] = win
	return true
}

// repoInfo は /repos の1行から ACL・表示に必要な分だけを抜いたもの。parentWC は
// worktree の親(ベース)作業コピーの workingCopyId で、repo 共有がプロジェクト全体を
// 覆うための鍵になる(docs/59 §1)。
type repoInfo struct {
	worktree bool
	parent   string
	parentWC string
	branch   string
}

func (a sessionShareAPI) syncCatalog(ctx context.Context, res *resolved) error {
	lock := a.mgr.shareLockFor(res.mv.MembershipID)
	lock.Lock()
	defer lock.Unlock()
	_, err := a.syncCatalogLocked(ctx, res)
	return err
}

// syncCatalogLocked は在庫を突合し、ついでに読んだ /repos の内容を返す。呼び出し側
// (put)が共有対象の実在確認で同じ情報を要るため、Agent への往復をもう1回増やさない。
// 返り値は Agent が /repos を答えられなかった場合 nil(= 在庫不明)。
func (a sessionShareAPI) syncCatalogLocked(ctx context.Context, res *resolved) (map[string]repoInfo, error) {
	if res.rt.State(ctx) != "running" {
		return nil, nil
	}
	body, err := agentText(ctx, res.rt, http.MethodGet, "/sessions/catalog", nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Sessions []sessionWire `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		return nil, err
	}
	// worktree/parent(受信側のプロジェクト/worktreeツリー表示用、docs/59)は working
	// copy 単位の情報なので /repos から working_copy_id をキーに引く。取得できなくても
	// (一時的な Agent 到達不可等)catalog 自体の同期は継続する — この付随情報が
	// 新規行なら空のままになるだけで、下の失効プルーニング(byWorkingCopy==nil)と
	// 同じ「transient error では何もしない」方針を踏襲する。
	var byWorkingCopy map[string]repoInfo
	if reposBody, reposErr := agentText(ctx, res.rt, http.MethodGet, "/repos", nil); reposErr == nil {
		var inventory struct {
			Repos []struct {
				Name          string `json:"name"`
				WorkingCopyID string `json:"workingCopyId"`
				Worktree      bool   `json:"worktree"`
				Parent        string `json:"parent"`
				Branch        string `json:"branch"`
			} `json:"repos"`
		}
		if json.Unmarshal([]byte(reposBody), &inventory) == nil {
			byWorkingCopy = map[string]repoInfo{}
			// Parent はフォルダ名なので、まず名前→workingCopyId を作ってから引き直す。
			wcByName := map[string]string{}
			for _, repo := range inventory.Repos {
				if !repo.Worktree {
					wcByName[repo.Name] = repo.WorkingCopyID
				}
			}
			for _, repo := range inventory.Repos {
				info := repoInfo{worktree: repo.Worktree, parent: repo.Parent, branch: repo.Branch}
				if repo.Worktree {
					info.parentWC = wcByName[repo.Parent]
				}
				byWorkingCopy[repo.WorkingCopyID] = info
			}
		}
	}
	now := nowTS()
	rows := make([]SharedSessionCatalog, 0, len(wire.Sessions))
	for _, s := range wire.Sessions {
		state := "stopped"
		if s.Alive {
			state = "running"
		}
		info := byWorkingCopy[s.WorkingCopyID]
		rows = append(rows, SharedSessionCatalog{ID: newID(), WorkspaceID: res.ws.ID,
			OwnerMembershipID: res.mv.MembershipID, Name: s.Name, Kind: s.Kind, Dir: s.Dir, Repo: s.Repo,
			WorkingCopyID: s.WorkingCopyID, Title: s.Title, Label: s.Label, CreatedAt: s.CreatedAt,
			State: state, Archived: s.Archived, LastSeen: now, Worktree: info.worktree, Parent: info.parent,
			ParentWorkingCopyID: info.parentWC, Branch: info.branch})
	}
	if err := a.mgr.store.ReplaceSharedSessionCatalog(ctx, res.ws.ID, res.mv.MembershipID, rows); errors.Is(err, errSessionShareOwnerBusy) {
		return byWorkingCopy, nil // another CP replica is applying an already-authorized operation
	} else if err != nil {
		return byWorkingCopy, err
	}
	// A deleted working copy terminates its dynamic rule. Only prune after a
	// successful live inventory, never on a transient Agent error.
	if byWorkingCopy == nil {
		return nil, nil
	}
	shares, _ := a.mgr.store.ListSessionSharesByOwner(ctx, res.mv.MembershipID)
	for _, share := range shares {
		if _, live := byWorkingCopy[share.ScopeKey]; share.ScopeType != "session" && !live {
			_ = a.mgr.store.DeleteSessionSharesByScope(ctx, res.mv.MembershipID, share.ScopeType, share.ScopeKey)
		}
	}
	return byWorkingCopy, nil
}

func memberByUserKey(ctx context.Context, st Store, tenantID, key string) (MemberInfo, bool, error) {
	rows, err := st.ListMembersByTenant(ctx, tenantID)
	if err != nil {
		return MemberInfo{}, false, err
	}
	for _, m := range rows {
		if m.UserKey == key {
			return m, true, nil
		}
	}
	return MemberInfo{}, false, nil
}

// searchRecipients — 同一テナントの共有先候補を email/user_key の部分一致で検索する。
// 一般メンバーが呼べる(withMembership、管理者権限は問わない) — Console の共有作成
// combobox はここで解決した user_key だけを送るので、利用者は正規化ルール
// (sanitizeUser)を意識しない。
func (a sessionShareAPI) searchRecipients(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	rows, err := a.mgr.store.ListMembersByTenant(r.Context(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]string, 0, 20)
	for _, m := range rows {
		if m.MembershipID == mv.MembershipID {
			continue // 自分自身は候補から除外(share_self を事前に避ける)
		}
		if q != "" && !strings.Contains(strings.ToLower(m.Email), q) && !strings.Contains(strings.ToLower(m.UserKey), q) {
			continue
		}
		out = append(out, map[string]string{"userKey": m.UserKey, "email": m.Email})
		if len(out) >= 20 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (a sessionShareAPI) recipientKey(ctx context.Context, tenantID, membershipID string) string {
	rows, _ := a.mgr.store.ListMembersByTenant(ctx, tenantID)
	for _, m := range rows {
		if m.MembershipID == membershipID {
			return m.UserKey
		}
	}
	return ""
}

func shareDTO(s SessionShare, recipient string) map[string]any {
	return map[string]any{"id": s.ID, "recipientUserKey": recipient, "scope": map[string]string{
		"type": s.ScopeType, "key": s.ScopeKey}, "permission": s.Permission, "createdAt": s.CreatedAt, "updatedAt": s.UpdatedAt}
}

func (a sessionShareAPI) listOwned(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	rows, err := a.mgr.store.ListSessionSharesByOwner(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, shareDTO(s, a.recipientKey(r.Context(), mv.TenantID, s.RecipientMembershipID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

type sharePutBody struct {
	RecipientUserKey string `json:"recipientUserKey"`
	Scope            struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	} `json:"scope"`
	Permission string `json:"permission"`
}

func validSharePermission(p string) bool { return p == "ro" || p == "rw" }
func validShareScope(s string) bool      { return s == "session" || s == "repo" || s == "worktree" }

func (a sessionShareAPI) put(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in sharePutBody
	if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in) != nil || !validSharePermission(in.Permission) ||
		!validShareScope(in.Scope.Type) || strings.TrimSpace(in.Scope.Key) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_share", "invalid share request"})
		return
	}
	recipient, ok, err := memberByUserKey(r.Context(), a.mgr.store, res.mv.TenantID, strings.TrimSpace(in.RecipientUserKey))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "member_not_found", "recipient is not an active tenant member"})
		return
	}
	if recipient.MembershipID == res.mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "share_self", "cannot share with yourself"})
		return
	}
	shareLock := a.mgr.shareLockFor(res.mv.MembershipID)
	shareLock.Lock()
	defer shareLock.Unlock()
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to create a share"})
		return
	}
	repos, err := a.syncCatalogLocked(r.Context(), res)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to create a share"})
		return
	}
	catalog, err := a.mgr.store.ListSharedSessionCatalogByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	found := false
	if in.Scope.Type == "session" {
		for _, c := range catalog {
			// アーカイブ済みは共有先に出さない(docs/59 §1)ので、共有対象にも選べない。
			if c.Name == in.Scope.Key && !c.Archived {
				found = true
				break
			}
		}
	} else if info, ok := repos[in.Scope.Key]; ok {
		found = info.worktree == (in.Scope.Type == "worktree")
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "share_target_not_found", "share target not found"})
		return
	}
	now := nowTS()
	row := SessionShare{ID: newID(), TenantID: res.mv.TenantID, OwnerMembershipID: res.mv.MembershipID,
		RecipientMembershipID: recipient.MembershipID, ScopeType: in.Scope.Type, ScopeKey: in.Scope.Key,
		Permission: in.Permission, CreatedAt: now, UpdatedAt: now}
	status := http.StatusCreated
	owned, err := a.mgr.store.ListSessionSharesByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, existing := range owned {
		if existing.RecipientMembershipID == row.RecipientMembershipID && existing.ScopeType == row.ScopeType && existing.ScopeKey == row.ScopeKey {
			row.ID = existing.ID
			row.CreatedAt = existing.CreatedAt
			status = http.StatusOK
			break
		}
	}
	if err := a.mgr.store.PutSessionShare(r.Context(), row); err != nil {
		if errors.Is(err, errSessionShareOwnerBusy) {
			writeAPIErr(w, &apiError{409, "share_operation_in_progress", "an approved Agent operation is in progress; retry the share change"})
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: res.mv.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "session.share", Target: in.Scope.Type + ":" + in.Scope.Key, Detail: "permission=" + in.Permission, HTTPStatus: http.StatusCreated, At: now})
	writeJSON(w, status, shareDTO(row, recipient.UserKey))
}

func (a sessionShareAPI) patch(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	var in struct {
		Permission string `json:"permission"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil || !validSharePermission(in.Permission) {
		writeAPIErr(w, &apiError{400, "bad_share", "invalid permission"})
		return
	}
	shareLock := a.mgr.shareLockFor(mv.MembershipID)
	shareLock.Lock()
	defer shareLock.Unlock()
	s, ok, err := a.mgr.store.GetSessionShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || s.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "share not found"})
		return
	}
	s.Permission = in.Permission
	s.UpdatedAt = nowTS()
	changed, err := a.mgr.store.UpdateSessionSharePermission(r.Context(), s.ID, mv.MembershipID, s.Permission, s.UpdatedAt)
	if err != nil {
		if errors.Is(err, errSessionShareOwnerBusy) {
			writeAPIErr(w, &apiError{409, "share_operation_in_progress", "an approved Agent operation is in progress; retry the share change"})
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		writeAPIErr(w, &apiError{404, "not_found", "share not found"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID, Action: "session.share.permission", Target: s.ID, Detail: "permission=" + in.Permission, HTTPStatus: 200, At: nowTS()})
	writeJSON(w, 200, shareDTO(s, a.recipientKey(r.Context(), mv.TenantID, s.RecipientMembershipID)))
}
func (a sessionShareAPI) delete(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	shareLock := a.mgr.shareLockFor(mv.MembershipID)
	shareLock.Lock()
	defer shareLock.Unlock()
	s, ok, err := a.mgr.store.GetSessionShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || s.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "share not found"})
		return
	}
	if err := a.mgr.store.DeleteSessionShare(r.Context(), s.ID, mv.MembershipID); err != nil {
		if errors.Is(err, errSessionShareOwnerBusy) {
			writeAPIErr(w, &apiError{409, "share_operation_in_progress", "an approved Agent operation is in progress; retry the share change"})
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID, Action: "session.unshare", Target: s.ID, HTTPStatus: 200, At: nowTS()})
	writeJSON(w, 200, map[string]any{"deleted": s.ID})
}

// effectivePermission — この catalog 行に効く最も強い権限("" なら共有されていない)。
//
// repo 規則はプロジェクト全体を覆う: ベース作業コピー直下のセッションに加えて、その
// 配下 linked worktree のセッションにも効く(docs/59 §1)。所有者の作業は基本 worktree
// 側で進むため、ベースだけを対象にすると「リポジトリを共有した」のに共有先には古い
// セッションしか見えない、という結果になっていた。worktree 規則は従来どおり、その
// worktree 1つだけの範囲。空の scope_key はここでも弾く: workingCopyId を持てない
// 作業コピー(marker を作れない)や親不明の行は "" を持つので、万一空の規則が入ると
// 無関係なセッションまで丸ごと巻き込んでしまう(put も空キーは拒否している)。
func effectivePermission(shares []SessionShare, c SharedSessionCatalog) string {
	p := ""
	for _, s := range shares {
		if s.OwnerMembershipID != c.OwnerMembershipID || s.ScopeKey == "" {
			continue
		}
		match := (s.ScopeType == "session" && s.ScopeKey == c.Name) ||
			(s.ScopeType == "worktree" && s.ScopeKey == c.WorkingCopyID) ||
			(s.ScopeType == "repo" && (s.ScopeKey == c.WorkingCopyID || s.ScopeKey == c.ParentWorkingCopyID))
		if match {
			if s.Permission == "rw" {
				return "rw"
			}
			p = "ro"
		}
	}
	return p
}

func (a sessionShareAPI) ownerResolved(ctx context.Context, owner string) (*resolved, *apiError) {
	iid, ok, err := a.mgr.store.IdentityIDForMembership(ctx, owner)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		return nil, &apiError{404, "not_found", "owner not found"}
	}
	return a.mgr.resolveByMembership(ctx, iid, owner)
}

func (a sessionShareAPI) listReceived(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	shares, err := a.mgr.store.ListSessionSharesByRecipient(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	owners := map[string]bool{}
	for _, s := range shares {
		owners[s.OwnerMembershipID] = true
	}
	members, _ := a.mgr.store.ListMembersByTenant(r.Context(), mv.TenantID)
	ownerKeys := map[string]string{}
	for _, m := range members {
		ownerKeys[m.MembershipID] = m.UserKey
	}
	out := []any{}
	for owner := range owners {
		// 所有者ごとに1回だけ解決する。State() は docker inspect 相当の外部呼び出しで、
		// この一覧は受信側のタブごとに5秒間隔で叩かれるため、以前の「解決2回 + State
		// 2〜3回 + 毎回フル同期」は所有者 Workspace への定常負荷そのものだった。
		res, e := a.ownerResolved(r.Context(), owner)
		wsState := "stopped"
		if e == nil {
			wsState = res.rt.State(r.Context())
		}
		// 在庫の再突合は authorizeCatalog と同じ per-owner throttle に乗せる。同期は
		// 所有者の share ロックを握ったまま Agent へ2往復するので、5秒ポーリングごとに
		// 走らせると共有の作成/解除(同じロックを取る)がその裏で待たされていた。ここで
		// 遅れるのは「所有者が消した/アーカイブしたことに気付く」までの最大 10 秒だけで、
		// ACL は下の effectivePermission が毎回 DB から評価する。
		if e == nil && wsState == "running" && !a.freshCatalog(owner) {
			if err := a.syncCatalog(r.Context(), res); err != nil {
				a.invalidateCatalog(owner) // 失敗を「同期済み」として数えない
			}
		}
		catalog, _ := a.mgr.store.ListSharedSessionCatalogByOwner(r.Context(), owner)
		for _, c := range catalog {
			// アーカイブ済みは所有者が畳んだ会話。共有規則は残す(復元すればまた見える)が、
			// 共有先の一覧には出さない — 出し続けると「所有者が消したはずの古いセッションが
			// 延々と残る」ように見える(docs/59 §1)。
			if c.Archived {
				continue
			}
			p := effectivePermission(shares, c)
			if p == "" {
				continue
			}
			out = append(out, map[string]any{"id": c.ID, "ownerUserKey": ownerKeys[owner], "name": c.Name, "kind": c.Kind, "repo": c.Repo, "workingCopyId": c.WorkingCopyID, "title": c.Title, "label": c.Label, "createdAt": c.CreatedAt, "state": c.State, "permission": p, "workspaceState": wsState, "worktree": c.Worktree, "parent": c.Parent,
				// ブランチは作業コピーの表示ラベル(所有者側の repo 行と同じ)。転写 DTO が落とす
				// turn の branch(会話の描画に不要な座標)とは別物で、これが無いと worktree は
				// ランダム slug のフォルダ名でしか見分けられない。
				"branch": c.Branch})
		}
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

func (a sessionShareAPI) authorizeCatalog(ctx context.Context, mv MembershipView, id string, wantRW bool) (SharedSessionCatalog, *resolved, *apiError) {
	c, ok, err := a.mgr.store.GetSharedSessionCatalog(ctx, id)
	if err != nil {
		return c, nil, internalErr(err)
	}
	if !ok {
		return c, nil, &apiError{404, "not_found", "shared session not found"}
	}
	res, e := a.ownerResolved(ctx, c.OwnerMembershipID)
	if e != nil {
		return c, nil, e
	}
	// Direct catalog URLs must not bypass live inventory reconciliation. A deleted
	// session or working copy removes its catalog/rule before ACL evaluation.
	// Throttled per owner (freshCatalog): the ACL check below still runs every request,
	// so this bounds only how long an upstream deletion can go unnoticed.
	if res.rt.State(ctx) == "running" && !a.freshCatalog(c.OwnerMembershipID) {
		if err := a.syncCatalog(ctx, res); err != nil {
			a.invalidateCatalog(c.OwnerMembershipID) // failed sync must not count as fresh
			return c, nil, &apiError{502, "owner_workspace_unreachable", "owner workspace inventory is unavailable"}
		}
		c, ok, err = a.mgr.store.GetSharedSessionCatalog(ctx, id)
		if err != nil {
			return c, nil, internalErr(err)
		}
		if !ok {
			return c, nil, &apiError{404, "not_found", "shared session not found"}
		}
	}
	shares, err := a.mgr.store.ListSessionSharesByRecipient(ctx, mv.MembershipID)
	if err != nil {
		return c, nil, internalErr(err)
	}
	p := effectivePermission(shares, c)
	if p == "" || (wantRW && p != "rw") {
		return c, nil, &apiError{404, "not_found", "shared session not found"}
	}
	// アーカイブ中は一覧から外れる(listReceived)ので、直リンクの読みも同じ扱いにする。
	// 権限判定の後に置く: 権限が無い相手には従来どおり 404 で、存在の有無すら答えない。
	if c.Archived {
		return c, nil, &apiError{http.StatusConflict, "owner_session_archived", "the owner archived this session"}
	}
	return c, res, nil
}

func (a sessionShareAPI) messages(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), false)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if !a.allowRead(mv.MembershipID + ":" + c.ID) {
		writeAPIErr(w, &apiError{http.StatusTooManyRequests, "shared_read_rate_limited", "too many shared transcript reads"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	path := "/sessions/" + url.PathEscape(c.Name) + "/messages"
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, res.rt.Endpoint()+path, nil)
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		writeAPIErr(w, &apiError{502, "owner_workspace_unreachable", "owner workspace is unreachable"})
		return
	}
	defer resp.Body.Close()
	var payload map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload) != nil {
		writeAPIErr(w, &apiError{502, "bad_owner_response", "invalid owner response"})
		return
	}
	payload = sharedTranscriptDTO(payload)
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, resp.StatusCode, payload)
}

// sharedTranscriptDTO is intentionally an allowlist, not a list of known path
// spellings. Agent kinds have historically emitted file, files, file_path and
// filePath; a future structured coordinate must stay private by default. Prose,
// tool summaries/output and edit text are conversation content and remain visible.
func sharedTranscriptDTO(payload map[string]any) map[string]any {
	out := map[string]any{}
	copyAllowed(out, payload, "cursor", "reset", "status", "alive", "firstLine", "hasMore")
	messages, _ := payload["messages"].([]any)
	shared := make([]any, 0, len(messages))
	for _, raw := range messages {
		turn, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		t := map[string]any{}
		// "compact" is a display flag (this turn is claude's auto-compaction summary), not
		// a coordinate: it carries no path and no Workspace location, and the summary text
		// it labels already flows through "text". Without it the recipient renders a
		// compaction summary as an ordinary giant turn.
		copyAllowed(t, turn, "role", "text", "source", "model", "effort", "ctxWindow", "sidechain",
			"compact", "inTok", "outTok", "cacheRead", "cacheCreate", "ts", "endTs", "idx", "anchorId")
		parts, _ := turn["parts"].([]any)
		visibleParts := make([]any, 0, len(parts))
		for _, partRaw := range parts {
			part, ok := partRaw.(map[string]any)
			if !ok {
				continue
			}
			p := map[string]any{}
			copyAllowed(p, part, "kind", "text", "tool", "info", "cause", "output", "prompt", "agentType",
				"status", "model", "answer", "plan", "caption", "qid")
			p["questions"] = sharedQuestions(part["questions"])
			p["edits"] = sharedEdits(part["edits"])
			visibleParts = append(visibleParts, p)
		}
		t["parts"] = visibleParts
		shared = append(shared, t)
	}
	out["messages"] = shared
	return out
}

func copyAllowed(dst, src map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}

func sharedQuestions(raw any) []any {
	items, _ := raw.([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		question, ok := item.(map[string]any)
		if !ok {
			continue
		}
		q := map[string]any{}
		copyAllowed(q, question, "id", "header", "question", "multiSelect")
		options, _ := question["options"].([]any)
		visibleOptions := make([]any, 0, len(options))
		for _, optionRaw := range options {
			option, ok := optionRaw.(map[string]any)
			if !ok {
				continue
			}
			o := map[string]any{}
			copyAllowed(o, option, "label", "description", "preview")
			visibleOptions = append(visibleOptions, o)
		}
		q["options"] = visibleOptions
		out = append(out, q)
	}
	return out
}

func sharedEdits(raw any) []any {
	items, _ := raw.([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		edit, ok := item.(map[string]any)
		if !ok {
			continue
		}
		e := map[string]any{}
		copyAllowed(e, edit, "old", "new")
		out = append(out, e)
	}
	return out
}

func (a sessionShareAPI) sealProposal(ctx context.Context, tenant string, body []byte) (string, string, error) {
	if a.mgr.custodian == nil {
		return base64.StdEncoding.EncodeToString(body), "", nil
	}
	ct, err := a.mgr.custodian.Wrap(ctx, tenant, body)
	return ct, tenant, err
}
func (a sessionShareAPI) openProposal(ctx context.Context, p SessionShareProposal) ([]byte, error) {
	if p.KeyRef == "" {
		return base64.StdEncoding.DecodeString(p.Ciphertext)
	}
	return a.mgr.custodian.Unwrap(ctx, p.KeyRef, p.Ciphertext)
}

var proposalActions = map[string]string{"turn": "turn", "respond": "respond", "answer-question": "answer-question", "plan-respond": "plan-respond"}

func (a sessionShareAPI) propose(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, shareProposalMaxBytes+1))
	if err != nil || len(raw) > shareProposalMaxBytes {
		writeAPIErr(w, &apiError{413, "proposal_too_large", "proposal exceeds 32 KiB"})
		return
	}
	var in struct {
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &in) != nil || proposalActions[in.Action] == "" || len(in.Payload) == 0 {
		writeAPIErr(w, &apiError{400, "bad_proposal", "invalid proposal"})
		return
	}
	if err := a.mgr.store.ExpireSessionShareProposals(r.Context(), c.OwnerMembershipID, nowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	ct, kr, err := a.sealProposal(r.Context(), mv.TenantID, in.Payload)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	now := time.Now().UTC()
	p := SessionShareProposal{ID: newID(), TenantID: mv.TenantID, CatalogID: c.ID, OwnerMembershipID: c.OwnerMembershipID, ProposerMembershipID: mv.MembershipID, Action: in.Action, Ciphertext: ct, KeyRef: kr, Status: "pending", CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)}
	created, err := a.mgr.store.CreateSessionShareProposalLimited(r.Context(), p, shareProposalMaxOpen)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !created {
		writeAPIErr(w, &apiError{429, "too_many_proposals", "too many pending proposals"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "session.share.proposal.create", Target: c.Name, Detail: "action=" + p.Action, HTTPStatus: http.StatusCreated, At: nowTS()})
	writeJSON(w, 201, map[string]any{"id": p.ID, "status": p.Status, "expiresAt": p.ExpiresAt})
}

func (a sessionShareAPI) listProposals(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	w.Header().Set("Cache-Control", "private, no-store")
	if err := a.mgr.store.ExpireSessionShareProposals(r.Context(), mv.MembershipID, nowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	rows, err := a.mgr.store.ListSessionShareProposalsByOwner(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := []any{}
	for _, p := range rows {
		body := json.RawMessage(nil)
		if p.Ciphertext != "" {
			body, _ = a.openProposal(r.Context(), p)
		}
		out = append(out, map[string]any{"id": p.ID, "sessionId": p.CatalogID, "proposerUserKey": a.recipientKey(r.Context(), mv.TenantID, p.ProposerMembershipID), "action": p.Action, "payload": body, "status": p.Status, "createdAt": p.CreatedAt, "expiresAt": p.ExpiresAt})
	}
	writeJSON(w, 200, map[string]any{"proposals": out})
}

func (a sessionShareAPI) reject(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	p, ok, err := a.mgr.store.GetSessionShareProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || p.OwnerMembershipID != mv.MembershipID || p.Status != "pending" {
		writeAPIErr(w, &apiError{404, "not_found", "proposal not found"})
		return
	}
	if changed, err := a.mgr.store.TransitionSessionShareProposal(r.Context(), p.ID, "pending", "rejected", ident.ID, nowTS(), true); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if !changed {
		writeAPIErr(w, &apiError{http.StatusConflict, "proposal_already_decided", "proposal was already decided"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "session.share.proposal.reject", Target: p.CatalogID, Detail: "proposer=" + p.ProposerMembershipID + " action=" + p.Action, HTTPStatus: http.StatusOK, At: nowTS()})
	writeJSON(w, 200, map[string]any{"status": "rejected"})
}

func (a sessionShareAPI) approve(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	p, ok, err := a.mgr.store.GetSessionShareProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || p.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "proposal not found"})
		return
	}
	res, e := a.ownerResolved(r.Context(), p.OwnerMembershipID)
	if e != nil {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	workspaceLock := a.mgr.startLockFor(res.ws.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	shareLock := a.mgr.shareLockFor(p.OwnerMembershipID)
	shareLock.Lock()
	defer shareLock.Unlock()
	p, ok, err = a.mgr.store.GetSessionShareProposal(r.Context(), p.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || p.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "proposal not found"})
		return
	}
	applyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if p.Status == "processing" {
		releaseFence, fenceErr := a.mgr.acquireWorkspaceOperationFence(applyCtx, res.ws.ID, res.rt)
		if fenceErr != nil {
			writeAPIErr(w, &apiError{409, "share_operation_in_progress", "workspace lifecycle operation is still quiescing"})
			return
		}
		defer releaseFence()
		state, status := lookupAgentShareOperation(res.rt, p.ID)
		if state == "applied" && status < http.StatusBadRequest {
			changed, err := a.mgr.store.FinalizeSessionShareProposal(r.Context(), p.ID, p.OwnerMembershipID, ident.ID, nowTS())
			if err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
			if changed {
				writeJSON(w, 200, map[string]any{"status": "approved", "reconciled": true})
				return
			}
		}
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the operation was claimed; its outcome cannot be retried safely"})
		return
	}
	if p.Status != "pending" {
		writeAPIErr(w, &apiError{409, "proposal_already_decided", "proposal was already decided"})
		return
	}
	if _, err := a.syncCatalogLocked(r.Context(), res); err != nil {
		writeAPIErr(w, &apiError{502, "owner_workspace_unreachable", "owner workspace inventory is unavailable"})
		return
	}
	body, err := a.openProposal(r.Context(), p)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Do not bind the durable claim/Agent call to the browser connection. Claiming
	// is a short transaction; the per-owner mutex (not a DB lock/connection) keeps
	// ACL and catalog changes from crossing the authorized Agent operation.
	claimNow := time.Now().UTC()
	claimed, catalog, state, err := a.mgr.store.ClaimSessionShareProposal(applyCtx, p.ID, mv.MembershipID, ident.ID,
		claimNow.Format(time.RFC3339), claimNow.Add(shareOwnerLease).Format(time.RFC3339))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	switch state {
	case "claimed":
	case "expired":
		writeAPIErr(w, &apiError{409, "share_changed", "proposal expired or RW share is no longer active"})
		return
	case "processing":
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the operation was already claimed; its outcome cannot be retried safely"})
		return
	case "busy":
		writeAPIErr(w, &apiError{409, "share_operation_in_progress", "another approved Agent operation is in progress"})
		return
	default:
		writeAPIErr(w, &apiError{404, "not_found", "proposal or shared session not found"})
		return
	}
	// Lock order is owner DB lease (acquired by Claim) → Postgres advisory fence
	// → adapter host fence. This matches lifecycle operations and prevents both a
	// cross-replica pause gap and an advisory/lease deadlock.
	releaseFence, err := a.mgr.acquireWorkspaceOperationFence(applyCtx, res.ws.ID, res.rt)
	if err != nil {
		writeAPIErr(w, &apiError{409, "share_operation_in_progress", "workspace lifecycle operation is still quiescing"})
		return
	}
	defer releaseFence()
	path := "/sessions/" + url.PathEscape(catalog.Name) + "/" + proposalActions[claimed.Action]
	if err := agentShareOperation(applyCtx, res.rt, path, body, claimed.ID); err != nil {
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the operation result is unknown and will not be retried: " + err.Error()})
		return
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer finalizeCancel()
	changed, err := a.mgr.store.FinalizeSessionShareProposal(finalizeCtx, claimed.ID, claimed.OwnerMembershipID, ident.ID, nowTS())
	if err != nil {
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the Agent applied the operation but Control Plane could not record the result: " + err.Error()})
		return
	}
	if !changed {
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the Agent applied the operation but the proposal result could not be finalized"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID, Action: "session.share.proposal.approve", Target: p.CatalogID, Detail: "proposer=" + p.ProposerMembershipID + " action=" + p.Action, HTTPStatus: 200, At: nowTS()})
	writeJSON(w, 200, map[string]any{"status": "approved"})
}

func agentShareOperation(ctx context.Context, rt Runtime, path string, body []byte, operationID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.Endpoint()+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Fleet-Operation-ID", operationID)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusBadRequest {
		return &agentHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(payload))}
	}
	return nil
}

func lookupAgentShareOperation(rt Runtime, operationID string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rt.Endpoint()+"/share-operations/"+url.PathEscape(operationID), nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	var result struct {
		State      string `json:"state"`
		StatusCode int    `json:"statusCode"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil {
		return "", 0
	}
	return result.State, result.StatusCode
}
