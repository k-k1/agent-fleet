package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fakes ------------------------------------------------------------------------

// fakeGoldenPool is the AWS side of the bake, as a map of snapshots.
type fakeGoldenPool struct {
	image    string
	snaps    map[string]*goldenSnap // id -> snap
	roles    map[string]string      // id -> role
	reasons  map[string]string
	blocked  bool
	blockWhy string
	baked    []string // volume ids captured, in order
	deleted  []string
	nextID   int
	now      time.Time
}

func newFakeGoldenPool() *fakeGoldenPool {
	return &fakeGoldenPool{
		image:   "img:1",
		snaps:   map[string]*goldenSnap{},
		roles:   map[string]string{},
		reasons: map[string]string{},
		// Real time, not a fixed date: the baker also dates a seed from its WORKSPACE
		// ROW when there is no volume yet, and that timestamp is written by the store
		// with the real clock. A frozen fake clock hours away from it would make every
		// seed look expired the moment it was created.
		now: time.Now().UTC(),
	}
}

func (f *fakeGoldenPool) workspaceImage() string { return f.image }
func (f *fakeGoldenPool) poolLabel() string      { return "test-pool" }

func (f *fakeGoldenPool) goldenFor(_ context.Context, role string) (goldenSnap, bool, error) {
	var best goldenSnap
	found := false
	for id, r := range f.roles {
		if r != role {
			continue
		}
		s := *f.snaps[id]
		if !found || (s.Completed && !best.Completed) || s.Started.After(best.Started) {
			best, found = s, true
		}
	}
	return best, found, nil
}

func (f *fakeGoldenPool) bakeBlocked(context.Context) (bool, string, error) {
	return f.blocked, f.blockWhy, nil
}

func (f *fakeGoldenPool) snapshotHome(_ context.Context, volumeID, _ string) (string, error) {
	f.nextID++
	id := "snap-" + string(rune('a'+f.nextID-1))
	f.snaps[id] = &goldenSnap{ID: id, Started: f.now}
	f.roles[id] = ec2RoleGoldenCandidate
	f.baked = append(f.baked, volumeID)
	return id, nil
}

func (f *fakeGoldenPool) setGoldenRole(_ context.Context, id, role, reason string) error {
	f.roles[id] = role
	if reason != "" {
		f.reasons[id] = reason
	}
	return nil
}

func (f *fakeGoldenPool) dropSupersededGoldens(_ context.Context, keepID string) error {
	for id, r := range f.roles {
		if id == keepID || (r != ec2RoleGolden && r != ec2RoleGoldenCandidate) {
			continue
		}
		f.deleted = append(f.deleted, id)
		delete(f.roles, id)
		delete(f.snaps, id)
	}
	return nil
}

func (f *fakeGoldenPool) rejectedAttempts(context.Context) (int, error) {
	n := 0
	for _, r := range f.roles {
		if r == ec2RoleGoldenRejected {
			n++
		}
	}
	return n, nil
}

// complete marks a candidate as finished copying.
func (f *fakeGoldenPool) complete(id string) { f.snaps[id].Completed = true }

// completeAnyCandidate is what EBS does on its own between two ticks.
func (f *fakeGoldenPool) completeAnyCandidate() {
	for id, r := range f.roles {
		if r == ec2RoleGoldenCandidate {
			f.snaps[id].Completed = true
		}
	}
}

// fakeSeedRuntime is one reserved workspace: a Runtime plus the bake capability.
type fakeSeedRuntime struct {
	name           string
	state          string
	home           goldenHome
	endpoint       string
	seededFromCand bool
	released       bool
	starts, stops  int
	destroys       int
	// failStart models a Start that dies BEFORE createHomeVolume — no slot, no capacity,
	// an AWS error. The workspace row exists; nothing else does.
	failStart bool
}

