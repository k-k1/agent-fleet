package routepolicy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/redirectpolicy"
)

// failIn makes every rename into dir fail until the test ends or the returned func is called.
func failIn(t *testing.T, dir string) (restore func()) {
	t.Helper()
	old := rename
	restore = func() { rename = old }
	t.Cleanup(restore)
	rename = func(a, b string) error {
		if filepath.Dir(b) == dir {
			return errors.New("injected: config directory unwritable")
		}
		return old(a, b)
	}
	return restore
}

// The config directory stops taking writes, the CP tightens the policy, the
// Agent restarts. The mirror recorded the tighter floor, so the restart does not hold the last
// copy that landed. Without the mirror (the control) it does.
func TestMirrorKeepsTheStricterRuleWhenTheConfigDirCannotBeWritten(t *testing.T) {
	t.Run("a higher revision", func(t *testing.T) {
		s, _ := testStore(t)
		if _, err := s.Accept(open1); err != nil {
			t.Fatal(err)
		}
		restore := failIn(t, filepath.Dir(s.path))
		if _, err := s.Accept(strict2); err == nil {
			t.Fatal("control: the write into the config directory did not fail")
		}
		if s.Allows("openai-api") {
			t.Fatal("the stricter policy is not enforced in memory")
		}
		restore()
		if r := Open(s.path, ""); !r.Allows("openai-api") {
			t.Fatalf("control: without the mirror the restart should hold the old copy: %+v", r.State())
		}
		r := reopen(s)
		if st := r.State(); r.Allows("openai-api") || r.Allows("anthropic-api") || !st.Placeholder || st.Policy.Revision != 2 {
			t.Fatalf("after the restart: %+v", st)
		}
		if r.Gate() == nil {
			t.Error("an unconfirmed placeholder let new work through")
		}
		// The first live answer replaces it and brings the floor beside the copy up to date.
		if changed, err := r.Accept(strict2); err != nil || !changed {
			t.Fatalf("live answer: %v %v", changed, err)
		}
		if f, fs := readFloor(floorPath(s.path)); fs != floorOK || f.Revision != 2 || !f.Restricting {
			t.Errorf("the floor beside the copy was not repaired: %+v %v", f, fs)
		}
	})
	t.Run("a start marker at the held revision", func(t *testing.T) {
		s, _ := testStore(t)
		if _, err := s.Accept(open1); err != nil {
			t.Fatal(err)
		}
		restore := failIn(t, filepath.Dir(s.path))
		if _, err := s.SeedUnknown("m1"); err == nil {
			t.Fatal("control: the write into the config directory did not fail")
		}
		restore()
		if r := Open(s.path, ""); !r.Allows("openai-api") {
			t.Fatalf("control: without the mirror the restart should hold the old copy: %+v", r.State())
		}
		r := reopen(s)
		if r.Allows("openai-api") || !r.State().Placeholder {
			t.Fatalf("after the restart the marker's restriction was lost: %+v", r.State())
		}
		if changed, _ := r.SeedUnknown("m1"); changed {
			t.Error("the marker the mirror recorded was taken again")
		}
	})
}

