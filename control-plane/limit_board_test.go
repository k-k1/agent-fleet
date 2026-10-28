package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

var limitNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

const agentEnvelopeBase = `{"sessions":[{"name":"s","kind":"claude","alive":true,"state":"working"},
 {"name":"t","kind":"codex","alive":true,"state":"limited","rateLimitResumeAt":"2026-10-12T14:00:00+09:00"},
 {"name":"u","kind":"codex","alive":true,"state":"spend_limit","rateLimitResumeAt":"2026-10-12T15:00:00Z"}],
 "repoJobs":1,"imageJobs":2`

func validSnapshot() string { return snapshotAt(limitNow) }

// snapshotAt is validSnapshot with its times moved so that now is limitNow's place in it.
func snapshotAt(now time.Time) string {
	d := now.Sub(limitNow)
	return regexp.MustCompile(`2026-10-1\dT\d\d:\d\d:\d\dZ`).ReplaceAllStringFunc(rawSnapshot, func(ts string) string {
		t, _ := time.Parse(time.RFC3339, ts)
		return t.Add(d).Format(time.RFC3339)
	})
}

const rawSnapshot = `{"v":1,"kinds":{
	 "claude":{"epoch":"e1","epochSince":"2026-10-12T08:00:00Z","units":[{"unit":"5h","usedPct":42.5,"resetsAt":"2026-10-12T14:00:00Z","windowMins":300,"observedAt":"2026-10-12T11:50:00Z"},
	                                  {"unit":"7d","usedPct":12,"resetsAt":"2026-10-15T00:00:00Z","windowMins":10080,"observedAt":"2026-10-12T11:50:00Z"}]},
	 "codex":{"epoch":"e2","epochSince":"2026-10-12T08:00:00Z","units":[{"unit":"5h","usedPct":97,"resetsAt":"2026-10-12T13:00:00Z","observedAt":"2026-10-12T11:00:00Z"}]}}}`

// The snapshot is isolated (ADR 0111 decision 6): whatever it holds, sessions, repo jobs and
// image jobs decode the same, and the busy decision taken from them is the same.
func TestLimitSnapshotNeverAffectsTheEnvelope(t *testing.T) {
	decode := func(body string) agentSessionsEnvelope {
		t.Helper()
		var env agentSessionsEnvelope
		if err := json.NewDecoder(strings.NewReader(body)).Decode(&env); err != nil {
			t.Fatalf("decode %q: %v", body, err)
		}
		return env
	}
	plain := decode(agentEnvelopeBase + `}`)
	cases := map[string]string{
		"old agent":       ``,
		"valid":           `,"limits":` + validSnapshot(),
		"string":          `,"limits":"garbage"`,
		"array":           `,"limits":[1,2]`,
		"null":            `,"limits":null`,
		"unknown version": `,"limits":{"v":2,"kinds":{"claude":{"epoch":"e","units":[]}}}`,
		"kind not object": `,"limits":{"v":1,"kinds":{"claude":"x","codex":7}}`,
		"wrong types":     `,"limits":{"v":"1","kinds":[]}`,
		"unit garbage":    `,"limits":{"v":1,"kinds":{"claude":{"epoch":"e","units":[{"unit":5,"usedPct":"x"}]}}}`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			env := decode(agentEnvelopeBase + extra + `}`)
			if !reflect.DeepEqual(env.Sessions, plain.Sessions) || env.RepoJobs != 1 || env.ImageJobs != 2 {
				t.Fatalf("envelope changed: %+v", env)
			}
			if env.holdsWorkspace(0) != plain.holdsWorkspace(0) || env.jobsRunning() != plain.jobsRunning() {
				t.Fatal("the snapshot changed a busy decision")
			}
			got := parseLimitSnapshot(env.Limits, limitNow)
			if name == "valid" {
				if len(got) != 2 || got[0].Kind != "claude" || got[1].Kind != "codex" {
					t.Fatalf("valid snapshot parsed to %+v", got)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("malformed snapshot produced observations: %+v", got)
			}
		})
	}
}

// One malformed kind costs only that kind.
func TestLimitSnapshotMalformedKindIsUnknownAlone(t *testing.T) {
	raw := json.RawMessage(`{"v":1,"kinds":{"claude":{"epoch":"e","units":"bad"},
	 "agy":{"epoch":"e","units":[{"unit":"5h","usedPct":1,"observedAt":"2026-10-12T11:00:00Z"}]},
	 "codex":{"epoch":"e","epochSince":"2026-10-12T08:00:00Z","units":[{"unit":"5h","usedPct":1,"observedAt":"2026-10-12T11:00:00Z"},
	   {"unit":"7d","usedPct":1,"observedAt":"2026-10-12T11:00:00Z","derived":true},
	   {"unit":"bogus","usedPct":1,"observedAt":"2026-10-12T11:00:00Z"},
	   {"unit":"7d","usedPct":1,"observedAt":"2026-10-12T13:00:00Z"}]}}}`)
	got := parseLimitSnapshot(raw, limitNow)
	if len(got) != 1 || got[0].Kind != "codex" || len(got[0].Obs) != 1 || got[0].Obs[0].Unit != "5h" {
		t.Fatalf("got %+v: want codex 5h only (claude malformed, agy not covered, derived/unknown/future units dropped)", got)
	}
}

