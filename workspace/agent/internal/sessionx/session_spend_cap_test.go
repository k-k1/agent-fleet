package sessionx

// Tests for the per-session spend budget (#1054): crossing arms the stop, the hard limit halts
// at once, a prompt after the crossing re-arms, raising the cap lifts it, and the stop it causes
// is announced as a budget stop.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

var fakeSpends = struct {
	sync.Mutex
	m map[string]session.Spend
}{m: map[string]session.Spend{}}

// fakeSessionSpend is the SessionSpend dependency in this package's tests (deps_test.go).
func fakeSessionSpend(m session.Meta) session.Spend {
	fakeSpends.Lock()
	defer fakeSpends.Unlock()
	return fakeSpends.m[m.Name]
}

func setFakeSpend(t *testing.T, name string, usd float64) {
	t.Helper()
	fakeSpends.Lock()
	fakeSpends.m[name] = session.Spend{USD: usd, Priced: true}
	fakeSpends.Unlock()
	forgetSpend(name)
	t.Cleanup(func() {
		fakeSpends.Lock()
		delete(fakeSpends.m, name)
		fakeSpends.Unlock()
		forgetSpend(name)
	})
}

// outboxKinds lists the notification kinds written for a session.
func outboxKinds(t *testing.T, name string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(paths.AgentStateDir(), "notification-outbox", "*.json"))
	var kinds []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var ev struct {
			Kind        string         `json:"kind"`
			SessionName string         `json:"sessionName"`
			Payload     map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.SessionName == name {
			k := ev.Kind
			if mt, _ := ev.Payload["midTurn"].(bool); mt {
				k += "+midTurn"
			}
			kinds = append(kinds, k)
		}
	}
	return kinds
}

func cappedMeta(t *testing.T, name string, capUSD float64) session.Meta {
	t.Helper()
	// Managed, so a halt has a live runtime to drop and stamps StoppedAt (a Terminal session
	// with no tmux pane takes the "already stopped" path and stamps nothing).
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverManaged,
		CreatedAt: time.Now().Format(time.RFC3339), SpendCapUSD: capUSD}
	session.WriteMeta(m)
	return m
}

func TestSpendCapCrossingArmsTheStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := cappedMeta(t, "cap1", 5)
	now := time.Now()

	evaluateSpendCap(m, session.Spend{USD: 4.99, Priced: true}, now)
	if got, _ := session.ReadMeta(m.Name); got.SpendCapHitAt != "" || got.StopAfterTurnAt != "" {
		t.Fatalf("under the cap nothing may change: hit=%q arm=%q", got.SpendCapHitAt, got.StopAfterTurnAt)
	}

	evaluateSpendCap(m, session.Spend{USD: 5, Priced: true}, now)
	got, _ := session.ReadMeta(m.Name)
	if got.SpendCapHitAt == "" {
		t.Fatal("reaching the cap must record the crossing")
	}
	if _, live := session.StopArmedAt(got, now); !live {
		t.Fatalf("reaching the cap must arm stop-after-turn, got %q", got.StopAfterTurnAt)
	}
	if got.StoppedAt != "" {
		t.Fatal("under the hard limit the turn must be allowed to finish")
	}
	if k := outboxKinds(t, m.Name); len(k) != 0 {
		t.Fatalf("crossing only arms; the notification comes with the stop, got %v", k)
	}

	// A user who releases the arm by hand after the crossing is not overridden by the sweep;
	// what keeps the budget is the re-arm on the next prompt.
	postStopAfterTurn(t, m.Name, `{"on":false}`)
	evaluateSpendCap(got, session.Spend{USD: 6, Priced: true}, now)
	if again, _ := session.ReadMeta(m.Name); again.StopAfterTurnAt != "" {
		t.Fatalf("an already-recorded crossing must not re-arm on every tick: %q", again.StopAfterTurnAt)
	}
}

func TestSpendCapHardLimitHaltsMidTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := cappedMeta(t, "cap2", 5)

	evaluateSpendCap(m, session.Spend{USD: 5 * session.SpendCapHardFactor, Priced: true}, time.Now())
	got, _ := session.ReadMeta(m.Name)
	if got.StoppedAt == "" {
		t.Fatal("at the hard limit the session must be halted without waiting for the turn")
	}
	if got.SpendCapHitAt == "" {
		t.Fatal("a hard stop is still a crossing: the row has to say why it stopped")
	}
	if k := outboxKinds(t, m.Name); len(k) != 1 || k[0] != "spend-budget+midTurn" {
		t.Fatalf("want one mid-turn spend-budget notification, got %v", k)
	}
}

