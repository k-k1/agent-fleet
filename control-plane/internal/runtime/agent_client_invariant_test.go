// Every CP→Agent HTTP call has to go through the shared Transport; this applies that
// rule to this package too.
//
// The CP-side check (TestAgentCallsUseTheSharedClient) reads `os.ReadDir(".")` and
// never descends into subdirectories, so the adapters here fall outside it. A check
// that stops covering something reports nothing, so without this one nobody notices.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdapterAgentCallsUseTheSharedClient fails a CP→Agent call built on
// http.DefaultClient. A Service Connect alias only lands in the /etc/hosts written when
// the task starts, so a workspace created after the CP task is NXDOMAIN on a plain dial;
// the Cloud Map re-resolution that saves it lives in the Transport, so it does not reach
// a default client. Hit twice on real infrastructure.
//
// The shared client here is healthzClient (injected by the CP through deps.go). Its zero
// value is a plain *http.Client, so a new call that assembles one itself has the same hole.
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
				code = code[:j] // naming it in a comment is fine; the call is what is banned
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
