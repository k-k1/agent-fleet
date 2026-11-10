package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestCanonicalRepoIdentity(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"github.com/k-k1/agent-fleet", "github.com/k-k1/agent-fleet", true},
		{"https://github.com/K-K1/Agent-Fleet.git", "github.com/k-k1/agent-fleet", true},
		{"https://user:secret@github.com/k-k1/agent-fleet?tab=x#readme", "github.com/k-k1/agent-fleet", true},
		{"git@github.com:k-k1/agent-fleet.git", "github.com/k-k1/agent-fleet", true},
		{"ssh://git@bitbucket.org/team/repo", "bitbucket.org/team/repo", true},
		{"https://gitlab.com/a/b", "", false},           // not a provider a member can connect
		{"https://github.com/a/b/tree/main", "", false}, // not owner/repo
		{"https://github.com/a", "", false},             // no repo
		{"file:///etc/passwd", "", false},               // not a remote
		{"github.com/a/b c", "", false},                 // not a name
		{"https://github.com/../etc", "", false},        // not a name
	} {
		got, ok := canonicalRepoIdentity(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("canonicalRepoIdentity(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func (e *inboxEnv) sendWith(who, kind string, attachments []map[string]any, to ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var keys []string
	for _, n := range to {
		keys = append(keys, key(n))
	}
	return e.do(who, "POST", "/api/inbox/items", map[string]any{"kind": kind, "recipients": keys,
		"body": "please look at the flaky test", "attachments": attachments})
}

var testRecipe = map[string]any{"type": "recipe", "recipe": map[string]any{
	"prompt": "Find why TestLogin is flaky", "kind": "claude", "model": "opus", "effort": "high",
	"repo": "https://x:tok@github.com/k-k1/agent-fleet.git?ref=1"}}

type takeInResp struct {
	Memo    memoDTO `json:"memo"`
	Created bool    `json:"created"`
}

// TestInboxRecipeTakeIn is docs/log/130 T-1..T-3 through the routes: the recipe is invisible
// to the memo queue until taken in, a request's recipe only by its assignee, once, with the
// server's origin, and a DM's by any recipient.
func TestInboxRecipeTakeIn(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	rec := e.sendWith("a", "request", []map[string]any{testRecipe}, "b", "c")
	if rec.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	id := decode[itemResp](t, rec).Item.ID
	got := decode[itemResp](t, e.do("b", "GET", "/api/inbox/items/"+id, nil)).Item
	if len(got.Attachments) != 1 || got.Attachments[0].Recipe == nil {
		t.Fatalf("attachments: %+v", got.Attachments)
	}
	r := got.Attachments[0].Recipe
	if r.Repo != "github.com/k-k1/agent-fleet" || r.Prompt != "Find why TestLogin is flaky" || r.CanTakeIn {
		t.Fatalf("recipe as b sees it before claiming: %+v", r)
	}
	if strings.Contains(rec.Body.String(), "tok@") {
		t.Fatal("the repository's userinfo was kept")
	}
	if n := e.count("memo"); n != 0 {
		t.Fatalf("T-1: %d memo row(s) before take-in", n)
	}
	if r := e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil); r.Code != http.StatusConflict {
		t.Fatalf("take-in before claiming: %d %s", r.Code, r.Body.String())
	}
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	first := e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("T-2 take-in: %d %s", first.Code, first.Body.String())
	}
	m := decode[takeInResp](t, first).Memo
	if m.Origin == nil || m.Origin.ItemID != id || m.Origin.Sender != key("a") || m.Body != "Find why TestLogin is flaky" {
		t.Fatalf("T-2 memo: %+v", m)
	}
	second := e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil)
	if second.Code != http.StatusOK || decode[takeInResp](t, second).Memo.ID != m.ID {
		t.Fatalf("T-3 second take-in: %d %s", second.Code, second.Body.String())
	}
	for _, who := range []string{"c", "a"} {
		if r := e.do(who, "POST", "/api/inbox/items/"+id+"/take-in", nil); r.Code != http.StatusConflict {
			t.Fatalf("T-3 take-in by %s: %d %s", who, r.Code, r.Body.String())
		}
	}
	if n := e.count("memo"); n != 1 {
		t.Fatalf("%d memos after the take-ins", n)
	}
	if got := decode[itemResp](t, e.do("b", "GET", "/api/inbox/items/"+id, nil)).Item.Attachments[0].Recipe; got.MemoID != m.ID || !got.CanTakeIn {
		t.Fatalf("the card does not show the memo taken in: %+v", got)
	}
	// What list_memos reads, through the memo token: the one memo with its origin.
	list := e.memoTokenDo("b", "GET", "/internal/memos", nil)
	var memos []memoDTO
	_ = json.Unmarshal(list.Body.Bytes(), &memos)
	if len(memos) != 1 || memos[0].Origin == nil || memos[0].Origin.ItemID != id {
		t.Fatalf("T-2 list_memos: %s", list.Body.String())
	}
	// A DM: any recipient may take it in.
	dm := decode[itemResp](t, e.sendWith("a", "dm", []map[string]any{testRecipe}, "b", "c")).Item.ID
	if r := e.do("c", "POST", "/api/inbox/items/"+dm+"/take-in", nil); r.Code != http.StatusCreated {
		t.Fatalf("DM take-in: %d %s", r.Code, r.Body.String())
	}
}

