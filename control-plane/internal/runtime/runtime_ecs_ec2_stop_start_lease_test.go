// Lease-loss cases of the stop-while-starting guards: a claim lease that ran out while the
// work was in flight must stop it, however the timer callbacks interleave.
package runtime

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"sync/atomic"
	"testing"
	"time"
)

type claimLostSSM struct {
	ssmCommandAPI
	hook func(context.Context)
}

func (w *claimLostSSM) GetCommandInvocation(ctx context.Context, in *ssm.GetCommandInvocationInput, opt ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	if f := w.hook; f != nil {
		w.hook = nil
		f(ctx)
	}
	return w.ssmCommandAPI.GetCommandInvocation(ctx, in, opt...)
}
func TestECSEC2QuarantineKeepsTheHomeAfterTheClaimLeaseIsLost(t *testing.T) {
	ctx := context.Background()
	h, a, b, st := twoReplicas(t)
	homeOnSlot(h)
	gen, err := a.beginStart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := &claimLostSSM{ssmCommandAPI: h.ssmc}
	a.ssmc = w
	stopped := false
	w.hook = func(c context.Context) {
		g := homeGuardOf(c)
		if g == nil {
			t.Fatal("positive control: command has no claim guard")
		}
		g.markLost()
		st.mu.Lock()
		delete(st.leases, homeLockClaim+a.base.name)
		st.mu.Unlock()
		if err := b.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		stopped = true
		if attachedInstance(h.ec2.volumes["vol-1"]) != "i-hot" {
			t.Fatal("positive control: home was already detached before Stop")
		}
	}
	a.quarantineSlot(ctx, ec2Placement{gen: gen, volumeID: "vol-1", instanceID: "i-hot"}, errors.New("mount failed"))
	if !stopped {
		t.Fatal("positive control: Stop never ran")
	}
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-hot" {
		t.Errorf("quarantine detached after claim lease lost and another replica Stop returned: %q", got)
	}
}

type countHookStore struct {
	HomeLeaseStore
	hook func(context.Context)
}

func (w *countHookStore) CPCounter(ctx context.Context, name string) (int64, error) {
	n, err := w.HomeLeaseStore.CPCounter(ctx, name)
	if f := w.hook; f != nil {
		w.hook = nil
		f(ctx)
	}
	return n, err
}
func TestECSEC2UnderStartDoesNotRaiseAfterItsLeaseExpired(t *testing.T) {
	ctx := context.Background()
	h, a, b, st := twoReplicas(t)
	homeOnSlot(h)
	var skew atomic.Int64
	a.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	w := &countHookStore{HomeLeaseStore: st}
	a.leases = w
	gen, err := a.beginStart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	w.hook = func(c context.Context) {
		skew.Store(int64(2 * homeLeaseTTL))
		if !errors.Is(homeGuardOf(c).check(), errHomeLockLost) {
			t.Fatal("positive control: lease did not expire")
		}
		st.mu.Lock()
		delete(st.leases, homeLockClaim+a.base.name)
		st.mu.Unlock()
		if err := b.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		stopped = true
	}
	err = a.underStart(ctx, ec2Placement{gen: gen, volumeID: "vol-1", instanceID: "i-hot"}, a.scaleUpService)
	if !stopped {
		t.Fatal("positive control: Stop never ran")
	}
	if got := h.ecs.services[a.base.name].DesiredCount; got != 0 {
		t.Errorf("old Start raised desiredCount after its lease expired and Stop returned: %d (error %v)", got, err)
	}
}

// ifGenCurrent reads the count under a claim lease; a lease that ran out while the answer
// was in flight must not let a stale phase write through after another replica's Stop.
func TestECSEC2GenCurrentDoesNotWriteAfterItsLeaseExpired(t *testing.T) {
	ctx := context.Background()
	h, a, b, st := twoReplicas(t)
	homeOnSlot(h)
	var skew atomic.Int64
	a.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	w := &countHookStore{HomeLeaseStore: st}
	a.leases = w
	gen, err := a.startGenNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.setPhase("")
	stopped := false
	w.hook = func(c context.Context) {
		skew.Store(int64(2 * homeLeaseTTL))
		if !errors.Is(homeGuardOf(c).check(), errHomeLockLost) {
			t.Fatal("positive control: lease did not expire")
		}
		st.mu.Lock()
		delete(st.leases, homeLockClaim+a.base.name)
		st.mu.Unlock()
		if err := b.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		stopped = true
	}
	ran := a.ifGenCurrent(ctx, gen, func() { a.setPhase(blockedPhasePrefix + "stale") })
	if !stopped {
		t.Fatal("positive control: Stop never ran")
	}
	if ran || a.BootPhase() != "" {
		t.Errorf("a stale diagnosis wrote after its lease expired and Stop returned: ran=%v phase=%q", ran, a.BootPhase())
	}
}
