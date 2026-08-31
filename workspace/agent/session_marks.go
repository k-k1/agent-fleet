package main

// 転写のマーカー（docs/log/69 / ADR 0050）。会話の「ここ」に線を引き、その印を共有先にも
// 同じ位置で見せるための、セッション単位の注釈ストア。
//
// アンカーは W3C Web Annotation の TextQuoteSelector 相当（引用文字列 + 出現番号）で、
// 数える範囲は「1つの part の描画後テキスト」に閉じている。転写行の序数（Idx）は
// compaction で動くので使わない（transcript.Idx のコメント参照）。詳細は docs/log/69 §69.3。
//
// ⚠️ Kind をここで検査するのは、共有 DTO が落としている座標（cwd / file / 差分）を
// マーカーの Quote が迂回して運び出さないため（docs/log/69 §69.4）。塗れるのは共有 DTO を
// 素通りする本文フィールドだけで、その不変条件を「保存時に」効かせておくと、後から
// Console 側で塗れる場所が広がっても漏れ出さない。

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// markQuoteMaxRunes は保存する引用の長さ。アンカーとしてはこれで十分で、一覧も潰れない
	// （Console の planComments.MAX_QUOTE と同じ値）。
	markQuoteMaxRunes = 300
	markTurnMaxBytes  = 256
	// markAuthorMaxBytes は login id（メールアドレス）の上限。
	markAuthorMaxBytes = 320
	// markMaxPerSession / markMaxPerAuthor は暴走ループとひとりの塗り潰しの歯止め。
	markMaxPerSession = 200
	markMaxPerAuthor  = 100
	markMaxParts      = 4096
)

// markProseKinds は「共有 DTO を素通りする本文」を描く part の kind。ここに無い kind の
// 上には印を付けられない（docs/log/69 §69.4）。"" はターン本文（Turn.Text）を指す。
var markProseKinds = map[string]bool{
	"":       true,
	"text":   true,
	"plan":   true,
	"answer": true,
	"output": true,
	"prompt": true,
}

var markColors = map[string]bool{"yellow": true, "green": true, "blue": true, "pink": true}