func (r *fakeSeedRuntime) Start(context.Context) error {
	r.starts++
	if r.failStart {
		return errors.New("no capacity")
	}
	r.state = "running"
	if r.home.VolumeID == "" {
		r.home = goldenHome{VolumeID: "vol-" + r.name, Created: time.Now().UTC()}
	}
	return nil
}
func (r *fakeSeedRuntime) Stop(context.Context) error   { r.stops++; r.state = "stopped"; return nil }
func (r *fakeSeedRuntime) State(context.Context) string { return r.state }
func (r *fakeSeedRuntime) Endpoint() string             { return r.endpoint }
func (r *fakeSeedRuntime) Token() string                { return "" }
func (r *fakeSeedRuntime) Name() string                 { return r.name }

func (r *fakeSeedRuntime) seedFromCandidate() { r.seededFromCand = true }
func (r *fakeSeedRuntime) homeForBake(context.Context) (goldenHome, error) {
	return r.home, nil
}
func (r *fakeSeedRuntime) markHomeBaked(_ context.Context, _ string) error {
	r.home.Baked = true
	return nil
}
func (r *fakeSeedRuntime) releaseForBake(context.Context) error {
	r.released = true
	r.home.Capturable = true
	return nil
}

// Destroy models what the real teardown means for the next round: the home volume AND
// the EFS access points go, and the memoized runtime is evicted — so a reused membership
// genuinely starts from nothing. That is the property the probe depends on (§64.28.3),
// and it is why seedFromCandidate has to be re-applied rather than set once.
func (r *fakeSeedRuntime) Destroy(context.Context) ([]string, error) {
	r.destroys++
	r.state = "none"
	r.home = goldenHome{}
	r.seededFromCand = false
	return nil, nil
}

// fakeGoldenFactory hands out one fakeSeedRuntime per workspace name, remembered so a
// test can look at the same object the baker is driving.
type fakeGoldenFactory struct {
	rts   map[string]*fakeSeedRuntime
	agent *httptest.Server
	image string
}

func (f *fakeGoldenFactory) New(ws Workspace, _ string, _ []string) Runtime {
	if rt, ok := f.rts[ws.ContainerName]; ok {
		return rt
	}
	rt := &fakeSeedRuntime{name: ws.ContainerName, state: "none", endpoint: f.agent.URL}
	f.rts[ws.ContainerName] = rt
	return rt
}
func (f *fakeGoldenFactory) WorkspaceImage() string { return f.image }

// --- fixture ----------------------------------------------------------------------

type goldenFixture struct {
	baker *goldenBaker
	pool  *fakeGoldenPool
	fac   *fakeGoldenFactory
	store *sqlStore
}

