package main

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestHomeDEKModeRandom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode, custodian string
		want, err       bool
	}{
		{"", "kms", false, false},
		{"derived", "local", false, false},
		{"random", "kms", true, false},
		{"random", "local", false, true},
		{"random", "", false, true},
		{"rnd", "kms", false, true},
	} {
		got, err := homeDEKModeRandom(tc.mode, tc.custodian)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("homeDEKModeRandom(%q, %q) = %v, %v", tc.mode, tc.custodian, got, err)
		}
	}
}

// homeDEKWorkspace makes a tenant, a member and a workspace row for that member.
func homeDEKWorkspace(t *testing.T, st *store.SQL, wid string) store.Workspace {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "sales-"+strings.ToLower(wid), "Sales")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, wid+"@acme.co.jp", "u-"+strings.ToLower(wid), "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	return recreateWorkspace(t, st, store.Workspace{TenantID: tn.ID, MembershipID: mem.ID}, wid)
}

func recreateWorkspace(t *testing.T, st *store.SQL, base store.Workspace, wid string) store.Workspace {
	t.Helper()
	ws := store.Workspace{ID: wid, TenantID: base.TenantID, MembershipID: base.MembershipID,
		ContainerName: "af-ws-" + wid, DataDir: "/srv/data/" + wid,
		AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestResolveDEKHomeKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			f := newFakeKMS(t)
			mgr := p3Manager(t, st)
			mgr.master32 = testMaster(t)
			mgr.custodian = newKMSCustodian(f, f.keyID, newLocalCustodian(mgr.master32), 0)
			ws := homeDEKWorkspace(t, st, "W1")
			userKey := "u-w1"
			derived := hex.EncodeToString(mgr.legacyDEK(userKey))

			// Off: the derived key alone, and no home key is minted.
			keys, err := mgr.resolveDEK(ctx, ws, userKey)
			if err != nil || keys != (runtime.SecretKeys{Key: derived}) {
				t.Fatalf("flag off: keys = %+v, %v", keys.Next != "", err)
			}
			if _, ok, _ := st.GetHomeDEK(ctx, ws.MembershipID); ok {
				t.Fatal("flag off minted a home key")
			}

			// On: the derived key stays as AF_SECRET_KEY and the home's random key rides beside it.
			mgr.homeDEKRandom = true
			keys, err = mgr.resolveDEK(ctx, ws, userKey)
			if err != nil || keys.Key != derived {
				t.Fatalf("flag on: Key changed or err %v", err)
			}
			if raw, err := hex.DecodeString(keys.Next); err != nil || len(raw) != 32 || keys.Next == derived {
				t.Fatalf("flag on: Next is not a fresh 32-byte key (err %v)", err)
			}
			d, ok, err := st.GetHomeDEK(ctx, ws.MembershipID)
			if err != nil || !ok || d.Scheme != store.HomeDEKMigrating || d.KeyRef != ws.TenantID || !strings.HasPrefix(d.Ciphertext, kmsSealPrefix) {
				t.Fatalf("home key row = %v/%v scheme %q ref %q kms=%v", ok, err, d.Scheme, d.KeyRef, strings.HasPrefix(d.Ciphertext, kmsSealPrefix))
			}
			next := keys.Next

			// The key belongs to the home: a workspace row recreated over it gets the same key.
			if err := st.DeleteWorkspace(ctx, ws.ID); err != nil {
				t.Fatal(err)
			}
			ws2 := recreateWorkspace(t, st, ws, "W2")
			if keys, err = mgr.resolveDEK(ctx, ws2, userKey); err != nil || keys.Next != next {
				t.Fatalf("recreated workspace: same home key = %v, %v", keys.Next == next, err)
			}

			// Turning the flag off does not take the key away from a home that has one: its
			// store may already be sealed under it.
			mgr.homeDEKRandom = false
			if keys, err = mgr.resolveDEK(ctx, ws2, userKey); err != nil || keys.Next != next {
				t.Fatalf("flag off after minting: home key kept = %v, %v", keys.Next == next, err)
			}

			// KMS refusing fails the start; it never falls back to the derived key alone.
			f.err = errors.New("DisabledException")
			if _, err := mgr.resolveDEK(ctx, ws2, userKey); err == nil {
				t.Fatal("a refused KMS key still produced keys")
			}
		})
	}
}

func TestResolveDEKMintFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	f := newFakeKMS(t)
	mgr := p3Manager(t, st)
	mgr.master32 = testMaster(t)
	// The derived key is already stored under the local format, so only the mint needs KMS.
	mgr.custodian = newLocalCustodian(mgr.master32)
	ws := homeDEKWorkspace(t, st, "W3")
	if _, err := mgr.resolveDEK(ctx, ws, "u-w3"); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("dial tcp: i/o timeout")
	mgr.custodian = newKMSCustodian(f, f.keyID, newLocalCustodian(mgr.master32), 0)
	mgr.homeDEKRandom = true
	if keys, err := mgr.resolveDEK(ctx, ws, "u-w3"); err == nil {
		t.Fatalf("KMS down during the mint: keys returned (Next set=%v)", keys.Next != "")
	}
	if _, ok, _ := st.GetHomeDEK(ctx, ws.MembershipID); ok {
		t.Fatal("a failed mint stored a home key")
	}

	// Once the home has its key, a KMS that refuses to open it fails the start too, even
	// though the derived key (local format here) still opens: starting on that alone would
	// hand the Agent a key its re-sealed store does not open.
	f.err = nil
	if keys, err := mgr.resolveDEK(ctx, ws, "u-w3"); err != nil || keys.Next == "" {
		t.Fatalf("mint after KMS came back = %v, %v", keys.Next != "", err)
	}
	f.err = errors.New("DisabledException")
	if keys, err := mgr.resolveDEK(ctx, ws, "u-w3"); err == nil {
		t.Fatalf("KMS refusing the home key: keys returned (Next set=%v)", keys.Next != "")
	}
}

// Destroy drops the home key only on the runtime's proof that the home is gone. "Destroy
// returned no leftovers" is not one (ecs without the home task removes the access points,
// leaves the EFS directories, and a retry then reports nothing), so a runtime that cannot
// prove it keeps the key: a dropped key makes a surviving secrets.enc unreadable for good.
func TestDestroyKeepsHomeDEKUnlessProvenGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		verdict   runtime.HomeVerdict
		scheme    string
		leftovers []string
		destroyOK bool
		wantKept  bool
	}{
		{"unknown, no leftovers", runtime.HomeUnknown, store.HomeDEKRandom, nil, true, true},
		{"unknown, leftovers", runtime.HomeUnknown, store.HomeDEKRandom, []string{"efs:fs-1/home/M-1"}, true, true},
		{"present", runtime.HomePresent, store.HomeDEKRandom, nil, true, true},
		{"proven gone", runtime.HomeGone, store.HomeDEKRandom, nil, true, false},
		{"proven gone, migrating", runtime.HomeGone, store.HomeDEKMigrating, nil, true, true},
		{"proven gone but destroy failed", runtime.HomeGone, store.HomeDEKRandom, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &destroyingFactory{leftovers: tc.leftovers, verdict: tc.verdict}
			if !tc.destroyOK {
				f.err = errors.New("boom")
			}
			st, mgr, victim, tn := destroyFixture(t, f)
			memID := membershipIDOf(t, st, victim, tn)
			seedHomeDEK(t, st, memID, tn.ID, tc.scheme)
			_, err := mgr.destroyWorkspaceByMembership(ctx, memID)
			if (err == nil) != tc.destroyOK {
				t.Fatalf("destroy err = %v", err)
			}
			_, ok, gerr := st.GetHomeDEK(ctx, memID)
			if gerr != nil || ok != tc.wantKept {
				t.Fatalf("home key kept = %v (err %v), want %v", ok, gerr, tc.wantKept)
			}
		})
	}
}

