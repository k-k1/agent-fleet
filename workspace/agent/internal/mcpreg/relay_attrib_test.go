package mcpreg

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeCaller makes the relay see its caller as pid, whose environment under a fake /proc is env.
// It records the addresses it was asked about.
func fakeCaller(t *testing.T, pid int, env string) *[2]*net.TCPAddr {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen [2]*net.TCPAddr
	oldPID, oldRoot := CallerPID, procRoot
	CallerPID = func(local, remote *net.TCPAddr) (int, bool) {
		seen[0], seen[1] = local, remote
		return pid, true
	}
	procRoot = root
	t.Cleanup(func() { CallerPID, procRoot = oldPID, oldRoot })
	return &seen
}

func TestElicitRelayAttributesTheHeldCallToTheCallersSession(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	seen := fakeCaller(t, 4242, "PATH=/bin\x00AF_SESSION_NAME=fix-mail\x00HOME=/h\x00")
	ch := e.startCall(elicitCallBody, sseHeaders)

	p := waitPending(t, 1)[0]
	if p.Session != "fix-mail" {
		t.Fatalf("session %q, want fix-mail", p.Session)
	}
	// The lookup is asked about this very connection: the relay's end and the CLI's.
	_, port, _ := net.SplitHostPort(e.base[len("http://"):])
	if seen[0] == nil || seen[1] == nil || strconv.Itoa(seen[0].Port) != port || !seen[1].IP.IsLoopback() || seen[1].Port == seen[0].Port {
		t.Fatalf("asked about local %v remote %v (relay port %s)", seen[0], seen[1], port)
	}
	got, err := AnswerElicitation(p.ID, allowOnce)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID || got.Session != "fix-mail" || got.Tool != "send_mail" || got.ServerID != relayTestID {
		t.Fatalf("answered %+v", got)
	}
	waitCall(t, ch)
}

func TestCallerSessionNamesOnlyAWellFormedSession(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
	}{
		{"named", "AF_SESSION_NAME=abc-1\x00", "abc-1"},
		{"none", "PATH=/bin\x00", ""},
		{"empty", "AF_SESSION_NAME=\x00", ""},
		{"not a slug", "AF_SESSION_NAME=../x\x00", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, jsonAnswer(askThenComplete))
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			fakeCaller(t, 77, tc.env)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if p.Session != tc.want {
				t.Fatalf("session %q, want %q", p.Session, tc.want)
			}
			if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); err != nil {
				t.Fatal(err)
			}
			waitCall(t, ch)
		})
	}
}

func TestElicitRelayLeavesACallUnattributedWithoutALookup(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	old := CallerPID
	CallerPID = func(*net.TCPAddr, *net.TCPAddr) (int, bool) { return 0, false }
	t.Cleanup(func() { CallerPID = old })
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	if p.Session != "" {
		t.Fatalf("session %q, want none", p.Session)
	}
	if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); err != nil {
		t.Fatal(err)
	}
	waitCall(t, ch)
}

// A form whose largest answer could not be sent back is refused before anyone is asked: the person
// could only decline it. One just under the bound is put to them.
func TestElicitFormWhoseAnswerCannotBeSentIsRefused(t *testing.T) {
	form := func(value string) json.RawMessage {
		return json.RawMessage(`{"inputRequests":{"k":{"method":"elicitation/create","params":{"mode":"form","message":"m",` +
			`"requestedSchema":{"type":"object","required":["c"],"properties":{"c":{"type":"string","enum":["NO",` + strconv.Quote(value) + `]}}}}}},"requestState":"s"}`)
	}
	big := strings.Repeat("x", ElicitAnswerMaxBytes)
	if _, err := parseInputRequired(form(big)); err == nil || !strings.Contains(err.Error(), "too large to send back") {
		t.Fatalf("a %d-byte required choice was accepted: %v", len(big), err)
	}
	// Escaping counts: Go encodes "<" as six bytes.
	if _, err := parseInputRequired(form(strings.Repeat("<", ElicitAnswerMaxBytes/5))); err == nil {
		t.Fatal("a choice that grows past the bound when encoded was accepted")
	}
	// Two choices whose raw and encoded lengths rank the other way round: the encoding decides.
	twoWays := json.RawMessage(`{"inputRequests":{"k":{"method":"elicitation/create","params":{"mode":"form","message":"m",` +
		`"requestedSchema":{"type":"object","required":["c"],"properties":{"c":{"type":"string","enum":[` +
		string(mustJSON(strings.Repeat("a", 12001))) + `,` + string(mustJSON(strings.Repeat("\x00", 12000))) + `]}}}}}},"requestState":"s"}`)
	if _, err := parseInputRequired(twoWays); err == nil {
		t.Fatal("a choice whose encoding is longest but whose raw value is not was missed")
	}
	ir, err := parseInputRequired(form(strings.Repeat("x", ElicitAnswerMaxBytes-64)))
	if err != nil {
		t.Fatal(err)
	}
	if n := largestAnswer(ir.inputs); n > ElicitAnswerMaxBytes {
		t.Fatalf("largest answer %d", n)
	}
}

// What held calls keep is bounded one by one and together; a call past either bound is answered
// with an error rather than held, and the room comes back when a call leaves the list.
func TestElicitHeldCallsAreBoundedInBytes(t *testing.T) {
	resetElicitations(t)
	oldOne, oldAll := elicitMaxHeldOne, elicitMaxHeld
	elicitMaxHeldOne, elicitMaxHeld = 400, 650
	t.Cleanup(func() { elicitMaxHeldOne, elicitMaxHeld = oldOne, oldAll })
	ir := inputRequired{inputs: []ElicitInput{{Key: "a"}}}
	call := func(n int) elicitCall {
		return elicitCall{tool: "t", params: map[string]json.RawMessage{"arguments": json.RawMessage(`"` + strings.Repeat("a", n) + `"`)}}
	}
	if _, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, call(450), ir); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("a call over the one-call bound was held: %v", err)
	}
	p1, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, call(250), ir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, call(250), ir); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("a call past the total bound was held: %v", err)
	}
	got, err := AnswerElicitation(p1.ID, ElicitAnswer{Choices: map[string]map[string]string{"a": {}}})
	if err != nil {
		t.Fatal(err)
	}
	// The answer names the call for the audit and copies nothing of its content.
	if got.ID != p1.ID || got.Tool != "t" || got.Arguments != nil || got.Inputs != nil {
		t.Fatalf("answered %+v", got)
	}
	p2, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, call(250), ir)
	if err != nil {
		t.Fatalf("the room of an answered call did not come back: %v", err)
	}
	elicitations.remove(p2.ID)
	elicitations.mu.Lock()
	left := elicitations.bytes
	elicitations.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d bytes still counted with nothing held", left)
	}
}

// A form of many short choices costs its items, not only its few bytes of text.
func TestElicitManyShortChoicesCountAgainstTheBound(t *testing.T) {
	resetElicitations(t)
	var opts []ElicitOption
	for i := 0; i < 20000; i++ {
		opts = append(opts, ElicitOption{Value: strconv.Itoa(i), Label: "x"})
	}
	ir := inputRequired{inputs: []ElicitInput{{Key: "a", Fields: []ElicitField{{Name: "c", Options: opts}}}}}
	if _, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("20000 short choices were held: %v", err)
	}
}