// agentHealthy flips the fake Agent between answering /sessions and refusing.
func newGoldenFixture(t *testing.T, agentHealthy *bool) *goldenFixture {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureDefaultTenant(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if agentHealthy != nil && *agentHealthy {
			// The Agent's shape, not a bare list: agentSessions decodes
			// {"sessions": [...]}, and a bare [] fails to decode — which would make
			// every health check in this file "unhealthy" for the wrong reason.
			_, _ = w.Write([]byte(`{"sessions":[]}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	fac := &fakeGoldenFactory{rts: map[string]*fakeSeedRuntime{}, agent: srv, image: "img:1"}
	mgr := &manager{store: st, rtFactory: fac, conns: newConnRegistry(), rts: map[string]cachedRT{}}
	pool := newFakeGoldenPool()
	b := newGoldenBaker(mgr, pool)
	b.now = func() time.Time { return pool.now }
	return &goldenFixture{baker: b, pool: pool, fac: fac, store: st}
}

// rt returns the reserved workspace's runtime once the baker has created it.
func (f *goldenFixture) rt(t *testing.T, key string) *fakeSeedRuntime {
	t.Helper()
	for name, rt := range f.fac.rts {
		if strings.Contains(name, key) {
			return rt
		}
	}
	t.Fatalf("no runtime for %s yet (have %v)", key, f.fac.rts)
	return nil
}

func (f *goldenFixture) role(id string) string { return f.pool.roles[id] }

// candidateID is the one snapshot currently sitting in the candidate role.
func (f *goldenFixture) candidateID(t *testing.T) string {
	t.Helper()
	for id, r := range f.pool.roles {
		if r == ec2RoleGoldenCandidate {
			return id
		}
	}
	t.Fatal("no candidate snapshot exists yet")
	return ""
}

// hasWorkspace reports whether one of the reserved memberships still owns a workspace.
func (f *goldenFixture) hasWorkspace(t *testing.T, key string) bool {
	t.Helper()
	ctx := context.Background()
	mem, ok, err := f.baker.membership(ctx, key, false)
	if err != nil {
		t.Fatalf("membership %s: %v", key, err)
	}
	if !ok {
		return false
	}
	_, exists, err := f.store.GetWorkspaceByMembership(ctx, mem.MembershipID)
	if err != nil {
		t.Fatalf("workspace for %s: %v", key, err)
	}
	return exists
}

// --- tests ------------------------------------------------------------------------

// The happy path, one tick at a time — which is the contract: a bake must never need
// two things to happen inside a single step.
func TestGoldenBakeRunsToPublication(t *testing.T) {
	ctx := context.Background()
	healthy := false
	f := newGoldenFixture(t, &healthy)

	f.baker.step(ctx) // 1: create + start the seed
	seed := f.rt(t, goldenSeedKey)
	if seed.starts != 1 || seed.state != "running" {
		t.Fatalf("seed was not started: %+v", seed)
	}

	f.baker.step(ctx) // 2: seed is up but its Agent is not answering yet — wait
	if seed.stops != 0 {
		t.Fatal("stopped the seed before its Agent answered — that captures a half-installed home")
	}

	healthy = true
	f.baker.step(ctx) // 3: Agent answers -> mark the home baked, stop the seed
	if !seed.home.Baked {
		t.Fatal("the home was not marked baked")
	}
	if seed.stops != 1 {
		t.Fatalf("seed stops = %d, want 1", seed.stops)
	}

	f.baker.step(ctx) // 4: the home is still on a running slot -> release it
	if !seed.released {
		t.Fatal("the slot was not released before the snapshot")
	}
	if len(f.pool.baked) != 0 {
		t.Fatal("snapshotted before releasing the slot")
	}

	f.baker.step(ctx) // 5: capture
	if len(f.pool.baked) != 1 {
		t.Fatalf("no snapshot was taken: %v", f.pool.baked)
	}
	candID := f.candidateID(t)

	f.baker.step(ctx) // 6: still copying — nothing may be published, nothing probed
	if _, probed := f.fac.rts["af-ws-af-golden-"+goldenProbeKey]; probed {
		t.Fatal("probed a snapshot that has not finished copying")
	}

	f.pool.complete(candID)
	f.baker.step(ctx) // 7: candidate ready -> create + start the probe
	probe := f.rt(t, goldenProbeKey)
	if !probe.seededFromCand {
		t.Fatal("the probe did not ask for the CANDIDATE — it would have proven nothing")
	}
	if f.role(candID) != ec2RoleGoldenCandidate {
		t.Fatal("published the candidate before the probe came up")
	}

	f.baker.step(ctx) // 8: probe healthy -> publish
	if f.role(candID) != ec2RoleGolden {
		t.Fatalf("candidate was not promoted, role = %q", f.role(candID))
	}
	// And both reserved workspaces are gone, so no slot and no volume is left behind.
	for _, key := range []string{goldenSeedKey, goldenProbeKey} {
		if f.hasWorkspace(t, key) {
			t.Fatalf("%s workspace was left behind after publication", key)
		}
	}
}

// The failure this whole phase exists for: a candidate whose home cannot boot must
// never become the golden, and the deployment must end up with NO golden rather than a
// broken one (docs/64 §64.28.3).
func TestGoldenBakeRejectsACandidateThatWillNotBoot(t *testing.T) {
	ctx := context.Background()
	healthy := true
	f := newGoldenFixture(t, &healthy)

	for i := 0; i < 4; i++ { // seed → booted → released → captured
		f.baker.step(ctx)
	}
	candID := f.candidateID(t)
	f.pool.complete(candID)

	// From here the probe never comes up.
	healthy = false
	f.baker.step(ctx) // create + start the probe
	f.baker.step(ctx) // still not healthy — inside the budget, so keep waiting
	if f.role(candID) != ec2RoleGoldenCandidate {
		t.Fatalf("gave up (or published) too early: role = %q", f.role(candID))
	}

	f.pool.now = f.pool.now.Add(f.baker.probeBudget + time.Minute)
	f.baker.step(ctx)
	if f.role(candID) != ec2RoleGoldenRejected {
		t.Fatalf("a probe that never came up must reject the candidate, got %q", f.role(candID))
	}
	if _, ok, _ := f.pool.goldenFor(ctx, ec2RoleGolden); ok {
		t.Fatal("published a golden despite the probe failing")
	}
	if !strings.Contains(f.pool.reasons[candID], "did not come up") {
		t.Fatalf("the reason was not recorded on the snapshot: %q", f.pool.reasons[candID])
	}
}

// Two failures for one image are enough. Otherwise a genuinely unbootable image would
// take a slot for a fresh seed on every tick, forever.
func TestGoldenBakeGivesUpAfterTwoRejections(t *testing.T) {
	ctx := context.Background()
	healthy := false
	f := newGoldenFixture(t, &healthy)
	for _, id := range []string{"snap-x", "snap-y"} {
		f.pool.snaps[id] = &goldenSnap{ID: id, Completed: true, Started: f.pool.now}
		f.pool.roles[id] = ec2RoleGoldenRejected
	}

	f.baker.step(ctx)
	if len(f.fac.rts) != 0 {
		t.Fatalf("started a bake for an image that has already failed twice: %v", f.fac.rts)
	}
}

// A pool with no room belongs to the people using it. A missing golden costs a slow
// first start, which is not a reason to evict anybody.
func TestGoldenBakeWillNotTakeTheLastSlots(t *testing.T) {
	ctx := context.Background()
	healthy := true
	f := newGoldenFixture(t, &healthy)
	f.pool.blocked, f.pool.blockWhy = true, "4/4 slots in use; a bake needs two free"

	f.baker.step(ctx)
	if len(f.fac.rts) != 0 {
		t.Fatalf("created a seed while the pool was full: %v", f.fac.rts)
	}

	// ...but a bake ALREADY under way is not abandoned when the pool fills up: that
	// would strand the seed's slot across every later tick.
	f.pool.blocked = false
	f.baker.step(ctx)
	seed := f.rt(t, goldenSeedKey)
	f.pool.blocked = true
	f.baker.step(ctx)
	if !seed.home.Baked {
		t.Fatal("a bake in flight was abandoned because the pool filled up")
	}
}

// A seed that never boots must not hold a slot forever, and must not count as evidence
// about the image (nothing was baked, so nothing was proven).
func TestGoldenBakeTearsDownASeedThatNeverBoots(t *testing.T) {
	ctx := context.Background()
	healthy := false
	f := newGoldenFixture(t, &healthy)

	f.baker.step(ctx)
	if !f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("no seed workspace was created")
	}

	f.pool.now = f.pool.now.Add(f.baker.seedBudget + time.Minute)
	f.baker.step(ctx)
	if f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("a seed that never booted was left holding its slot")
	}
	if n, _ := f.pool.rejectedAttempts(ctx); n != 0 {
		t.Fatalf("a seed that never booted burned a rejection attempt (%d) — that is evidence about the slot, not the image", n)
	}
}

// A published, current golden means there is nothing to do — and any seed or probe a
// previous round left behind has to go, or the pool pays for two idle homes forever.
func TestGoldenBakeTidiesUpWhenAGoldenIsAlreadyPublished(t *testing.T) {
	ctx := context.Background()
	healthy := true
	f := newGoldenFixture(t, &healthy)

	f.baker.step(ctx) // makes a seed
	if !f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("no seed workspace was created")
	}

	f.pool.snaps["snap-g"] = &goldenSnap{ID: "snap-g", Completed: true, Started: f.pool.now}
	f.pool.roles["snap-g"] = ec2RoleGolden
	f.baker.step(ctx)
	if f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("left a seed behind although the golden is already published")
	}
}

// The image moving is the whole trigger.
func TestGoldenBakeStartsOverWhenTheImageMoves(t *testing.T) {
	ctx := context.Background()
	healthy := true
	f := newGoldenFixture(t, &healthy)
	f.pool.snaps["snap-old"] = &goldenSnap{ID: "snap-old", Completed: true, Started: f.pool.now}
	f.pool.roles["snap-old"] = ec2RoleGolden
	f.baker.step(ctx)
	if len(f.fac.rts) != 0 {
		t.Fatal("baked although a current golden exists")
	}

	// goldenFor is image-scoped on the real pool, so an image bump simply has none.
	delete(f.pool.roles, "snap-old")
	f.baker.step(ctx)
	if len(f.fac.rts) == 0 {
		t.Fatal("did not start a bake after the golden stopped matching the image")
	}
}

// Destroying a workspace evicts its memoized runtime, so a second round gets a runtime
// with seedRole cleared. If seedFromCandidate were applied once at creation instead of
// on every tick, the second round's probe would silently read the published golden (or,
// with none, an empty home), come up perfectly, and prove nothing.
func TestGoldenBakeProbeStillReadsTheCandidateOnASecondRound(t *testing.T) {
	ctx := context.Background()
	healthy := true
	f := newGoldenFixture(t, &healthy)

	for i := 0; i < 8; i++ { // round one, all the way to publication
		f.baker.step(ctx)
		f.pool.completeAnyCandidate()
	}
	if _, ok, _ := f.pool.goldenFor(ctx, ec2RoleGolden); !ok {
		t.Fatal("the first round did not publish a golden")
	}

	// The image moves: nothing baked for the old one counts any more.
	f.pool.roles = map[string]string{}
	f.pool.snaps = map[string]*goldenSnap{}
	f.pool.image, f.fac.image = "img:2", "img:2"

	// Stop one step SHORT of publication: the fake's Destroy resets the flag, exactly as
	// the real cache eviction resets seedRole, so asserting after tidy would test nothing.
	for i := 0; i < 5; i++ {
		f.baker.step(ctx)
		f.pool.completeAnyCandidate()
	}
	probe := f.rt(t, goldenProbeKey)
	if !probe.seededFromCand {
		t.Fatal("the second round's probe did not ask for the candidate — it would have proven nothing")
	}
}

// goldenBakerFor is the capability gate: no other runtime profile pays anything.
func TestGoldenBakerOnlyOnAPoolThatSeedsFromSnapshots(t *testing.T) {
	mgr := &manager{rtFactory: &fakeGoldenFactory{rts: map[string]*fakeSeedRuntime{}}}
	if b := goldenBakerFor(mgr, true); b != nil {
		t.Fatal("built a baker for a factory that cannot seed from a shared snapshot")
	}
	if b := goldenBakerFor(&manager{rtFactory: &ecsEC2Factory{}}, false); b != nil {
		t.Fatal("AF_ECS_EC2_GOLDEN_AUTOBAKE=0 did not switch it off")
	}
	if b := goldenBakerFor(&manager{rtFactory: &ecsEC2Factory{}}, true); b == nil {
		t.Fatal("no baker on ecs-ec2")
	}
}

// A Start that fails before the home volume exists leaves nothing to date the seed from.
// Without a second anchor the baker would re-Start that row once a minute forever.
func TestGoldenBakeTearsDownASeedWhoseStartNeverGotAVolume(t *testing.T) {
	ctx := context.Background()
	healthy := false
	f := newGoldenFixture(t, &healthy)

	f.baker.step(ctx) // creates the workspace row; the Start below will fail from now on
	seed := f.rt(t, goldenSeedKey)
	seed.failStart = true
	seed.state, seed.home = "none", goldenHome{}
	if !f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("no seed workspace was created")
	}

	f.baker.step(ctx)
	if !f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("gave up on the seed inside its budget")
	}

	f.pool.now = f.pool.now.Add(f.baker.seedBudget + time.Minute)
	f.baker.step(ctx)
	if f.hasWorkspace(t, goldenSeedKey) {
		t.Fatal("a seed that never got a home volume was retried forever")
	}
}
