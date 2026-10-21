package runtime

import (
	"context"
	"errors"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// errStartSuperseded ends a background Start that a Stop (or a later Start) overtook. The
// work is dropped without cleanup: the claim and the phase are no longer this Start's to
// remove, and the Stop has already cleared them.
var errStartSuperseded = errors.New("start superseded by a newer start or a stop")

// abortStarts is what Stop does to a Start that is still converging in the background: it
// moves the Start count on, so every step that checks it (startSuperseded, raiseService)
// finds its own Start overtaken. The bump runs under homeLockClaim, the lock raiseService
// holds across its check and the call that raises desiredCount, so a Stop either lands
// before that check (the Start gives up) or after the call (Stop's scale-to-zero wins).
// Without it the goroutine goes on to put desiredCount back to 1 after Stop.
func (e *ecsEC2Runtime) abortStarts(ctx context.Context) error {
	_, err := e.beginStart(ctx)
	return err
}

// startSuperseded is the cheap, unlocked check at step boundaries. An unreadable count
// answers false: the locked check in raiseService is the one that must not guess, and the
// steps in between leave the home attached the way a finished Stop does.
func (e *ecsEC2Runtime) startSuperseded(ctx context.Context, p ec2Placement) bool {
	if p.gen == 0 {
		return false
	}
	moved, err := e.startedSince(ctx, p.gen)
	return err == nil && moved
}

// raiseService runs the call that puts the service at desiredCount 1 (scale-up or
// create/update) only if no Stop or newer Start has overtaken this Start, with the check
// and the call under the same lock abortStarts takes.
func (e *ecsEC2Runtime) raiseService(ctx context.Context, p ec2Placement, raise func(context.Context) error) error {
	if p.gen == 0 {
		return raise(ctx)
	}
	lctx, unlock, err := e.lockHome(ctx, homeLockClaim)
	if err != nil {
		return err
	}
	defer unlock()
	moved, err := e.startedSince(lctx, p.gen)
	if err != nil {
		return err
	}
	if moved {
		return errStartSuperseded
	}
	return raise(lctx)
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
