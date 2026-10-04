package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// HomeLeaseStore is what the ecs-ec2 adapter needs from the CP's database to serialise one
// workspace's home across Control Plane replicas (#1601). The process-local locks below
// (homeMountLocks, claimGenLocks) and the Start count (startGen) serialise one CP only: a
// release on one replica and a Start on another met on the same slot, and a release did
// not see a Start made elsewhere. The store's cp_lease and cp_counter tables give every
// replica the same lock and the same count. The CP's store satisfies it; nil keeps the
// process-local behaviour (a single CP, and this package's tests that do not ask for it).
type HomeLeaseStore interface {
	AcquireCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	RenewCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	ReleaseCPLease(ctx context.Context, name, holder string) error
	BumpCPCounter(ctx context.Context, name string) (int64, error)
	CPCounter(ctx context.Context, name string) (int64, error)
}

// The two per-workspace locks. homeLockMount keeps a mount of the home out of a release's
// umount→detach window; homeLockClaim keeps a Start's count increment out of a failed
// launch's last check of that count through its claim delete (unclaimIfOurs).
const (
	homeLockMount = "ec2-home-mount/"
	homeLockClaim = "ec2-home-claim/"
	// homeStartGen names the Start count in cp_counter.
	homeStartGen = "ec2-start-gen/"
)

// homeLeaseTTL is how long a lease outlives its last confirmed renewal: a CP that dies
// holding it blocks the workspace's mounts and releases for at most this long. Renewed
// every third of it while the work under it runs (an SSM umount and a detach take seconds,
// a mount waits for its device for longer).
const homeLeaseTTL = 90 * time.Second

// homeLeasePoll is how often a lock taken elsewhere is asked for again.
const homeLeasePoll = time.Second

// homeLeaseReleaseTimeout bounds giving a lease back. A release that does not arrive only
// makes the next holder wait out the expiry.
const homeLeaseReleaseTimeout = 10 * time.Second

// errHomeLockLost is the work under a home lock cut off because the lock could not be
// taken, or its lease was lost to another CP. Like errHomeLeftSlot it says nothing about
// the slot, so the caller must not quarantine for it.
var errHomeLockLost = errors.New("the home's lock could not be held")

// homeLeaseHolders makes each acquisition's holder unique, so a renewal still in flight
// for one acquisition can never extend the next one's.
var homeLeaseHolders atomic.Int64

// homeLeaseProcess names this process in the holder: a CP that restarts is a new holder.
var homeLeaseProcess = fmt.Sprintf("%d-%x", time.Now().UnixNano(), rand.Uint32())

// localKey keys the process-local maps. replica is empty in production; a test that
// stands two CPs up in one process gives each its own, so they share nothing but the store.
func (e *ecsEC2Runtime) localKey() string { return e.replica + e.base.name }

func localMutex(m *sync.Map, key string) *sync.Mutex {
	v, _ := m.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// lockHome takes one of the workspace's two home locks (homeLockMount, homeLockClaim): the
// process-local mutex, then, with a store, the lease every replica asks for. It waits for a
// lease another CP holds, until ctx ends.
//
// The work under the lock runs on the returned context, which ends with errHomeLockLost as
// its cause once the lease can no longer be shown to be this process's: a renewal answered
// that another holder has it, or the deadline the last confirmed renewal set has passed.
// That deadline is counted from when the renewal was sent, so it is never later than the
// database's.
func (e *ecsEC2Runtime) lockHome(ctx context.Context, which string) (context.Context, func(), error) {
	mu := localMutex(lockMap(which), e.localKey())
	mu.Lock()
	if e.leases == nil {
		return ctx, mu.Unlock, nil
	}
	name := which + e.base.name
	holder := fmt.Sprintf("%s-%d", homeLeaseProcess, homeLeaseHolders.Add(1))
	ttl := e.homeLeaseTTL()
	var sent time.Time
	logged := false
	for {
		sent = time.Now()
		ok, err := e.leases.AcquireCPLease(ctx, name, holder, ttl)
		if err == nil && ok {
			break
		}
		if err != nil && !logged {
			log.Printf("ecs-ec2: taking the lease %s: %v; retrying", name, err)
			logged = true
		}
		if werr := waitCtx(ctx, e.homeLeasePoll()); werr != nil {
			mu.Unlock()
			return nil, nil, fmt.Errorf("%w: %s: %w", errHomeLockLost, name, werr)
		}
	}
	lctx, cancel := context.WithCancelCause(ctx)
	expiry := time.AfterFunc(time.Until(sent.Add(ttl)), func() { cancel(errHomeLockLost) })
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		held := sent.Add(ttl)
		t := time.NewTicker(ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-lctx.Done():
				return
			case <-t.C:
			}
			at := time.Now()
			rctx, rcancel := context.WithDeadline(lctx, held)
			ok, err := e.leases.RenewCPLease(rctx, name, holder, ttl)
			rcancel()
			switch {
			case err != nil:
				// Nothing confirmed; the expiry timer decides.
			case !ok:
				log.Printf("ecs-ec2: the lease %s went to another Control Plane mid-operation", name)
				cancel(errHomeLockLost)
				return
			default:
				held = at.Add(ttl)
				expiry.Reset(time.Until(held))
			}
		}
	}()
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			close(done)
			<-renewed
			expiry.Stop()
			cancel(nil)
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), homeLeaseReleaseTimeout)
			if err := e.leases.ReleaseCPLease(rctx, name, holder); err != nil {
				log.Printf("ecs-ec2: giving back the lease %s: %v; it expires in %s", name, err, ttl)
			}
			rcancel()
			mu.Unlock()
		})
	}
	return lctx, unlock, nil
}

// lockLost wraps err with errHomeLockLost when the work failed because lctx was cut off by
// the lease, so the caller can tell that from a slot that failed.
func lockLost(lctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(lctx), errHomeLockLost) && !errors.Is(err, errHomeLockLost) {
		return fmt.Errorf("%w: %w", errHomeLockLost, err)
	}
	return err
}

func lockMap(which string) *sync.Map {
	if which == homeLockClaim {
		return &claimGenLocks
	}
	return &homeMountLocks
}

func (e *ecsEC2Runtime) homeLeaseTTL() time.Duration {
	if e.leaseTTL > 0 {
		return e.leaseTTL
	}
	return homeLeaseTTL
}

func (e *ecsEC2Runtime) homeLeasePoll() time.Duration {
	if e.leasePoll > 0 {
		return e.leasePoll
	}
	return homeLeasePoll
}

func waitCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// startGenNow is the workspace's Start count: the store's, which every replica bumps,
// where there is one.
func (e *ecsEC2Runtime) startGenNow(ctx context.Context) (int64, error) {
	if e.leases == nil {
		return e.generation().Load(), nil
	}
	return e.leases.CPCounter(ctx, homeStartGen+e.base.name)
}

// startedSince reports whether a Start has begun since the one counted gen. An unreadable
// count is an error, never "no": every caller only goes on to take the home away on a no.
func (e *ecsEC2Runtime) startedSince(ctx context.Context, gen int64) (bool, error) {
	now, err := e.startGenNow(ctx)
	if err != nil {
		return false, fmt.Errorf("read the start count of %s: %w", e.base.name, err)
	}
	return now != gen, nil
}
