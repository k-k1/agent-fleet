package browserx

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// buildMux は、この家系の HTTP 契約テストが使う mux。
//
// 移送前、これらのテストは package main の buildMux()（routes.go・247 本の全ルート）を
// 組んでいた。browserx から routes.go は見えない（逆向きの import になる）ので、browser
// 系の 15 本だけを同じパターン文字列で登録する mux をここに置く。パターン文字列が要る
// のはハンドラが r.PathValue("id") を読むからで、ハンドラを直接呼ぶ形では {id} が空になり
// テストが本物のリクエストを再現しなくなる。
//
// ⚠️ ここは routes.go の写しなので、放っておけば必ずずれる。ずれを赤くするのが下の
// TestBrowserRoutesMatchAgentRouteTable（Phase 0 の routes.golden と突き合わせる）。
// **routes.go を書き換えたら golden が動き、この表も直すまで赤いまま**になる。
//
// なお `/healthz` まで要る W5 の live サーバ（TestBrowserLiveServerHelper）は、CP の
// browser_live_e2e_test.go が `go test -c .` で **root パッケージ**をビルドして起動する
// ため、package main（workspace/agent/browser_live_helper_test.go）に残してある。
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range browserRoutes() {
		mux.HandleFunc(r.pattern, r.handler)
	}
	return mux
}

type browserRoute struct {
	pattern string
	handler http.HandlerFunc
}

// browserRoutes は routes.go の browser 節と 1 対 1 に対応する（順序も合わせてある）。
func browserRoutes() []browserRoute {
	return []browserRoute{
		{"POST /browser/pages", HandleBrowserPagesCreate},
		{"GET /browser/pages/{id}", HandleBrowserPageGet},
		{"DELETE /browser/pages/{id}", HandleBrowserPageDelete},
		{"GET /ws/browser", HandleBrowserWebSocket},

		{"GET /browser/attach-targets", HandleBrowserAttachTargets},
		{"POST /browser/attachments", HandleBrowserAttachmentCreate},
		{"GET /browser/attachments", HandleBrowserAttachmentList},
		{"GET /browser/attachments/{id}", HandleBrowserAttachmentGet},
		{"DELETE /browser/attachments/{id}", HandleBrowserAttachmentDelete},
		{"POST /browser/attachments/{id}/control-mode", HandleBrowserAttachmentControlMode},
		{"GET /browser/attachments/{id}/targets", HandleBrowserAttachmentSiblingTargets},
		{"POST /browser/attachments/{id}/retarget", HandleBrowserAttachmentRetarget},
		{"POST /browser/attachments/{id}/handoff", HandleBrowserAttachmentHandoff},
		{"POST /browser/attachments/{id}/handoff-result", HandleBrowserAttachmentHandoffResult},
		{"GET /ws/browser-attachments", HandleBrowserAttachmentWebSocket},
	}
}

// agentRouteGoldenPath: Phase 0（ADR 0067 決定 6）が撮った Agent の全ルート表。
const agentRouteGoldenPath = "../../testdata/routes.golden"

// TestBrowserRoutesMatchAgentRouteTable: 上の表が、実際に組み上がる Agent のルート表
// （routes.golden）の browser 節と完全に一致すること。
//
// 上の buildMux は routes.go の写しであり、写しは黙って腐る——routes.go で 1 本増えても
// browserx のテストは全部緑のまま通ってしまう。golden は本物の mux から取られているので、
// これを唯一の突き合わせ先にすれば、写しのずれは必ず赤くなる。
func TestBrowserRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Clean(agentRouteGoldenPath))
	if err != nil {
		t.Fatalf("read %s: %v", agentRouteGoldenPath, err)
	}
	defer f.Close()

	var want []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, path, ok := strings.Cut(line, " "); ok && isBrowserRoutePath(path) {
			want = append(want, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	// 0 件のゴールデンほど危険なものはない（routes_golden_test.go と同じ理由）。
	if len(want) == 0 {
		t.Fatalf("%s に browser のルートが 1 本も無い——golden の形式が変わったか、パスが変わった", agentRouteGoldenPath)
	}

	var got []string
	for _, r := range browserRoutes() {
		got = append(got, r.pattern)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("browserx の mux が Agent のルート表とずれている\n--- browserx (%d)\n%s\n--- routes.golden (%d)\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}

// isBrowserRoutePath は routes.go の browser 節が持つパス空間そのもの。
func isBrowserRoutePath(path string) bool {
	return strings.HasPrefix(path, "/browser/") ||
		path == "/ws/browser" ||
		path == "/ws/browser-attachments"
}
