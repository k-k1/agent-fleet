package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentCallsUseTheSharedClient — CP→Agent の HTTP は必ず agent_dial.go の
// Transport を通す（agentHTTPClient / agentLongCallClient / agentRelayClient /
// healthzClient、WebSocket は NetDialContext: dialAgent）。
//
// ★ 実機で 2 度踏んでいる。Service Connect の別名はタスク起動時に /etc/hosts へ
// 書かれるだけなので、CP タスクより後に作られたワークスペースは素の dial では
// NXDOMAIN になる。0.9.1 で Cloud Map への引き直しを入れたが、それは Transport に
// 載せた仕組みなので、http.DefaultClient を使った経路には効かない——
// 0.10.0 の Bitbucket OAuth 保存・MCP の agentText・通知の drainAgent が
// まさにそれで、実デプロイで
//
//	dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host
//
// を返していた（Console には「Workspace は起動していますか」と出るだけで、
// 起動しているので原因に辿り着けない）。
//
// 判定はファイル単位: Agent の URL を組み立てているファイル（Endpoint() を参照する）
// で http.DefaultClient を使っていたら落とす。外向き（IdP や bitbucket.org）の呼び出しは
// そのファイルに専用クライアントを持たせること（bbHTTPClient / oidcHTTPClient）。
func TestAgentCallsUseTheSharedClient(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		if !strings.Contains(body, "Endpoint()") {
			continue
		}
		checked++
		for i, line := range strings.Split(body, "\n") {
			code := line
			if j := strings.Index(code, "//"); j >= 0 {
				code = code[:j] // コメントで説明するのは許す（禁止するのは呼び出し）
			}
			if strings.Contains(code, "http.DefaultClient") {
				t.Errorf("%s:%d: CP→Agent の経路で http.DefaultClient を使っている。"+
					"agentHTTPClient（または長い呼び出しなら agentLongCallClient）を使うこと——"+
					"Cloud Map へのフォールバックは Transport に載っているので、素のクライアントでは"+
					"CP 起動より後にできたワークスペースが no such host になる。\n\t%s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("Endpoint() を参照するファイルが 1 つも無い — 検査が空振りしている")
	}
}
