package main

// 共有セッションの転写マーカー（docs/log/69 / ADR 0050）。
//
// 印の実体は所有者 Workspace（Agent の session-marks）にあり、CP は毎回 ACL を評価して
// 中継するだけ。転写・引き継ぎ提案とまったく同じ形で、本文の複製は CP DB に持たない。
//
// 書き込み（RW の共有先が自分で線を引く）は、docs/log/59 §2 の「提案 → 所有者の承認」には
// 載せない。あの承認が要るのは提案が**エージェントを動かす**（他人のセッションとトークンを
// 消費する副作用がある）からで、マーカーはエージェントに届かず転写にも入らない。1 本引く
// たびに承認待ちへ積むのは承認の意味を薄めるだけである（ADR 0050 決定 4）。
//
// ⚠️ 代わりに厳しくしてあるのが 2 点:
//   - author はクライアントの申告を採らない。CP が認証済みの login id で必ず上書きする。
//   - 消せるのは自分の印だけ。Agent へ author を渡し、Agent 側が突き合わせる。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// markProseKinds — 印を置ける part の kind。Agent 側の同名の表と、Console の
// MARKABLE_KINDS と同じ内容でなければならない。
//
// ⚠️ ここで検査するのは、共有 DTO が落としている座標（cwd / file / 差分）を印の quote が
// 迂回して運び出さないため（docs/log/69 §69.4）。塗る場所の制限は Console と Agent が既に
// 掛けているが、中継の出口にも置いておくと、片側が緩んだだけでは漏れない。
var markProseKinds = map[string]bool{"": true, "text": true, "plan": true, "answer": true, "output": true, "prompt": true}

const sharedMarkMaxBytes = 8 << 10

func (a sessionShareAPI) marks(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	switch r.Method {
	case http.MethodGet:
		a.marksRead(w, r, mv)
	case http.MethodPost:
		a.marksAdd(w, r, ident, mv)
	case http.MethodDelete:
		a.marksDelete(w, r, ident, mv)
	default:
		writeAPIErr(w, &apiError{http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"})
	}
}

func (a sessionShareAPI) marksRead(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), false)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	// 転写と同じバケツで数える。共有先1人あたりの所有者 Workspace への往復を、
	// 面ごとに増やさないため。
	if !a.allowRead(mv.MembershipID + ":" + c.ID) {
		writeAPIErr(w, &apiError{http.StatusTooManyRequests, "shared_read_rate_limited", "too many shared transcript reads"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	payload, status, e := a.ownerGET(r.Context(), res, "/sessions/"+url.PathEscape(c.Name)+"/marks")
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, sharedMarksDTO(payload))
}

func (a sessionShareAPI) marksAdd(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, sharedMarkMaxBytes+1))
	if err != nil || len(raw) > sharedMarkMaxBytes {
		writeAPIErr(w, &apiError{413, "mark_too_large", "mark exceeds 8 KiB"})
		return
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		writeAPIErr(w, &apiError{400, "bad_mark", "invalid mark"})
		return
	}
	kind, _ := in["kind"].(string)
	if !markProseKinds[kind] {
		writeAPIErr(w, &apiError{400, "mark_kind_not_markable", "this part kind cannot be marked"})
		return
	}
	// 申告された author は捨てて、認証済みの login id を刻む。共有先が所有者や別の
	// 共有先になりすませてはいけない。
	who := strings.TrimSpace(ident.Email)
	if who == "" {
		writeAPIErr(w, &apiError{403, "mark_no_identity", "no login id to attribute this mark to"})
		return
	}
	in["author"] = who
	body, _ := json.Marshal(in)
	payload, status, e := a.ownerSend(r.Context(), res, http.MethodPost, "/sessions/"+url.PathEscape(c.Name)+"/marks", body)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	one, _ := payload["mark"].(map[string]any)
	writeJSON(w, status, map[string]any{"mark": sharedMarkDTO(one)})
}

func (a sessionShareAPI) marksDelete(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	who := strings.TrimSpace(ident.Email)
	if id == "" || who == "" {
		writeAPIErr(w, &apiError{400, "mark_id_missing", "id is required"})
		return
	}
	// author を必ず添える = Agent 側が「自分の印だけ」に絞る。所有者経路（/api/sessions/…）は
	// これを渡さないので、所有者は誰の印でも消せる。
	q := url.Values{"id": {id}, "author": {who}}
	_, status, e := a.ownerSend(r.Context(), res, http.MethodDelete,
		"/sessions/"+url.PathEscape(c.Name)+"/marks?"+q.Encode(), nil)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if status == http.StatusForbidden {
		writeAPIErr(w, &apiError{403, "mark_not_yours", "this mark belongs to someone else"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownerSend relays a write to the owner's Agent. Like ownerGET the caller owns
// authorization, the DTO and the rate limit; unlike it, an empty body is normal
// (DELETE) and the decoded payload may be empty (204).
func (a sessionShareAPI) ownerSend(ctx context.Context, res *resolved, method, path string, body []byte) (map[string]any, int, *apiError) {
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, res.rt.Endpoint()+path, rdr)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return nil, 0, &apiError{502, "owner_workspace_unreachable", "owner workspace is unreachable"}
	}
	defer resp.Body.Close()
	var payload map[string]any
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return payload, resp.StatusCode, nil
}

// sharedMarksDTO — マーカーの allowlist。表示に要るのは位置（turn/part/kind/quote/nth）、
// 見た目（color）、そして「誰がいつ」（author/created_at）だけ。座標は元々持っていないが、
// kind が本文以外の印はここでも落とす（docs/log/69 §69.4 の二重の網）。
func sharedMarksDTO(payload map[string]any) map[string]any {
	items, _ := payload["marks"].([]any)
	out := make([]any, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		if !markProseKinds[kind] {
			continue
		}
		out = append(out, sharedMarkDTO(m))
	}
	return map[string]any{"marks": out}
}

func sharedMarkDTO(m map[string]any) map[string]any {
	q := map[string]any{}
	if m == nil {
		return q
	}
	copyAllowed(q, m, "id", "turn", "part", "kind", "quote", "nth", "color", "author", "created_at")
	return q
}
