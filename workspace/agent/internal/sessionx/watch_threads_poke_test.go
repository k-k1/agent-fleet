package sessionx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// The thread count is read through the shared PR cache; a poked read must see a count newer
// than the cached one, and a scheduled read may still reuse it.
func TestWatchThreadsAfterAPokeSkipsTheCachedCount(t *testing.T) {
	var calls, unresolved atomic.Int32
	unresolved.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		nodes := ""
		for i := int32(0); i < unresolved.Load(); i++ {
			if i > 0 {
				nodes += ","
			}
			nodes += `{"isResolved":false}`
		}
		fmt.Fprintf(w, `{"data":{"r0":{"owner":{"login":"o"},"pullRequest":{"number":9,"state":"OPEN","isDraft":false,`+
			`"url":"https://github.com/o/r/pull/9","headRepositoryOwner":{"login":"o"},"reviewThreads":{"nodes":[%s]},`+
			`"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}}`, nodes)
	}))
	defer srv.Close()
	c := branchpr.New(func() (string, error) { return "tok", nil })
	c.Endpoint = srv.URL
	old := sessionPRs
	sessionPRs = c
	defer func() { sessionPRs = old }()

	if n, err := watchThreads(context.Background(), "o/r", 9); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v", n, err)
	}
	unresolved.Store(2)
	if n, _ := watchThreads(context.Background(), "o/r", 9); n != 1 || calls.Load() != 1 {
		t.Fatalf("a scheduled read should use the cache: n=%d calls=%d", n, calls.Load())
	}
	time.Sleep(2 * time.Millisecond)
	n, err := watchThreads(watch.WithPokedSince(context.Background(), time.Now()), "o/r", 9)
	if err != nil || n != 2 || calls.Load() != 2 {
		t.Fatalf("poked read = %d, %v, calls=%d: answered from the cache", n, err, calls.Load())
	}
}
