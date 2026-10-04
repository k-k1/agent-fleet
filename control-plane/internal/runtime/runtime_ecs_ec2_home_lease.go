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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
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

// homeSem is a process-local lock a waiter can give up on: a channel of one.
type homeSem chan struct{}

func localSem(m *sync.Map, key string) homeSem {
	v, _ := m.LoadOrStore(key, make(homeSem, 1))
	return v.(homeSem)
}

// homeSettleBudget bounds how long a lock whose work left a slot command running is kept
// after its caller gave up, waiting to see that command end.
const homeSettleBudget = 5 * time.Minute

// homeLockGuard is one hold of a home lock as the work under it sees it. Every
// irreversible step asks check() right before it is sent — a DetachVolume, a slot command,
// a claim delete — because the lock's context is ended by a timer, and a goroutine resumed
// from a pause past the deadline can run before that timer's callback has. It also records
// the slot commands sent under the lock that have not been seen to end: the lock is not
// given up while one of them may still run on the slot.
type homeLockGuard struct {
	e     *ecsEC2Runtime
	store bool // false: no lease, only the process-local lock

	mu   sync.Mutex
	held time.Time // the lease's deadline by this process's count (store only)
	lost bool
	// pending holds the slot commands sent under the lock and not yet seen ending:
	// command id -> instance id. unknown is a SendCommand whose answer was lost, which
	// may have been accepted under an id nobody knows.
	pending map[string]string
	unknown bool
}

type homeLockKey struct{}

// homeGuardOf is the guard of the home lock ctx runs under, or nil outside one.
func homeGuardOf(ctx context.Context) *homeLockGuard {
	g, _ := ctx.Value(homeLockKey{}).(*homeLockGuard)
	return g
}

// check answers errHomeLockLost once the lock can no longer be shown to be this process's.
// nil-safe: outside a lock there is nothing to check.
func (g *homeLockGuard) check() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lost || (g.store && !g.e.now().Before(g.held)) {
		return errHomeLockLost
	}
	return nil
}

// outcome is err as the caller reports it: a lock lost during the work is the answer
// even when the work itself answered success, since that success may have landed after
// another holder took over.
func (g *homeLockGuard) outcome(err error) error {
	if lost := g.check(); lost != nil && !errors.Is(err, errHomeLockLost) {
		if err == nil {
			return lost
		}
		return fmt.Errorf("%w: %w", errHomeLockLost, err)
	}
	return err
}

func (g *homeLockGuard) markLost() {
	g.mu.Lock()
	g.lost = true
	g.mu.Unlock()
}

// extend moves the deadline to d, unless the lock is already lost: a renewal answered
// after the local deadline passed does not bring it back.
func (g *homeLockGuard) extend(d time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lost || !g.e.now().Before(g.held) || !d.After(g.held) {
		return false
	}
	g.held = d
	return true
}

func (g *homeLockGuard) deadline() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

func (g *homeLockGuard) sent(cmdID, instanceID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending[cmdID] = instanceID
	g.mu.Unlock()
}

func (g *homeLockGuard) sendUnknown() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.unknown = true
	g.mu.Unlock()
}

func (g *homeLockGuard) ended(cmdID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.pending, cmdID)
	g.mu.Unlock()
}

// open lists the commands still pending; unknown says one may exist that cannot be asked.
func (g *homeLockGuard) open() (cmds map[string]string, unknown bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cmds = make(map[string]string, len(g.pending))
	for k, v := range g.pending {
		cmds[k] = v
	}
	return cmds, g.unknown
}

