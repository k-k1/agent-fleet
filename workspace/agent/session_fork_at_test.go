package main

// POST /sessions/{name}/fork の分岐点まわり（docs/55）。ここで守りたいのは
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

// 地点分岐に対応しない kind（claude は会話まるごとの分岐だけ）で at を渡したら、
// 会話まるごと分岐へ倒さずに断る。
func TestForkAtUnsupportedKind(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "cl1", Dir: t.TempDir(), Kind: session.KindClaude})
	rec := forkReq(t, "cl1", `{"at":"some-uuid"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fork_at_unsupported") {
		t.Fatalf("body = %s; want fork_at_unsupported", rec.Body.String())
	}
}

// 対応 kind でも CLI(TUI) ルートには分岐点を渡す口が無い。導線側でも隠しているが、
// 直叩きされたときにここで止まること。
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

// 後方互換: ボディ無し（docs/55 以前のクライアント）は分岐点の検査に入らない。
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
