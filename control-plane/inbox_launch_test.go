package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// inboxFakeAgent answers the session create and the idempotency lookup the way the Agent does:
// a create under a known key replays the same session, and the lookup finds it while the
// Agent still remembers the key.
type inboxFakeAgent struct {
	mu      sync.Mutex
	creates int
	keys    map[string]string // idempotency key -> session name
	forget  bool              // the Agent restarted: its in-memory ledger is gone
	srv     *httptest.Server
}

func newInboxFakeAgent(t *testing.T) *inboxFakeAgent {
	f := &inboxFakeAgent{keys: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			var body struct {
				Key string `json:"idempotency_key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if name, ok := f.keys[body.Key]; ok && body.Key != "" {
				_, _ = io.WriteString(w, `{"name":"`+name+`","dir":"/home/dev/repos/app"}`)
				return
			}
			f.creates++
			name := "s-" + string(rune('0'+f.creates))
			f.keys[body.Key] = name
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"`+name+`","dir":"/home/dev/repos/app"}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/sessions-idempotency/"):
			name, ok := f.keys[strings.TrimPrefix(r.URL.Path, "/sessions-idempotency/")]
			if !ok || f.forget {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":"not_found"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"name":"`+name+`","dir":"/home/dev/repos/app"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *inboxFakeAgent) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

// inboxPauseFactory holds the first workspace resolution until resume is closed, which is where a
// cold workspace start would be waiting.
type inboxPauseFactory struct {
	once            *sync.Once
	entered, resume chan struct{}
	endpoint        string
}

func (f inboxPauseFactory) New(runtime.Workspace, runtime.SecretKeys, []string) runtime.Runtime {
	f.once.Do(func() { close(f.entered); <-f.resume })
	return stubRuntime{endpoint: f.endpoint, token: "tok"}
}

func (e *inboxEnv) launch(who, id string) *httptest.ResponseRecorder {
	return e.do(who, "POST", "/api/inbox/items/"+id+"/launch", map[string]any{"dir": "/home/dev/repos/app", "kind": "claude"})
}

// TestInboxLaunchLosesItsClaimWhileWaiting is docs/log/130 R-6 with the withdraw or the release
// landing while the launch waits for the workspace: the Agent is never asked, and after a
// release the next assignee's launch is the only session.
func TestInboxLaunchLosesItsClaimWhileWaiting(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"withdraw", "release"} {
		t.Run(op, func(t *testing.T) {
			e := newInboxEnv(t)
			agent := newInboxFakeAgent(t)
			entered, resume := make(chan struct{}), make(chan struct{})
			e.mgr.rtFactory = inboxPauseFactory{&sync.Once{}, entered, resume, agent.srv.URL}
			id := e.send("a", "request", "b", "c")
			if r := e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil); r.Code != 200 {
				t.Fatal(r.Code)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- e.launch("b", id) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the launch never reached workspace resolution")
			}
			who := "b"
			if op == "withdraw" {
				who = "a"
			}
			if r := e.do(who, "POST", "/api/inbox/items/"+id+"/"+op, nil); r.Code != 200 {
				t.Fatalf("%s while the launch waits: %d %s", op, r.Code, r.Body.String())
			}
			if op == "release" {
				if r := e.do("c", "POST", "/api/inbox/items/"+id+"/claim", nil); r.Code != 200 {
					t.Fatalf("c claims: %d", r.Code)
				}
			}
			close(resume)
			late := <-result
			if late.Code != http.StatusConflict || decode[errResp](t, late).Error.Code != errCodeInboxStateConflict {
				t.Fatalf("the late launch: %d %s", late.Code, late.Body.String())
			}
			if n := agent.count(); n != 0 {
				t.Fatalf("%d session(s) created after the claim was lost", n)
			}
			if op == "release" {
				if r := e.launch("c", id); r.Code != http.StatusCreated {
					t.Fatalf("the new assignee's launch: %d %s", r.Code, r.Body.String())
				}
				it, _, _ := e.st.GetInboxItem(context.Background(), id)
				if agent.count() != 1 || it.SessionMembershipID != e.members["c"] {
					t.Fatalf("sessions=%d, recorded for %s", agent.count(), it.SessionMembershipID)
				}
			}
		})
	}
}

// inboxFailFinishStore makes FinishInboxLaunch fail a given number of times: the Agent created the
// session and the CP could not write it down.
type inboxFailFinishStore struct {
	*store.SQL
	mu    sync.Mutex
	fails int
}

func (s *inboxFailFinishStore) FinishInboxLaunch(ctx context.Context, id, mid, token, name, dir, branch, now string) (bool, error) {
	s.mu.Lock()
	if s.fails > 0 {
		s.fails--
		s.mu.Unlock()
		return false, errors.New("database is down")
	}
	s.mu.Unlock()
	return s.SQL.FinishInboxLaunch(ctx, id, mid, token, name, dir, branch, now)
}

// TestInboxLaunchUnrecordedIsNeverCreatedTwice is docs/log/130 R-8 when the record fails after
// the Agent created the session: the next press asks the Agent about the same key and records
// what it finds, and when the Agent no longer knows the key the press refuses instead of
// creating again. Handing the request back is what starts over.
func TestInboxLaunchUnrecordedIsNeverCreatedTwice(t *testing.T) {
	t.Parallel()
	for _, forget := range []bool{false, true} {
		name := "agent-remembers"
		if forget {
			name = "agent-forgot"
		}
		t.Run(name, func(t *testing.T) {
			e := newInboxEnv(t)
			agent := newInboxFakeAgent(t)
			fs := &inboxFailFinishStore{SQL: e.st, fails: inboxLaunchRecordTries}
			e.mgr.store = fs
			e.mgr.rtFactory = inboxPauseFactory{&sync.Once{}, make(chan struct{}), inboxClosedChan(), agent.srv.URL}
			id := e.send("a", "request", "b")
			_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
			if r := e.launch("b", id); r.Code != http.StatusCreated {
				t.Fatalf("first launch: %d %s", r.Code, r.Body.String())
			}
			it, _, _ := e.st.GetInboxItem(context.Background(), id)
			if it.LaunchState != "creating" || it.SessionName != "" {
				t.Fatalf("after a failed record: %+v", it)
			}
			// The badge goes with the card: neither is recorded yet.
			if rec, _ := e.st.ListInboxSessionOrigins(context.Background(), e.members["b"]); len(rec) != 0 {
				t.Fatalf("provenance recorded without the card: %+v", rec)
			}
			if _, ok := agent.keys[inboxLaunchKey(id, it.LaunchToken)]; !ok {
				t.Fatalf("the Agent was not asked under the item's key: %v", agent.keys)
			}
			agent.forget = forget
			r := e.launch("b", id)
			if forget {
				if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeInboxLaunchUnknown {
					t.Fatalf("second press, Agent forgot: %d %s", r.Code, r.Body.String())
				}
				if agent.count() != 1 {
					t.Fatalf("%d sessions after the second press", agent.count())
				}
				_ = e.do("b", "POST", "/api/inbox/items/"+id+"/release", nil)
				_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
				if r := e.launch("b", id); r.Code != http.StatusCreated || agent.count() != 2 {
					t.Fatalf("start over after handing back: %d, sessions=%d", r.Code, agent.count())
				}
				return
			}
			if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeInboxLaunchBusy {
				t.Fatalf("second press: %d %s", r.Code, r.Body.String())
			}
			it, _, _ = e.st.GetInboxItem(context.Background(), id)
			if agent.count() != 1 || it.SessionName != "s-1" || it.LaunchState != "" {
				t.Fatalf("after reconciling: sessions=%d item=%+v", agent.count(), it)
			}
			// The recovered session has its provenance, recorded with the card.
			rec, _ := e.st.ListInboxSessionOrigins(context.Background(), e.members["b"])
			if o := rec["s-1"]; o.ItemID != id || o.SenderKey != key("a") {
				t.Fatalf("the recovered session's provenance: %+v", rec)
			}
			if r := e.launch("b", id); r.Code != http.StatusConflict || agent.count() != 1 {
				t.Fatalf("third press: %d, sessions=%d", r.Code, agent.count())
			}
		})
	}
}

func inboxClosedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// inboxPauseCustodian holds the body's seal, which the send does before its transaction.
type inboxPauseCustodian struct{ entered, resume chan struct{} }

func (c inboxPauseCustodian) Wrap(_ context.Context, _ string, b []byte) (string, error) {
	close(c.entered)
	<-c.resume
	return base64.StdEncoding.EncodeToString(b), nil
}
func (c inboxPauseCustodian) Unwrap(_ context.Context, _, s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// TestInboxSendRacesReceiveControls: a block, a chosen-members setting or a removal saved
// while the send is sealing its body still stops the whole item.
func TestInboxSendRacesReceiveControls(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"block", "chosen", "inactive"} {
		t.Run(mode, func(t *testing.T) {
			e := newInboxEnv(t)
			entered, resume := make(chan struct{}), make(chan struct{})
			e.mgr.custodian = inboxPauseCustodian{entered, resume}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				result <- e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "dm", "recipients": []string{key("c"), key("b")}, "body": "late"})
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the send never reached the seal")
			}
			switch mode {
			case "block":
				if r := e.do("b", "PUT", "/api/inbox/blocks/"+key("a"), nil); r.Code != 200 {
					t.Fatal(r.Code)
				}
			case "chosen":
				if r := e.do("b", "PUT", "/api/inbox/settings", map[string]any{"acceptFrom": "chosen", "allowed": []string{}}); r.Code != 200 {
					t.Fatal(r.Code)
				}
			case "inactive":
				if err := e.st.SetMembershipStatus(context.Background(), e.members["b"], "inactive"); err != nil {
					t.Fatal(err)
				}
			}
			close(resume)
			r := <-result
			want, code := http.StatusForbidden, errCodeInboxNotAccepting
			if mode == "inactive" {
				want, code = http.StatusBadRequest, errCodeInboxInvalidRecipient
			}
			if r.Code != want || decode[errResp](t, r).Error.Code != code {
				t.Fatalf("send: %d %s", r.Code, r.Body.String())
			}
			if n := e.count("inbox_item"); n != 0 {
				t.Fatalf("%d item(s) created", n)
			}
		})
	}
}

// TestInboxReadReconcilesFirst: the read route answers with the item, so a removed assignee's
// claim is already released in that answer (docs/log/130 M-1 when read is the first request).
func TestInboxReadReconcilesFirst(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b", "c")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	// Deactivate behind the store's back, the way an older CP replica would, so only the
	// read's own reconcile can release the claim.
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE membership SET status='inactive' WHERE id=?`, e.members["b"]); err != nil {
		t.Fatal(err)
	}
	got := decode[itemResp](t, e.do("c", "POST", "/api/inbox/items/"+id+"/read", nil)).Item
	if got.State != "open" || got.Assignee != nil || !got.CanClaim {
		t.Fatalf("read after the removal: %+v", got)
	}
}
