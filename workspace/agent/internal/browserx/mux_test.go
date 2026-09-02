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
// ★ 表そのものは browserx が 1 つだけ持つ（mux.go の Routes）。回収（ADR 0067 決定 2）
// までは routes.go とここに同じ 15 本が別々に書かれており、**写しは黙って腐る**状態
// だった。いまはここも routes.go も同じ Routes() を登録する。
//
// なお `/healthz` まで要る W5 の live サーバ（TestBrowserLiveServerHelper）は、CP の
// browser_live_e2e_test.go が `go test -c .` で **root パッケージ**をビルドして起動する
// ため、package main（workspace/agent/browser_live_helper_test.go）に残してある。
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	RegisterRoutes(mux)
	return mux
}

// agentRouteGoldenPath: Phase 0（ADR 0067 決定 6）が撮った Agent の全ルート表。
const agentRouteGoldenPath = "../../testdata/routes.golden"

// TestBrowserRoutesMatchAgentRouteTable: browserx が持つ表（mux.go の Routes）が、実際に
// 組み上がる Agent のルート表（routes.golden）の browser 節と完全に一致すること。
//
// 写しは消えたが、**登録の呼び忘れ**は残る失敗の形である: routes.go の
// browserx.RegisterRoutes(mux) が消えても、Routes() 自体は正しいままなのでこのパッケージの
// テストは全部緑になる。golden は本物の mux から取られているので、そこだけが気付ける。
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
	for _, r := range Routes() {
		got = append(got, r.Pattern)
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
