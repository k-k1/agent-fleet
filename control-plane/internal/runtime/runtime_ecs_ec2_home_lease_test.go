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
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// fakeLeaseStore is cp_lease and cp_counter as the store implements them: one holder per
// name until its expiry, renewals only while live, a counter every caller shares.
type fakeLeaseStore struct {
	mu       sync.Mutex
	leases   map[string]fakeLease
	counters map[string]int64
	// renewals, when set, answers every renewal instead of the table.
	renewals func(ctx context.Context, name, holder string) (bool, error)
	// onAcquire, when set, runs before every acquisition is answered.
	onAcquire func()
	acquires  atomic.Int32
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
	f.acquires.Add(1)
	if f.onAcquire != nil {
		f.onAcquire()
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
		return f.renewals(ctx, name, holder)
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

// waitFree waits until nobody holds name.
func (f *fakeLeaseStore) waitFree(t *testing.T, name string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		f.mu.Lock()
		l, ok := f.leases[name]
		f.mu.Unlock()
		if !ok || !time.Now().Before(l.until) {
			return
		}
	}
	t.Fatalf("%s is still held", name)
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
	// A live lease of another CP, by contrast, holds until the caller gives up. (unlock
	// gives the lease back in the background.)
	st.waitFree(t, homeLockMount+"af-ws-acme-alice")
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
	st.renewals = func(_ context.Context, name, _ string) (bool, error) {
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

// slowSSM keeps every command containing hold in progress until release is closed: a
// command SSM accepted and the slot has not run yet.
type slowSSM struct {
	*fakeSSMCmd
	hold    string
	release chan struct{}
}

func (s *slowSSM) GetCommandInvocation(ctx context.Context, in *ssm.GetCommandInvocationInput, opts ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	if strings.Contains(aws.ToString(in.CommandId), s.hold) {
		select {
		case <-s.release:
		default:
			return &ssm.GetCommandInvocationOutput{Status: ssmtypes.CommandInvocationStatusInProgress}, nil
		}
	}
	return s.fakeSSMCmd.GetCommandInvocation(ctx, in, opts...)
}

// A mount whose caller stopped waiting while its af-mount was still queued on the slot
// keeps the home's lock until that af-mount is seen ending: released at once, another
// replica's release would umount (nothing mounted yet), the queued mount would land, and
// the detach would pull it — the #1592 dead mount.
func TestECSEC2LockOutlivesASlotCommandItsCallerGaveUpOn(t *testing.T) {
	ctx := context.Background()
	h, a, b, _ := twoReplicas(t)
	homeOnSlot(h)
	slow := &slowSSM{fakeSSMCmd: h.ssmc, hold: "af-mount", release: make(chan struct{})}
	a.ssmc = slow
	a.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	mctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	err := a.mountHome(mctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"})
	cancel()
	if err == nil {
		t.Fatal("mountHome returned nil although its af-mount never finished")
	}
	released := make(chan error, 1)
	go func() { released <- b.releaseSlot(ctx) }()
	time.Sleep(150 * time.Millisecond)
	if calls := ec2Calls(h); firstCall(calls, "SSM af-umount") >= 0 || firstCall(calls, "DetachVolume") >= 0 {
		t.Fatalf("B's release ran while A's af-mount was still queued: %q", calls)
	}
	close(slow.release)
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("B's release after A's mount ended: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B's release never ran after A's mount ended")
	}
	calls := ec2Calls(h)
	if mount, umount, detach := firstCall(calls, "SSM af-mount"), firstCall(calls, "SSM af-umount"), firstCall(calls, "DetachVolume"); !(mount < umount && umount < detach) {
		t.Fatalf("want A's mount, then B's umount, then its detach: %q", calls)
	}
}

// A release that resumes past its lease's deadline — before the timer that ends its
// context has fired — sends no DetachVolume: the guard reads the clock itself.
func TestECSEC2ReleaseResumedPastItsLeaseDoesNotDetach(t *testing.T) {
	ctx := context.Background()
	h, a, _, _ := twoReplicas(t)
	homeOnSlot(h)
	var skew atomic.Int64
	a.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(ssmHelperLine(cmd), "af-umount") {
			skew.Store(int64(2 * homeLeaseTTL)) // the pause
		}
	}
	err := a.releaseSlot(ctx)
	if !errors.Is(err, errHomeLockLost) {
		t.Fatalf("releaseSlot = %v, want errHomeLockLost", err)
	}
	if i := firstCall(ec2Calls(h), "DetachVolume"); i >= 0 {
		t.Fatalf("detached after the lease ran out: %q", ec2Calls(h))
	}
}

// An acquisition answered after the lease it granted ran out grants nothing.
func TestECSEC2LateLeaseAnswerIsAskedAgain(t *testing.T) {
	ctx := context.Background()
	h, a, _, st := twoReplicas(t)
	homeOnSlot(h)
	var skew atomic.Int64
	a.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	var once sync.Once
	st.onAcquire = func() { once.Do(func() { skew.Store(int64(2 * homeLeaseTTL)) }) }
	lctx, unlock, err := a.lockHome(ctx, homeLockMount)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if n := st.acquires.Load(); n != 2 {
		t.Errorf("acquisitions = %d, want the late one asked again", n)
	}
	if err := homeGuardOf(lctx).check(); err != nil {
		t.Errorf("the lock taken on the second answer: %v", err)
	}
}

// A mount whose af-mount answers Success after the lease was lost reports the lost lease,
// so the launch does not start a task on a home another holder may be taking away.
func TestECSEC2MountSuccessAfterALostLeaseIsNoSuccess(t *testing.T) {
	ctx := context.Background()
	h, a, _, st := twoReplicas(t)
	homeOnSlot(h)
	a.leaseTTL = 60 * time.Millisecond
	lost := make(chan struct{})
	var lose sync.Once
	st.renewals = func(context.Context, string, string) (bool, error) {
		lose.Do(func() { close(lost) })
		return false, nil
	}
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(ssmHelperLine(cmd), "af-mount") {
			<-lost
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := a.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); !errors.Is(err, errHomeLockLost) {
		t.Fatalf("mountHome = %v, want errHomeLockLost", err)
	}
}

// A waiter for the process-local lock gives up with its context, and unlock does not wait
// for a renewal stuck on the database.
func TestECSEC2HomeLockWaitsAndUnlocksPromptly(t *testing.T) {
	ctx := context.Background()
	h, a, _, st := twoReplicas(t)
	homeOnSlot(h)
	_, unlock, err := a.lockHome(ctx, homeLockMount)
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	waited := make(chan error, 1)
	go func() {
		_, u, err := a.lockHome(wctx, homeLockMount)
		if u != nil {
			u()
		}
		waited <- err
	}()
	select {
	case err := <-waited:
		if !errors.Is(err, errHomeLockLost) {
			t.Errorf("a cancelled waiter = %v, want errHomeLockLost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter for the local lock ignored its cancelled context")
	}
	cancel()
	unlock()
	st.waitFree(t, homeLockMount+"af-ws-acme-alice")

	a.leaseTTL = 3 * time.Second
	stuck := make(chan struct{})
	var once sync.Once
	st.renewals = func(ctx context.Context, _, _ string) (bool, error) {
		once.Do(func() { close(stuck) })
		<-ctx.Done()
		return false, ctx.Err()
	}
	_, unlock, err = a.lockHome(ctx, homeLockMount)
	if err != nil {
		t.Fatal(err)
	}
	<-stuck
	t0 := time.Now()
	unlock()
	st.waitFree(t, homeLockMount+"af-ws-acme-alice")
	if d := time.Since(t0); d > time.Second {
		t.Errorf("the lease was given back %s after unlock; a stuck renewal held it", d)
	}
}

// ctxStopHook fails a StopInstances whose context has ended, as the SDK does.
type ctxStopHook struct {
	ec2API
	stopped atomic.Bool
}

func (c *ctxStopHook) StopInstances(ctx context.Context, in *ec2.StopInstancesInput, opts ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.stopped.Store(true)
	return c.ec2API.StopInstances(ctx, in, opts...)
}

// A quarantine that cannot take the home's lock — another replica holds it, and the
// launch's context has already ended — still stops the box, and leaves the home attached.
func TestECSEC2QuarantineStopsTheSlotWithoutTheLock(t *testing.T) {
	h, a, _, st := twoReplicas(t)
	homeOnSlot(h)
	defer func(d time.Duration) { quarantineLockBudget = d }(quarantineLockBudget)
	quarantineLockBudget = 50 * time.Millisecond
	if ok, _ := st.AcquireCPLease(context.Background(), homeLockMount+"af-ws-acme-alice", "other-cp", time.Hour); !ok {
		t.Fatal("seed lease")
	}
	hook := &ctxStopHook{ec2API: h.ec2}
	a.ec2 = hook
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.quarantineSlot(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}, errors.New("mount failed"))
	if !hook.stopped.Load() {
		t.Error("the quarantined slot was not stopped")
	}
	if i := firstCall(ec2Calls(h), "DetachVolume"); i >= 0 {
		t.Errorf("detached without the lock: %q", ec2Calls(h))
	}
}
