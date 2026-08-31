package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSlug = "k7f2q9x1w3ub5nzt0abc" // 20 文字

func TestParsePreviewHost(t *testing.T) {
	const domain = "pv.example.com"
	cases := []struct {
		name     string
		host     string
		wantOK   bool
		wantPort int
	}{
		{"react", testSlug + "-3000.pv.example.com", true, 3000},
		{"spring", testSlug + "-8080.pv.example.com", true, 8080},
		{"with port", testSlug + "-3000.pv.example.com:443", true, 3000},
		// DNS はケース非依存なので、大文字で来ても同じ Workspace に解決する（slug の
		// 保存は小文字・比較の前に畳む）。
		{"uppercase", strings.ToUpper(testSlug) + "-3000.PV.EXAMPLE.COM", true, 3000},
		{"trailing dot", testSlug + "-3000.pv.example.com.", true, 3000},
		// ★ ラベルが 2 段になる形は通さない。ワイルドカード証明書が 1 段しか
		// 受け持たないので、通したところで TLS が張れない（ADR 0062 決定 2）。
		{"two labels", "3000." + testSlug + ".pv.example.com", false, 0},
		{"console host", "af.example.com", false, 0},
		{"alb health check", "10.20.1.5", false, 0},
		{"other domain", testSlug + "-3000.evil.example.com", false, 0},
		{"no port", testSlug + ".pv.example.com", false, 0},
		{"short slug", "abc-3000.pv.example.com", false, 0},
		{"port zero", testSlug + "-0.pv.example.com", false, 0},
		{"port too big", testSlug + "-70000.pv.example.com", false, 0},
		{"padded port", testSlug + "-03000.pv.example.com", false, 0},
		{"empty slug", "-3000.pv.example.com", false, 0},
		{"the domain itself", "pv.example.com", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ph, ok := parsePreviewHost(c.host, domain)
			if ok != c.wantOK {
				t.Fatalf("parsePreviewHost(%q) ok = %v, want %v", c.host, ok, c.wantOK)
			}
			if ok && ph.port != c.wantPort {
				t.Errorf("port = %d, want %d", ph.port, c.wantPort)
			}
			if ok && ph.slug != testSlug {
				t.Errorf("slug = %q, want %q", ph.slug, testSlug)
			}
		})
	}
}

// 未設定のデプロイでは何も一致しない ＝ ホスト方式そのものが存在しない。
func TestParsePreviewHostDisabledWhenDomainEmpty(t *testing.T) {
	if _, ok := parsePreviewHost(testSlug+"-3000.pv.example.com", ""); ok {
		t.Fatal("preview host matched with no AF_PREVIEW_DOMAIN configured")
	}
}

func TestNewPreviewSlugShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s, err := newPreviewSlug()
		if err != nil {
			t.Fatalf("newPreviewSlug: %v", err)
		}
		if !validPreviewSlug(s) {
			t.Fatalf("newPreviewSlug returned %q, which parsePreviewHost would reject", s)
		}
		if strings.Contains(s, "-") {
			t.Fatalf("slug %q contains '-', which is the port separator", s)
		}
		if seen[s] {
			t.Fatalf("newPreviewSlug repeated %q within 200 draws", s)
		}
		seen[s] = true
	}
}

func TestPreviewPortAllowlist(t *testing.T) {
	// 既定は要望そのもの（React 3000 / Spring Boot 8080）。
	var zero wsSettings
	if !previewPortAllowed(zero, 3000) || !previewPortAllowed(zero, 8080) {
		t.Fatal("default preview ports should allow 3000 and 8080")
	}
	if previewPortAllowed(zero, 5432) {
		t.Fatal("default preview ports must not allow an arbitrary port")
	}
	st := wsSettings{PreviewPorts: []int{4200}}
	if previewPortAllowed(st, 3000) {
		t.Fatal("an explicit list must REPLACE the default, not extend it")
	}
	if !previewPortAllowed(st, 4200) {
		t.Fatal("explicitly listed port rejected")
	}
}

func TestSanitizePreviewPorts(t *testing.T) {
	got := sanitizePreviewPorts([]int{3000, 3000, 0, 70000, 8080, -1})
	if len(got) != 2 || got[0] != 3000 || got[1] != 8080 {
		t.Fatalf("sanitizePreviewPorts = %v, want [3000 8080]", got)
	}
	many := make([]int, 0, 40)
	for i := 0; i < 40; i++ {
		many = append(many, 3000+i)
	}
	if n := len(sanitizePreviewPorts(many)); n != maxPreviewPorts {
		t.Fatalf("sanitizePreviewPorts kept %d ports, want the cap %d", n, maxPreviewPorts)
	}
}

func TestPreviewURLFor(t *testing.T) {
	if got := previewURLFor(testSlug, 3000, "pv.example.com"); got != "https://"+testSlug+"-3000.pv.example.com" {
		t.Fatalf("previewURLFor = %q", got)
	}
	// 発行されていない（停止中）ときに URL を組み立てない — 押しても 404 のリンクを
	// 見せないため。
	if got := previewURLFor("", 3000, "pv.example.com"); got != "" {
		t.Fatalf("previewURLFor with no slug = %q, want empty", got)
	}
}

func TestSafePreviewNext(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/foo?a=1", "/foo?a=1"},
		{"", "/"},
		{"//evil.example.com/", "/"},
		{"/\\evil.example.com", "/"},
		{"https://evil.example.com", "/"},
		{previewAuthCallbackPath, "/"}, // ハンドシェイクへ戻すループを作らない
	} {
		if got := safePreviewNext(c.in); got != c.want {
			t.Errorf("safePreviewNext(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ★ プレビューのホストに来た「プレビュー以外」は 1 つも通さない。同じプロセスが
// Console と CP API を持っているので、ここが緩むと別オリジンにして閉じたはずの穴が
// 裏口から開く（ADR 0062 決定 8）。
func TestPreviewDispatchDoesNotServeTheConsole(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 「通常の mux に届いた」の印
	})
	mgr := &manager{previewDomain: "pv.example.com", store: nil}
	a := previewHostAPI{cfg: config{mgr: mgr}, mgr: mgr}
	h := a.dispatch(inner)

	// Console のホストは素通し（ALB のヘルスチェックも同様）。
	for _, host := range []string{"af.example.com", "10.20.1.5:8080"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://"+host+"/api/workspace", nil)
		req.Host = host
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusTeapot {
			t.Fatalf("host %q: code = %d, want the inner handler (418)", host, rr.Code)
		}
	}
	// プレビューのホストは、たとえ /api/... でも通常の mux へ行かない。store が nil
	// なので panic ではなく「通らなかったこと」を見たいだけ — 到達したら 418 になる。
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://"+testSlug+"-3000.pv.example.com/api/workspace", nil)
	req.Host = testSlug + "-3000.pv.example.com"
	func() {
		defer func() { _ = recover() }() // store nil の panic は許容（418 でないことが要点）
		h.ServeHTTP(rr, req)
	}()
	if rr.Code == http.StatusTeapot {
		t.Fatal("a preview host reached the Console/API mux")
	}
}

// af_pv（プレビューの入場券）はアプリに見せない。ホスト方式では cookie が
// アプリと同一オリジンに載るので、ブラウザは守ってくれない — この一覧だけが防波堤。
func TestPreviewAuthCookieIsStrippedFromTheApp(t *testing.T) {
	if !sensitiveBrowserCookie(previewAuthCookie) {
		t.Fatal("af_pv must never be forwarded to the previewed app")
	}
}