// The mirror stands in for the floor only when the config directory lost both files; beside a
// surviving copy the case stays "nothing vouches for the copy". A malformed mirror counts for
// nothing, and a missing one (an Agent from before it) is written by the next confirmation.
func TestMirrorStandsInOnlyForALostDirectory(t *testing.T) {
	t.Run("floor and copy lost", func(t *testing.T) {
		s, _ := testStore(t)
		if _, err := s.Accept(strict2); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(floorPath(s.path))
		_ = os.Remove(s.path)
		if st := Open(s.path, "").State(); st.Held {
			t.Fatalf("control: without the mirror nothing is held: %+v", st)
		}
		if r := reopen(s); r.Allows("anthropic-api") || r.State().Policy.Revision != 2 {
			t.Fatalf("the mirror did not stand in for the lost directory: %+v", r.State())
		}
	})
	t.Run("floor lost beside a copy, stale mirror", func(t *testing.T) {
		// The marker's placeholder landed beside the copy, the mirror missed it and still says
		// unrestricted at the same revision; then the floor beside the copy is lost. Taking the
		// mirror as the floor would read the placeholder's revision as unrestricted.
		s, _ := testStore(t)
		if _, err := s.Accept(open1); err != nil {
			t.Fatal(err)
		}
		restore := failIn(t, filepath.Dir(s.mirror))
		if _, err := s.SeedUnknown("m1"); err == nil {
			t.Fatal("control: the mirror write did not fail")
		}
		restore()
		_ = os.Remove(floorPath(s.path))
		if r := reopen(s); r.Allows("openai-api") || !r.State().Placeholder {
			t.Fatalf("a stale mirror vouched for the copy: %+v", r.State())
		}
	})
	t.Run("malformed mirror", func(t *testing.T) {
		s, _ := testStore(t)
		if _, err := s.Accept(strict2); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.mirror, []byte(`{"revision":9}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if r := reopen(s); r.State().Placeholder || !r.Allows("anthropic-api") || r.Allows("openai-api") {
			t.Fatalf("a malformed mirror changed the held policy: %+v", r.State())
		}
	})
	t.Run("missing mirror", func(t *testing.T) {
		s, _ := testStore(t)
		if _, err := s.Accept(strict2); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(s.mirror)
		r := reopen(s)
		if r.State().Placeholder || !r.Allows("anthropic-api") {
			t.Fatalf("an Agent from before the mirror lost its copy: %+v", r.State())
		}
		// At the same instant: a confirmation that brings nothing new still owes the mirror.
		if _, err := r.Accept(strict2); err != nil {
			t.Fatal(err)
		}
		if f, fs := readFloor(s.mirror); fs != floorOK || f.Revision != 2 || !f.Restricting {
			t.Errorf("the next confirmation did not write the mirror: %+v %v", f, fs)
		}
	})
}

// A start at which neither directory can be written holds new work until the first pull
// settles: the CP's answer decides, and when it cannot be reached, what is on disk does (the
// behaviour of any outage). One writable directory is enough not to hold.
func TestStartWithoutAWritableFloorWaitsForTheFirstPull(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "down", status)
			return
		}
		fmt.Fprint(w, `{"revision":2,"restricted":true,"ceilingRestricted":false,"allowed":["anthropic-api"],"issuedAt":1}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	tokenOnce, token = sync.Once{}, "afc_tok"
	tokenOnce.Do(func() {})
	t.Cleanup(func() { tokenOnce, token = sync.Once{}, "" })

	oldF := syncFile
	t.Cleanup(func() { syncFile = oldF })
	unwritable := func(dirs ...string) {
		syncFile = func(f *os.File) error {
			for _, d := range dirs {
				if filepath.Dir(f.Name()) == d {
					return errors.New("injected: no space left on device")
				}
			}
			return oldF(f)
		}
	}
	prepared := func(t *testing.T) *Store {
		s, _ := testStore(t)
		if _, err := s.Accept(open1); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("the CP answers", func(t *testing.T) {
		s := prepared(t)
		unwritable(filepath.Dir(s.path), filepath.Dir(s.mirror))
		t.Cleanup(func() { syncFile = oldF })
		r := reopen(s)
		if !r.State().Holding || !errors.Is(r.Gate(), ErrUnrecorded) || r.State().Confirmed(r.now()) {
			t.Fatalf("a start with no writable floor did not hold: %+v %v", r.State(), r.Gate())
		}
		status = http.StatusOK
		_ = r.Sync(context.Background(), "test") // the write fails; the answer is taken in memory
		if r.State().Holding || r.Gate() != nil || r.Allows("openai-api") {
			t.Fatalf("after the first answer: %+v %v", r.State(), r.Gate())
		}
	})
	t.Run("the CP cannot be reached", func(t *testing.T) {
		s := prepared(t)
		unwritable(filepath.Dir(s.path), filepath.Dir(s.mirror))
		t.Cleanup(func() { syncFile = oldF })
		r := reopen(s)
		status = http.StatusBadGateway
		if err := r.Sync(context.Background(), "test"); err == nil {
			t.Fatal("control: the pull did not fail")
		}
		if r.State().Holding || r.Gate() != nil || !r.Allows("openai-api") {
			t.Fatalf("a failed pull should leave the decision to the copy on disk: %+v %v", r.State(), r.Gate())
		}
	})
	t.Run("the mirror is writable", func(t *testing.T) {
		s := prepared(t)
		unwritable(filepath.Dir(s.path))
		t.Cleanup(func() { syncFile = oldF })
		if r := reopen(s); r.State().Holding || r.Gate() != nil {
			t.Fatalf("one writable directory should be enough: %+v %v", r.State(), r.Gate())
		}
	})
	t.Run("the first pull is not delayed", func(t *testing.T) {
		s := prepared(t)
		unwritable(filepath.Dir(s.path), filepath.Dir(s.mirror))
		t.Cleanup(func() { syncFile = oldF })
		r := reopen(s)
		ctx, cancel := context.WithCancel(context.Background())
		waits := make(chan time.Duration, 1)
		done := make(chan struct{})
		go func() {
			r.run(ctx, func() float64 { return 0.999 }, func(d time.Duration) <-chan time.Time { waits <- d; return make(chan time.Time) },
				time.Now)
			close(done)
		}()
		if d := <-waits; d != 0 {
			t.Errorf("the first pull of a holding start waits %v", d)
		}
		cancel()
		<-done
		// The control: a start that is not holding keeps the jittered first wait.
		syncFile = oldF
		r2 := reopen(s)
		ctx2, cancel2 := context.WithCancel(context.Background())
		done2 := make(chan struct{})
		go func() {
			r2.run(ctx2, func() float64 { return 0.999 }, func(d time.Duration) <-chan time.Time { waits <- d; return make(chan time.Time) },
				time.Now)
			close(done2)
		}()
		if d := <-waits; d == 0 {
			t.Error("control: the first wait of a normal start is not jittered")
		}
		cancel2()
		<-done2
	})
}

// Within one revision the state moves both ways: a start marker restricts, a live answer clears
// it. When only the mirror recorded the clearing, the restart holds the cleared state and keeps
// the marker's receipt, so the marker does not block an unrestricted tenant again. Whether the
// marker's placeholder landed beside the copy or only in the mirror makes no difference.
func TestMirrorKeepsAClearedMarkerAtTheSameRevision(t *testing.T) {
	for name, markerLanded := range map[string]bool{"marker beside the copy": true, "marker only in the mirror": false} {
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			if _, err := s.Accept(open1); err != nil {
				t.Fatal(err)
			}
			var restore func()
			if !markerLanded {
				restore = failIn(t, filepath.Dir(s.path))
			}
			if _, err := s.SeedUnknown("m1"); markerLanded && err != nil {
				t.Fatal(err)
			}
			if markerLanded {
				restore = failIn(t, filepath.Dir(s.path))
			}
			if _, err := s.Accept(open1); err == nil {
				t.Fatal("control: the write into the config directory did not fail")
			}
			if s.Gate() != nil || !s.Allows("openai-api") {
				t.Fatal("control: the live answer did not clear the marker in memory")
			}
			restore()
			r := reopen(s)
			if changed, err := r.SeedUnknown("m1"); err != nil || changed {
				t.Fatalf("the marker was taken again: %v %v", changed, err)
			}
			if r.Gate() != nil || !r.Allows("openai-api") || r.State().Placeholder {
				t.Fatalf("the cleared state the mirror recorded was lost: %+v", r.State())
			}
		})
	}
}

// Floors from before the sequence number tell no order at one revision: the restricting one is
// taken, the safe direction.
func TestLegacyFloorsAtATieTakeTheRestrictingOne(t *testing.T) {
	a := floor{Revision: 3, Restricting: true}
	b := floor{Revision: 3}
	if !a.newer(b) || b.newer(a) {
		t.Error("between unnumbered floors at one revision the restricting one must win")
	}
	if c := (floor{Revision: 3, Seq: 2}); !c.newer(a) || a.newer(c) {
		t.Error("a numbered floor written later must win at one revision")
	}
	if d := (floor{Revision: 4}); !d.newer(floor{Revision: 3, Restricting: true, Seq: 9}) {
		t.Error("a higher revision must win whatever the sequence")
	}
}

// The hold of a start with no writable floor ends only on the CP's current answer or on a pull
// that shows the CP cannot answer (no connection, a 5xx). A replayed answer, a malformed 200 and
// a refusal keep it: the CP answered, and nothing it said confirms the held policy. A current
// answer after them still releases it.
func TestHoldEndsOnlyOnACurrentAnswerOrAnOutage(t *testing.T) {
	var status int
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	tokenOnce, token = sync.Once{}, "afc_tok"
	tokenOnce.Do(func() {})
	t.Cleanup(func() { tokenOnce, token = sync.Once{}, "" })
	oldF := syncFile
	t.Cleanup(func() { syncFile = oldF })
	holding := func(t *testing.T) *Store {
		syncFile = oldF
		s, _ := testStore(t)
		if _, err := s.Accept(Policy{Revision: 1, Allowed: []string{}}); err != nil {
			t.Fatal(err)
		}
		syncFile = func(*os.File) error { return errors.New("injected: both disks unwritable") }
		r := reopen(s)
		if !errors.Is(r.Gate(), ErrUnrecorded) {
			t.Fatal("control: the start does not hold")
		}
		return r
	}
	current := `{"revision":1,"restricted":false,"ceilingRestricted":false,"allowed":[]}`
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"replayed revision": {200, `{"revision":0,"restricted":false,"ceilingRestricted":false,"allowed":[]}`},
		"malformed 200":     {200, `{`},
		"partial 200":       {200, `{"revision":1,"allowed":[]}`},
		"unauthorised":      {401, `{}`},
		"forbidden":         {403, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			r := holding(t)
			status, body = tc.status, tc.body
			_ = r.Sync(context.Background(), "test")
			if !errors.Is(r.Gate(), ErrUnrecorded) {
				t.Fatalf("released by an answer that confirms nothing: %+v", r.State())
			}
			status, body = 200, current
			_ = r.Sync(context.Background(), "test")
			if r.State().Holding || r.Gate() != nil {
				t.Fatalf("the current answer did not release it: %+v", r.State())
			}
		})
	}
	for name, st := range map[string]int{"500": 500, "503": 503} {
		t.Run(name, func(t *testing.T) {
			r := holding(t)
			status, body = st, "down"
			_ = r.Sync(context.Background(), "test")
			if r.State().Holding {
				t.Fatalf("a %d did not release it", st)
			}
		})
	}
	t.Run("no connection", func(t *testing.T) {
		r := holding(t)
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		t.Setenv("AF_CP_BASE_URL", dead.URL)
		if err := r.Sync(context.Background(), "test"); err == nil {
			t.Fatal("control: the pull reached a closed server")
		}
		if r.State().Holding {
			t.Fatal("a pull with no connection did not release it")
		}
	})
	t.Run("no bridge", func(t *testing.T) {
		r := holding(t)
		t.Setenv("AF_CP_BASE_URL", "")
		if err := r.Sync(context.Background(), "test"); !errors.Is(err, ErrBridgeOff) {
			t.Fatalf("control: %v", err)
		}
		if r.State().Holding {
			t.Fatal("a deployment that distributes no policy kept the hold")
		}
	})
}

