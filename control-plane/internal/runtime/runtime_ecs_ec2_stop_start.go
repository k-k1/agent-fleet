package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// errStartSuperseded ends a background Start that a Stop (or a later Start) overtook. The
// work is dropped without cleanup: the claim and the phase are no longer this Start's to
// remove, and the Stop has already cleared them.
var errStartSuperseded = errors.New("start superseded by a newer start or a stop")

// errStartLockUnavailable: the claim lock was never taken, so nothing was checked.
var errStartLockUnavailable = errors.New("claim lock unavailable")

// errStartCountUnknown: the Start count could not be read, so ownership is unknown.
var errStartCountUnknown = errors.New("start count unreadable")

// abortStarts is what Stop does to a Start that is still converging in the background: it
// moves the Start count on, so every step that checks it (startSuperseded, underStart)
// finds its own Start overtaken. The bump runs under homeLockClaim, the lock underStart
// holds across its check and the call that raises desiredCount, so a Stop either lands
// before that check (the Start gives up) or after the call (Stop's scale-to-zero wins).
// Without it the goroutine goes on to put desiredCount back to 1 after Stop.
func (e *ecsEC2Runtime) abortStarts(ctx context.Context) error {
	_, err := e.beginStart(ctx)
	return err
}

// startSuperseded is the cheap, unlocked check at step boundaries. An unreadable count
// answers false: the locked check in underStart is the one that must not guess, and the
// steps in between leave the home attached the way a finished Stop does.
func (e *ecsEC2Runtime) startSuperseded(ctx context.Context, p ec2Placement) bool {
	if p.gen == 0 {
		return false
	}
	moved, err := e.startedSince(ctx, p.gen)
	return err == nil && moved
}

// underStart runs a service update (the task-definition pre-point, or the call that puts
// desiredCount at 1) only if no Stop or newer Start has overtaken this Start, with the check
// and the call under the same lock abortStarts takes.
func (e *ecsEC2Runtime) underStart(ctx context.Context, p ec2Placement, raise func(context.Context) error) error {
	if p.gen == 0 {
		return raise(ctx)
	}
	lctx, unlock, err := e.lockHome(ctx, homeLockClaim)
	if err != nil {
		return fmt.Errorf("%w: %w", errStartLockUnavailable, err)
	}
	defer unlock()
	moved, err := e.startedSince(lctx, p.gen)
	if err != nil {
		return fmt.Errorf("%w: %w", errStartCountUnknown, err)
	}
	if moved {
		return errStartSuperseded
	}
	// The count was read under a lease that may have run out since (a paused process, a slow
	// answer): another replica's Stop can then have completed while the count still reads
	// as ours. The clock decides, not the timer callback (homeLockGuard).
	if err := homeGuardOf(lctx).check(); err != nil {
		return err
	}
	return homeGuardOf(lctx).outcome(raise(lctx))
}

// dropClaimOnStop removes the claim a converging Start put on the home, so State() reads
// stopped as soon as Stop returns instead of when the claim expires.
func (e *ecsEC2Runtime) dropClaimOnStop(ctx context.Context) {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return
	}
	if ec2TagValue(vol.Tags, EC2TagClaim) == "" {
		return
	}
	e.unclaim(ctx, aws.ToString(vol.VolumeId))
	log.Printf("ecs-ec2: stop dropped the claim of a converging start on %s", e.base.name)
}

// ifOwned runs fn (one AWS call, or a short run of them) only while this Start has not
// been overtaken, with the check and fn under the same lock abortStarts takes: a Stop
// either lands before the check (fn is skipped, false) or after fn (it wins afterwards).
// Destructive steps of a failed launch go through it, because the slot or home they act on
// may already be a newer Start's. A lock that cannot be taken runs fn unguarded, as those
// steps did before: skipping a quarantine would leave a broken slot in the pool.
func (e *ecsEC2Runtime) ifOwned(ctx context.Context, p ec2Placement, fn func(context.Context)) bool {
	err := e.underStart(ctx, p, func(c context.Context) error {
		fn(c)
		return nil
	})
	if errors.Is(err, errStartSuperseded) {
		return false
	}
	if errors.Is(err, errStartCountUnknown) {
		return false
	}
	if errors.Is(err, errStartLockUnavailable) {
		log.Printf("ecs-ec2: could not take the claim lock to check ownership of %s: %v", e.base.name, err)
		fn(ctx)
		return true
	}
	// A lease lost after it was taken is not "could not lock": ownership is unknown, so the
	// step is skipped.
	return err == nil
}

// ifOwnedStrict is ifOwned for steps that must not run on a doubt (dropping a claim or a
// dormancy mark): an unreadable count or an untaken lock keeps them from running, and the
// claim expires on its own.
func (e *ecsEC2Runtime) ifOwnedStrict(ctx context.Context, p ec2Placement, fn func(context.Context)) bool {
	return e.underStart(ctx, p, func(c context.Context) error {
		fn(c)
		return nil
	}) == nil
}

// unclaimUnlessSuperseded drops this Start's claim on a failure path, unless a Stop or a
// newer Start has overtaken it: the claim then belongs to them. The check and the delete
// share one lock, so a delete in flight cannot land on a claim written after it.
func (e *ecsEC2Runtime) unclaimUnlessSuperseded(ctx context.Context, p ec2Placement) {
	e.ifOwnedStrict(ctx, p, func(c context.Context) { e.unclaim(c, p.volumeID) })
}
