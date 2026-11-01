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
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// opencode1Events are the event names handleServeEvent still has cases for from opencode 1. Their
// names are still in the 2.x binary, so whether 2.x emits them is measured here: a turn, a tool
// call, a provider failure, a retry, an interrupt and a compaction, on the stream af subscribes to.
var opencode1Events = []string{"session.status", "message.updated", "message.part.updated", "session.idle", "session.compacted"}

func TestContractV2EventStreamHasNoOpencode1Events(t *testing.T) {
	_, addr, dir := startStubbedV2(t)

	var mu sync.Mutex
	seen := map[string]int{}
	stream, err := (&http.Client{Transport: authTransport{}}).Get(eventURL(addr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.Body.Close() })
	go func() {
		sc := bufio.NewScanner(stream.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue
			}
			var ev struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &ev) == nil && ev.Type != "" {
				mu.Lock()
				seen[ev.Type]++
				mu.Unlock()
			}
		}
	}()

	ses, err := serveCreateSession(addr, dir, "events")
	if err != nil {
		t.Fatal(err)
	}
	st := agents.ThreadSettings{Model: "stub/m"}
	// turn runs one turn to its end; it also runs on its own goroutine, so a failure is logged, not
	// fatal (the stream's content is what the test judges).
	turn := func(prompt string) {
		req, err := newTurnRequest(addr, ses, agents.TurnInput{Prompt: prompt})
		if err != nil {
			t.Errorf("turn %q: %v", prompt, err)
			return
		}
		if res, err := sendTurn(addr, ses, req, st); err == nil {
			res.Body.Close()
		}
	}
	waitFor := func(typ string, within time.Duration) bool {
		for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			mu.Lock()
			n := seen[typ]
			mu.Unlock()
			if n > 0 {
				return true
			}
		}
		return false
	}

	turn("hello TOOL")
	turn("please FAIL")
	// A retry: the stub answers 500 until the turn is interrupted.
	go turn("please RETRY")
	if !waitFor("session.retry.scheduled", 30*time.Second) {
		t.Error("a 500 was not announced as session.retry.scheduled")
	}
	_ = serveAbort(addr, ses)
	waitFor("session.execution.interrupted", 30*time.Second)
	// An interrupt of a running stream.
	go turn("go SLOW")
	time.Sleep(2 * time.Second)
	_ = serveAbort(addr, ses)
	time.Sleep(2 * time.Second)
	// A compaction.
	if res, err := serveClient.Post(addr+"/api/session/"+ses+"/compact", "application/json", bytes.NewReader([]byte(`{}`))); err == nil {
		res.Body.Close()
	}
	if !waitFor("session.compaction.ended", 30*time.Second) {
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
	for _, want := range []string{"session.execution.started", "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted"} {
		if seen[want] == 0 {
			t.Errorf("%s never arrived: the stream did not carry the scenario", want)
		}
	}
	for _, old := range opencode1Events {
		if seen[old] > 0 {
			t.Errorf("opencode 2 emitted the opencode 1 event %s (%d times): its case in handleServeEvent is still live", old, seen[old])
		}
	}
}
