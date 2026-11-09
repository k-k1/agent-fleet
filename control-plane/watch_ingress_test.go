package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/watchingress"
	"github.com/k-k1/agent-fleet/control-plane/internal/webhookpoke"
)

type wiFixture struct {
	t     *testing.T
	st    *store.SQL
	mgr   *manager
	wi    *watchingress.Ingress
	mux   *http.ServeMux
	now   time.Time
	mu    sync.Mutex
	pokes []string // "workspace|source|subject"
	mv    map[string]store.MembershipView
	ws    map[string]store.Workspace
}

func newWIFixture(t *testing.T, appSecret string) *wiFixture {
	t.Helper()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.publicBaseURL = "https://af.example"
	f := &wiFixture{t: t, st: st, mgr: mgr, now: time.Now(), mv: map[string]store.MembershipView{}, ws: map[string]store.Workspace{}}
	f.wi = watchingress.New(watchDeps{mgr}, appSecret)
	f.mux = http.NewServeMux()
	f.wi.Register(f.mux)
	f.wi.Now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	f.wi.Poke = func(_ context.Context, ws store.Workspace, source, subject string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.pokes = append(f.pokes, ws.ID+"|"+source+"|"+subject)
		return nil
	}
	return f
}

// member adds a member with a workspace in the given state to a tenant (created on first use).
func (f *wiFixture) member(tenant, email, state string) store.MembershipView {
	f.t.Helper()
	ctx := context.Background()
	tn, ok, err := f.st.GetTenantBySlug(ctx, tenant)
	if err != nil || !ok {
		tn = seedGitOAuthTenant(f.t, f.st, tenant, "admin@"+tenant+".example")
	}
	mv := addBranchRulesMember(f.t, f.st, tn.ID, email, "member")
	ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: mv.MembershipID, ContainerName: "af-" + email,
		Network: "n", DataDir: "d-" + email, AgentPort: "1", AgentToken: "t", State: state, CreatedAt: store.NowTS()}
	if err := f.st.CreateWorkspace(ctx, ws); err != nil {
		f.t.Fatal(err)
	}
	f.mv[email], f.ws[email] = mv, ws
	return mv
}

func (f *wiFixture) token(mv store.MembershipView) string {
	return watchingress.MintToken(watchingress.SignKey(f.mgr.tokenSignMaster()), mv.MembershipID)
}

func (f *wiFixture) register(mv store.MembershipView, subjects ...string) *httptest.ResponseRecorder {
	body := `{"subjects":[` + `"` + strings.Join(subjects, `","`) + `"` + `]}`
	if len(subjects) == 0 {
		body = `{"subjects":[]}`
	}
	r := httptest.NewRequest(http.MethodPut, "/internal/watch-subjects", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.token(mv))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func sign(secret []byte, body string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (f *wiFixture) waitPokes(n int) []string {
	f.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := append([]string(nil), f.pokes...)
		f.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pokes...)
}

// quiet gives the background forwarder time to (wrongly) poke, then returns what it sent.
func (f *wiFixture) quiet() []string {
	time.Sleep(60 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pokes...)
}

const prEvent = `{"action":"synchronize","number":7,"pull_request":{"number":7},"repository":{"full_name":"Acme/Repo","private":%s}}`

func prBody(private string) string { return strings.Replace(prEvent, "%s", private, 1) }

func (f *wiFixture) appDelivery(body, sig string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", "pull_request")
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *wiFixture) hookDelivery(id, event, body, sig string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/hooks/"+id, strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", event)
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *wiFixture) createHook(mv store.MembershipView, source string) (id, secret string) {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/internal/watch-hooks", strings.NewReader(`{"source":"`+source+`","repo":"Acme/Repo"}`))
	r.Header.Set("Authorization", "Bearer "+f.token(mv))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		f.t.Fatalf("create hook: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	field := func(k string) string {
		i := strings.Index(body, `"`+k+`":"`)
		if i < 0 {
			f.t.Fatalf("no %s in %s", k, body)
		}
		rest := body[i+len(k)+4:]
		return rest[:strings.IndexByte(rest, '"')]
	}
	if !strings.HasPrefix(field("url"), "https://af.example/webhooks/hooks/") {
		f.t.Fatalf("url: %s", body)
	}
	return field("id"), field("secret")
}

const subj = "github:acme/repo#7"

