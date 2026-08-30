package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func marksCall(t *testing.T, name, method, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/sessions/" + name + "/marks"
	if query != "" {
		url += "?" + query
	}
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	handleSessionMarks(w, r)
	return w
}

func decodeMarks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var resp struct {
		Marks []map[string]any `json:"marks"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode marks: %v (body=%s)", err, body)
	}
	return resp.Marks
}

func TestSessionMarksRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "marks1"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	const body = `{"id":"mk_00112233","turn":"uuid-a","part":2,"kind":"text","quote":"the sentence","nth":1,"color":"yellow"}`
	if got := marksCall(t, name, http.MethodPost, "", body); got.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", got.Code, got.Body.String())
	}
	list := decodeMarks(t, marksCall(t, name, http.MethodGet, "", "").Body.String())
	if len(list) != 1 {
		t.Fatalf("want 1 mark, got %+v", list)
	}
	if list[0]["turn"] != "uuid-a" || list[0]["quote"] != "the sentence" || list[0]["color"] != "yellow" {
		t.Fatalf("mark round-tripped wrong: %+v", list[0])
	}
	if nth, _ := list[0]["nth"].(float64); nth != 1 {
		t.Fatalf("nth lost: %+v", list[0])
	}
	if created, _ := list[0]["created_at"].(float64); created == 0 {
		t.Fatalf("created_at not stamped: %+v", list[0])
	}
	// Author empty = the owner. It must not be serialised as a name.
	if _, ok := list[0]["author"]; ok {
		t.Fatalf("owner mark should carry no author: %+v", list[0])
	}

	// Re-sending the SAME id is a no-op, not a second mark. That is what lets the
	// caller retry without an Operation-ID ledger (ADR 0050 決定 4).
	if got := marksCall(t, name, http.MethodPost, "", body); got.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", got.Code, got.Body.String())
	}
	if list = decodeMarks(t, marksCall(t, name, http.MethodGet, "", "").Body.String()); len(list) != 1 {
		t.Fatalf("replay of the same id must not add a mark: %+v", list)
	}

	if got := marksCall(t, name, http.MethodDelete, "id=mk_00112233", ""); got.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", got.Code, got.Body.String())
	}
	if list = decodeMarks(t, marksCall(t, name, http.MethodGet, "", "").Body.String()); len(list) != 0 {
		t.Fatalf("mark survived delete: %+v", list)
	}
	// The empty list leaves no file behind.
	if _, err := os.Stat(sessionMarksPath(name)); !os.IsNotExist(err) {
		t.Fatalf("marks file should be gone, stat err=%v", err)
	}
}

// ⚠️ 共有 DTO が落としている座標（cwd / file / 差分）を Quote が迂回して運び出さないよう、
// 塗れる kind は保存時に閉じてある（docs/log/69 §69.4）。ここが緩むと、Console 側の描画が
// 広がっただけでパスが共有先へ渡る。
func TestSessionMarksRejectNonProseKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "marks2"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	for _, kind := range []string{"tool", "userfile", "delegation", "question", "thinking"} {
		body := `{"id":"mk_aabbccdd","turn":"uuid-a","part":0,"kind":"` + kind +
			`","quote":"/home/dev/repos/private/secret.ts","nth":0,"color":"yellow"}`
		got := marksCall(t, name, http.MethodPost, "", body)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("kind=%s should be rejected, status=%d body=%s", kind, got.Code, got.Body.String())
		}
	}
	for _, kind := range []string{"", "text", "plan", "answer", "output", "prompt"} {
		body := `{"id":"mk_` + strings.Repeat("a", 8) + `","turn":"t-` + kind +
			`","part":0,"kind":"` + kind + `","quote":"prose","nth":0,"color":"blue"}`
		got := marksCall(t, name, http.MethodPost, "", body)
		if got.Code != http.StatusOK {
			t.Fatalf("kind=%q should be markable, status=%d body=%s", kind, got.Code, got.Body.String())
		}
		// Same id every iteration: the replay path keeps this loop about kind only.
		_ = marksCall(t, name, http.MethodDelete, "id=mk_aaaaaaaa", "")
	}
}

func TestSessionMarksValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "marks3"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	cases := map[string]string{
		"bad id":        `{"id":"nope","turn":"t","part":0,"kind":"text","quote":"q","nth":0,"color":"yellow"}`,
		"no turn":       `{"id":"mk_00000001","turn":"","part":0,"kind":"text","quote":"q","nth":0,"color":"yellow"}`,
		"empty quote":   `{"id":"mk_00000002","turn":"t","part":0,"kind":"text","quote":"","nth":0,"color":"yellow"}`,
		"unknown color": `{"id":"mk_00000003","turn":"t","part":0,"kind":"text","quote":"q","nth":0,"color":"chartreuse"}`,
		"negative nth":  `{"id":"mk_00000004","turn":"t","part":0,"kind":"text","quote":"q","nth":-1,"color":"yellow"}`,
	}
	for label, body := range cases {
		if got := marksCall(t, name, http.MethodPost, "", body); got.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", label, got.Code, got.Body.String())
		}
	}

	// 長すぎる引用は弾かずに切る（アンカーとしては先頭で足りる）。
	long := strings.Repeat("あ", markQuoteMaxRunes+50)
	body := `{"id":"mk_00000005","turn":"t","part":0,"kind":"text","quote":"` + long + `","nth":0,"color":"green"}`
	if got := marksCall(t, name, http.MethodPost, "", body); got.Code != http.StatusOK {
		t.Fatalf("long quote status=%d body=%s", got.Code, got.Body.String())
	}
	list := decodeMarks(t, marksCall(t, name, http.MethodGet, "", "").Body.String())
	if q, _ := list[0]["quote"].(string); len([]rune(q)) != markQuoteMaxRunes {
		t.Fatalf("quote not truncated to %d runes: %d", markQuoteMaxRunes, len([]rune(q)))
	}
}

// 共有先は自分の印しか消せない（CP がその login id を author として渡す）。所有者経由の
// 削除は author を渡さないので、誰の印でも消せる。
func TestSessionMarksDeleteAuthorship(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "marks4"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	owner := `{"id":"mk_0000000a","turn":"t","part":0,"kind":"text","quote":"owner","nth":0,"color":"yellow"}`
	guest := `{"id":"mk_0000000b","turn":"t","part":0,"kind":"text","quote":"guest","nth":0,"color":"blue","author":"b@example.com"}`
	for _, b := range []string{owner, guest} {
		if got := marksCall(t, name, http.MethodPost, "", b); got.Code != http.StatusOK {
			t.Fatalf("seed status=%d body=%s", got.Code, got.Body.String())
		}
	}

	// 別人の印を消そうとしたら 403（共有先経路）。
	if got := marksCall(t, name, http.MethodDelete, "id=mk_0000000a&author=b@example.com", ""); got.Code != http.StatusForbidden {
		t.Fatalf("cross-author delete status=%d body=%s", got.Code, got.Body.String())
	}
	// 自分の印は消せる。
	if got := marksCall(t, name, http.MethodDelete, "id=mk_0000000b&author=b@example.com", ""); got.Code != http.StatusNoContent {
		t.Fatalf("own delete status=%d body=%s", got.Code, got.Body.String())
	}
	// 所有者経路（author なし）は誰の印でも消せる。
	if got := marksCall(t, name, http.MethodDelete, "id=mk_0000000a", ""); got.Code != http.StatusNoContent {
		t.Fatalf("owner delete status=%d body=%s", got.Code, got.Body.String())
	}
}

// ⚠️ セッション名はスロット名で再利用される。消し忘れると次にそのスロットへ入った
// セッションに前のセッションの印と引き継ぎカードが出る。
func TestRemoveSessionSideFilesOnDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const name = "marks5"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	body := `{"id":"mk_0000000c","turn":"t","part":0,"kind":"text","quote":"q","nth":0,"color":"pink"}`
	if got := marksCall(t, name, http.MethodPost, "", body); got.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := addHandoffProposal(name, "next", "title"); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}

	removeSessionSideFiles(name)

	if _, err := os.Stat(sessionMarksPath(name)); !os.IsNotExist(err) {
		t.Fatalf("marks file survived deletion, stat err=%v", err)
	}
	if _, err := os.Stat(handoffProposalPath(name)); !os.IsNotExist(err) {
		t.Fatalf("handoff file survived deletion, stat err=%v", err)
	}
}
