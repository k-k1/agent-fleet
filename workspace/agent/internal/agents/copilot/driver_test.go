package copilot

// Unit tests for the managed driver. The ACP server (what `copilot --acp` is) is faked over
// io.Pipe, covering the turn state machine (Send → completed, queueing while running,
// interrupt → cancelled, ledger idempotency) and the permission → Interaction → Respond
// round trip (answering a server-initiated JSON-RPC request).

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/acp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeACP is a scriptable ACP peer on the other end of the client's pipes.
type fakeACP struct {
	toClient  *io.PipeWriter // server → client stdout
	mu        sync.Mutex
	prompts   []string             // text of each session/prompt, in arrival order
	gotPrompt chan int64           // session/prompt request ids as they arrive
	gotCancel chan struct{}        // session/cancel notifications
	gotResp   chan json.RawMessage // responses to server-initiated requests
}

func newFakeACP(t *testing.T) (*acp.Client, *fakeACP) {
	t.Helper()
	cIn, sOut := io.Pipe() // server writes → client reads
	sIn, cOut := io.Pipe() // client writes → server reads
	f := &fakeACP{toClient: sOut,
		gotPrompt: make(chan int64, 8),
		gotCancel: make(chan struct{}, 8),
		gotResp:   make(chan json.RawMessage, 8),
	}
	go f.serve(sIn)
	cl := newACPClient(cOut, cIn)
	t.Cleanup(func() { sOut.Close(); cOut.Close() })
	return cl, f
}

func (f *fakeACP) serve(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method == "" && len(msg.ID) > 0 { // response to OUR request
			f.gotResp <- append(json.RawMessage(nil), sc.Bytes()...)
			continue
		}
		var id int64
		_ = json.Unmarshal(msg.ID, &id)
		switch msg.Method {
		case "session/prompt":
			var p struct {
				Prompt []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			text := ""
			for _, b := range p.Prompt {
				if b.Type == "text" {
					text = b.Text
					break
				}
			}
			f.mu.Lock()
			f.prompts = append(f.prompts, text)
			f.mu.Unlock()
			f.gotPrompt <- id // held: test decides when/how to answer
		case "session/cancel":
			f.gotCancel <- struct{}{}
		case "session/set_mode":
			f.reply(id, map[string]any{})
		default:
			if len(msg.ID) > 0 {
				f.reply(id, map[string]any{})
			}
		}
	}
}

// promptTexts is the text of each session/prompt received, in order.
func (f *fakeACP) promptTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

func (f *fakeACP) reply(id int64, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_, _ = f.toClient.Write(append(b, '\n'))
}

// send emits a server-initiated request or notification to the client.
func (f *fakeACP) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = f.toClient.Write(append(b, '\n'))
}

func newTestHandle(t *testing.T) (*threadHandle, *fakeACP) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cl, f := newFakeACP(t)
	h := newThreadHandle("s1", t.TempDir(), "slot-1")
	h.AttachForTest(cl, "sess-1")
	// t.Cleanup is LIFO, so this wait, pushed after the t.Setenv("HOME", …) above, runs before
	// HOME is restored (pushed before it, it would run after the restore, which is too late).
	t.Cleanup(func() { endTurnsBeforeHomeRestore(t, h) })
	return h, f
}

// waitPumpIdle blocks until the handle's turn goroutine has drained (no queue, no running
// turn). The HOME isolation only holds if we wait: a test returning with a turn still
// running lets MarkTurnEnd → status.Persist run after `t.Setenv` restores HOME, writing into
// the real `~/.config/agent-fleet` (measured: slot-1.json was left in the user's
// session-status/). Failing is the safe direction — giving up on the wait would dirty the
// real environment, so it fails instead.
func waitPumpIdle(t *testing.T, h *threadHandle) {
	t.Helper()
	if !pumpIdleWithin(h, 30*time.Second) {
		t.Error("a turn is still running after the test: restoring HOME now would write into the real ~/.config/agent-fleet")
	}
}