// seedHomeDEK stores a key row in the given scheme.
func seedHomeDEK(t *testing.T, st *store.SQL, memID, tenantID, scheme string) store.HomeDEK {
	t.Helper()
	ctx := context.Background()
	d, err := st.InsertHomeDEK(ctx, store.HomeDEK{MembershipID: memID, Ciphertext: "sealed", KeyRef: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	if scheme == store.HomeDEKRandom {
		if ok, err := st.ConfirmHomeDEK(ctx, d); err != nil || !ok {
			t.Fatalf("confirm = %v, %v", ok, err)
		}
	}
	return d
}

// A stale decision never deletes a key that changed under it: the proof is taken, then a
// workspace row is made for the membership again, or the key is put back to migrating.
func TestDestroyedHomeDEKSurvivesAChangeDuringTheProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("home re-created", func(t *testing.T) {
		f := &destroyingFactory{verdict: runtime.HomeGone}
		st, mgr, victim, tn := destroyFixture(t, f)
		memID := membershipIDOf(t, st, victim, tn)
		seedHomeDEK(t, st, memID, tn.ID, store.HomeDEKRandom)
		f.between = func() {
			recreateWorkspace(t, st, store.Workspace{TenantID: tn.ID, MembershipID: memID}, "W-NEW")
		}
		if _, err := mgr.destroyWorkspaceByMembership(ctx, memID); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.GetHomeDEK(ctx, memID); !ok {
			t.Fatal("the key of a home that has a workspace again was deleted")
		}
	})
	t.Run("remigrated", func(t *testing.T) {
		f := &destroyingFactory{verdict: runtime.HomeGone}
		st, mgr, victim, tn := destroyFixture(t, f)
		memID := membershipIDOf(t, st, victim, tn)
		seedHomeDEK(t, st, memID, tn.ID, store.HomeDEKRandom)
		f.between = func() {
			if ok, err := st.RemigrateHomeDEK(ctx, memID); err != nil || !ok {
				t.Fatalf("remigrate = %v, %v", ok, err)
			}
		}
		if _, err := mgr.destroyWorkspaceByMembership(ctx, memID); err != nil {
			t.Fatal(err)
		}
		if d, ok, _ := st.GetHomeDEK(ctx, memID); !ok || d.Scheme != store.HomeDEKMigrating {
			t.Fatalf("a key put back to migrating during the proof was deleted (ok=%v)", ok)
		}
	})
}

// The destroys that run as a recorded home operation (the home task adapters) drop a proven
// key too: by the starter that finishes the record, and by the reconciler of a replaced CP.
func TestRecordedDestroyDropsProvenHomeDEK(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, via := range []string{"starter", "reconciler"} {
		for _, verdict := range []runtime.HomeVerdict{runtime.HomeGone, runtime.HomeUnknown} {
			t.Run(via+"/"+verdict.String(), func(t *testing.T) {
				rt := newRecordedHomeRuntime("stopped")
				rt.verdict = verdict
				close(rt.gate)
				st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
				memID := membershipIDOf(t, st, victim, tn)
				seedHomeDEK(t, st, memID, tn.ID, store.HomeDEKRandom)
				if via == "starter" {
					if _, err := mgr.destroyWorkspaceByMembership(ctx, memID); err != nil {
						t.Fatal(err)
					}
				} else {
					ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
					openRecord(t, st, ws, store.HomeOpDestroy, "destroy", nil, "")
					reconcileNow(mgr)
					reconcileNow(mgr)
				}
				if _, ok, _ := st.GetWorkspaceByMembership(ctx, memID); ok {
					t.Fatal("precondition: the destroy did not finish")
				}
				_, kept, _ := st.GetHomeDEK(ctx, memID)
				if kept != (verdict != runtime.HomeGone) {
					t.Fatalf("home key kept = %v with verdict %s", kept, verdict)
				}
			})
		}
	}
}

// A destroy whose lifecycle lease is gone (lost to a takeover, or expired) keeps the key: its
// proof may describe a home an earlier generation had, and another CP may have re-created it.
func TestReleaseHomeDEKNeedsTheLiveLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		mutate   func(*workspaceLifecycleLeaseGuard)
		wantKept bool
	}{
		{"held", func(*workspaceLifecycleLeaseGuard) {}, false},
		{"marked lost", func(g *workspaceLifecycleLeaseGuard) { g.lost.Store(true) }, true},
		{"taken over", func(g *workspaceLifecycleLeaseGuard) {
			g.token = "someone-elses-token" // what the guard holds is no longer the stored lease
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &destroyingFactory{verdict: runtime.HomeGone}
			st, mgr, victim, tn := destroyFixture(t, f)
			memID := membershipIDOf(t, st, victim, tn)
			ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
			seedHomeDEK(t, st, memID, tn.ID, store.HomeDEKRandom)
			if err := st.DeleteWorkspace(ctx, ws.ID); err != nil {
				t.Fatal(err)
			}
			lease, err := acquireWorkspaceLifecycleLease(ctx, st, memID)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			tc.mutate(lease)
			mgr.releaseHomeDEK(ctx, memID, f.New(runtime.Workspace(ws), runtime.SecretKeys{}, nil), lease)
			if _, ok, _ := st.GetHomeDEK(ctx, memID); ok != tc.wantKept {
				t.Fatalf("key kept = %v, want %v", ok, tc.wantKept)
			}
		})
	}
}

