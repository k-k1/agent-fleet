package memoryx

// mux_test.go — この家系の HTTP 契約テストが使う mux とその周辺（移送で main から
// 見えなくなった `buildMux` / `smokeDo` / `containsString` の置き換え）。
//
// 移送前、これらのテストは package main の `buildMux()`（routes.go・247 本の全ルート）を
// 組んでいた。memoryx から routes.go は見えない（逆向きの import になる）ので、memory 系の
// 10 本だけを**同じパターン文字列で**登録する mux をここに置く。パターン文字列をそのまま
// 使うのは、`mux.Handler(req)` が返すパターンを見る検査と、ハンドラが読む r.PathValue が
// 移送前と同じに保たれるため。
//
// 🔥 **写しは黙って腐る。** ここの 10 本は routes.go の写しなので、
// TestMemoryRoutesMatchAgentRouteTable が **本物の mux から撮られた routes.golden** と
// 突き合わせる（browserx が mux_test.go で採った形と同じ）。
//
// ★ **「ルート表への登録そのもの」を見る 2 本は package main に残してある**
// （workspace/agent/memory_routes_test.go の TestMemoryRoutesRegistered /
// TestMemoryP2RoutesRegistered）。あの 2 本は memoryx の未公開シンボルを 1 つも使わず、
// 逆に**本物の mux でしか確かめられないこと**——memory の 10 本が既存の
// `/agents/{kind}/models` を食い潰していない——を見ているので、こちらへ持ってくると
// 検査が空回りする。

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// memoryTestRoutes は routes.go の memory 節（10 本）と同じ (method, path) → ハンドラ。
var memoryTestRoutes = map[string]http.HandlerFunc{
	"GET /agents/memory/roots":         HandleMemoryRoots,
	"GET /agents/memory/snapshots":     HandleMemorySnapshots,
	"POST /agents/memory/snapshots":    HandleMemorySnapshotCreate,
	"GET /agents/memory/diff":          HandleMemoryDiff,
	"GET /agents/memory/tree":          HandleMemoryTree,
	"POST /agents/memory/restore":      HandleMemoryRestore,
	"PUT /agents/memory/settings":      HandleMemorySettings,
	"GET /agents/memory/export":        HandleMemoryExport,
	"POST /agents/memory/import":       HandleMemoryImport,
	"POST /agents/memory/import/apply": HandleMemoryImportApply,
}

func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, h := range memoryTestRoutes {
		mux.HandleFunc(pattern, h)
	}
	return mux
}

// smokeDo は package main の routes_test.go にあるものと同じ（docs/log/23 P0-2）。
func smokeDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// agentRouteGoldenPath: Phase 0（ADR 0067 決定 6）が撮った Agent の全ルート表。
const agentRouteGoldenPath = "../../testdata/routes.golden"

// TestMemoryRoutesMatchAgentRouteTable: 上の写し（memoryTestRoutes）が、実際に組み上がる
// Agent のルート表（routes.golden）の memory 節と完全に一致すること。
//
// **登録の呼び忘れも、パターン文字列のズレも、ここでしか気付けない**: routes.go の
// `mux.HandleFunc("GET /agents/memory/roots", handleMemoryRoots)` が消えても、この
// パッケージのテストは自前の mux を組むので全部緑のままになる。golden は本物の mux から
// 撮られているので、そこだけが差を持つ。
func TestMemoryRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Clean(agentRouteGoldenPath))
	if err != nil {
		// Skip にしない —— 移送で相対パスの深さが変わると、Skip は緑のまま黙って飛ぶ。
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
		if _, path, ok := strings.Cut(line, " "); ok && strings.HasPrefix(path, "/agents/memory/") {
			want = append(want, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	// 0 件のゴールデンほど危険なものはない（browserx / routes_golden_test.go と同じ理由）。
	if len(want) == 0 {
		t.Fatalf("%s に memory のルートが 1 本も無い——golden の形式が変わったか、パスが変わった", agentRouteGoldenPath)
	}

	got := make([]string, 0, len(memoryTestRoutes))
	for pattern := range memoryTestRoutes {
		got = append(got, pattern)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("memoryx のテスト用 mux が Agent のルート表とずれている\n--- memoryx (%d)\n%s\n--- routes.golden (%d)\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}