func TestInboxRecipeRefusesBadRepo(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	bad := map[string]any{"type": "recipe", "recipe": map[string]any{"prompt": "x", "repo": "https://gitlab.example/a/b"}}
	rec := e.sendWith("a", "dm", []map[string]any{bad}, "b")
	if rec.Code != http.StatusBadRequest || decode[errResp](t, rec).Error.Code != errCodeInboxRepoInvalid {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	if e.count("inbox_item") != 0 {
		t.Fatal("a refused recipe left an item")
	}
}

// memoTokenDo calls the /internal/memos face as the member's agent would, with its memo token.
func (e *inboxEnv) memoTokenDo(who, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+mintMemoToken(memoSignKey(e.mgr.tokenSignMaster()), e.members[who]))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// TestInboxOriginCannotBeForged is docs/log/130 V-2 and V-3: add_memo and update_memo reach
// the CP through /internal/memos with their arguments as they are, and neither can set,
// change or remove an origin. The Console's own memo routes cannot either.
func TestInboxOriginCannotBeForged(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	forged := map[string]any{"kind": "text", "body": "mine", "origin": map[string]any{"itemId": "x", "sender": "boss"}}
	for _, r := range []*httptest.ResponseRecorder{
		e.memoTokenDo("b", "POST", "/internal/memos", forged),
		e.do("b", "POST", "/api/memos", forged),
	} {
		if r.Code != http.StatusCreated || strings.Contains(r.Body.String(), `"origin"`) {
			t.Fatalf("V-3 create with an origin: %d %s", r.Code, r.Body.String())
		}
	}
	id := decode[itemResp](t, e.sendWith("a", "dm", []map[string]any{testRecipe}, "b")).Item.ID
	m := decode[takeInResp](t, e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil)).Memo
	for _, r := range []*httptest.ResponseRecorder{
		e.memoTokenDo("b", "PATCH", "/internal/memos/"+m.ID, map[string]any{"body": "edited by the agent", "origin": nil}),
		e.do("b", "PATCH", "/api/memos/"+m.ID, map[string]any{"body": "edited", "origin": map[string]any{"itemId": "y", "sender": "z"}}),
	} {
		if r.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", r.Code, r.Body.String())
		}
		got := decode[memoDTO](t, r)
		if got.Origin == nil || got.Origin.ItemID != id || got.Origin.Sender != key("a") {
			t.Fatalf("V-2/V-3: the origin after an edit: %+v", got.Origin)
		}
	}
}