func TestHomeDEKOrphanCLI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	live := homeDEKWorkspace(t, st, "W7")
	gone := homeDEKWorkspace(t, st, "W8")
	seedHomeDEK(t, st, live.MembershipID, live.TenantID, store.HomeDEKRandom)
	seedHomeDEK(t, st, gone.MembershipID, gone.TenantID, store.HomeDEKMigrating)
	if err := st.DeleteWorkspace(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := printOrphanHomeDEKs(ctx, st, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), gone.MembershipID) || strings.Contains(b.String(), live.MembershipID) ||
		strings.Contains(b.String(), "sealed") {
		t.Fatalf("orphan listing:\n%s", b.String())
	}
	b.Reset()
	if code := removeOrphanHomeDEK(ctx, st, gone.MembershipID, false, &b); code != 1 {
		t.Fatalf("without --force: exit %d", code)
	}
	if _, ok, _ := st.GetHomeDEK(ctx, gone.MembershipID); !ok {
		t.Fatal("removed without --force")
	}
	if code := removeOrphanHomeDEK(ctx, st, live.MembershipID, true, &b); code != 1 {
		t.Fatalf("a key a workspace names: exit %d", code)
	}
	if _, ok, _ := st.GetHomeDEK(ctx, live.MembershipID); !ok {
		t.Fatal("--force removed the key of a live workspace")
	}
	if code := removeOrphanHomeDEK(ctx, st, gone.MembershipID, true, &b); code != 0 {
		t.Fatalf("orphan with --force: exit %d", code)
	}
	if _, ok, _ := st.GetHomeDEK(ctx, gone.MembershipID); ok {
		t.Fatal("the orphan survived --force")
	}
}

func TestPrintHomeDEKStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := ssmAPIStores(t)["sqlite"]
	ws := homeDEKWorkspace(t, st, "W5")
	homeDEKWorkspace(t, st, "W6")
	if _, err := st.InsertHomeDEK(ctx, store.HomeDEK{MembershipID: ws.MembershipID, Ciphertext: "sealed", KeyRef: ws.TenantID}); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := printHomeDEKStatus(ctx, st, &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"migrating: 1\n", "confirmed: 0\n", "no key of its own yet: 1\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, b.String())
		}
	}
}

// keyRecordingFactory records the keys of every runtime built and of every one started.
type keyRecordingFactory struct {
	endpoint       string // what the built runtimes answer on ("" = nowhere)
	mu             sync.Mutex
	built, started []runtime.SecretKeys
	env            [][]string // the extraEnv of each runtime built
}

type keyRecordingRuntime struct {
	stubRuntime
	f    *keyRecordingFactory
	keys runtime.SecretKeys
}

// lastStartNonce is the AF_HOME_KEY_START of the last runtime built, "" if none.
func (f *keyRecordingFactory) lastStartNonce() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.env) - 1; i >= 0; i-- {
		for _, kv := range f.env[i] {
			if v, ok := strings.CutPrefix(kv, homeKeyStartEnv+"="); ok {
				return v
			}
		}
	}
	return ""
}

func (r *keyRecordingRuntime) Start(context.Context) error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	r.f.started = append(r.f.started, r.keys)
	return nil
}

func (f *keyRecordingFactory) New(_ runtime.Workspace, keys runtime.SecretKeys, env []string) runtime.Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built = append(f.built, keys)
	f.env = append(f.env, env)
	return &keyRecordingRuntime{stubRuntime: stubRuntime{state: "stopped", endpoint: f.endpoint}, f: f, keys: keys}
}

// The memoized runtime holds the keys it was built with. Every real start resolves them
// again, so a KMS key disabled since the memo was built stops the start instead of the memo
// starting the workspace on keys the custodian would no longer open.
func TestStartResolvesKeysEveryTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, preview := range []string{"", "preview.example"} {
		f := &keyRecordingFactory{}
		st, mgr, victim, tn := destroyFixture(t, f)
		memID := membershipIDOf(t, st, victim, tn)
		mgr.previewDomain = preview
		mgr.master32 = testMaster(t)
		kf := newFakeKMS(t)
		mgr.custodian = newKMSCustodian(kf, kf.keyID, newLocalCustodian(mgr.master32), 0)
		mgr.homeDEKRandom = true
		mv, _, _ := st.GetMembershipByID(ctx, memID)
		res, aerr := mgr.buildResolved(ctx, victim, mv)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if len(f.built) == 0 || f.built[0].Next == "" {
			t.Fatalf("precondition: the memo was built without the home key")
		}
		kf.err = errors.New("DisabledException")
		res, aerr = mgr.buildResolved(ctx, victim, mv) // the memo, no custodian call
		if aerr != nil {
			t.Fatalf("precondition: the memo should answer without KMS: %v", aerr)
		}
		if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(ctx, res); aerr == nil {
			t.Fatalf("preview %q: started with KMS refusing the keys", preview)
		}
		if len(f.started) != 0 {
			t.Fatalf("preview %q: a runtime was started (%d)", preview, len(f.started))
		}
		kf.err = nil
		if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(ctx, res); aerr != nil {
			t.Fatalf("preview %q: start with KMS back: %v", preview, aerr)
		}
		if len(f.started) != 1 || f.started[0] != f.built[0] {
			t.Fatalf("preview %q: started keys differ from the home's keys", preview)
		}
	}
}
