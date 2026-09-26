package msp_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

func TestCallReturnsTheResult(t *testing.T) {
	host, client := msptest.New(t, msp.Handler{})
	host.Handle(msp.MethodSessionStart, func(m msptest.Message) (any, *msp.Error) {
		return msp.SessionStartResult{ViewCursor: "v:1"}, nil
	})

	var res msp.SessionStartResult
	if err := client.CallInto(msp.MethodSessionStart, map[string]any{"commandId": msp.NewCommandID()}, 5*time.Second, &res); err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.ViewCursor != "v:1" {
		t.Errorf("viewCursor = %q, want v:1", res.ViewCursor)
	}
}

func TestCallSurfacesTheErrorObject(t *testing.T) {
	host, client := msptest.New(t, msp.Handler{})
	host.Handle(msp.MethodApprovalDecide, func(m msptest.Message) (any, *msp.Error) {
		return nil, &msp.Error{
			Code:    msp.ErrCodeApprovalRequirementStale,
			Message: "stale requirement",
			Data:    json.RawMessage(`{"approvalId":"a-1"}`),
		}
	})

	_, err := client.Call(msp.MethodApprovalDecide, map[string]any{}, 5*time.Second)
	if err == nil {
		t.Fatal("want an error")
	}
	if !msp.HasCode(err, msp.ErrCodeApprovalRequirementStale) {
		t.Errorf("HasCode(requirementStale) = false for %v", err)
	}
	if msp.HasCode(err, msp.ErrCodeApprovalNotFound) {
		t.Error("HasCode matched a code the server did not send")
	}
	var e *msp.Error
	if !errors.As(err, &e) {
		t.Fatalf("errors.As: %v", err)
	}
	if d := e.Detail(); d == nil || d.ApprovalID == nil || *d.ApprovalID != "a-1" {
		t.Errorf("Detail().ApprovalID = %v, want a-1", d)
	}
}

// The schema's own retryable set decides this. A client that retried on, say, a settled
// approval would turn a clean refusal into a loop.
func TestRetryableFollowsTheSchema(t *testing.T) {
	retryable := &msp.Error{Code: msp.ErrCodeOverloaded}
	if !msp.Retryable(retryable) {
		t.Error("overloaded should be retryable")
	}
	for _, code := range []int{msp.ErrCodeInvalidParams, msp.ErrCodeApprovalAlreadyResolved, msp.ErrCodeCommandRejected} {
		if msp.Retryable(&msp.Error{Code: code}) {
			t.Errorf("code %d should not be retryable", code)
		}
	}
	if msp.Retryable(errors.New("plain")) {
		t.Error("a non-MSP error is not retryable")
	}
}

// Both answer channels re-deliver, so answering twice is normal traffic: the second answer
// must read as success, not as a failed turn (ADR 0095 decision 13, measured).
func TestSettledCoversBothAnswerChannels(t *testing.T) {
	if !msp.Settled(&msp.Error{Code: msp.ErrCodeUserInputAlreadySettled}) {
		t.Error("userInputAlreadySettled should read as settled")
	}
	if !msp.Settled(&msp.Error{Code: msp.ErrCodeApprovalAlreadyResolved}) {
		t.Error("approvalAlreadyResolved should read as settled")
	}
	if msp.Settled(&msp.Error{Code: msp.ErrCodeUserInputAnswerInvalid}) {
		t.Error("an invalid answer is a real failure, not a settled one")
	}
}