// An old CP decodes GET /sessions into the envelope it had before this field existed. It must
// keep working when a new Agent adds the snapshot.
func TestOldEnvelopeIgnoresTheSnapshot(t *testing.T) {
	var old struct {
		Sessions  []sessionWire `json:"sessions"`
		RepoJobs  int           `json:"repoJobs"`
		ImageJobs int           `json:"imageJobs"`
	}
	if err := json.Unmarshal([]byte(agentEnvelopeBase+`,"limits":`+validSnapshot()+`}`), &old); err != nil {
		t.Fatal(err)
	}
	if len(old.Sessions) != 3 || old.RepoJobs != 1 || old.ImageJobs != 2 {
		t.Fatalf("old envelope = %+v", old)
	}
}

func TestLimitSessionsOf(t *testing.T) {
	var env agentSessionsEnvelope
	_ = json.Unmarshal([]byte(agentEnvelopeBase+`}`), &env)
	got := limitSessionsOf(env.Sessions)
	want := []limitSessionWire{
		{Kind: "codex", State: "limited", ResumeAt: "2026-10-12T05:00:00Z"},
		// spend_limit never carries a resume time, whatever the Agent sent.
		{Kind: "codex", State: "spend_limit"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

type limitFixture struct {
	st      *store.SQL
	tenant  store.Tenant
	members map[string]store.MembershipView
	idents  map[string]store.Identity
	api     limitBoardAPI
}

func newLimitFixture(t *testing.T, st *store.SQL, keys ...string) *limitFixture {
	t.Helper()
	ctx := context.Background()
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &limitFixture{st: st, tenant: tn, members: map[string]store.MembershipView{}, idents: map[string]store.Identity{}}
	for _, k := range keys {
		id, _ := st.UpsertIdentity(ctx, k+"@example.com", k, "")
		m, _ := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
		f.members[k] = store.MembershipView{MembershipID: m.ID, TenantID: tn.ID, TenantSlug: tn.Slug, TenantName: tn.Name}
		f.idents[k] = id
	}
	f.api = limitBoardAPI{store: st, now: func() time.Time { return limitNow }}
	return f
}

func openLimitStore(t *testing.T) *store.SQL {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *limitFixture) observe(t *testing.T, who string, raw string) {
	t.Helper()
	m := &manager{store: f.st}
	var env agentSessionsEnvelope
	if err := json.Unmarshal([]byte(agentEnvelopeBase+`,"limits":`+raw+`}`), &env); err != nil {
		t.Fatal(err)
	}
	m.recordLimitsFromEnvelope(context.Background(), store.Workspace{MembershipID: f.members[who].MembershipID, TenantID: f.tenant.ID}, env, limitNow)
}

func (f *limitFixture) publish(t *testing.T, who, audience, level string, members ...string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"audience": audience, "level": level, "members": members})
	w := httptest.NewRecorder()
	f.api.setMe(w, httptest.NewRequest("PUT", "/api/limit-board/me", bytes.NewReader(body)), f.idents[who], f.members[who])
	if w.Code != 200 {
		t.Fatalf("publish %s: %d %s", who, w.Code, w.Body)
	}
}

type boardResp struct {
	Enabled bool              `json:"enabled"`
	Members []json.RawMessage `json:"members"`
	Next    string            `json:"next"`
}

func (f *limitFixture) board(t *testing.T, viewer, query string) boardResp {
	t.Helper()
	w := httptest.NewRecorder()
	f.api.board(w, httptest.NewRequest("GET", "/api/limit-board"+query, nil), f.idents[viewer], f.members[viewer])
	if w.Code != 200 {
		t.Fatalf("board: %d %s", w.Code, w.Body)
	}
	var out boardResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// entryOf finds one member's board entry with the identifying fields removed, so two entries
// can be compared for "looks the same".
func entryOf(t *testing.T, r boardResp, userKey string) map[string]any {
	t.Helper()
	for _, raw := range r.Members {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["userKey"] == userKey {
			delete(m, "userKey")
			delete(m, "email")
			return m
		}
	}
	t.Fatalf("%s not on the board", userKey)
	return nil
}

// The reduction (decision 6): a viewer outside the audience gets exactly what a never-observed
// member gives, and the band level never carries the exact values.
func TestLimitBoardReduction(t *testing.T) {
	f := newLimitFixture(t, openLimitStore(t), "alice", "bob", "carol", "dave", "erin")
	for _, who := range []string{"alice", "bob", "carol"} {
		f.observe(t, who, validSnapshot())
	}
	f.publish(t, "alice", "members", "band", "bob")
	f.publish(t, "carol", "tenant", "exact")
	// bob is hidden by default; erin is published but never observed.
	f.publish(t, "erin", "tenant", "exact")

	byBob := f.board(t, "bob", "")
	byDave := f.board(t, "dave", "")
	never := entryOf(t, byDave, "erin")
	if !reflect.DeepEqual(never, map[string]any{"kinds": []any{}}) {
		t.Fatalf("never-observed entry = %v", never)
	}
	// alice publishes to bob only: dave sees her exactly as he sees a never-observed member.
	if got := entryOf(t, byDave, "alice"); !reflect.DeepEqual(got, never) {
		t.Errorf("outside the audience: %v, want the never-observed %v", got, never)
	}
	// bob publishes nothing (the default), and has data: same again.
	if got := entryOf(t, byDave, "bob"); !reflect.DeepEqual(got, never) {
		t.Errorf("hidden by default: %v, want %v", got, never)
	}
	// bob is in alice's audience at band level: bands, observation times, no exact values.
	raw := ""
	for _, m := range byBob.Members {
		if strings.Contains(string(m), `"alice"`) {
			raw = string(m)
		}
	}
	for _, forbidden := range []string{"usedPct", "resetsAt", "42.5", "epoch", "e1", "source", "receivedAt"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("band entry leaks %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"band":"lt50"`) || !strings.Contains(raw, `"band":"ge95"`) {
		t.Errorf("band entry = %s", raw)
	}
	// carol publishes exact to the tenant.
	if raw := limitJSON(entryOf(t, byDave, "carol")); !strings.Contains(raw, `"usedPct":42.5`) || strings.Contains(raw, "e1") {
		t.Errorf("exact entry = %s", raw)
	}
	// The caller is never on their own roster.
	for _, m := range byBob.Members {
		if strings.Contains(string(m), `"userKey":"bob"`) {
			t.Error("caller listed on their own board")
		}
	}
}

func limitJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// boardEntry on its own, with the publisher's data at hand: the board handler also avoids
// reading data it may not show, and that must not be the only thing standing between a viewer
// and the data.
func TestBoardEntryOutsideTheAudienceIsNeverObserved(t *testing.T) {
	obs := []store.LimitObservation{{Kind: "claude", Unit: "5h", UsedPct: 70, ObservedAt: "2026-10-12T11:59:00Z"}}
	ss := &store.LimitSessionStates{States: `[{"kind":"claude","state":"limited"}]`, ObservedAt: "2026-10-12T11:59:00Z"}
	never := limitJSON(boardEntry(store.LimitPublication{}, false, "v", nil, nil, limitNow))
	for name, pub := range map[string]store.LimitPublication{
		"hidden":        {Audience: "none", Level: "exact"},
		"other members": {Audience: "members", Level: "exact", Viewers: []string{"w"}},
		"unknown":       {Audience: "everyone", Level: "exact"},
	} {
		if got := limitJSON(boardEntry(pub, true, "v", obs, ss, limitNow)); got != never {
			t.Errorf("%s: %s, want %s", name, got, never)
		}
	}
	if got := limitJSON(boardEntry(store.LimitPublication{Audience: "members", Level: "state", Viewers: []string{"v"}}, true, "v", obs, ss, limitNow)); got == never {
		t.Error("a named viewer saw nothing")
	}
}

// The state level shows what sessions ran into and nothing of the quota.
func TestLimitBoardStateLevel(t *testing.T) {
	f := newLimitFixture(t, openLimitStore(t), "alice", "bob")
	f.observe(t, "alice", validSnapshot())
	f.publish(t, "alice", "tenant", "state")
	raw := limitJSON(entryOf(t, f.board(t, "bob", ""), "alice"))
	if strings.Contains(raw, "units") || strings.Contains(raw, "band") {
		t.Fatalf("state level leaks quota: %s", raw)
	}
	if !strings.Contains(raw, `{"resumeAt":"2026-10-12T05:00:00Z","state":"limited"}`) || !strings.Contains(raw, `{"state":"spend_limit"}`) {
		t.Fatalf("state level = %s", raw)
	}
}

// Display honesty (decision 6, addendum item 6), on the data the reaper last stored.
func TestProjectLimitsStatuses(t *testing.T) {
	obs := []store.LimitObservation{
		{Kind: "claude", Unit: "5h", UsedPct: 60, ResetsAt: "2026-10-12T13:00:00Z", ObservedAt: "2026-10-12T11:30:00Z"},
		{Kind: "claude", Unit: "7d", UsedPct: 30, ResetsAt: "2026-10-15T00:00:00Z", ObservedAt: "2026-10-12T10:30:00Z"},
		{Kind: "codex", Unit: "5h", UsedPct: 99, ResetsAt: "2026-10-12T11:00:00Z", ObservedAt: "2026-10-12T10:00:00Z"},
	}
	v := projectLimits("exact", obs, nil, limitNow)
	got := map[string]limitUnitView{}
	for _, k := range v.Kinds {
		for _, u := range k.Units {
			got[k.Kind+"/"+u.Unit] = u
		}
	}
	if u := got["claude/5h"]; u.Status != "current" || u.Band != "lt80" || *u.UsedPct != 60 {
		t.Errorf("fresh = %+v", u)
	}
	// Issue #1962 acceptance: a workspace nobody reaches keeps its last value, which turns stale.
	if u := got["claude/7d"]; u.Status != "stale" || u.UsedPct == nil {
		t.Errorf("90 min old = %+v, want stale and still a lower bound", u)
	}
	// Past the reset: never 0%, never a band, never "available".
	if u := got["codex/5h"]; u.Status != "reset_not_observed" || u.Band != "" || u.UsedPct != nil {
		t.Errorf("past reset = %+v", u)
	}
	if v := projectLimits("bogus", obs, nil, limitNow); len(v.Kinds) != 0 {
		t.Errorf("unknown level projected %+v", v)
	}
}

// A setting is per tenant membership: one person in two tenants publishes separately, and a
// co-member in the other tenant sees nothing of the first.
func TestLimitBoardCrossTenantIsolation(t *testing.T) {
	st := openLimitStore(t)
	ctx := context.Background()
	f := newLimitFixture(t, st, "alice", "bob")
	other, err := st.CreateTenant(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	aliceOther, _ := st.EnsureMembership(ctx, f.idents["alice"].ID, other.ID, "member")
	zid, _ := st.UpsertIdentity(ctx, "zed@example.com", "zed", "")
	zed, _ := st.EnsureMembership(ctx, zid.ID, other.ID, "member")
	f.members["alice@other"] = store.MembershipView{MembershipID: aliceOther.ID, TenantID: other.ID, TenantSlug: "other"}
	f.members["zed"] = store.MembershipView{MembershipID: zed.ID, TenantID: other.ID, TenantSlug: "other"}
	f.idents["zed"] = zid

	f.observe(t, "alice", validSnapshot())
	f.publish(t, "alice", "tenant", "exact")
	// In "other", alice is hidden and has never been observed.
	if got := entryOf(t, f.board(t, "zed", ""), "alice"); !reflect.DeepEqual(got, map[string]any{"kinds": []any{}}) {
		t.Fatalf("other tenant sees alice's default-tenant data: %v", got)
	}
	if got := limitJSON(entryOf(t, f.board(t, "bob", ""), "alice")); !strings.Contains(got, "usedPct") {
		t.Fatalf("same tenant should see it: %s", got)
	}
	// Naming a member of another tenant is refused.
	body, _ := json.Marshal(map[string]any{"audience": "members", "level": "exact", "members": []string{"zed"}})
	w := httptest.NewRecorder()
	f.api.setMe(w, httptest.NewRequest("PUT", "/", bytes.NewReader(body)), f.idents["alice"], f.members["alice"])
	if w.Code != 400 {
		t.Fatalf("naming another tenant's member: %d %s", w.Code, w.Body)
	}
	// The member sees which tenants they publish to.
	w = httptest.NewRecorder()
	f.api.me(w, httptest.NewRequest("GET", "/", nil), f.idents["alice"], f.members["alice@other"])
	var me struct {
		Audience  string                 `json:"audience"`
		Publishes []limitPublishedTenant `json:"publishes"`
		Own       limitView              `json:"own"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me.Audience != "none" || len(me.Publishes) != 1 || me.Publishes[0].Slug != f.tenant.Slug || len(me.Own.Kinds) != 0 {
		t.Fatalf("me in other tenant = %s", w.Body)
	}
}

func TestLimitBoardTenantSwitchAndRoster(t *testing.T) {
	f := newLimitFixture(t, openLimitStore(t), "alice", "bob", "carol", "dave")
	f.observe(t, "alice", validSnapshot())
	f.publish(t, "alice", "tenant", "exact")
	page := f.board(t, "bob", "?limit=2")
	if len(page.Members) != 1 || page.Next != "bob" {
		t.Fatalf("page 1 = %+v (alice, bob-excluded; next after bob)", page)
	}
	page = f.board(t, "bob", "?limit=2&after="+page.Next)
	if len(page.Members) != 2 || page.Next != "" {
		t.Fatalf("page 2 = %+v", page)
	}
	if err := f.st.SetLimitBoardEnabled(context.Background(), f.tenant.ID, false, "t"); err != nil {
		t.Fatal(err)
	}
	if r := f.board(t, "bob", ""); r.Enabled || len(r.Members) != 0 {
		t.Fatalf("switched-off board = %+v", r)
	}
	// Off also stops collection.
	f.observe(t, "bob", validSnapshot())
	if obs, _ := f.st.ListLimitObservations(context.Background(), []string{f.members["bob"].MembershipID}); len(obs) != 0 {
		t.Fatalf("collected while the board is off: %+v", obs)
	}
}

// The browser fallback (POST /api/notifications/usage-observations) under the same contract.
func TestUsageObservationFeedsTheBoardOnlyWithAnObservationTime(t *testing.T) {
	st := openLimitStore(t)
	f := newLimitFixture(t, st, "alice")
	mv := f.members["alice"]
	a := notificationAPI{store: st, limits: st, now: func() time.Time { return limitNow }}
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		a.observeUsage(w, httptest.NewRequest("POST", "/", bytes.NewBufferString(body)), store.Identity{}, mv)
		if w.Code != 204 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	list := func() []store.LimitObservation {
		obs, _ := st.ListLimitObservations(context.Background(), []string{mv.MembershipID})
		return obs
	}
	// An old Console: no observedAt, no epoch. The send time is never used in its place.
	post(`{"source":"claude","windows":[{"windowKey":"5h","percent":50,"resetsAt":"2026-10-12T14:00:00Z"}]}`)
	if obs := list(); len(obs) != 0 {
		t.Fatalf("a post without observedAt reached the board: %+v", obs)
	}
	// An epoch alone is not enough: still no observation time to store.
	post(`{"source":"claude","limitEpoch":"e","limitEpochSince":"2026-10-12T08:00:00Z","windows":[{"windowKey":"5h","percent":50,"resetsAt":"2026-10-12T14:00:00Z"}]}`)
	if obs := list(); len(obs) != 0 {
		t.Fatalf("the send time stood in for observedAt: %+v", obs)
	}
	// Derived: neither the board nor the armed state.
	post(`{"source":"codex","limitEpoch":"e","limitEpochSince":"2026-10-12T08:00:00Z","windows":[{"windowKey":"5h","percent":95,"resetsAt":"2026-10-12T14:00:00Z","observedAt":"2026-10-12T11:59:00Z","derived":true}]}`)
	if obs := list(); len(obs) != 0 {
		t.Fatalf("a derived value reached the board: %+v", obs)
	}
	if st, found, _ := st.GetUsageNotificationState(context.Background(), mv.MembershipID, "codex", "5h"); found && st.Armed == 1 {
		t.Fatal("a derived value armed the reset notification")
	}
	// Stale by age: no arming either.
	post(`{"source":"codex","limitEpoch":"e","limitEpochSince":"2026-10-12T08:00:00Z","windows":[{"windowKey":"7d","percent":95,"resetsAt":"2026-10-14T14:00:00Z","observedAt":"2026-10-12T09:00:00Z"}]}`)
	if st, found, _ := st.GetUsageNotificationState(context.Background(), mv.MembershipID, "codex", "7d"); found && st.Armed == 1 {
		t.Fatal("a three-hour-old reading armed the reset notification")
	}
	// A real one: stored with its own observation time, received and source apart.
	post(`{"source":"claude","limitEpoch":"e","limitEpochSince":"2026-10-12T08:00:00Z","windows":[{"windowKey":"5h","percent":61,"resetsAt":"2026-10-12T14:00:00Z","observedAt":"2026-10-12T11:58:00+00:00"}]}`)
	obs := list()
	var got *store.LimitObservation
	for i := range obs {
		if obs[i].Kind == "claude" {
			got = &obs[i]
		}
	}
	if got == nil || got.ObservedAt != "2026-10-12T11:58:00Z" || got.ReceivedAt != "2026-10-12T12:00:00Z" || got.Source != "browser" || got.WindowMins != 300 {
		t.Fatalf("browser observation = %+v", obs)
	}
	// The reaper's identical observation later is a duplicate; an older one never wins.
	f.observe(t, "alice", `{"v":1,"kinds":{"claude":{"epoch":"e","epochSince":"2026-10-12T08:00:00Z","units":[{"unit":"5h","usedPct":10,"resetsAt":"2026-10-12T14:00:00Z","observedAt":"2026-10-12T11:00:00Z"}]}}}`)
	if obs := list(); obs[0].UsedPct != 61 || obs[0].Source != "browser" {
		t.Fatalf("an older observation overwrote a newer one: %+v", obs)
	}
	// Re-login (another epoch) hides the previous account's values at once.
	f.observe(t, "alice", `{"v":1,"kinds":{"claude":{"epoch":"other","epochSince":"2026-10-12T11:59:30Z","units":[{"unit":"7d","usedPct":1,"resetsAt":"2026-10-14T00:00:00Z","observedAt":"2026-10-12T11:59:00Z"}]}}}`)
	for _, o := range list() {
		if o.Kind == "claude" && o.Unit == "5h" {
			t.Fatalf("previous account's 5h survived the re-login: %+v", o)
		}
	}
}

// The sweep itself: a reaper read that carries a snapshot (valid or broken) takes the same
// idle decision as one without, and the valid one lands on the board.
func TestReaperSweepRecordsTheSnapshotWithoutChangingDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, limits string
		want         int
	}{{"none", "", 0}, {"broken", `,"limits":{"v":1,"kinds":"x"}`, 0}, {"valid", `,"limits":` + snapshotAt(time.Now()), 3}} {
		t.Run(tc.name, func(t *testing.T) {
			st, ws, mgr := reaperLifecycleFixture(t)
			var asked string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = r.URL.RawQuery
				_, _ = w.Write([]byte(agentEnvelopeBase + tc.limits + `}`))
			}))
			defer srv.Close()
			mgr.rtFactory = stubFactory{rt: &limitSweepRuntime{endpoint: srv.URL}}
			rp := &reaper{mgr: mgr, idleSince: map[string]time.Time{}}
			rp.sweepWorkspace(context.Background(), ws, tierClocks{wsOn: true, ws: time.Hour}, map[string]bool{})
			if asked != "limits=1" {
				t.Errorf("reaper asked %q, want limits=1", asked)
			}
			f := mgr.idleForecasts[ws.ID]
			if len(f.Holders) == 0 {
				t.Fatalf("no holders recorded: %+v", f)
			}
			obs, _ := st.ListLimitObservations(context.Background(), []string{ws.MembershipID})
			if len(obs) != tc.want {
				t.Fatalf("stored %d observations, want %d", len(obs), tc.want)
			}
			if tc.name == "valid" {
				// Compare against a sweep without the snapshot.
				st2, ws2, mgr2 := reaperLifecycleFixture(t)
				_ = st2
				srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(agentEnvelopeBase + `}`))
				}))
				defer srv2.Close()
				mgr2.rtFactory = stubFactory{rt: &limitSweepRuntime{endpoint: srv2.URL}}
				(&reaper{mgr: mgr2, idleSince: map[string]time.Time{}}).sweepWorkspace(context.Background(), ws2,
					tierClocks{wsOn: true, ws: time.Hour}, map[string]bool{})
				f2 := mgr2.idleForecasts[ws2.ID]
				if !reflect.DeepEqual(f.Holders, f2.Holders) || f.Enabled != f2.Enabled {
					t.Fatalf("snapshot changed the forecast: %+v vs %+v", f, f2)
				}
			}
		})
	}
}

type limitSweepRuntime struct{ endpoint string }

func (r *limitSweepRuntime) Start(context.Context) error  { return nil }
func (r *limitSweepRuntime) Stop(context.Context) error   { return nil }
func (r *limitSweepRuntime) State(context.Context) string { return "running" }
func (r *limitSweepRuntime) Endpoint() string             { return r.endpoint }
func (r *limitSweepRuntime) Token() string                { return "" }
func (r *limitSweepRuntime) Name() string                 { return "limit-sweep" }

var _ runtime.Runtime = (*limitSweepRuntime)(nil)

// Both dialects for the board's read path, end to end through the projection.
func TestLimitBoardPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres half")
	}
	st, err := store.OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := newLimitFixture(t, st, "alice", "bob", "carol")
	f.observe(t, "alice", validSnapshot())
	f.publish(t, "alice", "members", "band", "bob")
	if raw := limitJSON(entryOf(t, f.board(t, "bob", ""), "alice")); !strings.Contains(raw, `"band":"ge95"`) || strings.Contains(raw, "usedPct") {
		t.Fatalf("bob sees %s", raw)
	}
	if got := entryOf(t, f.board(t, "carol", ""), "alice"); !reflect.DeepEqual(got, map[string]any{"kinds": []any{}}) {
		t.Fatalf("carol sees %v", got)
	}
}

// Review finding (#2017): after a re-login the Agent reports the new epoch before it has
// observed anything of the new account. That epoch alone must hide the previous account's
// values; waiting for the first new observation left them published as current.
func TestLimitBoardEpochOnlyReloginHidesThePreviousAccount(t *testing.T) {
	f := newLimitFixture(t, openLimitStore(t), "alice", "bob")
	f.observe(t, "alice", validSnapshot())
	f.publish(t, "alice", "tenant", "exact")
	if raw := limitJSON(entryOf(t, f.board(t, "bob", ""), "alice")); !strings.Contains(raw, "42.5") {
		t.Fatalf("before the re-login: %s", raw)
	}
	f.observe(t, "alice", `{"v":1,"kinds":{"claude":{"epoch":"b","epochSince":"2026-10-12T11:55:00Z","units":[]}}}`)
	raw := limitJSON(entryOf(t, f.board(t, "bob", ""), "alice"))
	if strings.Contains(raw, "42.5") || strings.Contains(raw, `"kind":"claude","units"`) {
		t.Fatalf("the previous account's claude values survived an epoch-only re-login: %s", raw)
	}
	if !strings.Contains(raw, `"usedPct":97`) {
		t.Fatalf("another kind's values went with it: %s", raw)
	}
}

// Review finding (#2017): a browser post read before a re-login and delivered after the reaper
// stored the new account must not bring the previous account back.
func TestUsageObservationLatePostFromBeforeAReloginIsRefused(t *testing.T) {
	st := openLimitStore(t)
	f := newLimitFixture(t, st, "alice")
	mv := f.members["alice"]
	a := notificationAPI{store: st, limits: st, now: func() time.Time { return limitNow }}
	f.observe(t, "alice", `{"v":1,"kinds":{"claude":{"epoch":"b","epochSince":"2026-10-12T11:55:00Z","units":[{"unit":"5h","usedPct":3,"resetsAt":"2026-10-12T16:00:00Z","observedAt":"2026-10-12T11:59:00Z"}]}}}`)
	w := httptest.NewRecorder()
	a.observeUsage(w, httptest.NewRequest("POST", "/", bytes.NewBufferString(
		`{"source":"claude","limitEpoch":"a","limitEpochSince":"2026-10-12T08:00:00Z","windows":[{"windowKey":"5h","percent":90,"resetsAt":"2026-10-12T14:00:00Z","observedAt":"2026-10-12T11:50:00Z"}]}`)),
		store.Identity{}, mv)
	if w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	obs, _ := st.ListLimitObservations(context.Background(), []string{mv.MembershipID})
	if len(obs) != 1 || obs[0].Epoch != "b" || obs[0].UsedPct != 3 {
		t.Fatalf("the late post replaced the new account: %+v", obs)
	}
	// Round 2: nor may it arm the reset notification, even with no notification state yet: the
	// board already knows a later login.
	if ns, found, _ := st.GetUsageNotificationState(context.Background(), mv.MembershipID, "claude", "5h"); found && ns.Armed == 1 {
		t.Fatalf("the previous login's 90%% armed the reset notification: %+v", ns)
	}
}

// notifyDialects runs f on SQLite and, when AF_TEST_DATABASE_URL is set, Postgres: the reset
// notification's fence is a transaction plus conditional writes, which is dialect territory.
func notifyDialects(t *testing.T, f func(t *testing.T, st *store.SQL)) {
	t.Run("sqlite", func(t *testing.T) { f(t, openLimitStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run the Postgres half")
		}
		st, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		f(t, st)
	})
}

// notifyHarness posts to the real observeUsage, with the tenant's board switched off (the
// notification must be fenced without it).
type notifyHarness struct {
	t  *testing.T
	st *store.SQL
	mv store.MembershipView
	a  notificationAPI
}

func newNotifyHarness(t *testing.T, st *store.SQL, ns store.NotificationStore) *notifyHarness {
	f := newLimitFixture(t, st, "alice")
	if err := st.SetLimitBoardEnabled(context.Background(), f.tenant.ID, false, "t"); err != nil {
		t.Fatal(err)
	}
	if ns == nil {
		ns = st
	}
	return &notifyHarness{t: t, st: st, mv: f.members["alice"],
		a: notificationAPI{store: ns, limits: st, now: func() time.Time { return limitNow }}}
}

func (h *notifyHarness) post(ctx context.Context, epoch, gen, window string, pct int, resets string) {
	h.t.Helper()
	body := fmt.Sprintf(`{"source":"claude","limitEpoch":%q,"limitEpochSince":%q,"windows":[{"windowKey":%q,"percent":%d,"resetsAt":%q,"observedAt":"2026-10-12T11:59:00Z"}]}`,
		epoch, gen, window, pct, resets)
	w := httptest.NewRecorder()
	h.a.observeUsage(w, httptest.NewRequest("POST", "/", bytes.NewBufferString(body)).WithContext(ctx), store.Identity{}, h.mv)
	if w.Code != 204 {
		h.t.Errorf("%d %s", w.Code, w.Body)
	}
}

func (h *notifyHarness) state(window string) store.UsageNotificationState {
	ns, _, _ := h.st.GetUsageNotificationState(context.Background(), h.mv.MembershipID, "claude", window)
	return ns
}

func (h *notifyHarness) fired() int {
	n, _ := h.st.ListNotifications(context.Background(), h.mv.MembershipID, "", 50)
	return len(n)
}

const (
	genA = "2026-10-12T08:00:00Z"
	genB = "2026-10-12T11:58:00.000000001Z"
)

// Round 2: an older login's post does nothing, and a newer login does not inherit the old armed
// state, so its first reading is not taken for a reset.
func TestUsageResetNotificationIsFencedByLogin(t *testing.T) {
	notifyDialects(t, func(t *testing.T, st *store.SQL) {
		h := newNotifyHarness(t, st, nil)
		ctx := context.Background()
		h.post(ctx, "a", genA, "5h", 95, "2026-10-12T12:30:00Z")
		if h.state("5h").Armed != 1 {
			t.Fatalf("armed = %+v", h.state("5h"))
		}
		h.post(ctx, "b", genB, "5h", 5, "2026-10-12T16:00:00Z")
		if h.state("5h").Armed != 0 || h.fired() != 0 {
			t.Fatalf("b inherited a's armed state or fired a reset: %+v fired=%d", h.state("5h"), h.fired())
		}
		h.post(ctx, "a", genA, "5h", 99, "2026-10-12T12:30:00Z")
		if h.state("5h").Armed != 0 {
			t.Fatalf("an older login's post moved the state: %+v", h.state("5h"))
		}
	})
}

// Round 3 (b): the generation is per source, not per window. B observed only on the 7-day
// window must already clear A's armed 5-hour window and refuse A's late 5-hour post.
func TestUsageResetNotificationNewLoginOnAnotherWindowFencesAll(t *testing.T) {
	notifyDialects(t, func(t *testing.T, st *store.SQL) {
		h := newNotifyHarness(t, st, nil)
		ctx := context.Background()
		h.post(ctx, "a", genA, "5h", 90, "2026-10-12T12:30:00Z")
		h.post(ctx, "b", genB, "7d", 3, "2026-10-15T00:00:00Z")
		if h.state("5h").Armed != 0 {
			t.Fatalf("a's armed 5-hour window survived b's login: %+v", h.state("5h"))
		}
		h.post(ctx, "a", genA, "5h", 99, "2026-10-12T12:30:00Z")
		if h.state("5h").Armed != 0 {
			t.Fatalf("a's late 5-hour post armed it under b: %+v", h.state("5h"))
		}
		// b's own later 5-hour reading with a later reset fires nothing: b never armed it.
		h.post(ctx, "b", genB, "5h", 2, "2026-10-12T17:00:00Z")
		if h.fired() != 0 {
			t.Fatalf("a reset fired from the previous login's armed state: %d", h.fired())
		}
	})
}

// gateStore pauses one request's notification work at its first store call: after the read in
// a read-then-write handler, before the transaction in a transactional one. That is exactly the
// window a concurrent post used to slip through.
type gateStore struct {
	*store.SQL
	reached, release chan struct{}
}

type gateKey struct{}

func (g *gateStore) pause(ctx context.Context) {
	if ctx.Value(gateKey{}) != nil {
		close(g.reached)
		<-g.release
	}
}

func (g *gateStore) GetUsageNotificationState(ctx context.Context, m, s, k string) (store.UsageNotificationState, bool, error) {
	st, found, err := g.SQL.GetUsageNotificationState(ctx, m, s, k)
	g.pause(ctx)
	return st, found, err
}

func (g *gateStore) ApplyUsageObservation(ctx context.Context, m, s, e, gen string, keys []string,
	decide func(string, store.UsageNotificationState, bool) (store.UsageNotificationState, *store.Notification)) (bool, error) {
	g.pause(ctx)
	return g.SQL.ApplyUsageObservation(ctx, m, s, e, gen, keys, decide)
}

// Round 3 (a): A's post is held after it has read the state; B's post completes; A resumes.
// A's decision must be taken against B's committed generation, not the one it read, or it rolls
// the state back to A and armed=1.
func TestUsageResetNotificationConcurrentOlderPostCannotRollBack(t *testing.T) {
	notifyDialects(t, func(t *testing.T, st *store.SQL) {
		g := &gateStore{SQL: st, reached: make(chan struct{}), release: make(chan struct{})}
		h := newNotifyHarness(t, st, g)
		ctx := context.Background()
		h.post(ctx, "a", genA, "5h", 10, "2026-10-12T12:30:00Z")
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.post(context.WithValue(ctx, gateKey{}, true), "a", genA, "5h", 95, "2026-10-12T12:30:00Z")
		}()
		<-g.reached
		h.post(ctx, "b", genB, "5h", 4, "2026-10-12T16:00:00Z")
		close(g.release)
		<-done
		if ns := h.state("5h"); ns.Armed != 0 || ns.ResetsAt != "2026-10-12T16:00:00Z" {
			t.Fatalf("the held older post rolled b's state back: %+v", ns)
		}
	})
}

// Round 2: two logins recognised in the same second are still ordered (the generation keeps
// its nanoseconds all the way to the store).
func TestLimitBoardSameSecondReloginSwitches(t *testing.T) {
	f := newLimitFixture(t, openLimitStore(t), "alice", "bob")
	f.observe(t, "alice", `{"v":1,"kinds":{"claude":{"epoch":"a","epochSince":"2026-10-12T11:58:00.000000000Z","units":[{"unit":"5h","usedPct":90,"resetsAt":"2026-10-12T14:00:00Z","observedAt":"2026-10-12T11:57:00Z"}]}}}`)
	f.observe(t, "alice", `{"v":1,"kinds":{"claude":{"epoch":"b","epochSince":"2026-10-12T11:58:00.000000001Z","units":[]}}}`)
	if obs, _ := f.st.ListLimitObservations(context.Background(), []string{f.members["alice"].MembershipID}); len(obs) != 0 {
		t.Fatalf("a's 90%% survived a re-login in the same second: %+v", obs)
	}
}

// Review finding (#2017): with the tenant switch off, /me shows no values either, and the
// setting stays editable.
func TestLimitBoardMeShowsNothingWhenTheTenantIsOff(t *testing.T) {
	f := newLimitFixture(t, openLimitStore(t), "alice")
	f.observe(t, "alice", validSnapshot())
	if err := f.st.SetLimitBoardEnabled(context.Background(), f.tenant.ID, false, "t"); err != nil {
		t.Fatal(err)
	}
	f.publish(t, "alice", "tenant", "band") // still editable
	w := httptest.NewRecorder()
	f.api.me(w, httptest.NewRequest("GET", "/", nil), f.idents["alice"], f.members["alice"])
	var me limitBoardMe
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Enabled || len(me.Own.Kinds) != 0 || me.Own.SessionsObservedAt != "" || me.Audience != "tenant" || me.Level != "band" {
		t.Fatalf("me with the tenant off = %s", w.Body)
	}
}

// limitKindUnits is keyed by kind name: per-kind unit rules, not a list the catalog could
// supply. A key the catalog does not name (a typo, a renamed kind) would silently drop that
// kind's quota from the board, so every key must be a catalog kind.
func TestLimitKindUnitsAreCatalogKinds(t *testing.T) {
	known := map[string]bool{}
	for _, e := range agentkinds.All() {
		known[e.Kind] = true
	}
	if len(limitKindUnits) == 0 {
		t.Fatal("limitKindUnits is empty; this guard checks nothing")
	}
	for k := range limitKindUnits {
		if !known[k] {
			t.Errorf("limitKindUnits has %q, which is not a kind in agentkinds/catalog.json", k)
		}
	}
}