func TestWatchIngressHookDeliveryPokesOnlyThatTenantsRunningWorkspace(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	b := f.member("beta", "b@beta.example", "running")
	f.member("alpha", "stopped@alpha.example", "stopped")
	stopped := f.mv["stopped@alpha.example"]
	for _, mv := range []store.MembershipView{a, b, stopped} {
		if w := f.register(mv, subj); w.Code != http.StatusOK {
			t.Fatalf("register: %d %s", w.Code, w.Body.String())
		}
	}
	id, secret := f.createHook(a, "github")
	body := prBody("true")
	if w := f.hookDelivery(id, "pull_request", body, sign([]byte(secret), body)); w.Code != http.StatusAccepted {
		t.Fatalf("delivery: %d %s", w.Code, w.Body.String())
	}
	got := f.waitPokes(1)
	want := f.ws["a@alpha.example"].ID + "|github|" + subj
	if len(got) != 1 || got[0] != want {
		t.Fatalf("pokes %v, want exactly [%s]: tenant beta's workspace and the stopped workspace must not be poked", got, want)
	}
	if more := f.quiet(); len(more) != 1 {
		t.Fatalf("late pokes: %v", more)
	}
}

func TestWatchIngressRejectsBeforeParsingAndPokesNothing(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	f.register(a, subj)
	id, secret := f.createHook(a, "github")
	otherID, otherSecret := f.createHook(a, "github")
	body := prBody("true")
	for name, c := range map[string]struct{ id, sig, body string }{
		"no signature":          {id, "", body},
		"wrong secret":          {id, sign([]byte("nope"), body), body},
		"another hook's secret": {id, sign([]byte(otherSecret), body), body},
		"unsigned prefix":       {id, strings.TrimPrefix(sign([]byte(secret), body), "sha256="), body},
		"empty hex":             {id, "sha256=", body},
		"signed other body":     {id, sign([]byte(secret), body+" "), body},
		// Garbage JSON with a bad signature must read as 401, proving the signature is checked
		// before the body is parsed.
		"garbage and unsigned": {id, sign([]byte("nope"), "{{{"), "{{{"},
	} {
		if w := f.hookDelivery(c.id, "pull_request", c.body, c.sig); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
	}
	_ = otherID
	// Correctly signed garbage is a 400, not a poke.
	if w := f.hookDelivery(id, "pull_request", "{{{", sign([]byte(secret), "{{{")); w.Code != http.StatusBadRequest {
		t.Errorf("signed garbage: %d", w.Code)
	}
	if w := f.hookDelivery(strings.Repeat("0", 32), "pull_request", body, sign([]byte(secret), body)); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown hook: %d", w.Code)
	}
	if w := f.hookDelivery("not-hex", "pull_request", body, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("malformed hook id: %d", w.Code)
	}
	if p := f.quiet(); len(p) != 0 {
		t.Fatalf("a rejected delivery poked: %v", p)
	}
}

func TestWatchIngressBodyLimitAppliesBeforeVerification(t *testing.T) {
	f := newWIFixture(t, "app-secret")
	big := strings.Repeat("x", webhookpoke.MaxBody+1)
	if w := f.appDelivery(big, sign([]byte("app-secret"), big)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", w.Code)
	}
}

func TestWatchIngressAppEndpointIsOffWithoutASecretAndForwardsPublicReposOnly(t *testing.T) {
	off := newWIFixture(t, "")
	if w := off.appDelivery(prBody("false"), sign(nil, prBody("false"))); w.Code != http.StatusNotFound {
		t.Fatalf("no app secret configured: %d, want 404", w.Code)
	}
	f := newWIFixture(t, "app-secret")
	a := f.member("alpha", "a@alpha.example", "running")
	b := f.member("beta", "b@beta.example", "running")
	f.register(a, subj)
	f.register(b, subj)
	// Private: accepted (the provider must not retry) but forwarded to nobody.
	pb := prBody("true")
	if w := f.appDelivery(pb, sign([]byte("app-secret"), pb)); w.Code != http.StatusAccepted {
		t.Fatalf("private delivery: %d", w.Code)
	}
	if p := f.quiet(); len(p) != 0 {
		t.Fatalf("the shared App poked for a private repository: %v", p)
	}
	// Public: every tenant that registered the subject may be poked, it is public news.
	pub := prBody("false")
	if w := f.appDelivery(pub, sign([]byte("app-secret"), pub)); w.Code != http.StatusAccepted {
		t.Fatalf("public delivery: %d", w.Code)
	}
	if p := f.waitPokes(2); len(p) != 2 {
		t.Fatalf("public pokes: %v", p)
	}
	// A payload that does not say whether the repository is private counts as private.
	f.mu.Lock()
	f.pokes = nil
	f.now = f.now.Add(time.Minute)
	f.mu.Unlock()
	un := `{"number":7,"pull_request":{"number":7},"repository":{"full_name":"acme/repo"}}`
	f.appDelivery(un, sign([]byte("app-secret"), un))
	if p := f.quiet(); len(p) != 0 {
		t.Fatalf("unknown privacy was forwarded: %v", p)
	}
}