func TestSpendCapSweepSkipsUncappedAndStopped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	free := cappedMeta(t, "cap3free", 0)
	stopped := cappedMeta(t, "cap3stopped", 1)
	stopped.StoppedAt = time.Now().Format(time.RFC3339)
	session.WriteMeta(stopped)
	live := cappedMeta(t, "cap3live", 1)
	for _, n := range []string{free.Name, stopped.Name, live.Name} {
		setFakeSpend(t, n, 1.5)
	}

	SweepSpendCaps(time.Now())
	if m, _ := session.ReadMeta(free.Name); m.SpendCapHitAt != "" {
		t.Fatal("a session with no budget must never be stopped by one")
	}
	if m, _ := session.ReadMeta(stopped.Name); m.SpendCapHitAt != "" || m.StopAfterTurnAt != "" {
		t.Fatal("a stopped session must not be armed")
	}
	if m, _ := session.ReadMeta(live.Name); m.SpendCapHitAt == "" {
		t.Fatal("the sweep must arm the live capped session over its budget")
	}
}

func TestNewPromptReArmsAnOverBudgetSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := cappedMeta(t, "cap4", 5)
	m.SpendCapHitAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	session.WriteMeta(m) // crossed earlier, its stop consumed, resumed without raising the cap

	cancelStopArmOnNewPrompt(m.Name)
	got, _ := session.ReadMeta(m.Name)
	if _, live := session.StopArmedAt(got, time.Now()); !live {
		t.Fatalf("a prompt to an over-budget session must re-arm the stop, got %q", got.StopAfterTurnAt)
	}

	// Raised above the spend: the next prompt is ordinary work again.
	setFakeSpend(t, m.Name, 6)
	w := postSpendCap(t, m.Name, `{"usd":10}`)
	if w.Code != http.StatusOK {
		t.Fatalf("raise status=%d body=%s", w.Code, w.Body.String())
	}
	got, _ = session.ReadMeta(m.Name)
	if got.SpendCapHitAt != "" || got.StopAfterTurnAt != "" || got.SpendCapUSD != 10 {
		t.Fatalf("raising the cap must lift the crossing and its arm: %+v", got)
	}
	postStopAfterTurn(t, m.Name, `{"on":true}`)
	cancelStopArmOnNewPrompt(m.Name)
	if got, _ = session.ReadMeta(m.Name); got.StopAfterTurnAt != "" {
		t.Fatal("once lifted, a new prompt releases a user's arm as before")
	}
}

func postSpendCap(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/spend-cap", strings.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleSessionSpendCap(w, r)
	return w
}

