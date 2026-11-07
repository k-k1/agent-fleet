package routegw

import (
	"context"
	"io"
	"maps"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Fresh pulls inside MinInterval, a failed fresh pull keeps the last copy (never an older one,
// never a list it did not get) and MaxAge still ends it.
func TestSettingsSourceFreshPullsInsideTheIntervalAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	pulls, ok := 0, true
	list := map[string]awsx.Profile{"p": {ID: "1", ModelRoute: true}}
	s := &SettingsSource{
		Now: func() time.Time { return now }, MinInterval: 30 * time.Second, MaxAge: 10 * time.Minute,
		Pull: func() (map[string]awsx.Profile, bool) { pulls++; return list, ok },
	}
	s.Get()
	list = map[string]awsx.Profile{"p": {ID: "1", ModelRoute: false}}
	now = now.Add(time.Second)
	if m, got := s.Fresh(); !got || m["p"].ModelRoute || pulls != 2 {
		t.Fatalf("Fresh inside the interval: %+v ok=%v pulls=%d", m, got, pulls)
	}
	// The fresh pull counts as the interval's pull.
	now = now.Add(time.Second)
	if m, _ := s.Get(); m["p"].ModelRoute || pulls != 2 {
		t.Fatalf("Get after Fresh: %+v pulls=%d", m, pulls)
	}
	// The CP unreachable: Fresh pulls, fails, and answers from the newest copy it had.
	ok = false
	list = map[string]awsx.Profile{"p": {ID: "1", ModelRoute: true}}
	now = now.Add(5 * time.Minute)
	if m, got := s.Fresh(); !got || m["p"].ModelRoute || pulls != 3 {
		t.Fatalf("Fresh with the CP unreachable: %+v ok=%v pulls=%d", m, got, pulls)
	}
	now = now.Add(6 * time.Minute)
	if _, got := s.Fresh(); got {
		t.Fatal("Fresh served a copy older than MaxAge")
	}
}

// A Fresh caller never takes a pull that was already running when it arrived (that pull may have
// read Settings before the edit), and callers arriving together share one pull.
func TestSettingsSourceFreshWaitsOutAnEarlierPullAndCoalesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		pulls := 0
		marked := true
		release := make(chan struct{})
		s := &SettingsSource{
			MinInterval: 30 * time.Second, MaxAge: 10 * time.Minute,
			Pull: func() (map[string]awsx.Profile, bool) {
				mu.Lock()
				pulls++
				first, m := pulls == 1, marked
				mu.Unlock()
				if first {
					<-release // the first pull read Settings before the edit and is slow to return
				}
				return map[string]awsx.Profile{"p": {ID: "1", ModelRoute: m}}, true
			},
		}
		var first map[string]awsx.Profile
		go func() { first, _ = s.Fresh() }()
		synctest.Wait()
		mu.Lock()
		marked = false // the member unmarks while that pull is running
		mu.Unlock()
		const n = 8
		got := make([]map[string]awsx.Profile, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { got[i], _ = s.Fresh() })
		}
		synctest.Wait()
		close(release)
		wg.Wait()
		synctest.Wait()
		// The first caller may answer with the second pull's copy already: newer is never wrong.
		if first == nil {
			t.Fatal("the first caller got nothing")
		}
		for i, m := range got {
			if m["p"].ModelRoute {
				t.Fatalf("caller %d got the pull that started before it", i)
			}
		}
		if pulls != 2 {
			t.Fatalf("%d concurrent fresh callers made %d pulls, want 2", n+1, pulls)
		}
	})
}

// A Pull that panics counts as a failed pull: it neither crashes the Agent nor leaves later
// callers blocked on its flight.
func TestSettingsSourceSurvivesAPanickingPull(t *testing.T) {
	var mu sync.Mutex
	boom := true
	s := &SettingsSource{MinInterval: time.Minute, MaxAge: time.Hour, Wait: 5 * time.Second,
		Pull: func() (map[string]awsx.Profile, bool) {
			mu.Lock()
			defer mu.Unlock()
			if boom {
				panic("pull")
			}
			return map[string]awsx.Profile{"p": {ID: "1"}}, true
		}}
	if _, ok := s.Fresh(); ok {
		t.Fatal("a panicking pull served a list")
	}
	mu.Lock()
	boom = false
	mu.Unlock()
	if _, ok := s.Fresh(); !ok {
		t.Fatal("the pull after a panic served nothing")
	}
}

