// agent_client_invariant_test.go — CP→Agent の HTTP は必ず共有 Transport を通す、を
// このパッケージにも掛ける。
//
// ★ これは移送で開いた穴を塞ぐためにある。同じ検査は CP 側にもあるが
// （agent_client_invariant_test.go の TestAgentCallsUseTheSharedClient）、あちらは
// `os.ReadDir(".")` でパッケージ直下しか見ずサブディレクトリへ降りない。アダプタが
// internal/runtime へ移った時点で、あちらの検査対象から静かに外れていた——
// 検査が消えたことは何も報告しないので、これが無ければ誰も気付かない。
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// なぜ効くのか（CP 側と同じ経緯）: Service Connect の別名はタスク起動時に書かれる
// /etc/hosts にしか載らないので、CP タスクより後に作られたワークスペースは素の dial
// では NXDOMAIN になる。Cloud Map への引き直しは Transport に載せた仕組みなので、
// http.DefaultClient を使った経路には効かない。実機で 2 度踏んでいる。
//
// このパッケージでは共有クライアントは healthzClient（deps.go 経由で CP が注入する）。
// 既定値は素の *http.Client なので、それを直接組み立てる新しい呼び出しも同じ穴になる。
func TestAdapterAgentCallsUseTheSharedClient(t *testing.T) {
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
					"healthzClient（deps.go）を使うこと——Cloud Map へのフォールバックは "+
					"CP が注入する Transport に載っているので、素のクライアントでは CP 起動より"+
					"後にできたワークスペースが no such host になる。\n\t%s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("Endpoint() を参照するファイルが 1 つも無い — 検査が空振りしている")
	}
}
