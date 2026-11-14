package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The first delivery is the slower one (a longer paste, a later composer): without the chain
// the second finishes first.
func TestInputOrderRunsInEnqueueOrder(t *testing.T) {
	o := &inputOrder{tails: map[string]*orderTail{}}
	var mu sync.Mutex
	var got []string
	rec := func(s string, d time.Duration) func() {
		return func() {
			time.Sleep(d)
			mu.Lock()
			got = append(got, s)
			mu.Unlock()
		}
	}
	var wg sync.WaitGroup
	for _, c := range []struct {
		s string
		d time.Duration
	}{{"first", 150 * time.Millisecond}, {"second", 0}, {"third", 10 * time.Millisecond}} {
		run := o.enqueue("s", rec(c.s, c.d))
		wg.Add(1)
		go func() { defer wg.Done(); run() }()
	}
	// A different session is not held up by "s".
	other := o.enqueue("t", rec("other", 0))
	other()
	mu.Lock()
	if len(got) != 1 || got[0] != "other" {
		t.Errorf("other session waited behind s: %v", got)
	}
	mu.Unlock()
	wg.Wait()
	want := []string{"other", "first", "second", "third"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if len(o.tails) != 0 {
		t.Errorf("tails leaked: %d", len(o.tails))
	}
}

// #4178 end to end: two when_ready sends to one session, the first one's composer wait slower.
// The pane has to see them in request order. codex stands in for claude because claude's
// delivery confirmation (a transcript the fake cannot produce) retypes for a minute; the
// ordering code does not look at the kind.
func TestWhenReadyInputsReachPaneInRequestOrder(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%9\n' ;;
  capture-pane)
    # the first composer probe stalls, so an unordered second delivery overtakes it
    if mkdir "$TMUX_TEST_LOG.once" 2>/dev/null; then sleep 1.5; fi
    printf 'ready\nmedium · ~/repos\n' ;;
  load-buffer) /bin/cat >> "$TMUX_TEST_LOG.stdin"; echo >> "$TMUX_TEST_LOG.stdin" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Cleanup(WaitInitialDeliveries)

	const name = "order_when_ready"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
	first, second := "FIRST-PROMPT", strings.Repeat("S", 300)
	for _, p := range []string{first, second} {
		b, _ := json.Marshal(map[string]any{"prompt": p, "when_ready": true})
		if rec := postInput(t, name, string(b)); rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		if strings.Count(string(b), "send-keys -t %9 Enter") >= 4 { // submit + nudge, twice
			got, _ := os.ReadFile(logPath + ".stdin")
			fi, si := strings.Index(string(got), first), strings.Index(string(got), "SSS")
			if fi < 0 || si < 0 || fi > si {
				t.Fatalf("first at %d, second at %d: not in request order\n%s", fi, si, got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("deliveries never finished:\n%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