// The delivery that matters: a host sends approvals and user input as NOTIFICATIONS. A client
// that only implements OnRequest parks the turn on approvalPending forever.
func TestNotificationsReachTheHandler(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]json.RawMessage{}
	got := make(chan string, 8)
	host, _ := msptest.New(t, msp.Handler{
		OnNotification: func(method string, params json.RawMessage) {
			mu.Lock()
			seen[method] = params
			mu.Unlock()
			got <- method
		},
	})

	host.Notify(msp.NotificationApprovalRequested, map[string]any{"approvalId": "a-1"})
	host.Notify(msp.NotificationSessionStatusChanged, map[string]any{"status": "running"})
	host.Notify(msp.NotificationUserInputRequested, map[string]any{"userInputId": "u-1"})

	for i := 0; i < 3; i++ {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("notification did not arrive")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{msp.NotificationApprovalRequested, msp.NotificationSessionStatusChanged, msp.NotificationUserInputRequested} {
		if _, ok := seen[want]; !ok {
			t.Errorf("notification %s never reached the handler", want)
		}
	}
	if !strings.Contains(string(seen[msp.NotificationApprovalRequested]), "a-1") {
		t.Errorf("params were not passed through: %s", seen[msp.NotificationApprovalRequested])
	}
}

// Each direction owns its own id space and an id may be a string, so a server request's id
// must travel back byte-for-byte. Reinterpreting it as a number is how a receipt lands on the
// wrong request.
func TestRespondEchoesAStringIDVerbatim(t *testing.T) {
	type reqSeen struct {
		id     json.RawMessage
		method string
	}
	reqs := make(chan reqSeen, 4)
	host, client := msptest.New(t, msp.Handler{
		OnRequest: func(id json.RawMessage, method string, params json.RawMessage) {
			reqs <- reqSeen{id: id, method: method}
		},
	})

	host.Request(`"req-7"`, msp.ServerRequestApprovalRequest, map[string]any{"approvalId": "a-1"})

	var seen reqSeen
	select {
	case seen = <-reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("server request did not reach the handler")
	}
	if seen.method != msp.ServerRequestApprovalRequest {
		t.Errorf("method = %q", seen.method)
	}
	if err := client.Respond(seen.id, msp.RequestReceipt{}); err != nil {
		t.Fatalf("respond: %v", err)
	}

	reply := host.WaitFor(func(m msptest.Message) bool { return len(m.Result) > 0 })
	if string(reply.ID) != `"req-7"` {
		t.Errorf("responded with id %s, want \"req-7\" verbatim", reply.ID)
	}
}

// A notification carries no id, so it must never be routed as a server request — the client
// would then wait for an answer nobody owes it.
func TestNullIDIsNotTreatedAsARequest(t *testing.T) {
	requests := make(chan string, 4)
	notes := make(chan string, 4)
	host, _ := msptest.New(t, msp.Handler{
		OnRequest:      func(id json.RawMessage, method string, params json.RawMessage) { requests <- method },
		OnNotification: func(method string, params json.RawMessage) { notes <- method },
	})

	host.Request(`null`, msp.NotificationTurnCompleted, map[string]any{})

	select {
	case m := <-notes:
		if m != msp.NotificationTurnCompleted {
			t.Errorf("notification method = %q", m)
		}
	case m := <-requests:
		t.Fatalf("a null id was routed as a server request: %s", m)
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
	}
}

func TestCallTimesOut(t *testing.T) {
	_, client := msptest.New(t, msp.Handler{}) // no handler: nothing ever answers
	_, err := client.Call(msp.MethodUsageRead, nil, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout")
	}
	if !strings.Contains(err.Error(), msp.MethodUsageRead) {
		t.Errorf("the timeout does not name the method: %v", err)
	}
}

// A dead child must fail every in-flight call rather than leave a turn hanging on a channel
// nobody will ever write to.
func TestClosedConnectionFailsInflightCalls(t *testing.T) {
	host, client := msptest.New(t, msp.Handler{})
	// Park a call, then kill the connection from the host side.
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Call(msp.MethodSessionRead, map[string]any{}, 0)
		errCh <- err
	}()
	host.WaitForMethod(msp.MethodSessionRead)
	host.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, msp.ErrClosed) {
			t.Errorf("err = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight call never returned")
	}

	select {
	case <-client.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("Closed() never fired")
	}
	if _, err := client.Call(msp.MethodUsageRead, nil, time.Second); !errors.Is(err, msp.ErrClosed) {
		t.Errorf("a call after close returned %v, want ErrClosed", err)
	}
}

