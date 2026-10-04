package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeLeaseStore is cp_lease and cp_counter as the store implements them: one holder per
// name until its expiry, renewals only while live, a counter every caller shares.
type fakeLeaseStore struct {
	mu       sync.Mutex
	leases   map[string]fakeLease
	counters map[string]int64
	// renewals, when set, answers every renewal instead of the table.
	renewals func(name, holder string) (bool, error)
}

type fakeLease struct {
	holder string
	until  time.Time
}

func newFakeLeaseStore() *fakeLeaseStore {
	return &fakeLeaseStore{leases: map[string]fakeLease{}, counters: map[string]int64{}}
}

func (f *fakeLeaseStore) AcquireCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.leases[name]; ok && l.holder != holder && time.Now().Before(l.until) {
		return false, nil
	}
	f.leases[name] = fakeLease{holder, time.Now().Add(ttl)}
	return true, nil
}

func (f *fakeLeaseStore) RenewCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.renewals != nil {
		return f.renewals(name, holder)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.leases[name]
	if !ok || l.holder != holder || !time.Now().Before(l.until) {
		return false, nil
	}
	f.leases[name] = fakeLease{holder, time.Now().Add(ttl)}
	return true, nil
}

func (f *fakeLeaseStore) ReleaseCPLease(_ context.Context, name, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.leases[name]; ok && l.holder == holder {
		delete(f.leases, name)
	}
	return nil
}

func (f *fakeLeaseStore) BumpCPCounter(ctx context.Context, name string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[name]++
	return f.counters[name], nil
}

func (f *fakeLeaseStore) CPCounter(ctx context.Context, name string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counters[name], nil
}

// twoReplicas stands up two CPs over one fake AWS and one store: each runtime keeps its own
// process-local locks and Start count (replica), so the store is all they share.
func twoReplicas(t *testing.T) (h *ec2Harness, a, b *ecsEC2Runtime, st *fakeLeaseStore) {
	t.Helper()
	h = newEC2Harness(t)
	st = newFakeLeaseStore()
	h.rt.leases = st
	h.rt.replica = "A/"
	h.rt.leasePoll = 2 * time.Millisecond
	other := *h.rt
	other.replica = "B/"
	t.Cleanup(func() {
		for _, k := range []string{"A/af-ws-acme-alice", "B/af-ws-acme-alice"} {
			startGen.Delete(k)
			homeMountLocks.Delete(k)
			claimGenLocks.Delete(k)
		}
	})
	return h, h.rt, &other, st
}

// homeOnSlot puts alice's home on a running slot with nothing left running on it.
func homeOnSlot(h *ec2Harness) {
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
}

func ec2Calls(h *ec2Harness) []string {
	h.ec2.mu.Lock()
	defer h.ec2.mu.Unlock()
	return append([]string(nil), h.ec2.calls...)
}

func firstCall(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// The #1592 interleaving across two CPs: a release on replica A is between its umount and
// its detach when a launch on replica B mounts the same home. B must wait for A's detach
// and then find the home gone, exactly as a launch on A itself does.
func TestECSEC2ReplicasKeepAMountOutOfAnotherReplicasRelease(t *testing.T) {
	ctx := context.Background()
	h, a, b, _ := twoReplicas(t)
	homeOnSlot(h)

	var started sync.Once
	mountErr := make(chan error, 1)
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(ssmHelperLine(cmd), "af-umount") {
			started.Do(func() {
				go func() {
					mountErr <- b.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"})
				}()
			})
		}
	}
	// A's first poll of its umount gives B's mount every chance to reach the slot first.
	var waited atomic.Bool
	a.sleep = func(context.Context, time.Duration) error {
		if waited.CompareAndSwap(false, true) {
			for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				h.ssmc.mu.Lock()
				n := len(h.ssmc.commands)
				h.ssmc.mu.Unlock()
				if n > 1 {
					break
				}
			}
		}
		return nil
	}

	if err := a.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot on A: %v", err)
	}
	var err error
	select {
	case err = <-mountErr:
	case <-time.After(5 * time.Second):
		t.Fatal("B's mount never finished")
	}
	calls := ec2Calls(h)
	umount, detach, mount := firstCall(calls, "SSM af-umount"), firstCall(calls, "DetachVolume"), firstCall(calls, "SSM af-mount")
	if mount >= 0 && mount < detach {
		t.Fatalf("B's mount landed between A's umount and its detach: %q", calls)
	}
	if umount < 0 || detach < umount {
		t.Fatalf("umount=%d detach=%d in %q", umount, detach, calls)
	}
	if !errors.Is(err, errHomeLeftSlot) {
		t.Errorf("B's mount = %v, want errHomeLeftSlot", err)
	}
}