func TestWatchIngressReplayCostsOnePokeAndFloodIsLimited(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	f.register(a, subj)
	id, secret := f.createHook(a, "github")
	body := prBody("true")
	sig := sign([]byte(secret), body)
	codes := map[int]int{}
	for i := 0; i < 100; i++ {
		codes[f.hookDelivery(id, "pull_request", body, sig).Code]++
	}
	if codes[http.StatusTooManyRequests] == 0 {
		t.Errorf("100 deliveries in one instant were never limited: %v", codes)
	}
	if p := f.quiet(); len(p) != 1 {
		t.Fatalf("a replayed delivery caused %d pokes, want 1: %v", len(p), p)
	}
	// After the gap a delivery pokes again.
	f.mu.Lock()
	f.now = f.now.Add(watchingress.PokeMinGap + time.Second)
	f.mu.Unlock()
	if w := f.hookDelivery(id, "pull_request", body, sig); w.Code != http.StatusAccepted {
		t.Fatalf("after the gap: %d", w.Code)
	}
	if p := f.waitPokes(2); len(p) != 2 {
		t.Fatalf("pokes after the gap: %v", p)
	}
}

func TestWatchIngressBitbucketHook(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	f.register(a, "bitbucket:acme/repo#9")
	id, secret := f.createHook(a, "bitbucket")
	body := `{"pullrequest":{"id":9},"repository":{"full_name":"Acme/Repo","is_private":true}}`
	send := func(sig string) int {
		r := httptest.NewRequest(http.MethodPost, "/webhooks/hooks/"+id, strings.NewReader(body))
		r.Header.Set("X-Event-Key", "pullrequest:updated")
		r.Header.Set("X-Hub-Signature", sig)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w.Code
	}
	if c := send(sign([]byte("wrong"), body)); c != http.StatusUnauthorized {
		t.Fatalf("bad bitbucket signature: %d", c)
	}
	if c := send(sign([]byte(secret), body)); c != http.StatusAccepted {
		t.Fatalf("bitbucket delivery: %d", c)
	}
	if p := f.waitPokes(1); len(p) != 1 || !strings.HasSuffix(p[0], "|bitbucket|bitbucket:acme/repo#9") {
		t.Fatalf("pokes %v", p)
	}
}

func TestWatchIngressRegistrationIsBoundedAndReplaced(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	var many []string
	for i := 1; i <= watchingress.MaxSubjects+1; i++ {
		many = append(many, "github:acme/repo#"+strconv.Itoa(i))
	}
	if w := f.register(a, many...); w.Code != http.StatusBadRequest {
		t.Fatalf("201 subjects: %d, want 400", w.Code)
	}
	if w := f.register(a, many[:watchingress.MaxSubjects]...); w.Code != http.StatusOK {
		t.Fatalf("200 subjects: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"github:acme/repo", "gitlab:acme/repo#1", "github:acme/repo#0", "github:../x#1", "github:Acme/Repo#1", "github:a/b#1\\n"} {
		if w := f.register(a, bad); w.Code != http.StatusBadRequest {
			t.Errorf("subject %q accepted: %d", bad, w.Code)
		}
	}
	// A refused set changes nothing: the 200 are still there.
	targets, _ := f.st.WatchTargets(context.Background(), "github:acme/repo#5", a.TenantID, store.NowTS(), 10)
	if len(targets) != 1 {
		t.Fatalf("a refused registration disturbed the stored set: %v", targets)
	}
	// Replacing with an empty set withdraws everything (the last subscription ended).
	if w := f.register(a); w.Code != http.StatusOK {
		t.Fatalf("withdraw: %d", w.Code)
	}
	targets, _ = f.st.WatchTargets(context.Background(), "github:acme/repo#5", a.TenantID, store.NowTS(), 10)
	if len(targets) != 0 {
		t.Fatalf("withdrawn subject still registered: %v", targets)
	}
}

func TestWatchIngressRegistrationExpires(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	f.register(a, subj)
	later := time.Now().UTC().Add(watchingress.SubjectTTL + time.Minute).Format(time.RFC3339)
	if got, _ := f.st.WatchTargets(context.Background(), subj, "", later, 10); len(got) != 0 {
		t.Fatalf("an expired registration still matches: %v", got)
	}
}

func TestWatchIngressBridgeTokenAndHooks(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	b := f.member("alpha", "b@alpha.example", "running")
	for name, tok := range map[string]string{
		"empty":      "",
		"other key":  mintBranchRulesToken(branchRulesSignKey(f.mgr.tokenSignMaster()), a.MembershipID),
		"wrong sign": watchingress.MintToken(branchRulesSignKey(f.mgr.tokenSignMaster()), a.MembershipID),
	} {
		r := httptest.NewRequest(http.MethodPut, "/internal/watch-subjects", strings.NewReader(`{"subjects":[]}`))
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	id, _ := f.createHook(a, "github")
	// Another member cannot delete it; the owner can.
	del := func(mv store.MembershipView) int {
		r := httptest.NewRequest(http.MethodDelete, "/internal/watch-hooks/"+id, nil)
		r.Header.Set("Authorization", "Bearer "+f.token(mv))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w.Code
	}
	if c := del(b); c != http.StatusNotFound {
		t.Fatalf("another member deleted the hook: %d", c)
	}
	if c := del(a); c != http.StatusOK {
		t.Fatalf("owner delete: %d", c)
	}
	body := prBody("true")
	_, secret := f.createHook(a, "github")
	if w := f.hookDelivery(id, "pull_request", body, sign([]byte(secret), body)); w.Code != http.StatusUnauthorized {
		t.Fatalf("a deleted hook still answers: %d", w.Code)
	}
	for i := 1; i < watchingress.MaxHooks; i++ {
		f.createHook(a, "github")
	}
	r := httptest.NewRequest(http.MethodPost, "/internal/watch-hooks", bytes.NewReader([]byte(`{"source":"github"}`)))
	r.Header.Set("Authorization", "Bearer "+f.token(a))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("hook %d: %d, want 409", watchingress.MaxHooks+1, w.Code)
	}
}

func TestWatchHookSecretIsPerHookAndNotTheIDOrAnotherRoot(t *testing.T) {
	root := []byte("root-0000000000000000000000000000")
	s1, s2 := watchingress.HookSecret(root, "aaaa"), watchingress.HookSecret(root, "bbbb")
	if bytes.Equal(s1, s2) || bytes.Contains(s1, []byte("aaaa")) || bytes.Equal(s1, watchingress.HookSecret([]byte("root-1111111111111111111111111111"), "aaaa")) {
		t.Fatal("hook secrets must differ per hook and per signing root")
	}
	if len(s1) != 64 {
		t.Fatalf("secret length %d", len(s1))
	}
}

func TestWorkspaceEnvCarriesTheWatchToken(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"
	ws := store.Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID}
	var token string
	for _, kv := range mgr.workspaceExtraEnv(context.Background(), ws) {
		if v, ok := strings.CutPrefix(kv, "AF_WATCH_TOKEN="); ok {
			token = v
		}
	}
	if mid, ok := watchingress.VerifyToken(watchingress.SignKey(mgr.tokenSignMaster()), token); !ok || mid != mv.MembershipID {
		t.Fatalf("injected token %q does not verify to this membership: (%q,%v)", token, mid, ok)
	}
}