// capturingAgent records the prompts a flush sends and the session creates it is asked for.
type capturingAgent struct {
	mu      sync.Mutex
	prompts []string
	creates []map[string]any
	names   []string
	spoof   map[string]any
	srv     *httptest.Server
}

func newCapturingAgent(t *testing.T) *capturingAgent {
	c := &capturingAgent{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/turn"):
			p, _ := body["prompt"].(string)
			c.prompts = append(c.prompts, p)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == "POST" && r.URL.Path == "/sessions":
			c.creates = append(c.creates, body)
			name := "s-" + strconv.Itoa(len(c.creates))
			c.names = append(c.names, name)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"`+name+`","dir":"/home/dev/repos/app"}`)
		case r.Method == "GET" && r.URL.Path == "/sessions":
			// Every session claims a provenance, and one the CP never created claims one too:
			// what the workspace says must not decide the badge.
			list := []map[string]any{}
			for _, n := range append(append([]string{}, c.names...), "spoof") {
				list = append(list, map[string]any{"name": n, "kind": "claude", "inboxOrigin": c.spoof})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": list})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (e *inboxEnv) workspaceFor(who string, agent *capturingAgent) store.Workspace {
	e.t.Helper()
	ws := store.Workspace{ID: "ws-" + who, TenantID: e.t1.ID, MembershipID: e.members[who], ContainerName: who,
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
		e.t.Fatal(err)
	}
	e.mgr.rts[e.members[who]] = cachedRT{rt: stubRuntime{endpoint: agent.srv.URL, token: "tok"}, ws: ws}
	return ws
}

// TestMemoFlushKeepsOriginAlone is docs/log/130 T-4: a taken-in memo is never sent in one
// message with the member's own memos, an edited text included, and it goes with the
// server's origin line.
func TestMemoFlushKeepsOriginAlone(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	agent := newCapturingAgent(t)
	e.workspaceFor("b", agent)
	id := decode[itemResp](t, e.sendWith("a", "dm", []map[string]any{testRecipe}, "b")).Item.ID
	origin := decode[takeInResp](t, e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil)).Memo
	own := decode[memoDTO](t, e.do("b", "POST", "/api/memos", map[string]any{"kind": "text", "body": "my own note"}))
	for _, text := range []string{"", "edited together"} {
		r := e.do("b", "POST", "/api/memos/flush", map[string]any{"sessionName": "s1", "ids": []string{own.ID, origin.ID}, "text": text})
		if r.Code != http.StatusConflict || decode[errResp](t, r).Error.Code != errCodeMemoOriginAlone {
			t.Fatalf("flush of mixed memos (text=%q): %d %s", text, r.Code, r.Body.String())
		}
	}
	if len(agent.prompts) != 0 {
		t.Fatalf("a refused flush reached the session: %q", agent.prompts)
	}
	if r := e.do("b", "POST", "/api/memos/flush", map[string]any{"sessionName": "s1", "ids": []string{origin.ID}, "text": "my edit of it"}); r.Code != 200 {
		t.Fatalf("flush of the origin memo alone: %d %s", r.Code, r.Body.String())
	}
	if r := e.do("b", "POST", "/api/memos/flush", map[string]any{"sessionName": "s1", "ids": []string{own.ID}}); r.Code != 200 {
		t.Fatalf("flush of the own memo: %d %s", r.Code, r.Body.String())
	}
	if len(agent.prompts) != 2 {
		t.Fatalf("prompts: %q", agent.prompts)
	}
	if !strings.HasPrefix(agent.prompts[0], memoOriginLine(store.Memo{OriginItemID: id, OriginSenderKey: key("a")})) ||
		!strings.HasSuffix(agent.prompts[0], "my edit of it") {
		t.Fatalf("the origin memo's message: %q", agent.prompts[0])
	}
	if agent.prompts[1] != "my own note\n" && agent.prompts[1] != "my own note" {
		t.Fatalf("the own memo's message changed: %q", agent.prompts[1])
	}
	m := decode[[]memoDTO](t, e.do("b", "GET", "/api/memos", nil))
	for _, x := range m {
		if x.ID == origin.ID && (x.Origin == nil || x.SentAt == "") {
			t.Fatalf("after its flush the memo lost its origin or was not marked sent: %+v", x)
		}
	}
}

// badges reads the provenance badges through the real session list route.
func (e *inboxEnv) badges(who string) map[string]inboxOriginRef {
	e.t.Helper()
	r := e.do(who, "GET", "/api/sessions", nil)
	if r.Code != http.StatusOK {
		e.t.Fatalf("sessions: %d %s", r.Code, r.Body.String())
	}
	var out struct {
		Sessions []sessionWire `json:"sessions"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil || len(out.Sessions) == 0 {
		e.t.Fatalf("sessions: %v %s", err, r.Body.String())
	}
	got := map[string]inboxOriginRef{}
	for _, s := range out.Sessions {
		if len(s.InboxOrigin) > 0 {
			var o inboxOriginRef
			_ = json.Unmarshal(s.InboxOrigin, &o)
			got[s.Name] = o
		}
	}
	return got
}