// A Start counted on replica B while replica A releases the slot: A must see it (the count
// is the store's) and re-mount instead of detaching the home B's workspace is coming up on.
func TestECSEC2ReleaseSeesAStartOnAnotherReplica(t *testing.T) {
	ctx := context.Background()
	h, a, b, _ := twoReplicas(t)
	homeOnSlot(h)

	var once sync.Once
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(ssmHelperLine(cmd), "af-umount") {
			once.Do(func() {
				if _, err := b.beginStart(ctx); err != nil {
					t.Errorf("beginStart on B: %v", err)
				}
			})
		}
	}
	if err := a.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot on A: %v", err)
	}
	calls := ec2Calls(h)
	if i := firstCall(calls, "DetachVolume"); i >= 0 {
		t.Fatalf("A detached the home of a workspace B had started: %q", calls)
	}
	if firstCall(calls, "SSM af-mount") < 0 {
		t.Errorf("A did not re-mount the home for the Start: %q", calls)
	}
}

// A lease a dead CP left behind holds the home until it expires, and no longer.
func TestECSEC2HomeLeaseOfADeadReplicaExpires(t *testing.T) {
	ctx := context.Background()
	h, a, _, st := twoReplicas(t)
	homeOnSlot(h)
	const left = 150 * time.Millisecond
	if ok, _ := st.AcquireCPLease(ctx, homeLockMount+"af-ws-acme-alice", "dead-cp", left); !ok {
		t.Fatal("seed lease")
	}
	t0 := time.Now()
	if err := a.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); err != nil {
		t.Fatalf("mountHome: %v", err)
	}
	if d := time.Since(t0); d < left-20*time.Millisecond {
		t.Errorf("mounted after %s, inside the dead CP's lease (%s)", d, left)
	}
	if firstCall(ec2Calls(h), "SSM af-mount") < 0 {
		t.Errorf("never mounted after the lease expired")
	}
	// A live lease of another CP, by contrast, holds until the caller gives up.
	if ok, _ := st.AcquireCPLease(ctx, homeLockMount+"af-ws-acme-alice", "live-cp", time.Hour); !ok {
		t.Fatal("seed live lease")
	}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := a.mountHome(cctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); !errors.Is(err, errHomeLockLost) {
		t.Errorf("mountHome under a live foreign lease = %v, want errHomeLockLost", err)
	}
}

// A release whose lease goes to another CP between its umount and its detach does not
// detach: the new holder may be mounting.
func TestECSEC2ReleaseStopsWhenItsLeaseIsLost(t *testing.T) {
	ctx := context.Background()
	h, a, _, st := twoReplicas(t)
	homeOnSlot(h)
	a.leaseTTL = 60 * time.Millisecond
	lost := make(chan struct{})
	var lose sync.Once
	st.renewals = func(name, _ string) (bool, error) {
		if strings.HasPrefix(name, homeLockMount) {
			lose.Do(func() { close(lost) })
			return false, nil
		}
		return true, nil
	}
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(ssmHelperLine(cmd), "af-umount") {
			select {
			case <-lost:
			case <-time.After(2 * time.Second):
				t.Error("no renewal was asked for")
			}
			time.Sleep(10 * time.Millisecond) // the cancellation lands
		}
	}
	err := a.releaseSlot(ctx)
	if !errors.Is(err, errHomeLockLost) {
		t.Fatalf("releaseSlot = %v, want it cut off by the lost lease", err)
	}
	if i := firstCall(ec2Calls(h), "DetachVolume"); i >= 0 {
		t.Fatalf("detached after the lease was lost: %q", ec2Calls(h))
	}
}

// A failed launch on A drops its claim while a Start on B begins: B's count increment
// waits for A's delete, so B's claim is the one left. The cross-replica form of
// TestECSEC2FailedLaunchKeepsALaterStartsClaim's "a Start began during the delete".
func TestECSEC2FailedLaunchKeepsAnotherReplicasClaim(t *testing.T) {
	ctx := context.Background()
	h, a, b, _ := twoReplicas(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.setTag("vol-1", EC2TagClaim, "i-new1")
	h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
	gen, err := a.beginStart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := ec2Placement{volumeID: "vol-1", instanceID: "i-new1", gen: gen}
	started := make(chan struct{})
	var once sync.Once
	a.ec2 = &deleteTagsHook{ec2API: h.ec2, before: func() {
		once.Do(func() {
			go func() {
				defer close(started)
				if _, err := b.beginStart(ctx); err != nil {
					t.Errorf("beginStart on B: %v", err)
				}
				h.ec2.mu.Lock()
				h.ec2.setTag("vol-1", EC2TagClaim, "i-new1")
				h.ec2.mu.Unlock()
			}()
			select {
			case <-started:
			case <-time.After(200 * time.Millisecond):
			}
		})
	}}
	a.unclaimIfOurs(ctx, p)
	<-started
	h.ec2.mu.Lock()
	got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim)
	h.ec2.mu.Unlock()
	if got != "i-new1" {
		t.Errorf("claim = %q, want B's claim written after A's delete", got)
	}
	// And a Start B counted before the delete keeps the claim outright.
	if _, err := b.beginStart(ctx); err != nil {
		t.Fatal(err)
	}
	a.ec2 = h.ec2
	a.unclaimIfOurs(ctx, p)
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
		t.Errorf("claim = %q, want it kept for B's later Start", got)
	}
}
