package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeAgentHealthz answers /healthz like the Agent: 503 for the first `down` calls (still
// booting), then the report.
func fakeAgentHealthz(t *testing.T, down int32, report map[string]any) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) <= down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(report)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// homeDEKFixture is a workspace with a migrating home key, under a fake KMS.
func homeDEKFixture(t *testing.T, f runtime.RuntimeFactory) (*store.SQL, *manager, store.Identity, store.MembershipView, string) {
	t.Helper()
	ctx := context.Background()
	st, mgr, victim, tn := destroyFixture(t, f)
	memID := membershipIDOf(t, st, victim, tn)
	mgr.master32 = testMaster(t)
	kf := newFakeKMS(t)
	mgr.custodian = newKMSCustodian(kf, kf.keyID, newLocalCustodian(mgr.master32), 0)
	mgr.homeDEKRandom = true
	mgr.homeDEKConfirmBudget, mgr.homeDEKConfirmPoll = 5*time.Second, 10*time.Millisecond
	mv, _, _ := st.GetMembershipByID(ctx, memID)
	return st, mgr, victim, mv, memID
}

func homeScheme(t *testing.T, st *store.SQL, memID string) string {
	t.Helper()
	d, ok, err := st.GetHomeDEK(context.Background(), memID)
	if err != nil || !ok {
		t.Fatalf("home key row: ok=%v err=%v", ok, err)
	}
	return d.Scheme
}

func TestApplyHomeDEKReport(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		report  agentKeyReport
		confirm bool
	}{
		{"migrated with next", agentKeyReport{State: agentKeyMigrated, Next: true}, true},
		{"current with next", agentKeyReport{State: agentKeyCurrent, Next: true}, true},
		{"no store with next", agentKeyReport{State: agentKeyNone, Next: true}, true},
		// "current" without NEXT in use only says the store opens with AF_SECRET_KEY.
		{"current without next", agentKeyReport{State: agentKeyCurrent}, false},
		{"unreadable", agentKeyReport{State: agentKeyUnreadable, Next: true}, false},
		{"re-seal failed", agentKeyReport{State: agentKeyDerived, Next: true}, false},
		{"old agent", agentKeyReport{}, false},
		{"unknown", agentKeyReport{State: "later", Next: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, mgr, _, _, memID := homeDEKFixture(t, &keyRecordingFactory{})
			ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
			if _, err := mgr.resolveDEK(ctx, ws, "leaver-acme-co-jp"); err != nil {
				t.Fatal(err)
			}
			d, _, _ := st.GetHomeDEK(ctx, memID)
			mgr.applyHomeDEKReport(ctx, d, tc.report)
			want := store.HomeDEKMigrating
			if tc.confirm {
				want = store.HomeDEKRandom
			}
			if got := homeScheme(t, st, memID); got != want {
				t.Fatalf("scheme = %q, want %q", got, want)
			}
		})
	}
}

// A report about a key that is not the stored one never confirms the row.
func TestConfirmHomeDEKIsConditionedOnTheKey(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, memID := homeDEKFixture(t, &keyRecordingFactory{})
	ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
	if _, err := mgr.resolveDEK(ctx, ws, "leaver-acme-co-jp"); err != nil {
		t.Fatal(err)
	}
	d, _, _ := st.GetHomeDEK(ctx, memID)
	stale := d
	stale.Ciphertext = "kms1:another"
	mgr.applyHomeDEKReport(ctx, stale, agentKeyReport{State: agentKeyMigrated, Next: true})
	if got := homeScheme(t, st, memID); got != store.HomeDEKMigrating {
		t.Fatalf("a report about another key confirmed the home (%s)", got)
	}
}