// Anonymous requests are limited before any database lookup, so a flood of guessed hook ids costs
// a counter and not a read.
func TestWatchIngressLimitsAnonymousRequestsBeforeLookup(t *testing.T) {
	f := newWIFixture(t, "")
	codes := map[int]int{}
	for i := 0; i < 300; i++ {
		codes[f.hookDelivery(strings.Repeat("0", 32), "pull_request", "{}", "").Code]++
	}
	if codes[http.StatusTooManyRequests] == 0 || codes[http.StatusUnauthorized] == 0 {
		t.Fatalf("300 guesses in one instant: %v, want some 404 and some 429", codes)
	}
}

// A hook made for one repository speaks only for it, and stops speaking when its owner leaves.
func TestWatchIngressHookIsBoundToItsRepoAndOwner(t *testing.T) {
	f := newWIFixture(t, "")
	a := f.member("alpha", "a@alpha.example", "running")
	f.register(a, subj, "github:acme/other#7")
	id, secret := f.createHook(a, "github") // repo Acme/Repo
	other := `{"number":7,"pull_request":{"number":7},"repository":{"full_name":"acme/other","private":true}}`
	if w := f.hookDelivery(id, "pull_request", other, sign([]byte(secret), other)); w.Code != http.StatusAccepted {
		t.Fatalf("delivery: %d", w.Code)
	}
	if p := f.quiet(); len(p) != 0 {
		t.Fatalf("a hook made for acme/repo poked for acme/other: %v", p)
	}
	own := prBody("true")
	if w := f.hookDelivery(id, "pull_request", own, sign([]byte(secret), own)); w.Code != http.StatusAccepted {
		t.Fatalf("delivery: %d", w.Code)
	}
	if p := f.waitPokes(1); len(p) != 1 {
		t.Fatalf("own repo pokes: %v", p)
	}
	if err := f.st.SetMembershipStatus(context.Background(), a.MembershipID, "inactive"); err != nil {
		t.Fatal(err)
	}
	f.mgr.evictMembershipCache(a.MembershipID)
	f.mu.Lock()
	f.now = f.now.Add(time.Minute)
	f.mu.Unlock()
	if w := f.hookDelivery(id, "pull_request", own, sign([]byte(secret), own)); w.Code != http.StatusUnauthorized {
		t.Fatalf("a removed owner's hook still answers: %d", w.Code)
	}
}