// TestInboxProvenanceFromCPRecordOnly is decision 3's provenance badge end to end: it shows for
// a session the CP created from a taken-in memo or from the request launch (with or without
// anything about provenance in the body), and for nothing else — not for a client's
// inbox_origin in any spelling, not for a memo that is not the member's, and not for what the
// workspace reports about its sessions, even naming a real item with its real sender.
func TestInboxProvenanceFromCPRecordOnly(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	agent := newCapturingAgent(t)
	e.workspaceFor("b", agent)
	id := decode[itemResp](t, e.sendWith("a", "request", []map[string]any{testRecipe}, "b")).Item.ID
	agent.spoof = map[string]any{"sender": key("a"), "item": id}
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	memo := decode[takeInResp](t, e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil)).Memo
	create := func(raw string) string {
		t.Helper()
		r := e.do("b", "POST", "/api/sessions", json.RawMessage(raw))
		if r.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", raw, r.Code, r.Body.String())
		}
		for k := range agent.creates[len(agent.creates)-1] {
			if strings.EqualFold(k, "inbox_origin") || strings.EqualFold(k, "inbox_memo_id") {
				t.Fatalf("create %s: %q reached the Agent", raw, k)
			}
		}
		return agent.names[len(agent.names)-1]
	}
	forged := `{"sender":"boss","item":"` + id + `"}`
	plain := create(`{"dir":"/d","kind":"claude"}`)
	escaped := create(`{"dir":"/d","kind":"claude","\u0069nbox_origin":` + forged + `}`)
	upper := create(`{"dir":"/d","kind":"claude","inbox_memo_id":"","INBOX_ORIGIN":` + forged + `}`)
	dup := create(`{"dir":"/d","kind":"claude","inbox_origin":{"item":"x"},"inbox_origin":` + forged + `}`)
	notMine := create(`{"dir":"/d","kind":"claude","inbox_memo_id":"not-mine"}`)
	fromMemo := create(`{"dir":"/d","kind":"claude","Inbox_Memo_Id":"` + memo.ID + `","inbox_origin":` + forged + `}`)
	// The request launch, with nothing about provenance in its body.
	if r := e.do("b", "POST", "/api/inbox/items/"+id+"/launch", map[string]any{"dir": "/d", "kind": "claude"}); r.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", r.Code, r.Body.String())
	}
	launched := agent.names[len(agent.names)-1]
	got := e.badges("b")
	want := inboxOriginRef{Sender: key("a"), Item: id}
	for _, n := range []string{fromMemo, launched} {
		if got[n] != want {
			t.Errorf("%s: badge %+v, want %+v", n, got[n], want)
		}
	}
	for _, n := range []string{plain, escaped, upper, dup, notMine, "spoof"} {
		if o, ok := got[n]; ok {
			t.Errorf("%s: a badge the CP never recorded: %+v", n, o)
		}
	}
	if r := e.do("b", "POST", "/api/sessions", json.RawMessage(`[1,2]`)); r.Code != http.StatusBadRequest {
		t.Fatalf("a body that is not an object: %d", r.Code)
	}
}