// lockHome takes one of the workspace's two home locks (homeLockMount, homeLockClaim): the
// process-local lock, then, with a store, the lease every replica asks for. It waits for
// either until ctx ends, and then answers errHomeLockLost.
//
// The work under the lock runs on the returned context, which carries the lock's guard
// (homeLockGuard) and ends with errHomeLockLost as its cause once the lease can no longer
// be shown to be this process's: a renewal answered that another holder has it, or the
// deadline the last confirmed renewal set has passed. That deadline is counted from when
// the request was sent, so it is never later than the database's.
//
// unlock returns at once. A slot command the work sent and did not see end (its caller
// gave up waiting) keeps the lock — renewed, for at most homeSettleBudget — until it is
// seen ending, and one that cannot be asked about keeps the lease until it expires: giving
// the lock up earlier lets another holder umount and detach while that command can still
// mount.
func (e *ecsEC2Runtime) lockHome(ctx context.Context, which string) (context.Context, func(), error) {
	sem := localSem(lockMap(which), e.localKey())
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("%w: %s%s: %w", errHomeLockLost, which, e.base.name, ctx.Err())
	}
	g := &homeLockGuard{e: e, store: e.leases != nil, pending: map[string]string{}}
	if e.leases == nil {
		lctx, cancel := context.WithCancel(context.WithValue(ctx, homeLockKey{}, g))
		var once sync.Once
		return lctx, func() {
			once.Do(func() {
				cancel()
				go func() {
					e.settleSlotCommands(g, nil)
					<-sem
				}()
			})
		}, nil
	}
	name := which + e.base.name
	holder := fmt.Sprintf("%s-%d", homeLeaseProcess, homeLeaseHolders.Add(1))
	ttl := e.homeLeaseTTL()
	logged := false
	for {
		sent := e.now()
		ok, err := e.leases.AcquireCPLease(ctx, name, holder, ttl)
		if err == nil && ok {
			// An answer that arrives after the lease it granted has run out by this
			// process's count grants nothing; asking again under the same holder does.
			if e.now().Before(sent.Add(ttl)) {
				g.held = sent.Add(ttl)
				break
			}
			continue
		}
		if err != nil && !logged {
			log.Printf("ecs-ec2: taking the lease %s: %v; retrying", name, err)
			logged = true
		}
		if werr := waitCtx(ctx, e.homeLeasePoll()); werr != nil {
			<-sem
			return nil, nil, fmt.Errorf("%w: %s: %w", errHomeLockLost, name, werr)
		}
	}
	lctx, cancel := context.WithCancelCause(context.WithValue(ctx, homeLockKey{}, g))
	lose := func() {
		g.markLost()
		cancel(errHomeLockLost)
	}
	expiry := time.AfterFunc(g.deadline().Sub(e.now()), lose)
	// Renewals outlive the caller's context: a lock kept to settle a slot command is still
	// renewed. stopRenew ends them, an in-flight one included.
	renewCtx, stopRenew := context.WithCancel(context.WithoutCancel(ctx))
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		t := time.NewTicker(ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-t.C:
			}
			at := e.now()
			rctx, rcancel := context.WithDeadline(renewCtx, g.deadline())
			ok, err := e.leases.RenewCPLease(rctx, name, holder, ttl)
			rcancel()
			switch {
			case err != nil:
				// Nothing confirmed; the deadline decides.
			case !ok:
				log.Printf("ecs-ec2: the lease %s went to another Control Plane mid-operation", name)
				lose()
				return
			default:
				if g.extend(at.Add(ttl)) {
					expiry.Reset(g.deadline().Sub(e.now()))
				}
			}
		}
	}()
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			cancel(nil)
			go func() {
				settled := e.settleSlotCommands(g, renewCtx)
				stopRenew()
				<-renewed
				expiry.Stop()
				if settled && g.check() == nil {
					rctx, rcancel := context.WithTimeout(context.Background(), homeLeaseReleaseTimeout)
					if err := e.leases.ReleaseCPLease(rctx, name, holder); err != nil {
						log.Printf("ecs-ec2: giving back the lease %s: %v; it expires in %s", name, err, ttl)
					}
					rcancel()
				}
				<-sem
			}()
		})
	}
	return lctx, unlock, nil
}

// settleSlotCommands waits, for at most homeSettleBudget, until every slot command sent
// under g has been seen ending, and reports whether that is known. A lease lost meanwhile
// ends the wait: whoever holds it now is no longer kept out by this one.
func (e *ecsEC2Runtime) settleSlotCommands(g *homeLockGuard, alive context.Context) bool {
	cmds, unknown := g.open()
	if unknown {
		log.Printf("ecs-ec2: a slot command for %s may have been sent with its answer lost; its home lock "+
			"is kept until the lease expires", e.base.name)
		return false
	}
	if len(cmds) == 0 {
		return true
	}
	parent := context.Background()
	if alive != nil {
		parent = alive
	}
	ctx, cancel := context.WithTimeout(parent, homeSettleBudget)
	defer cancel()
	for id, instanceID := range cmds {
		for {
			if g.store && g.check() != nil {
				return false
			}
			inv, err := e.ssmc.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
				CommandId: aws.String(id), InstanceId: aws.String(instanceID),
			})
			if err == nil && ssmCommandEnded(inv.Status) {
				g.ended(id)
				break
			}
			if werr := waitCtx(ctx, e.homeLeasePoll()); werr != nil {
				log.Printf("ecs-ec2: slot command %s on %s for %s was not seen ending within %s; its home "+
					"lock is let go", id, instanceID, e.base.name, homeSettleBudget)
				return false
			}
		}
	}
	return true
}

// ssmCommandEnded is a GetCommandInvocation status after which the command runs no more.
func ssmCommandEnded(s ssmtypes.CommandInvocationStatus) bool {
	switch s {
	case ssmtypes.CommandInvocationStatusSuccess, ssmtypes.CommandInvocationStatusFailed,
		ssmtypes.CommandInvocationStatusCancelled, ssmtypes.CommandInvocationStatusTimedOut:
		return true
	}
	return false
}

// ssmSendRefused reports whether a failed SendCommand is known to have sent nothing: SSM
// refused it as the caller's fault (an instance not registered yet, a bad parameter).
func ssmSendRefused(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorFault() == smithy.FaultClient
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