func TestSpendCapRaiseThatStaysUnderSpendKeepsTheStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := cappedMeta(t, "cap5", 5)
	evaluateSpendCap(m, session.Spend{USD: 7, Priced: true}, time.Now())
	setFakeSpend(t, m.Name, 7)

	postSpendCap(t, m.Name, `{"usd":6}`)
	got, _ := session.ReadMeta(m.Name)
	if got.SpendCapHitAt == "" || got.StopAfterTurnAt == "" {
		t.Fatalf("a cap still under the spend must not cancel the pending stop: %+v", got)
	}
	postSpendCap(t, m.Name, `{"usd":0}`)
	if got, _ = session.ReadMeta(m.Name); got.SpendCapHitAt != "" || got.StopAfterTurnAt != "" {
		t.Fatalf("removing the budget lifts the crossing: %+v", got)
	}

	for _, bad := range []string{`{"usd":-1}`, `{"usd":1e9}`, `{}`, `{"usd":"5"}`} {
		if w := postSpendCap(t, m.Name, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, w.Code)
		}
	}
	if w := postSpendCap(t, "nosuch", `{"usd":1}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", w.Code)
	}
}

// One stop, one notification: the budget's stop says it was the budget, not "stopped after
// turn" — the user did not ask for that stop, and the action it needs is "raise and resume".
func TestBudgetStopIsAnnouncedAsSpendBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	over := cappedMeta(t, "cap6", 5)
	evaluateSpendCap(over, session.Spend{USD: 6, Priced: true}, time.Now())
	if err := StopArmedSession(over.Name); err != nil {
		t.Fatal(err)
	}
	if k := outboxKinds(t, over.Name); len(k) != 1 || k[0] != "spend-budget" {
		t.Fatalf("budget stop notifications = %v, want [spend-budget]", k)
	}

	plain := cappedMeta(t, "cap6plain", 5)
	postStopAfterTurn(t, plain.Name, `{"on":true}`)
	if err := StopArmedSession(plain.Name); err != nil {
		t.Fatal(err)
	}
	if k := outboxKinds(t, plain.Name); len(k) != 1 || k[0] != "stop-after-turn" {
		t.Fatalf("a user's own arm keeps its notification, got %v", k)
	}
}

func TestSpendDescendantsFollowsCreateSessionOnly(t *testing.T) {
	metas := []session.Meta{
		{Name: "p"},
		{Name: "c1", Origin: session.OriginSession, OriginSession: "p"},
		{Name: "c2", Origin: session.OriginSession, OriginSession: "p"},
		{Name: "g1", Origin: session.OriginSession, OriginSession: "c1"},
		// A fork of a child carries the lineage but is not something p started.
		{Name: "fork", Origin: session.OriginHandoff, OriginSession: "p"},
		{Name: "gone", Origin: session.OriginSession, OriginSession: "p", Archived: true},
		{Name: "other", Origin: session.OriginSession, OriginSession: "q"},
	}
	var got []string
	for _, m := range spendDescendants("p", metas) {
		got = append(got, m.Name)
	}
	if strings.Join(got, ",") != "c1,c2,g1" {
		t.Fatalf("descendants = %v, want [c1 c2 g1]", got)
	}
}

func TestSessionSpendViewShowsChildrenWithoutChargingThem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := cappedMeta(t, "sp-parent", 5)
	c := cappedMeta(t, "sp-child", 2)
	c.Origin, c.OriginSession = session.OriginSession, p.Name
	session.WriteMeta(c)
	setFakeSpend(t, p.Name, 1.25)
	setFakeSpend(t, c.Name, 3)

	r := httptest.NewRequest(http.MethodGet, "/sessions/"+p.Name+"/spend", nil)
	r.SetPathValue("name", p.Name)
	w := httptest.NewRecorder()
	HandleSessionSpend(w, r)
	var out struct {
		SpendUSD    float64 `json:"spendUsd"`
		CapUSD      float64 `json:"spendCapUsd"`
		HardFactor  float64 `json:"hardFactor"`
		ChildrenUSD float64 `json:"childrenUsd"`
		Children    []spendChild
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body.String(), err)
	}
	if out.SpendUSD != 1.25 || out.CapUSD != 5 || out.ChildrenUSD != 3 || len(out.Children) != 1 ||
		out.HardFactor != session.SpendCapHardFactor {
		t.Fatalf("spend view = %+v", out)
	}
	SweepSpendCaps(time.Now())
	if m, _ := session.ReadMeta(p.Name); m.SpendCapHitAt != "" {
		t.Fatal("a child's spend must not stop its parent")
	}
}

// The cap rides the create (explicit, or the user's default when the body names none), and a
// fork / recreate keeps the value but never the crossing.
func TestCreateSessionTakesSpendCap(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	testguard.IsolateTmux(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	prev := session.SpendCapDefaultPref
	session.SpendCapDefaultPref = func() float64 { return 3 }
	t.Cleanup(func() { session.SpendCapDefaultPref = prev })

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, tc := range []struct {
		name string
		body map[string]any
		want float64
	}{
		{"default", map[string]any{}, 3},
		{"explicit", map[string]any{"spend_cap_usd": 7.5}, 7.5},
		{"explicit-none", map[string]any{"spend_cap_usd": 0}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"dir": home, "kind": "shell"}
			for k, v := range tc.body {
				body[k] = v
			}
			var created session.Session
			do(t, srv, "POST", "/sessions", body, http.StatusCreated, &created)
			defer tmuxx.Cmd("kill-session", "-t", session.TmuxName(created.Name)).Run()
			m, _ := session.ReadMeta(created.Name)
			if m.SpendCapUSD != tc.want || created.SpendCapUSD != tc.want {
				t.Fatalf("meta cap=%v wire cap=%v, want %v", m.SpendCapUSD, created.SpendCapUSD, tc.want)
			}
		})
	}
	do(t, srv, "POST", "/sessions", map[string]any{"dir": home, "kind": "shell", "spend_cap_usd": -2},
		http.StatusBadRequest, nil)

	src := session.Meta{Name: "src", SpendCapUSD: 4, SpendCapHitAt: time.Now().Format(time.RFC3339)}
	f := forkMeta(src, "dst", "", "", "")
	if f.SpendCapUSD != 4 || f.SpendCapHitAt != "" {
		t.Fatalf("fork cap=%v hit=%q, want 4 and no crossing", f.SpendCapUSD, f.SpendCapHitAt)
	}
	// The fork's spend starts at the sub-second instant it was made, not CreatedAt's whole second.
	from, err := time.Parse(time.RFC3339Nano, f.SpendFrom)
	created, _ := time.Parse(time.RFC3339, f.CreatedAt)
	if err != nil || from.Before(created) || from.Sub(created) >= time.Second {
		t.Fatalf("fork SpendFrom=%q CreatedAt=%q: want the same second, kept to the sub-second", f.SpendFrom, f.CreatedAt)
	}
	if start, _ := session.SpendStart(f); !start.Equal(from) {
		t.Fatalf("SpendStart = %v, want SpendFrom %v", start, from)
	}
}

// The crossing is seen on a tick, often after the short turn that crossed has already ended.
// The arm has to sit at or before that turn's end, or its end-of-turn evidence is discarded and
// the stop waits for some later turn (review of #1652).
func TestSpendCapArmCoversATurnThatAlreadyEnded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := cappedMeta(t, "cap7", 5)
	under := time.Now().Add(-40 * time.Second).Truncate(time.Second)
	crossed := under.Add(20 * time.Second) // the crossing turn ended 20 s before the tick
	sp := session.Spend{USD: 5.2, Priced: true, Marks: []session.SpendMark{
		{End: under.Add(-time.Minute), USD: 3}, {End: under, USD: 4.9}, {End: crossed, USD: 5.2},
	}}
	evaluateSpendCap(m, sp, time.Now())
	got, _ := session.ReadMeta(m.Name)
	at, live := session.StopArmedAt(got, time.Now())
	if !live || at.After(crossed) {
		t.Fatalf("arm at %v must be at or before the crossing turn's end %v (live=%v)", at, crossed, live)
	}
	if !at.Equal(under) {
		t.Fatalf("arm at %v, want the end of the last turn under the cap %v", at, under)
	}
	if !session.SpendCapOwnsArm(got) {
		t.Fatal("the arm the budget wrote must be marked as the budget's")
	}
}

// A stop the user (or a schedule) asked for is not the budget's to drop.
func TestSpendCapLeavesOtherArmsAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	under := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	sp := session.Spend{USD: 6, Priced: true, Marks: []session.SpendMark{{End: under, USD: 4}, {End: under.Add(10 * time.Second), USD: 6}}}

	t.Run("an earlier user arm is kept and survives the raise", func(t *testing.T) {
		m := cappedMeta(t, "cap8a", 5)
		m.StopAfterTurnAt = under.Add(-time.Minute).Format(time.RFC3339)
		session.WriteMeta(m)
		evaluateSpendCap(m, sp, time.Now())
		got, _ := session.ReadMeta(m.Name)
		if got.StopAfterTurnAt != m.StopAfterTurnAt || session.SpendCapOwnsArm(got) {
			t.Fatalf("an arm already covering the crossing turn must stay the user's: %+v", got)
		}
		setFakeSpend(t, m.Name, 6)
		postSpendCap(t, m.Name, `{"usd":10}`)
		if got, _ = session.ReadMeta(m.Name); got.StopAfterTurnAt != m.StopAfterTurnAt {
			t.Fatalf("raising the cap dropped the user's stop: %q", got.StopAfterTurnAt)
		}
	})

	t.Run("a later user arm is displaced and put back", func(t *testing.T) {
		m := cappedMeta(t, "cap8b", 5)
		user := time.Now().Add(-2 * time.Second).Format(time.RFC3339)
		m.StopAfterTurnAt = user
		session.WriteMeta(m)
		evaluateSpendCap(m, sp, time.Now())
		got, _ := session.ReadMeta(m.Name)
		if got.StopAfterTurnAt != under.Format(time.RFC3339) || got.SpendCapArmPrev != user {
			t.Fatalf("want the budget's earlier arm with the user's kept aside: %+v", got)
		}
		setFakeSpend(t, m.Name, 6)
		postSpendCap(t, m.Name, `{"usd":10}`)
		if got, _ = session.ReadMeta(m.Name); got.StopAfterTurnAt != user {
			t.Fatalf("raising the cap must put the user's stop back, got %q", got.StopAfterTurnAt)
		}
	})

	t.Run("a user re-arm after the crossing survives the raise", func(t *testing.T) {
		m := cappedMeta(t, "cap8c", 5)
		evaluateSpendCap(m, sp, time.Now())
		postStopAfterTurn(t, m.Name, `{"on":true}`)
		got, _ := session.ReadMeta(m.Name)
		armed := got.StopAfterTurnAt
		if session.SpendCapOwnsArm(got) {
			t.Fatal("an arm the user set is the user's")
		}
		setFakeSpend(t, m.Name, 6)
		postSpendCap(t, m.Name, `{"usd":10}`)
		if got, _ = session.ReadMeta(m.Name); got.StopAfterTurnAt != armed || got.SpendCapHitAt != "" {
			t.Fatalf("the raise must lift the crossing and keep the user's arm: %+v", got)
		}
	})
}
