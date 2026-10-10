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

// Destroy does not drop the home key. An adapter's Destroy does not prove the whole home is
// gone (ecs without the home task removes the access points and leaves the EFS directories,
// and a retry after a partial failure reports no leftovers), so a dropped key could make a
// surviving secrets.enc unreadable.
func TestDestroyKeepsHomeDEK(t *testing.T) {
	ctx := context.Background()
	for _, leftovers := range [][]string{nil, {"efs:fs-1/home/M-1"}} {
		f := &destroyingFactory{leftovers: leftovers}
		st, mgr, victim, tn := destroyFixture(t, f)
		memID := membershipIDOf(t, st, victim, tn)
		if _, err := st.InsertHomeDEK(ctx, store.HomeDEK{MembershipID: memID, Ciphertext: "sealed", KeyRef: tn.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.destroyWorkspaceByMembership(ctx, memID); err != nil {
			t.Fatalf("destroy (leftovers %v): %v", leftovers, err)
		}
		if f.destroyed != 1 {
			t.Fatalf("precondition: Destroy ran %d times", f.destroyed)
		}
		if _, ok, err := st.GetHomeDEK(ctx, memID); err != nil || !ok {
			t.Fatalf("destroy (leftovers %v) dropped the home key (ok=%v, err=%v)", leftovers, ok, err)
		}
	}
}

func TestPrintHomeDEKStatus(t *testing.T) {
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