// removeDuringTakeIn deactivates a member right after the take-in handler read the item, the
// last step before the store's transaction.
type removeDuringTakeIn struct {
	*store.SQL
	mid string
}

func (s *removeDuringTakeIn) ListInboxAttachments(ctx context.Context, id string) ([]store.InboxAttachment, error) {
	rows, err := s.SQL.ListInboxAttachments(ctx, id)
	if err != nil {
		return rows, err
	}
	return rows, s.SQL.SetMembershipStatus(ctx, s.mid, "inactive")
}

// TestInboxTakeInRacesRemoval: a member removed after the handler's checks and before the
// take-in's transaction gets no memo, for a DM and for a request they finished.
func TestInboxTakeInRacesRemoval(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"dm", "request"} {
		t.Run(kind, func(t *testing.T) {
			e := newInboxEnv(t)
			id := decode[itemResp](t, e.sendWith("a", kind, []map[string]any{testRecipe}, "b")).Item.ID
			if kind == "request" {
				_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
				_ = e.do("b", "POST", "/api/inbox/items/"+id+"/done", nil)
			}
			e.mgr.store = &removeDuringTakeIn{SQL: e.st, mid: e.members["b"]}
			if r := e.do("b", "POST", "/api/inbox/items/"+id+"/take-in", nil); r.Code == http.StatusOK || r.Code == http.StatusCreated {
				t.Fatalf("a removed member took the recipe in: %d %s", r.Code, r.Body.String())
			}
			if n := e.count("memo"); n != 0 {
				t.Fatalf("%d memo(s) for a removed member", n)
			}
		})
	}
}

