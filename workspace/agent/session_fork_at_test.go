package main

// POST /sessions/{name}/fork の分岐点まわり（docs/log/55）。ここで守りたいのは
// **「地点を指したのに会話まるごと分岐された」が起きないこと** で、そのために
// 解決できない分岐点はすべて 4xx で止める。逆に、分岐点なしの旧クライアントは
// これまでどおり通す（後方互換）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func forkReq(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/fork", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/fork", strings.NewReader(body))
	}
	r.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleForkSession(rec, r)
	return rec
}

// 分岐そのものに対応しない kind は、at の有無より先に断る。
func TestForkUnsupportedKind(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "cur1", Dir: t.TempDir(), Kind: session.KindCursor})
	rec := forkReq(t, "cur1", `{"at":"some-uuid"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fork_unsupported_kind") {
		t.Fatalf("body = %s; want fork_unsupported_kind", rec.Body.String())
	}
}

// 対応 kind でも CLI(TUI) ルートには分岐点を渡す口が無い（opencode/codex）。導線側でも
// 隠しているが、直叩きされたときにここで止まること。**「会話がまだ無い」より先に**この
// 理由が出ることも込みで押さえる: 経路が違うセッションに「会話を増やせ」と言っても直らない。
func TestForkAtRefusedOnCLIRoute(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{
		Name: "oc1", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI,
	})
	rec := forkReq(t, "oc1", `{"at":"msg_1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fork_at_unsupported") {
		t.Fatalf("body = %s; want fork_at_unsupported", rec.Body.String())
	}
}

// claude は managed driver を持たない。一律に managed を要求していた頃はここで永久に
// 弾かれていたので、TUI の claude が経路を理由に断られないことを固定する
// （会話が無いので別の理由では落ちる — それが fork_at_unsupported でないことを見る）。
func TestForkAtClaudeTUINotRefusedByRoute(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{
		Name: "cl1", Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverTUI,
	})
	rec := forkReq(t, "cl1", `{"at":"some-uuid"}`)
	if strings.Contains(rec.Body.String(), "fork_at_unsupported") {
		t.Fatalf("body = %s; claude は TUI しか無いので経路を理由に断ってはいけない", rec.Body.String())
	}
}

// 壊れたボディを黙って無視すると、地点指定のつもりの要求が会話まるごと分岐になる。
func TestForkRejectsMalformedBody(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "oc2", Dir: t.TempDir(), Kind: session.KindOpencode})
	rec := forkReq(t, "oc2", `{"at":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bad_request") {
		t.Fatalf("body = %s; want bad_request", rec.Body.String())
	}
}

// 後方互換: ボディ無し（docs/log/55 以前のクライアント）は分岐点の検査に入らない。
// 会話がまだ無いので not_resumable で止まるが、fork_at_* ではないこと＝
// 分岐点まわりの新しいゲートを一切踏んでいないことを確かめる。
func TestForkWithoutBodyKeepsWholeConversationPath(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "oc3", Dir: t.TempDir(), Kind: session.KindOpencode})
	for _, body := range []string{"", "{}"} {
		rec := forkReq(t, "oc3", body)
		if got := rec.Body.String(); strings.Contains(got, "fork_at_unsupported") || strings.Contains(got, "fork_bad_anchor") {
			t.Fatalf("body %q → %s; the anchor gates must not fire without `at`", body, got)
		}
	}
}