func pumpIdleWithin(h *threadHandle, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if h.IdleForTest() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// endTurnsBeforeHomeRestore is the handle's cleanup. A test that failed with a turn still held
// by the fake (a mutation run, a real regression) would otherwise leave that turn to end after
// HOME is restored — measured: slot-1 completion-key and session-status files landed in the
// real ~/.local/state/agent-fleet. So it ends the turn itself by closing the client, while HOME
// is still the test's.
func endTurnsBeforeHomeRestore(t *testing.T, h *threadHandle) {
	t.Helper()
	if pumpIdleWithin(h, 10*time.Second) {
		return
	}
	t.Error("a turn is still running after the test: ending it by closing the client")
	if cl := h.ClientForTest(); cl != nil {
		cl.MarkClosed()
	}
	if !pumpIdleWithin(h, 10*time.Second) {
		t.Error("a turn is still running after the test: restoring HOME now would write into the real ~/.config/agent-fleet")
	}
}

func waitState(t *testing.T, h *threadHandle, want agents.TurnState) {
	t.Helper()
	for deadline := time.Now().Add(hangGuard); time.Now().Before(deadline); {
		if h.TurnState() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state never reached %s (now %s)", want, h.TurnState())
}

func TestUpdateSettingsMode(t *testing.T) {
	h, _ := newTestHandle(t)
	if err := h.UpdateSettings(agents.ThreadSettings{Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	if snap, _ := h.Snapshot(); snap.Settings.Mode != "plan" {
		t.Fatalf("mode not applied: %+v", snap.Settings)
	}
	// Changing model/effort dynamically is an explicit error (the guard behind
	// DynamicModel:false).
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "gpt-5.4"}); err == nil {
		t.Fatal("dynamic model change must be rejected")
	}
}

func jsonContains(raw json.RawMessage, s string) bool {
	return json.Valid(raw) && strings.Contains(string(raw), s)
}

// hangGuard bounds every wait for something that must happen. It is a hang guard, not a
// latency budget: on a loaded host the pump's first file writes (status, ledger) have taken
// seconds, and a 5 s wait turned that into a red run (cursor, go test -p 2).
const hangGuard = 30 * time.Second

var _ agents.LiveHandles = managedDriver{}

// Input accepted while nothing runs is the turn being started even before the pump takes it: a
// first stop in that window stops it, and what was queued behind it continues.

// LiveHandle never starts anything: no handle, no answer.
func TestLiveHandle(t *testing.T) {
	h, _ := newTestHandle(t)
	m := session.Meta{Name: h.SessionName(), Driver: session.DriverManaged}
	if got, ok := (managedDriver{}).LiveHandle(m); ok || got != nil {
		t.Fatalf("LiveHandle with no handle = %v, %v", got, ok)
	}
	handlesMu.Lock()
	handles[h.SessionName()] = h
	handlesMu.Unlock()
	defer func() {
		handlesMu.Lock()
		delete(handles, h.SessionName())
		handlesMu.Unlock()
	}()
	if got, ok := (managedDriver{}).LiveHandle(m); !ok || got != agents.ThreadHandle(h) {
		t.Fatalf("LiveHandle = %v, %v, want the registered handle", got, ok)
	}
}

// A resend is answered as accepted and nothing else: not queued, no state change, no turn —
// whether the session is idle or another turn is running.

// The permission card carries the command under the tool call's title, through this kind's
// PermissionDetail.
func TestPermissionCardShowsTheCommand(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "run something", ClientMessageID: "m1"})
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	f.send(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "sess-1",
		"toolCall":  map[string]any{"toolCallId": "t9", "title": "Run echo", "kind": "execute", "rawInput": map[string]any{"command": "echo hi"}},
		"options":   []map[string]any{{"optionId": "allow_once", "kind": "allow_once", "name": "Allow once"}},
	}})
	waitState(t, h, agents.TurnWaitingInteraction)
	snap, _ := h.Snapshot()
	if snap.Interaction == nil || len(snap.Interaction.Questions) != 1 || snap.Interaction.Questions[0].Question != "Run echo\n`echo hi`" {
		t.Fatalf("interaction = %+v, want the title with the command under it", snap.Interaction)
	}
	if err := h.Respond(agents.InteractionReply{ID: "t9", Decision: agents.DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	<-f.gotResp
	f.reply(id, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)
}

// copilot's decoder took rawInput as an object with a string command: a request whose rawInput
// does not decode that way gets no card (and no answer).
func TestPermissionWithAnUndecodableRawInputIsDropped(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`"echo hi"`), json.RawMessage(`{"command": 5}`)} {
		if _, ok := permissionDetail(raw); ok {
			t.Errorf("permissionDetail(%s) accepted it", raw)
		}
	}
	for raw, want := range map[string]string{``: "", `null`: "", `{}`: "", `{"command":"ls"}`: "ls"} {
		if got, ok := permissionDetail(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("permissionDetail(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
}