// A restricting floor stands either for a policy with an allowed list or for a placeholder that
// allows nothing. When a start marker's placeholder landed in a floor but not in the copy (the
// config directory failed and the mirror took it, or only the copy failed), the restart must not
// take the older restricted copy for it, nor hand that copy the marker's receipt.
func TestPlaceholderFloorDoesNotBlessARestrictedCopy(t *testing.T) {
	restricted := Policy{Revision: 5, Restricted: true, Allowed: []string{"anthropic-api"}}
	for name, fails := range map[string]func(s *Store, b string) bool{
		"config directory unwritable": func(s *Store, b string) bool { return filepath.Dir(b) == filepath.Dir(s.path) },
		"only the copy fails":         func(s *Store, b string) bool { return b == s.path },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			if _, err := s.Accept(restricted); err != nil {
				t.Fatal(err)
			}
			old := rename
			t.Cleanup(func() { rename = old })
			rename = func(a, b string) error {
				if fails(s, b) {
					return errors.New("injected")
				}
				return old(a, b)
			}
			if _, err := s.SeedUnknown("new-ceiling"); err == nil {
				t.Fatal("control: no write failed")
			}
			rename = old
			r := reopen(s)
			if changed, err := r.SeedUnknown("new-ceiling"); err != nil || changed {
				t.Fatalf("the marker was taken again: %v %v", changed, err)
			}
			if st := r.State(); r.Allows("anthropic-api") || r.Gate() == nil || !st.Placeholder {
				t.Fatalf("the restricted copy stood in for the marker's placeholder: %+v", st)
			}
		})
	}
}

