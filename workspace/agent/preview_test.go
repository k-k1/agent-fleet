package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPreviewForwardsPublicHostHeaders は「CP が付けた公開名が、プレビューされる
// アプリまで届く」ことを固定する。
//
// ★ なぜ要るか（2026-08-31 に実測で見つけた）: httputil.ReverseProxy は **Rewrite
// モードだと Out から Forwarded / X-Forwarded-For / -Host / -Proto を削除してから**
// Rewrite を呼ぶ。「触らなければ素通りする」は嘘で、素通りしていたのは
// X-Forwarded-Prefix だけだった（あれはその一覧に無い）。
//
// 落ちると何が起きるか: Next.js は Server Actions で Origin と x-forwarded-host の
// 一致を検査して 403 を返す。ヘッダが消えていれば、プレビュー越しの Server Action は
// 全部 403 になる —— しかも「プロキシが壊れている」ではなく「アプリが壊れている」
// ように見える（docs/log/81 §2.5 (c)）。
func TestPreviewForwardsPublicHostHeaders(t *testing.T) {
	var got http.Header
	var sawHost string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		sawHost = r.Host
	}))
	defer app.Close()

	port := app.Listener.Addr().(interface{ String() string }).String()
	// httptest は 127.0.0.1:<port> で待つ。handlePreview は 127.0.0.1:{port} へ繋ぐので
	// ポート番号だけ取り出して渡す。
	p := port[len("127.0.0.1:"):]

	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/{port}/{rest...}", handlePreview)
	req := httptest.NewRequest("GET", "/proxy/"+p+"/some/path", nil)
	req.Header.Set("X-Forwarded-Host", "abcdefghij0123456789-3000.pv.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if v := got.Get("X-Forwarded-Host"); v != "abcdefghij0123456789-3000.pv.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want the public preview host", v)
	}
	if v := got.Get("X-Forwarded-Proto"); v != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", v)
	}
	// ★ Host は内部アドレスのまま渡す（決定 9）。dev サーバのホスト検査
	// （Vite の allowedHosts / Next の allowedDevOrigins）を利用者に設定させずに
	// 素通りするための唯一の両立点で、公開名は X-Forwarded-Host が運ぶ。
	if sawHost != "127.0.0.1:"+p {
		t.Errorf("upstream Host = %q, want 127.0.0.1:%s", sawHost, p)
	}
	// CP↔Agent の bearer はアプリに見せない。
	if v := got.Get("Authorization"); v != "" {
		t.Errorf("Authorization leaked to the previewed app: %q", v)
	}
}