// The whole step: a start of a migrating home injects both keys, the CP waits out the
// Agent's boot, confirms on its report, and the next start hands out the home's key alone.
func TestStartConfirmsHomeDEKAndThenInjectsItAlone(t *testing.T) {
	ctx := context.Background()
	srv, calls := fakeAgentHealthz(t, 3, map[string]any{"ok": true, "secrets_key": "migrated", "secrets_key_next": true})
	f := &keyRecordingFactory{endpoint: srv.URL}
	st, mgr, victim, mv, memID := homeDEKFixture(t, f)
	api := newWorkspaceAPI(mgr, false)
	res, aerr := mgr.buildResolved(ctx, victim, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if aerr := api.ensureWorkspaceStarted(ctx, res); aerr != nil {
		t.Fatal(aerr)
	}
	f.mu.Lock()
	first := f.started[0]
	f.mu.Unlock()
	if first.Key == "" || first.Next == "" || first.Key == first.Next {
		t.Fatalf("migrating start keys: Key set=%v Next set=%v", first.Key != "", first.Next != "")
	}
	deadline := time.Now().Add(5 * time.Second)
	for homeScheme(t, st, memID) != store.HomeDEKRandom {
		if time.Now().After(deadline) {
			t.Fatalf("not confirmed after %d healthz calls", calls.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() < 4 {
		t.Fatalf("confirmed after %d calls, before the Agent answered", calls.Load())
	}
	ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
	_ = st.SetWorkspaceState(ctx, ws.ID, "stopped")
	if aerr := api.ensureWorkspaceStarted(ctx, res); aerr != nil {
		t.Fatal(aerr)
	}
	f.mu.Lock()
	second := f.started[len(f.started)-1]
	f.mu.Unlock()
	if second != (runtime.SecretKeys{Key: first.Next}) {
		t.Fatalf("confirmed start: Key is the home key=%v, Next set=%v", second.Key == first.Next, second.Next != "")
	}
}

func TestStartKeepsMigratingOnAnUnreadableReport(t *testing.T) {
	ctx := context.Background()
	srv, calls := fakeAgentHealthz(t, 0, map[string]any{"ok": true, "secrets_key": "unreadable", "secrets_key_next": true})
	f := &keyRecordingFactory{endpoint: srv.URL}
	st, mgr, victim, mv, memID := homeDEKFixture(t, f)
	res, aerr := mgr.buildResolved(ctx, victim, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(ctx, res); aerr != nil {
		t.Fatal(aerr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := homeScheme(t, st, memID); got != store.HomeDEKMigrating {
		t.Fatalf("an unreadable report changed the home to %q", got)
	}
}

// A confirmed home needs neither the derived key nor its wrapped_dek row.
func TestConfirmedHomeStartsWithoutTheDerivedKey(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, memID := homeDEKFixture(t, &keyRecordingFactory{})
	ws, _, _ := st.GetWorkspaceByMembership(ctx, memID)
	first, err := mgr.resolveDEK(ctx, ws, "leaver-acme-co-jp")
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := st.GetHomeDEK(ctx, memID)
	if ok, err := st.ConfirmHomeDEK(ctx, memID, d.Ciphertext); err != nil || !ok {
		t.Fatalf("confirm = %v, %v", ok, err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE wrapped_dek SET ciphertext='kms1:broken' WHERE workspace_id=?`, ws.ID); err != nil {
		t.Fatal(err)
	}
	keys, err := mgr.resolveDEK(ctx, ws, "leaver-acme-co-jp")
	if err != nil || keys != (runtime.SecretKeys{Key: first.Next}) {
		t.Fatalf("confirmed keys: home key alone=%v, err %v", keys == (runtime.SecretKeys{Key: first.Next}), err)
	}

	// --remigrate puts it back: the derived key rides beside it again.
	// (The broken wrapped_dek row goes; the derived key is minted again from the member.)
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM wrapped_dek WHERE workspace_id=?`, ws.ID); err != nil {
		t.Fatal(err)
	}
	var out testingWriter
	if code := remigrateHomeDEK(ctx, st, memID, &out); code != 0 {
		t.Fatalf("remigrate = %d", code)
	}
	if code := remigrateHomeDEK(ctx, st, memID, &out); code != 1 {
		t.Fatalf("second remigrate = %d, want 1", code)
	}
	keys, err = mgr.resolveDEK(ctx, ws, "leaver-acme-co-jp")
	if err != nil || keys.Next != first.Next || keys.Key != first.Key {
		t.Fatalf("remigrated keys: derived+home=%v, err %v", keys.Next == first.Next && keys.Key == first.Key, err)
	}
}

type testingWriter struct{ b []byte }

func (w *testingWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