func TestHandshakeSendsInitializeThenInitialized(t *testing.T) {
	host, client := msptest.New(t, msp.Handler{})
	host.Handle(msp.MethodInitialize, func(m msptest.Message) (any, *msp.Error) {
		var p msp.InitializeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("initialize params: %v", err)
		}
		if p.ClientInfo.Name != msp.ClientName {
			t.Errorf("clientInfo.name = %q, want %q", p.ClientInfo.Name, msp.ClientName)
		}
		if p.Capabilities == nil || len(p.Capabilities.RequestedCapabilities) != 1 ||
			p.Capabilities.RequestedCapabilities[0] != string(msp.CapabilityNameSessionMCP) {
			t.Errorf("requestedCapabilities = %+v", p.Capabilities)
		}
		return msp.InitializeResult{
			GrantedCapabilities: []msp.CapabilityName{msp.CapabilityNameSessionMCP},
			Schema:              msp.SchemaInfo{Fingerprint: msp.SchemaFingerprint, Version: 1},
		}, nil
	})

	res, err := msp.Handshake(client, "0.1.0", []msp.CapabilityName{msp.CapabilityNameSessionMCP})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !msp.Granted(res, msp.CapabilityNameSessionMCP) {
		t.Error("sessionMcp should read as granted")
	}
	if msp.Granted(res, msp.CapabilityNameUserShell) {
		t.Error("userShell was not granted and must not read as granted")
	}
	if d := msp.SchemaDrift(res); d != "" {
		t.Errorf("SchemaDrift = %q, want empty for a matching fingerprint", d)
	}
	host.WaitForMethod(msp.NotificationInitialized)
}

func TestSchemaDriftNamesBothFingerprints(t *testing.T) {
	res := &msp.InitializeResult{Schema: msp.SchemaInfo{Fingerprint: "sha256:other", Version: 1}}
	d := msp.SchemaDrift(res)
	if !strings.Contains(d, "sha256:other") || !strings.Contains(d, msp.SchemaFingerprint) {
		t.Errorf("SchemaDrift = %q, want both fingerprints", d)
	}
	if msp.SchemaDrift(nil) != "" {
		t.Error("SchemaDrift(nil) should be empty")
	}
}

func TestDeclaredNotificationsCoverTheSchema(t *testing.T) {
	if got := len(msp.DeclaredNotifications()); got != 33 {
		t.Errorf("the schema declares %d notifications, want 33", got)
	}
	typ, ok := msp.DeclaredNotification(msp.NotificationTurnCompleted)
	if !ok || typ != "TurnCompletedParams" {
		t.Errorf("DeclaredNotification(turn/completed) = %q, %v", typ, ok)
	}
}

// session/started and session/closed were on the wire before the stable surface declared them
// (1.3.0-R3401.1 emitted session/started ahead of the session/start response; the bundle from
// 1.4.0-R4161.1 on declares both). Their params now decode, but the table stays a decode map:
// a host still on 1.3.0 sends them undeclared, and the next undeclared name will arrive the
// same way, so a dispatcher that used the table as an allow-list would reject real traffic.
func TestLifecycleNotificationsAreDeclared(t *testing.T) {
	for method, want := range map[string]string{
		msp.NotificationSessionStarted: "SessionStartedParams",
		msp.NotificationSessionClosed:  "SessionClosedParams",
	} {
		if typ, ok := msp.DeclaredNotification(method); !ok || typ != want {
			t.Errorf("DeclaredNotification(%s) = %q, %v; want %q", method, typ, ok, want)
		}
	}
}

// A named empty object is a wire value, not free-form JSON. Rendering RequestReceipt the way
// an untyped `properties: {}` property is rendered marshals the zero value as `""`, and the
// host would be answering a must-answer request with a string.
func TestEmptyObjectTypesMarshalAsObjects(t *testing.T) {
	b, err := json.Marshal(msp.RequestReceipt{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Errorf("RequestReceipt{} marshals as %s, want {}", b)
	}
	if b, _ := json.Marshal(msp.ViewUnsubscribeResult{}); string(b) != "{}" {
		t.Errorf("ViewUnsubscribeResult{} marshals as %s, want {}", b)
	}
}
