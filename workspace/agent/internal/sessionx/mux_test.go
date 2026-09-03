package sessionx

// mux_test.go — この家系の HTTP 契約テストが使う mux（移送で main から見えなくなった
// `buildMux` の置き換え）。
//
// 移送前、これらのテストは package main の `buildMux()`（routes.go・228 本の全ルート）を
// 組んでいた。sessionx から routes.go は見えない（逆向きの import になる）ので、
// session 家系の 47 本だけを**同じパターン文字列で**登録する mux をここに置く。
// パターン文字列をそのまま使うのは、`mux.Handler(req)` が返すパターンを見る検査と、
// ハンドラが読む `r.PathValue` が移送前と同じに保たれるため。
//
// 🔥 **写しは黙って腐る。** ここの 47 本は routes.go の写しなので、
// TestSessionRoutesMatchAgentRouteTable が **本物の mux から撮られた routes.golden** と
// 突き合わせる（browserx / memoryx が mux_test.go で採った形と同じ）。

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sessionTestRoutes は routes.go の session 節（47 本）と同じ (method, path) → ハンドラ。
var sessionTestRoutes = map[string]http.HandlerFunc{
	"GET /sessions":                              HandleListSessions,
	"GET /sessions/catalog":                      HandleSessionCatalog,
	"POST /sessions":                             HandleCreateSession,
	"GET /sessions-idempotency/{key}":            HandleIdempotencyLookup,
	"POST /sessions/{name}/fork":                 HandleForkSession,
	"POST /sessions/{name}/stop":                 HandleStopSession,
	"POST /sessions/{name}/halt":                 HandleHaltSession,
	"POST /sessions/{name}/recreate":             HandleRecreateSession,
	"GET /sessions/archived":                     HandleListArchived,
	"GET /sessions/usage":                        HandleSessionsUsage,
	"GET /sessions/cleanup":                      HandleSessionsCleanup,
	"POST /sessions/{name}/lock":                 HandleSessionLock,
	"POST /sessions/{name}/keep-awake":           HandleSessionKeepAwake,
	"POST /sessions/{name}/archive":              HandleArchiveSession,
	"POST /sessions/{name}/restore":              HandleRestoreSession,
	"POST /sessions/{name}/input":                HandleSessionInput,
	"POST /sessions/{name}/carried-answer":       HandleSessionCarriedAnswer,
	"GET /sessions/{name}/settings":              HandleSessionSettingsGet,
	"POST /sessions/{name}/settings":             HandleSessionSettings,
	"POST /sessions/{name}/driver":               HandleSessionDriver,
	"POST /sessions/{name}/paste-image":          HandlePasteImage,
	"GET /sessions/{name}/pasted/{file}":         HandlePastedImage,
	"GET /sessions/{name}/status":                HandleSessionStatus,
	"GET /sessions/{name}/output":                HandleSessionOutput,
	"GET /sessions/{name}/ssm-login":             HandleSSMLoginStatus,
	"POST /sessions/{name}/start":                HandleStartSession,
	"GET /sessions/{name}/messages":              HandleSessionMessages,
	"GET /sessions/{name}/handoff-proposal":      HandleSessionHandoffProposal,
	"POST /sessions/{name}/handoff-proposal":     HandleSessionHandoffProposal,
	"DELETE /sessions/{name}/handoff-proposal":   HandleSessionHandoffProposal,
	"GET /sessions/{name}/handoff-context":       HandleSessionHandoffContext,
	"GET /sessions/{name}/marks":                 HandleSessionMarks,
	"POST /sessions/{name}/marks":                HandleSessionMarks,
	"DELETE /sessions/{name}/marks":              HandleSessionMarks,
	"POST /sessions/{name}/title/accept":         HandleAcceptSuggestedTitle,
	"POST /sessions/{name}/title/dismiss":        HandleDismissSuggestedTitle,
	"POST /sessions/{name}/title/suggest":        HandleSuggestTitle,
	"POST /sessions/{name}/title/set":            HandleSetTitle,
	"POST /sessions/{name}/suggest-branch":       HandleSessionSuggestBranch,
	"POST /sessions/{name}/suggest-replies":      HandleSuggestReplies,
	"GET /sessions/{name}/skills":                HandleSessionSkills,
	"GET /sessions/{name}/committed":             HandleSessionCommittedFiles,
	"POST /sessions/{name}/rename-branch":        HandleSessionRenameBranch,
	"POST /chat/conversations/{id}/lock":         HandleChatLock,
	"POST /chat/conversations/{id}/paste-image":  HandleChatPasteImage,
	"GET /chat/conversations/{id}/pasted/{file}": HandleChatPastedImage,
	"POST /repos/{name}/lock":                    HandleRepoLock,
}

func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, h := range sessionTestRoutes {
		mux.HandleFunc(pattern, h)
	}
	return mux
}

// TestSessionRoutesMatchAgentRouteTable は、上の写しが本物のルート表と一致していることを
// 見る。**ゴールデンは本物の mux から撮られている**ので、routes.go 側でパターンが変わったのに
// ここを直し忘れると落ちる。
//
// 🔥 **一致の向きは「写し ⊆ ゴールデン」**。逆（ゴールデン ⊆ 写し）は成り立たない
// （agent には session 以外のルートもある）。だから**本数の下限も見る** ——
// 写しが空になっても「部分集合である」は真になってしまう（#320 の型）。
func TestSessionRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "routes.golden"))
	if err != nil {
		t.Fatalf("routes.golden が読めない: %v（移送で相対パスの深さが変わっていないか）", err)
	}
	defer f.Close()

	golden := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		golden[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(golden) < 200 {
		t.Fatalf("routes.golden から %d 本しか読めていない＝この検査が無言化している", len(golden))
	}

	var missing []string
	for pattern := range sessionTestRoutes {
		if !golden[pattern] {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("写しのルートが本物のルート表に無い（routes.go 側で変わった？）: %v", missing)
	}
	if len(sessionTestRoutes) != 47 {
		t.Fatalf("写しのルートが %d 本（want 47）＝写しが痩せると上の部分集合検査が空回りする",
			len(sessionTestRoutes))
	}
}