// A pull that never returns (the ~/.aws/config lock held by another process) does not hang its
// callers: they give up after Wait and answer from the last copy, and refuse once it is older
// than MaxAge.
func TestSettingsSourceBoundsAPullThatNeverReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		pulls := 0
		hang := make(chan struct{})
		defer close(hang)
		s := &SettingsSource{MinInterval: 30 * time.Second, MaxAge: 10 * time.Minute, Wait: 15 * time.Second,
			Pull: func() (map[string]awsx.Profile, bool) {
				mu.Lock()
				pulls++
				first := pulls == 1
				mu.Unlock()
				if !first {
					<-hang
				}
				return map[string]awsx.Profile{"p": {ID: "1", ModelRoute: true}}, true
			}}
		if _, ok := s.Get(); !ok {
			t.Fatal("first pull")
		}
		start := time.Now()
		for range 3 {
			if m, ok := s.Fresh(); !ok || !m["p"].ModelRoute {
				t.Fatalf("Fresh on a hung pull: %+v ok=%v", m, ok)
			}
		}
		if waited := time.Since(start); waited != 45*time.Second {
			t.Fatalf("three Fresh calls on a hung pull took %v, want 3 × Wait", waited)
		}
		mu.Lock()
		n := pulls
		mu.Unlock()
		if n != 2 {
			t.Fatalf("pulls = %d, want 2 (the hung one is waited on, not doubled)", n)
		}
		time.Sleep(10 * time.Minute)
		if _, ok := s.Fresh(); ok {
			t.Fatal("a copy older than MaxAge was served while the pull hangs")
		}
	})
}

// The gateway on the real SettingsSource: a profile unmarked, edited or deleted is enforced at
// once by a create's check and bind, a start's issue, and any call that has to resolve
// credentials. Only a call on cached credentials, with no fresh pull since the change, keeps the
// per-call interval (the window the guide states).
func TestAChangeIsEnforcedAtOnceOnCreateStartAndCredentialResolution(t *testing.T) {
	cases := map[string]struct {
		change func(e *env)
		code   string
	}{
		"unmarked": {func(e *env) {
			p := e.settings["bedrock-invoke"]
			p.ModelRoute = false
			e.settings["bedrock-invoke"] = p
		}, routes.CodeRouteForbidden},
		"role edited": {func(e *env) {
			p := e.settings["bedrock-invoke"]
			p.RoleName = "Admin"
			e.settings["bedrock-invoke"] = p
		}, "route_connection_changed"},
		"deleted": {func(e *env) { delete(e.settings, "bedrock-invoke") }, routes.CodeRouteForbidden},
	}
	setup := func(t *testing.T) (*env, session.Meta, string) {
		e := newEnv(t)
		useSettingsSource(e)
		// Credentials usable for 2 s, so a call 3 s on has to resolve them again.
		e.credExp = e.now.Add(credReserve + 2*time.Second)
		m := e.session("s1", "p1", "")
		tok := e.issue(m)
		if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
			t.Fatalf("before = %d", resp.StatusCode)
		}
		return e, m, tok
	}
	for name, c := range cases {
		t.Run(name+"/create and start", func(t *testing.T) {
			e, m, tok := setup(t)
			e.mu.Lock()
			c.change(e)
			e.mu.Unlock()
			e.advance(time.Second)
			// Cached credentials, inside the interval: the per-call window this source keeps.
			if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
				t.Fatalf("a call on cached credentials inside the interval = %d", resp.StatusCode)
			}
			if ref := e.g.CheckRoute(context.Background(), *m.Route, ""); ref == nil || ref.Code != c.code {
				t.Fatalf("create check 1 s after the change = %+v", ref)
			}
			if _, ref := e.g.Bind(*m.Route); ref == nil || ref.Code != c.code {
				t.Fatalf("create bind 1 s after the change = %+v", ref)
			}
			if _, ref := e.g.Issue(m); ref == nil || ref.Code != c.code {
				t.Fatalf("start 1 s after the change = %+v", ref)
			}
			// The fresh pull is the copy every later call is checked against too.
			if resp, body := e.call(tok, invokePath(), `{}`); resp.StatusCode != http.StatusUnauthorized || codeOf(t, body) != c.code {
				t.Fatalf("a call after a fresh pull = %d %s", resp.StatusCode, body)
			}
		})
		t.Run(name+"/credential resolution", func(t *testing.T) {
			e, _, tok := setup(t)
			e.mu.Lock()
			c.change(e)
			e.mu.Unlock()
			e.advance(3 * time.Second)
			resp, body := e.call(tok, invokePath(), `{}`)
			if resp.StatusCode != http.StatusUnauthorized || codeOf(t, body) != c.code {
				t.Fatalf("a call that resolves credentials 3 s after the change = %d %s", resp.StatusCode, body)
			}
			if _, n := e.counts(); n != 1 {
				t.Fatalf("credentials were resolved %d times, want 1 (none after the change)", n)
			}
		})
	}
}

