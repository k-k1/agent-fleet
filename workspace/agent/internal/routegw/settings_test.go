package routegw

import (
	"context"
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
		if !first["p"].ModelRoute {
			t.Fatal("the first caller did not get its own pull")
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

// A Pull that panics does not leave later callers blocked on its flight.
func TestSettingsSourceSurvivesAPanickingPull(t *testing.T) {
	boom := true
	s := &SettingsSource{MinInterval: time.Minute, MaxAge: time.Hour,
		Pull: func() (map[string]awsx.Profile, bool) {
			if boom {
				panic("pull")
			}
			return map[string]awsx.Profile{"p": {ID: "1"}}, true
		}}
	func() {
		defer func() { _ = recover() }()
		s.Fresh()
	}()
	boom = false
	done := make(chan bool)
	go func() { _, ok := s.Fresh(); done <- ok }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("the pull after a panic served nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fresh blocked after a panicking pull")
	}
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
		src := &SettingsSource{Now: e.g.d.Now, MinInterval: 30 * time.Second, MaxAge: 15 * time.Minute,
			Pull: func() (map[string]awsx.Profile, bool) {
				e.mu.Lock()
				defer e.mu.Unlock()
				return maps.Clone(e.settings), true
			}}
		e.g.d.Settings, e.g.d.FreshSettings = src.Get, src.Fresh
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