// sessionFixture puts one session of a's in the share catalogue.
func (e *inboxEnv) sessionFixture() store.SharedSessionCatalog {
	e.t.Helper()
	ctx := context.Background()
	ws := store.Workspace{ID: "ws-a", TenantID: e.t1.ID, MembershipID: e.members["a"], ContainerName: "a",
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkspace(ctx, ws); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ReplaceSharedSessionCatalog(ctx, ws.ID, e.members["a"], []store.SharedSessionCatalog{{ID: store.NewID(),
		WorkspaceID: ws.ID, OwnerMembershipID: e.members["a"], Name: "s1", Kind: "claude", Dir: "/repos/app", Repo: "app",
		Title: "Fix the login race", WorkingCopyID: "wc-1", CreatedAt: store.NowTS(), State: "running", LastSeen: store.NowTS()}}); err != nil {
		e.t.Fatal(err)
	}
	rows, err := e.st.ListSharedSessionCatalogByOwner(ctx, e.members["a"])
	if err != nil || len(rows) != 1 {
		e.t.Fatalf("catalog: %v %v", rows, err)
	}
	return rows[0]
}

// TestInboxSessionAttachment is decision 5: only the owner shares by attaching, with every
// recipient and the scope confirmed; a reader without access sees an opaque placeholder,
// decided on every read; a revoked share turns the card into the placeholder; withdrawing a
// message revokes nothing; a non-owner can attach a session shared with them, granting nothing.
func TestInboxSessionAttachment(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	cat := e.sessionFixture()
	confirm := map[string]any{"recipients": []string{key("b"), key("c")}, "scope": "session"}
	// The share confirmation has to name every recipient.
	partial := map[string]any{"type": "session", "sessionName": "s1", "share": map[string]any{"recipients": []string{key("b")}, "scope": "session"}}
	if r := e.sendWith("a", "dm", []map[string]any{partial}, "b", "c"); r.Code != http.StatusBadRequest ||
		decode[errResp](t, r).Error.Code != errCodeInboxShareUnconfirmed {
		t.Fatalf("partial confirmation: %d %s", r.Code, r.Body.String())
	}
	// Attached without a share: neither recipient has access, so both see the placeholder.
	plain := decode[itemResp](t, e.sendWith("a", "dm", []map[string]any{{"type": "session", "sessionName": "s1"}}, "b", "c")).Item.ID
	for _, who := range []string{"b", "c"} {
		body := e.do(who, "GET", "/api/inbox/items/"+plain, nil).Body.String()
		got := decode[itemResp](t, e.do(who, "GET", "/api/inbox/items/"+plain, nil)).Item.Attachments
		if len(got) != 1 || got[0].Available || got[0].Session != nil {
			t.Fatalf("%s sees an unshared session: %+v", who, got)
		}
		for _, leak := range []string{cat.ID, "Fix the login race", "wc-1", `"s1"`, "/repos/app"} {
			if strings.Contains(body, leak) {
				t.Fatalf("the placeholder for %s leaks %q: %s", who, leak, body)
			}
		}
	}
	// With the confirmed share: both see it, and a share rule exists for each.
	shared := decode[itemResp](t, e.sendWith("a", "dm", []map[string]any{{"type": "session", "sessionName": "s1", "share": confirm}}, "b", "c")).Item.ID
	for _, who := range []string{"b", "c"} {
		got := decode[itemResp](t, e.do(who, "GET", "/api/inbox/items/"+shared, nil)).Item.Attachments
		if len(got) != 1 || !got[0].Available || got[0].Session.CatalogID != cat.ID || got[0].Session.Mine {
			t.Fatalf("%s after the share: %+v", who, got)
		}
		if rows, _ := e.st.ListSessionSharesByRecipient(context.Background(), e.members[who]); len(rows) != 1 {
			t.Fatalf("%s's shares: %+v", who, rows)
		}
	}
	// Decided on every read: the earlier message now shows the session too.
	if got := decode[itemResp](t, e.do("b", "GET", "/api/inbox/items/"+plain, nil)).Item.Attachments; !got[0].Available {
		t.Fatal("the earlier card did not follow the share")
	}
	// A non-owner may attach a session shared with them, granting nothing; one not shared
	// with them is refused, and so is a share they cannot make.
	if r := e.sendWith("b", "dm", []map[string]any{{"type": "session", "catalogId": cat.ID}}, "c"); r.Code != http.StatusCreated {
		t.Fatalf("b attaches a session shared with b: %d %s", r.Code, r.Body.String())
	}
	if r := e.sendWith("b", "dm", []map[string]any{{"type": "session", "catalogId": cat.ID,
		"share": map[string]any{"recipients": []string{key("c")}, "scope": "session"}}}, "c"); r.Code != http.StatusForbidden {
		t.Fatalf("b shares a's session: %d %s", r.Code, r.Body.String())
	}
	// Withdrawing a message revokes no share.
	_ = e.do("a", "POST", "/api/inbox/items/"+shared+"/withdraw", nil)
	if rows, _ := e.st.ListSessionSharesByRecipient(context.Background(), e.members["b"]); len(rows) != 1 {
		t.Fatal("deleting the message took the share with it")
	}
	// Revoking the share turns every card into the placeholder.
	rows, _ := e.st.ListSessionSharesByRecipient(context.Background(), e.members["c"])
	if err := e.st.DeleteSessionShare(context.Background(), rows[0].ID, e.members["a"]); err != nil {
		t.Fatal(err)
	}
	if got := decode[itemResp](t, e.do("c", "GET", "/api/inbox/items/"+shared, nil)).Item.Attachments; got[0].Available {
		t.Fatal("a revoked share still shows the session")
	}
	if r := e.sendWith("c", "dm", []map[string]any{{"type": "session", "catalogId": cat.ID}}, "b"); r.Code != http.StatusNotFound {
		t.Fatalf("c attaches a session no longer shared with c: %d %s", r.Code, r.Body.String())
	}
	// The owner sees their own.
	if got := decode[itemResp](t, e.do("a", "GET", "/api/inbox/items/"+plain, nil)).Item.Attachments; !got[0].Available || !got[0].Session.Mine {
		t.Fatalf("the owner's view: %+v", got)
	}
}

// TestInboxRecoveredLaunchKeepsProvenance: a request launch whose create answer was lost (the
// Agent made the session and the CP saw a 500) is recovered through the idempotency lookup on
// the next press, and that session shows its provenance badge in the real session list.
func TestInboxRecoveredLaunchKeepsProvenance(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	agent := newCapturingAgent(t)
	var mu sync.Mutex
	created := false
	agent.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			created = true
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":"response_lost"}}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/sessions-idempotency/"):
			if !created {
				t.Error("looked up before the create")
			}
			_, _ = io.WriteString(w, `{"name":"s-recovered","dir":"/d"}`)
		case r.Method == "GET" && r.URL.Path == "/sessions":
			_, _ = io.WriteString(w, `{"sessions":[{"name":"s-recovered","kind":"claude"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	e.workspaceFor("b", agent)
	id := e.send("a", "request", "b")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	if r := e.do("b", "POST", "/api/inbox/items/"+id+"/launch", map[string]any{"dir": "/d", "kind": "claude"}); r.Code < 500 {
		t.Fatalf("first launch: %d %s", r.Code, r.Body.String())
	}
	if r := e.do("b", "POST", "/api/inbox/items/"+id+"/launch", map[string]any{"dir": "/d", "kind": "claude"}); r.Code != http.StatusConflict {
		t.Fatalf("recovering press: %d %s", r.Code, r.Body.String())
	}
	it, _, _ := e.st.GetInboxItem(context.Background(), id)
	if it.SessionName != "s-recovered" || it.LaunchState != "" {
		t.Fatalf("the card did not recover: %+v", it)
	}
	if got := e.badges("b"); got["s-recovered"] != (inboxOriginRef{Sender: key("a"), Item: id}) {
		t.Fatalf("the recovered session has no provenance badge: %+v", got)
	}
}

// TestInboxReleaseAfterConfirmKeepsProvenance: the assignee hands the request back after the
// launch was confirmed and before the Agent answers. The release wins the card, which
// decision 2 allows, but the session was still started from the request: it keeps the
// provenance the CP decided at the confirm.
func TestInboxReleaseAfterConfirmKeepsProvenance(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	agent := newCapturingAgent(t)
	agent.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			if rel := e.do("b", "POST", "/api/inbox/items/"+id+"/release", nil); rel.Code != http.StatusOK {
				t.Errorf("release while the Agent creates: %d %s", rel.Code, rel.Body.String())
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"s-after-release","dir":"/d"}`)
		case r.Method == "GET" && r.URL.Path == "/sessions":
			_, _ = io.WriteString(w, `{"sessions":[{"name":"s-after-release","kind":"claude"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	e.workspaceFor("b", agent)
	if r := e.do("b", "POST", "/api/inbox/items/"+id+"/launch", map[string]any{"dir": "/d", "kind": "claude"}); r.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", r.Code, r.Body.String())
	}
	it, _, _ := e.st.GetInboxItem(context.Background(), id)
	if it.State != "open" || it.LaunchToken != "" || it.SessionName != "" {
		t.Fatalf("the release did not win the card: %+v", it)
	}
	if got := e.badges("b"); got["s-after-release"] != (inboxOriginRef{Sender: key("a"), Item: id}) {
		t.Fatalf("the session lost its provenance: %+v", got)
	}
}