// useSettingsSource puts e's gateway on a real SettingsSource that pulls e.settings.
func useSettingsSource(e *env) *SettingsSource {
	src := &SettingsSource{Now: e.g.d.Now, MinInterval: 30 * time.Second, MaxAge: 15 * time.Minute,
		Pull: func() (map[string]awsx.Profile, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return maps.Clone(e.settings), true
		}}
	e.g.d.Settings, e.g.d.FreshSettings = src.Get, src.Fresh
	return src
}

// The decision to read Settings fresh is taken where credentials are resolved, not at the check:
// a call checked on cached credentials whose body arrives after they lapsed resolves nothing on
// that check. Here the body is held until the per-call check has run, the profile is unmarked and
// the credentials lapse; the call must be refused and resolve nothing.
func TestCredentialsLapsingWhileTheBodyArrivesAreNotResolvedOnAStaleCheck(t *testing.T) {
	e := newEnv(t)
	src := useSettingsSource(e)
	checked := make(chan struct{}, 16)
	e.g.d.Settings = func() (map[string]awsx.Profile, bool) {
		m, ok := src.Get()
		select {
		case checked <- struct{}{}:
		default:
		}
		return m, ok
	}
	e.credExp = e.now.Add(credReserve + 2*time.Second)
	tok := e.issue(e.session("s1", "p1", ""))
	if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
		t.Fatalf("before = %d", resp.StatusCode)
	}
	for len(checked) > 0 {
		<-checked
	}

	pr, pw := io.Pipe()
	req, _ := http.NewRequest("POST", SocketBaseURL+invokePath(), pr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := e.client.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: string(b)}
	}()
	select {
	case <-checked: // the per-call check ran on cached credentials; the body is still held
	case <-time.After(10 * time.Second):
		t.Fatal("the call was never checked")
	}
	e.mu.Lock()
	p := e.settings["bedrock-invoke"]
	p.ModelRoute = false
	e.settings["bedrock-invoke"] = p
	e.mu.Unlock()
	e.advance(3 * time.Second) // inside Settings' interval, past the credentials' use
	_, _ = pw.Write([]byte(`{}`))
	_ = pw.Close()
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.status != http.StatusUnauthorized || codeOf(t, r.body) != routes.CodeRouteForbidden {
		t.Fatalf("the call = %d %s", r.status, r.body)
	}
	if _, n := e.counts(); n != 1 {
		t.Fatalf("credentials were resolved %d times, want 1 (none for the stale check)", n)
	}
}

// Credentials already inside credReserve are reused for credShortReuse rather than resolved, with
// a fresh Settings pull, on every call.
func TestShortLivedCredentialsAreReusedBriefly(t *testing.T) {
	e := newEnv(t)
	pulls := 0
	src := useSettingsSource(e)
	inner := src.Pull
	src.Pull = func() (map[string]awsx.Profile, bool) { pulls++; return inner() }
	e.credExp = e.now.Add(4 * time.Minute)
	tok := e.issue(e.session("s1", "p1", ""))
	before := pulls
	for range 5 {
		if resp, _ := e.call(tok, invokePath(), `{}`); resp.StatusCode != 200 {
			t.Fatalf("call = %d", resp.StatusCode)
		}
	}
	if _, n := e.counts(); n != 1 || pulls-before != 1 {
		t.Fatalf("five calls on credentials with 4 min left: %d resolutions, %d pulls; want 1 and 1", n, pulls-before)
	}
	e.advance(credShortReuse)
	e.call(tok, invokePath(), `{}`)
	if _, n := e.counts(); n != 2 {
		t.Fatalf("resolutions after credShortReuse = %d, want 2", n)
	}
	// Never used within credShortReuse of their expiry.
	if u := credUsableUntil(e.now, e.now.Add(40*time.Second)); u != e.now.Add(10*time.Second) {
		t.Fatalf("usable until %v for 40 s left", u.Sub(e.now))
	}
	if u := credUsableUntil(e.now, e.now.Add(20*time.Second)); u.After(e.now) {
		t.Fatalf("credentials with 20 s left were cached for %v", u.Sub(e.now))
	}
}
