//go:build contract

// The event stream af subscribes to, measured against the REAL opencode 2 on PATH with the stub
// provider of contract_v2state_test.go (no credentials, no billing).
//
//	go test -tags contract -run V2EventStream ./internal/agents/opencode/
package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// opencode1Events are the opencode 1 events handleServeEvent had cases for. Their names are still in
// the 2.x binary, and the cases were removed because 2.x was measured not to emit them: a turn, a
// tool call, a provider failure, a retry, an interrupt of a running stream and a compaction, on the
// stream af subscribes to. If 2.x starts emitting one, af drops it.
var opencode1Events = []string{"session.status", "message.updated", "message.part.updated", "session.idle", "session.compacted"}

func TestContractV2EventStreamHasNoOpencode1Events(t *testing.T) {
	_, addr, dir := startStubbedV2(t)

	var mu sync.Mutex
	seen := map[string]int{}
	count := func(typ string) int {
		mu.Lock()
		defer mu.Unlock()
		return seen[typ]
	}
	stream, err := (&http.Client{Transport: authTransport{}}).Get(eventURL(addr))
	if err != nil {
		t.Fatal(err)
	}
	readerDone, closed := make(chan struct{}), make(chan struct{})
	// The reader is ended and waited for before the test completes: reporting after that panics.
	t.Cleanup(func() {
		close(closed)
		stream.Body.Close()
		<-readerDone
	})
	go func() {
		defer close(readerDone)
		sc := bufio.NewScanner(stream.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20) // the same limits as streamEvents
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), "data: ") // the prefix streamEvents requires
			if !ok {
				continue
			}
			var ev struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(line), &ev) == nil && ev.Type != "" {
				mu.Lock()
				seen[ev.Type]++
				mu.Unlock()
			}
		}
		select {
		case <-closed: // the cleanup closed the stream
		default:
			t.Errorf("event stream ended early: %v", sc.Err())
		}
	}()

	ses, err := serveCreateSession(addr, dir, "events")
	if err != nil {
		t.Fatal(err)
	}
	st := agents.ThreadSettings{Model: "stub/m"}
	// A turn left running by a Fatal must not report after the test completes (that panics the
	// binary): cleanup interrupts the session and waits for the background turns, and only after a
	// wait that timed out are their reports dropped.
	var backgrounds []<-chan struct{}
	var reportsOff atomic.Bool
	t.Cleanup(func() {
		_ = serveAbort(addr, ses)
		for _, done := range backgrounds {
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				reportsOff.Store(true)
			}
		}
	})
	// turn runs one turn to its end. It also runs on its own goroutine, so a failure is reported
	// with Errorf, not Fatal.
	turn := func(prompt string) {
		req, err := newTurnRequest(addr, ses, agents.TurnInput{Prompt: prompt})
		if err == nil {
			var res *http.Response
			res, err = sendTurn(addr, ses, req, st)
			if res != nil {
				res.Body.Close()
			}
		}
		if err != nil && !reportsOff.Load() {
			t.Errorf("turn %q: %v", prompt, err)
		}
	}
	// background starts a turn and returns a channel closed when it has ended.
	background := func(prompt string) <-chan struct{} {
		done := make(chan struct{})
		backgrounds = append(backgrounds, done)
		go func() { defer close(done); turn(prompt) }()
		return done
	}
	// grows waits for typ to be seen more often than from times.
	grows := func(typ string, from int, within time.Duration) bool {
		for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if count(typ) > from {
				return true
			}
		}
		return false
	}
	wait := func(done <-chan struct{}, what string) {
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("%s never ended", what)
		}
	}

	turn("hello TOOL")
	if count("session.tool.called") == 0 {
		t.Error("the tool turn announced no session.tool.called")
	}
	turn("please FAIL")

	// A retry: the stub answers 500 until the turn is interrupted. Counted from here, since a
	// failure before it may already have announced one.
	retries, interrupts := count("session.retry.scheduled"), count("session.execution.interrupted")
	done := background("please RETRY")
	if !grows("session.retry.scheduled", retries, 30*time.Second) {
		t.Error("a 500 was not announced as session.retry.scheduled")
	}
	if err := serveAbort(addr, ses); err != nil {
		t.Fatal(err)
	}
	wait(done, "the interrupted retry")
	if !grows("session.execution.interrupted", interrupts, 10*time.Second) {
		t.Error("the interrupted retry did not end as session.execution.interrupted")
	}

	// An interrupt of a stream that is running: once its text has started.
	texts, interrupts := count("session.text.started"), count("session.execution.interrupted")
	done = background("go SLOW")
	if !grows("session.text.started", texts, 30*time.Second) {
		t.Fatal("the slow turn never started its text")
	}
	if err := serveAbort(addr, ses); err != nil {
		t.Fatal(err)
	}
	wait(done, "the interrupted stream")
	if !grows("session.execution.interrupted", interrupts, 10*time.Second) {
		t.Error("the interrupted stream did not end as session.execution.interrupted")
	}

	// A compaction.
	ended := count("session.compaction.ended")
	if res, err := serveClient.Post(addr+"/api/session/"+ses+"/compact", "application/json", bytes.NewReader([]byte(`{}`))); err == nil {
		res.Body.Close()
	} else {
		t.Fatal(err)
	}
	if !grows("session.compaction.ended", ended, 30*time.Second) {
		t.Error("the compaction did not end on the stream")
	}
	time.Sleep(time.Second)

	mu.Lock()
	defer mu.Unlock()
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("opencode %s emitted: %s", opencodeVersion(t), strings.Join(names, ", "))
	for _, want := range []string{"session.execution.started", "session.execution.succeeded", "session.execution.failed",
		"session.compaction.started"} {
		if seen[want] == 0 {
			t.Errorf("%s never arrived: the stream did not carry the scenario", want)
		}
	}
	for _, old := range opencode1Events {
		if seen[old] > 0 {
			t.Errorf("opencode 2 emitted the opencode 1 event %s (%d times); handleServeEvent has no case for it any more, so af drops it", old, seen[old])
		}
	}
}