// A restricting floor from before Placeholder was recorded cannot say which it stood for, so it
// vouches for no restricting copy at its revision; an unrestricted one still vouches for its copy.
func TestLegacyRestrictingFloorVouchesForNoRestrictingCopy(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Accept(strict2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(floorPath(s.path), []byte(`{"revision":2,"restricting":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(s.mirror)
	if r := reopen(s); !r.State().Placeholder || r.Allows("anthropic-api") {
		t.Fatalf("a legacy restricting floor blessed a restricted copy: %+v", r.State())
	}
	s2, _ := testStore(t)
	if _, err := s2.Accept(open1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(floorPath(s2.path), []byte(`{"revision":1,"restricting":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(s2.mirror)
	if r := reopen(s2); r.State().Placeholder || !r.Allows("openai-api") {
		t.Fatalf("a legacy unrestricted floor stopped vouching for its copy: %+v", r.State())
	}
	// A floor that claims a placeholder without restricting is malformed.
	if err := os.WriteFile(floorPath(s2.path), []byte(`{"revision":1,"restricting":false,"placeholder":true,"seq":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := reopen(s2); !r.State().Placeholder {
		t.Fatalf("a malformed floor was trusted: %+v", r.State())
	}
}

// A placeholder only a live answer may replace keeps that rule through a lost copy: the floor
// records it, so a start environment still cannot replace it after the restart.
func TestNeedsLiveSurvivesALostCopy(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Accept(strict2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(floorPath(s.path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(s.mirror)
	r := reopen(s) // nothing vouches for the copy: a placeholder only a live answer replaces
	if _, err := r.SeedUnknown("m1"); err != nil {
		t.Fatal(err) // records the placeholder, NeedsLive included
	}
	_ = os.Remove(s.path)
	r2 := reopen(s)
	if changed, _ := r2.Seed(Policy{Revision: 9, Allowed: []string{}}, 1); changed || !r2.State().Placeholder {
		t.Fatalf("a start environment replaced a placeholder that needs a live answer: %+v", r2.State())
	}
	if changed, _ := r2.Accept(Policy{Revision: 9, Allowed: []string{}}); !changed {
		t.Fatal("control: a live answer must replace it")
	}
}

// A redirect the client refuses is the CP answering, not an outage: it keeps the hold. A
// connection the CP drops without an answer is an outage and releases it.
func TestHoldBoundaryOfClientDecisions(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the refused redirect's target was reached")
	}))
	t.Cleanup(target.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+bridgePath, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	dropping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(dropping.Close)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	tokenOnce, token = sync.Once{}, "afc_tok"
	tokenOnce.Do(func() {})
	t.Cleanup(func() { tokenOnce, token = sync.Once{}, "" })
	oldF := syncFile
	t.Cleanup(func() { syncFile = oldF })
	holding := func(t *testing.T) *Store {
		syncFile = oldF
		s, _ := testStore(t)
		if _, err := s.Accept(open1); err != nil {
			t.Fatal(err)
		}
		syncFile = func(*os.File) error { return errors.New("injected: both disks unwritable") }
		r := reopen(s)
		if !errors.Is(r.Gate(), ErrUnrecorded) {
			t.Fatal("control: the start does not hold")
		}
		return r
	}
	t.Run("refused redirect", func(t *testing.T) {
		r := holding(t)
		t.Setenv("AF_CP_BASE_URL", redirecting.URL)
		if err := r.Sync(context.Background(), "test"); !redirectpolicy.IsRefused(err) {
			t.Fatalf("control: %v", err)
		}
		if !errors.Is(r.Gate(), ErrUnrecorded) {
			t.Fatalf("a refused redirect released the hold: %+v", r.State())
		}
	})
	t.Run("dropped connection", func(t *testing.T) {
		r := holding(t)
		t.Setenv("AF_CP_BASE_URL", dropping.URL)
		if err := r.Sync(context.Background(), "test"); err == nil {
			t.Fatal("control: the pull got an answer")
		}
		if r.State().Holding {
			t.Fatal("a dropped connection did not release the hold")
		}
	})
}