type sessionMark struct {
	ID string `json:"id"`
	// Turn は元ターンの安定した同一性 — Agent 自身の anchorId か、それが無い kind では
	// Console が本文から作る "h:<hash>"。Idx（行序数）は compaction で動くので使わない。
	Turn string `json:"turn"`
	// Part は元ターンの中での part 番号。ブロック（Group）相対ではない — groupTurns() は
	// 連続ターンの parts を連結するので、ブロック相対の番号は tail 窓の切れ目で両側がずれる。
	Part int `json:"part"`
	// Kind はその part の kind（"" = ターン本文）。markProseKinds の検査に使う。
	Kind  string `json:"kind"`
	Quote string `json:"quote"`
	Nth   int    `json:"nth"`
	Color string `json:"color"`
	// Author は空＝このセッションの所有者。共有先が付けたものは CP が認証済みの login id で
	// 上書きして渡す（Agent は呼び出し元の身元を知らない）。
	Author    string `json:"author,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func sessionMarksPath(name string) string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "session-marks", name+".json")
}

func readSessionMarks(name string) ([]*sessionMark, error) {
	b, err := os.ReadFile(sessionMarksPath(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*sessionMark
	if err := json.Unmarshal(b, &list); err != nil {
		// 壊れた JSON でマーカーが読めないだけで会話を止めない（補助機能なので空から始める）。
		return nil, nil
	}
	out := list[:0]
	for _, m := range list {
		if m != nil && m.ID != "" && m.Quote != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// writeSessionMarks は list をそのまま保存する。空なら空配列を残さずファイルごと消す。
func writeSessionMarks(name string, list []*sessionMark) error {
	path := sessionMarksPath(name)
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// removeSessionMarks はセッションが消えた（＝スロット名が再利用され得る）ときの後片付け。
// 残すと次にそのスロットへ入ったセッションに前のセッションの印が出る。
func removeSessionMarks(name string) {
	_ = os.Remove(sessionMarksPath(name))
}

var errMarkExists = errors.New("mark already exists")

// addSessionMark は create-only。同じ id の再送は保存済みをそのまま返すので、呼び出し側が
// id を採番していれば再試行が二重に印を増やさない（＝Operation-ID 台帳を持ち出さずに冪等）。
func addSessionMark(name string, m *sessionMark) (*sessionMark, error) {
	list, err := readSessionMarks(name)
	if err != nil {
		return nil, err
	}
	byAuthor := 0
	for _, x := range list {
		if x.ID == m.ID {
			return x, errMarkExists
		}
		if x.Author == m.Author {
			byAuthor++
		}
	}
	if len(list) >= markMaxPerSession {
		return nil, errors.New("too many marks in this session")
	}
	if byAuthor >= markMaxPerAuthor {
		return nil, errors.New("too many marks by this author")
	}
	m.CreatedAt = time.Now().UnixMilli()
	list = append(list, m)
	if err := writeSessionMarks(name, list); err != nil {
		return nil, err
	}
	return m, nil
}

// deleteSessionMark は id の印を消す。author が非空なら「その作成者のものだけ」に限る —
// 共有先は自分の印しか消せない（CP がその login id を渡す）。所有者経由の削除は author を
// 渡さないので、誰の印でも消せる。
func deleteSessionMark(name, id, author string) error {
	list, err := readSessionMarks(name)
	if err != nil {
		return err
	}
	out := list[:0]
	found := false
	for _, m := range list {
		if m.ID == id {
			if author != "" && m.Author != author {
				return os.ErrPermission
			}
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return os.ErrNotExist
	}
	return writeSessionMarks(name, out)
}

// validMarkID — 呼び出し側が採番する（create-only の冪等性のため）ので、形だけ検査する。
func validMarkID(id string) bool {
	if !strings.HasPrefix(id, "mk_") || len(id) < 6 || len(id) > 40 {
		return false
	}
	for _, r := range id[3:] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// handleSessionMarks — GET 一覧 / POST 追加 / DELETE 削除。
//
// 呼び出し元は2種類ある: 所有者の Console（CP の /api/sessions/... プロキシ経由）と、
// 共有先（CP の /api/shared-sessions/{id}/marks 経由）。Agent は身元を知らないので、
// 共有先の login id は CP が body / query に載せて渡す。CP はクライアントの申告を必ず
// 上書きするため、ここでは「渡された値を保存する」だけでよい。
func handleSessionMarks(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := readSessionMarks(name)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "marks_read", err.Error())
			return
		}
		if list == nil {
			list = []*sessionMark{}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"marks": list})
	case http.MethodPost:
		var body struct {
			ID     string `json:"id"`
			Turn   string `json:"turn"`
			Part   int    `json:"part"`
			Kind   string `json:"kind"`
			Quote  string `json:"quote"`
			Nth    int    `json:"nth"`
			Color  string `json:"color"`
			Author string `json:"author"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		m := &sessionMark{
			ID:     strings.TrimSpace(body.ID),
			Turn:   strings.TrimSpace(body.Turn),
			Part:   body.Part,
			Kind:   body.Kind,
			Quote:  truncateRunes(body.Quote, markQuoteMaxRunes),
			Nth:    body.Nth,
			Color:  body.Color,
			Author: truncateRunes(strings.TrimSpace(body.Author), markAuthorMaxBytes),
		}
		if !validMarkID(m.ID) {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_id", "id must be mk_<hex>")
			return
		}
		if m.Turn == "" || len(m.Turn) > markTurnMaxBytes {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_turn", "turn anchor is missing or too long")
			return
		}
		if m.Part < 0 || m.Part > markMaxParts || m.Nth < 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_anchor", "part/nth out of range")
			return
		}
		if m.Quote == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_quote_empty", "quote is empty")
			return
		}
		if !markProseKinds[m.Kind] {
			// 座標を持つ part（ツール行のパス・差分）の上には印を置かせない。ここを緩めると
			// 共有 DTO が落としているはずのパスが Quote として共有先へ渡る（docs/log/69 §69.4）。
			httpx.WriteErr(w, http.StatusBadRequest, "mark_kind_not_markable", "this part kind cannot be marked")
			return
		}
		if !markColors[m.Color] {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_color", "unknown color")
			return
		}
		saved, err := addSessionMark(name, m)
		if errors.Is(err, errMarkExists) {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"mark": saved}) // 再送は no-op
			return
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_write", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"mark": saved})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_id_missing", "id is required")
			return
		}
		err := deleteSessionMark(name, id, strings.TrimSpace(r.URL.Query().Get("author")))
		if errors.Is(err, os.ErrPermission) {
			httpx.WriteErr(w, http.StatusForbidden, "mark_not_yours", "this mark belongs to someone else")
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			httpx.WriteErr(w, http.StatusInternalServerError, "mark_delete", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.WriteErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
