package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// --- Start internals ---

// ec2Prep is the launch-type-independent half of a Start, done before any slot is
// chosen because it is cheap and its failures are the caller's to see.
type ec2Prep struct {
	claudeAP string
	keepAP   string
	secrets  []ecstypes.Secret
}

func (e *ecsEC2Runtime) prepare(ctx context.Context) (ec2Prep, error) {
	var p ec2Prep
	var err error
	// home is on EBS now, but the credentials/identity set stays on EFS: a single-AZ
	// volume is one bad day away from taking the user's logins with it, and the whole
	// keep-list is under 100 MiB (ADR 0045 decision 3-6). The entrypoint links ~ into it.
	var rec eraseRecord
	if p.keepAP, rec, err = e.ensureKeepAccessPoint(ctx); err != nil {
		return p, fmt.Errorf("efs keep access point: %w", err)
	}
	if len(rec.pending) > 0 {
		e.dropStalePendingErase(ctx, p.keepAP, rec)
	}
	if p.claudeAP, err = e.ensureAccessPoint(ctx, "claude-ec2", "/claude-config/"+e.base.membershipID); err != nil {
		return p, fmt.Errorf("efs claude access point: %w", err)
	}
	if p.secrets, err = e.base.putSecrets(ctx); err != nil {
		return p, fmt.Errorf("ssm secrets: %w", err)
	}
	return p, nil
}

// ensureAccessPoint is the EC2 variant of the Fargate adapter's access-point helper,
// and it exists for one measured reason: **the access point must NOT carry a PosixUser
// on this launch type.**
//
// On EC2, ECS hands EFS volumes to the Docker daemon, and Docker initializes a fresh
// volume by copying the image directory's ownership onto it. The workspace image ships
// /var/lib/af/claude owned by root, so Docker calls lchown(0,0) on the EFS mount — which
// an access point with PosixUser 1000 performs AS uid 1000, and only root may chown. The
// task then never starts:
//
//	CannotCreateContainerError: failed to copy file info for /var/lib/ecs/volumes/…-claude-…:
//	failed to chown …: operation not permitted
//
// Fargate never sees this because it mounts EFS itself instead of going through Docker.
// Dropping PosixUser lets the mount act as root (so the chown succeeds) while the access
// point's rootDirectory still confines the task to that membership's subtree — the
// isolation we actually wanted from it. CreationInfo still creates that directory owned
// by the container user.
//
// The role tags are distinct from the Fargate ones (claude-ec2 / keep-ec2) so the two
// adapters never hand each other an access point whose posix config breaks them, while
// the rootDirectory paths are IDENTICAL — a deployment that switches profiles keeps its
// Claude state and logins.
func (e *ecsEC2Runtime) ensureAccessPoint(ctx context.Context, role, path string) (string, error) {
	aps, err := e.base.accessPoints(ctx)
	if err != nil {
		return "", err
	}
	for _, ap := range aps {
		if tagValue(ap.Tags, "af-membership") == e.base.membershipID && tagValue(ap.Tags, "af-role") == role {
			return aws.ToString(ap.AccessPointId), nil
		}
	}
	created, err := e.base.efs.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{
		FileSystemId: aws.String(e.base.cfg.efsFileSystem),
		RootDirectory: &efstypes.RootDirectory{
			Path: aws.String(path),
			CreationInfo: &efstypes.CreationInfo{
				OwnerUid:    aws.Int64(e.base.cfg.posixUID),
				OwnerGid:    aws.Int64(e.base.cfg.posixGID),
				Permissions: aws.String("0755"),
			},
		},
		// No PosixUser — see above. This is the difference from the Fargate adapter.
		Tags: appendEFSTenantTag(e.base.tenantSlug, []efstypes.Tag{
			{Key: aws.String("af-membership"), Value: aws.String(e.base.membershipID)},
			{Key: aws.String("af-role"), Value: aws.String(role)},
			{Key: aws.String("Name"), Value: aws.String(e.base.name + "-" + role)},
		}),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(created.AccessPointId), nil
}

// ec2Placement is the answer to "which slot is this user's home on".
type ec2Placement struct {
	volumeID   string
	instanceID string
	az         string
	// deferred means the volume is not attached yet (or the slot is not registered
	// with ECS yet) and the rest must run in the background.
	deferred bool
	// claimed means a claim tag was written and has to be cleared once the volume is
	// really attached.
	claimed bool
	// wake means the slot has to be started before anything can be placed on it. The
	// StartInstances itself is deferred: a slot the sweeper has just put to sleep is in
	// `stopping`, and EC2 refuses to start it from there ("IncorrectInstanceState") —
	// measured. Waiting for `stopped` is a background job, not something a Start inside
	// an HTTP request can sit on.
	wake bool
	// wipe is the home's pending mark (homeWipeKey), "" when there is none. launch
	// performs it between the mount and the task, and Start always defers a placement
	// that carries one.
	wipe HomeWipe
	// claimErr is the claim write that failed. Inline it costs nothing — the caller holds
	// the lifecycle lease until launch returns — but a background half without a claim is
	// invisible: State() does not say `starting`, and a member's wipe (HomeWipeBlocked)
	// would be let through while that half goes on to scale up past the mark.
	claimErr error
	// replacement marks a slot launched or adopted to replace a reserved one; a successful
	// launch clears its af-replaces-home link (clearReplacesHome).
	replacement bool
	// releaseFrom is the slot the home is still attached to although it no longer fits the
	// workspace's size or class. The placement is a promise, not a place: converge releases
	// that slot and then places the home again. Always deferred.
	releaseFrom string
	// gen is the Start count (startGen) of the Start this placement belongs to, taken when
	// that Start began; 0 when no Start made it. A launch may drop its claim only while
	// no later Start exists (unclaimIfOurs).
	gen int64
}

// placeHome resolves the volume and the slot, attaching the two together when it can.
// The order follows the AZ (docs/log/64 §64.15.3): the volume pins the AZ, the AZ picks
// the candidate slots, and only a user with no volume yet is free to follow the pool.
// slotTypeMatches reports whether an instance is still the size AND class this
// workspace needs. One DescribeInstances, no caching — it is asked once per Start.
//
// An instance that has vanished (terminated between the volume read and here) counts
// as NOT matching, so the caller releases and re-places rather than pinning a task to
// a box that is gone.
//
// reserved is the other fact placeHome needs from the same read: an operator has reserved
// this slot for replacement (ec2TagSlotReplace), so it may not be reused whatever its type.
//
// outdated is the third: automatic replacement is on and the slot is below the launch
// template's $Latest (#1934). The template is read here, once per Start; unreadable means
// "not outdated", so a failing DescribeLaunchTemplates can never move anybody.
func (e *ecsEC2Runtime) slotTypeMatches(ctx context.Context, instanceID string) (matches, reserved, outdated bool, err error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		if isAWSNotFound(err) {
			return false, false, false, nil
		}
		return false, false, false, fmt.Errorf("describe slot %s: %w", instanceID, err)
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if !e.pool.noAutoReplace {
				if lt, ltErr := describeLaunchTemplate(ctx, e.ec2, e.pool.launchTemplate); ltErr != nil {
					log.Printf("ecs-ec2: reading the slot launch template for %s: %v", e.base.name, ltErr)
				} else {
					outdated = e.pool.slotOutdated(inst, lt)
				}
			}
			// A sweeper fence on a slot a Start won anyway is the automatic move's cue too.
			outdated = outdated || (!e.pool.noAutoReplace && slotRetireTagged(inst))
			return string(inst.InstanceType) == e.instanceType, slotReserved(inst), outdated, nil
		}
	}
	return false, false, false, nil
}

func (e *ecsEC2Runtime) placeHome(ctx context.Context) (ec2Placement, error) {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return ec2Placement{}, fmt.Errorf("describe home volume: %w", err)
	}
	if vol != nil {
		if inst := attachedInstance(vol); inst != "" {
			// ⚠️ The attachment is AFFINITY, not a decision. It records the slot this
			// workspace used last time, for the size and class it had THEN — and both
			// can change underneath it (a memory edit moves the rung, a class edit
			// moves the ladder). Reusing a slot of the wrong instance type is not
			// merely suboptimal:
			//
			//   ACROSS ARCHITECTURES IT STRANDS THE WORKSPACE. buildTaskDef declares
			//   RuntimePlatform for the new class while the placement constraint still
			//   pins THIS instance, so ECS refuses to place the task at all — "no
			//   container instance met all of its requirements … missing an attribute
			//   required by your task" — and keeps refusing, with desiredCount 1 and
			//   nothing running, forever.
			//
			// Measured on a live deployment (docs/log/70 §70.14.5): a member moved from
			// the saver class (m6i, x86_64) to arm (m8g, arm64) and their workspace
			// could not start again. So the match is checked before the affinity is
			// honoured, and a stale slot is released rather than reused.
			matches, reserved, outdated, err := e.slotTypeMatches(ctx, inst)
			if err != nil {
				return ec2Placement{}, err
			}
			if reserved {
				// Checked before the type: a reservation is the operator saying this box
				// must not run anybody again, and a type mismatch would otherwise release
				// the home and leave the reserved box free for nobody.
				return e.replaceReservedSlot(ctx, vol, inst, false)
			}
			if matches && outdated {
				// Below $Latest: the same move, unannounced. A new slot that cannot be had
				// falls through to the slot the home is on and the next Start tries again.
				p, err := e.replaceReservedSlot(ctx, vol, inst, true)
				if !errors.Is(err, errAutoReplaceDeferred) {
					return p, err
				}
				// The slot may have been reserved while the launch was failing; the stale read
				// above says otherwise, and a reservation never falls back. Unreadable counts
				// as reserved.
				if e.slotNowReservedByOperator(ctx, inst) {
					return ec2Placement{}, fmt.Errorf("slot %s is reserved for replacement and no new slot could be launched "+
						"(the reservation stays; the old slot is not reused): %w", inst, err)
				}
			}
			if !matches {
				// Never released here: this runs on the request thread, and releasing is
				// waitTasksGone + umount over SSM + detach + waitDeviceFree, none of it
				// bounded in seconds. On a slot whose agents have stopped answering it
				// outlasts the ingress timeout on EVERY retry. The claim keeps State() at
				// `starting` while converge releases and re-places in the background.
				log.Printf("ecs-ec2: %s now needs %s but its home is on %s; releasing that slot in the background",
					e.base.name, e.instanceType, inst)
				volID := aws.ToString(vol.VolumeId)
				claimErr := e.claim(ctx, volID, inst)
				if claimErr != nil {
					log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, claimErr)
				}
				e.clearDormancy(ctx, volID)
				return ec2Placement{
					volumeID: volID, instanceID: inst, az: aws.ToString(vol.AvailabilityZone),
					deferred: true, releaseFrom: inst, wipe: homeWipeOf(vol), claimErr: claimErr,
				}, nil
			} else {
				// The home is still on a slot — the normal case now that Stop leaves it
				// there (lazy release). This is both the affinity ("the same user gets the
				// same slot") and the cheapest path: no attach, no mount to redo.
				volID := aws.ToString(vol.VolumeId)
				claimErr := e.claim(ctx, volID, inst)
				if claimErr != nil {
					log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, claimErr)
				}
				e.clearDormancy(ctx, volID)
				running, err := e.instanceRunning(ctx, inst)
				if err != nil {
					return ec2Placement{}, err
				}
				// ⚠️ RUNNING IS NOT ENOUGH, and asking only EC2 is what turned one wedged
				// box into hours of 504s (docs/log/64 §64.40). `deferred` decides whether the
				// rest of this Start happens on the CALLER'S THREAD, and the caller is an
				// HTTP handler behind a 60s-idle ingress — so it may only stay inline for
				// work that is measured in seconds. launch()'s first act is to wait for the
				// slot to be a usable container instance; on a box whose ECS agent has died
				// that wait cannot finish, and holding the request through it delivers a
				// 504 instead of the `starting` the Console knows how to show.
				//
				// The homeless path below has always asked both questions (s.running &&
				// s.registered). This branch asked only the first, which is the asymmetry
				// the incident found: the difference is invisible on a healthy pool,
				// because a running slot is normally a registered one.
				reg, err := e.registeredSlots(ctx)
				if err != nil {
					// Unknown, not "no": defer rather than fail. Deferring costs a
					// background hand-off; failing costs the user their Start.
					log.Printf("ecs-ec2 start: could not tell whether slot %s is registered (%v); converging in the background", inst, err)
				}
				ready := running && reg[inst]
				// A dormant slot keeps both the image cache and the attachment, so waking it
				// is the ~90s path rather than the 135s of building a new one.
				return ec2Placement{
					volumeID: volID, instanceID: inst, az: aws.ToString(vol.AvailabilityZone),
					deferred: !ready, wake: !running, wipe: homeWipeOf(vol), claimErr: claimErr,
				}, nil
			}
		}
	}
	azFilter := ""
	if vol != nil {
		azFilter = aws.ToString(vol.AvailabilityZone)
	}
	slots, err := e.freeSlots(ctx, azFilter)
	if err != nil {
		return ec2Placement{}, fmt.Errorf("list free slots: %w", err)
	}
	// A brand-new home has no AZ yet, and an EBS volume can NEVER change the one it was
	// created in — so the AZ is decided by where a slot can actually be had, and the volume
	// is created last, once that is settled.
	//
	// Creating it first, in the first pool AZ, is the same answer whenever a slot is free,
	// but it makes the two growth paths below unable to look anywhere else: a capacity
	// shortfall in that one AZ can only be answered by deleting the volume and starting
	// over, which is harmless for an empty home and DATA LOSS for a restored one —
	// createHomeVolume drops the snapshot it restored from as soon as the volume is usable.
	// Not creating it until the destination is known removes the choice entirely.
	create := func(az string) error {
		v, err := e.createHomeVolume(ctx, az)
		if err != nil {
			return fmt.Errorf("create home volume: %w", err)
		}
		vol, azFilter = v, az
		return nil
	}
	if vol == nil && len(slots) > 0 {
		if err := create(slots[0].az); err != nil {
			return ec2Placement{}, err
		}
		slots = filterSlotsByAZ(slots, azFilter)
	}
	// Try the candidates in order (hot first, then stopped). A failed AttachVolume is
	// the normal outcome of losing a race for the last free slot, not an error to
	// surface — move to the next one.
	for _, s := range slots {
		volID := aws.ToString(vol.VolumeId)
		// The candidate list was read before; a reservation may have landed since.
		if e.slotNowReserved(ctx, s.id) {
			continue
		}
		if err := e.waitVolumeAttachable(ctx, volID); err != nil {
			return ec2Placement{}, err
		}
		if err := e.attachHomeWithRetry(ctx, volID, s.id); err != nil {
			log.Printf("ecs-ec2 start: slot %s did not take %s (%v); trying the next one", s.id, volID, err)
			continue
		}
		// Claim it for the whole launch, not just until the attach: State() reads this
		// tag to say `starting`, and Start early-returns on `starting`.
		claimErr := e.claim(ctx, volID, s.id)
		if claimErr != nil {
			log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, claimErr)
		}
		if e.slotNowReserved(ctx, s.id) {
			// Reserved between the check above and the attach. Nothing is mounted yet, so
			// stepping off is safe; see slotNowReserved for why the second check closes it.
			if err := e.backOffReservedSlot(ctx, volID, s.id); err != nil {
				return ec2Placement{}, err
			}
			continue
		}
		// A hot, already-registered slot is the only case that can finish inline.
		return ec2Placement{
			volumeID: volID, instanceID: s.id, az: azFilter,
			deferred: !(s.running && s.registered), wake: !s.running, wipe: homeWipeOf(vol), claimErr: claimErr,
		}, nil
	}
	// No free slot. Growing the pool is preferred while there is room — it disturbs
	// nobody, and an extra slot at rest costs only its root volume now that idle slots
	// are stopped. The cap is therefore the real knob, and at the cap the only way to
	// serve this user is to take a slot back from the longest-dormant one.
	if full, err := e.poolFull(ctx); err != nil {
		return ec2Placement{}, err
	} else if full {
		// azFilter is "" for a home that does not exist yet, so the victim is the
		// longest-dormant one in the WHOLE pool rather than the longest-dormant one in an
		// AZ that was picked before anybody looked.
		victim, victimAZ, evictErr := e.evictLongestIdle(ctx, azFilter)
		if evictErr != nil {
			// Nothing of this SIZE can be taken back — but the cap may be held by boxes
			// this workspace could never run on, and reuse cannot turn one of those into
			// what it needs. Terminate one and build the right box instead of failing:
			// "slower" is a cost the operator accepted, "cannot start at all" is not
			// (docs/log/64 §64.33). Falls through to growPool below.
			e.setPhase("slot: making room")
			freed, err := e.makeRoom(ctx)
			if err != nil {
				log.Printf("ecs-ec2 start: could not free a slot for %s: %v", e.base.name, err)
			}
			if !freed {
				return ec2Placement{}, evictErr // the original message is the useful one
			}
		} else {
			if vol == nil {
				if err := create(victimAZ); err != nil {
					return ec2Placement{}, err
				}
			}
			volID := aws.ToString(vol.VolumeId)
			// The victim was picked from a list read before its release; a reservation may
			// have landed since. The freed box is then free and reserved, and the sweeper
			// retires it; this Start fails rather than run on it, and the next one has room.
			errReserved := fmt.Errorf("the reclaimed slot %s was reserved for replacement during this start; start again", victim)
			if e.slotNowReserved(ctx, victim) {
				return ec2Placement{}, errReserved
			}
			if err := e.waitVolumeAttachable(ctx, volID); err != nil {
				return ec2Placement{}, err
			}
			if err := e.attachHomeWithRetry(ctx, volID, victim); err != nil {
				return ec2Placement{}, fmt.Errorf("attach %s to the reclaimed slot %s: %w", volID, victim, err)
			}
			claimErr := e.claim(ctx, volID, victim)
			if claimErr != nil {
				log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, claimErr)
			}
			if e.slotNowReserved(ctx, victim) {
				if err := e.backOffReservedSlot(ctx, volID, victim); err != nil {
					return ec2Placement{}, err
				}
				return ec2Placement{}, errReserved
			}
			e.clearDormancy(ctx, volID)
			running, err := e.instanceRunning(ctx, victim)
			if err != nil {
				return ec2Placement{}, err
			}
			return ec2Placement{volumeID: volID, instanceID: victim, az: azFilter, deferred: true, wake: !running, wipe: homeWipeOf(vol), claimErr: claimErr}, nil
		}
	}
	// RunInstances answers immediately with a PENDING instance that cannot accept a
	// volume yet, so the claim tag carries the "starting" state until the background
	// half attaches for real.
	e.setPhase("slot: creating")
	inst, az, err := e.growPool(ctx, azFilter)
	if err != nil {
		return ec2Placement{}, err
	}
	if vol == nil {
		if err := create(az); err != nil {
			return ec2Placement{}, err
		}
	}
	volID := aws.ToString(vol.VolumeId)
	if err := e.claim(ctx, volID, inst); err != nil {
		return ec2Placement{}, fmt.Errorf("claim %s for %s: %w", volID, inst, err)
	}
	return ec2Placement{volumeID: volID, instanceID: inst, az: azFilter, deferred: true, claimed: true, wipe: homeWipeOf(vol)}, nil
}

// finishStart is the background half: wait for the slot, attach if the claim path left
// that undone, then launch. Everything it does is idempotent and re-derivable, so a CP
// restart in the middle costs a retry, not a wedged workspace.
func (e *ecsEC2Runtime) finishStart(ctx context.Context, p ec2Placement, prep ec2Prep) {
	// Whatever happens — converged, failed, or gave up — the workspace is no longer
	// mid-provisioning, and the Console's dialog keys off exactly this. A blocked phase is
	// the exception: it is the reason the start gave up, and nothing else would say it.
	defer e.clearProgressPhase(ctx, p.gen)
	e.converge(ctx, p, prep, 1)
}

// ifCurrentStart runs f only while no Stop or newer Start has overtaken the Start that
// made the placement (gen), and answers whether it did. It is ifOwnedStrict on a context
// detached from ctx: the check and f share homeLockClaim with beginStart, so a
// Stop either lands before the check (f is skipped) or after f (it wins afterwards), and the
// background budget that ended the work is no reason to skip a write that explains why.
// An unreadable count or a lock that cannot be had answers false. gen 0 means no Start made
// the placement, so f runs.
func (e *ecsEC2Runtime) ifCurrentStart(ctx context.Context, gen int64, f func()) bool {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return e.ifOwnedStrict(dctx, ec2Placement{gen: gen}, func(context.Context) { f() })
}

// clearProgressPhase drops the phase when this Start still owns it, unless it is a blocked
// one, which a failed start leaves behind on purpose (State() or Stop clears it).
func (e *ecsEC2Runtime) clearProgressPhase(ctx context.Context, gen int64) {
	e.ifCurrentStart(ctx, gen, func() {
		v, ok := startPhase.Load(e.base.name)
		if !ok {
			return
		}
		if s, _ := v.(string); !strings.HasPrefix(s, blockedPhasePrefix) {
			startPhase.CompareAndDelete(e.base.name, v)
		}
	})
}

// superseded reports whether a later Start has begun since p's. A Stop that counts as a
// Start lands here too. An unreadable count is "superseded": this half then does nothing
// further, and the claim expires on its own.
func (e *ecsEC2Runtime) superseded(ctx context.Context, p ec2Placement) bool {
	if p.gen == 0 {
		return false
	}
	moved, err := e.startedSince(ctx, p.gen)
	return err != nil || moved
}

// clipMiddle shortens s to about max runes, keeping both ends. A slot command's error is
// "<the whole quoted script> on <instance>: <status>: <stderr>", so the cause is at the end
// and the operation at the start; cutting only the tail would keep the least useful part.
func clipMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max / 3
	return string(r[:head]) + " … " + string(r[len(r)-(max-head):])
}

// releaseAndReplace is the background half of a size/class change: take the home off the
// slot it no longer fits, then place it like any homeless home. ok is false when the
// start stopped here. A real failure leaves the blocked phase as its reason, because a
// bare `starting` that never ends, or a silent `stopped`, tells the member nothing.
//
// A later Start (or Stop) that overtakes this one ends it silently, touching neither phase
// nor claim: they are the newer Start's now. The claim TTL is shorter than the background
// budget, so this does happen. It is checked on entry, after the release, and again before
// and after the re-placement; the last one cannot undo an attach, which the newer Start
// adopts like any attachment.
func (e *ecsEC2Runtime) releaseAndReplace(ctx context.Context, p ec2Placement) (ec2Placement, bool) {
	fail := func(why string, err error) (ec2Placement, bool) {
		// Ownership is asked on a detached context (ifCurrentStart): a failure is most often
		// the background budget running out, and reading the Start count on that expired
		// context fails too, which would read as "overtaken" and drop the one reason there is.
		// Reason and claim go together under one ownership check: a Stop that lands after
		// it clears both itself, and one that landed before leaves both alone.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		e.ifOwnedStrict(dctx, p, func(c context.Context) {
			log.Printf("ecs-ec2 start: %s for %s: %v", why, e.base.name, err)
			e.setPhase(blockedPhasePrefix + why + ": " + clipMiddle(err.Error(), 300))
			e.unclaim(c, p.volumeID)
		})
		return ec2Placement{}, false
	}
	if e.superseded(ctx, p) {
		return ec2Placement{}, false
	}
	e.ifCurrentStart(ctx, p.gen, func() { e.setPhase("slot: releasing") })
	if err := e.releaseSlotSince(ctx, p.gen); err != nil {
		return fail("the old slot could not be released after a size/class change", err)
	}
	if e.superseded(ctx, p) {
		return ec2Placement{}, false
	}
	next, err := e.placeHome(ctx)
	if err == nil {
		err = next.claimErr
	}
	if err == nil && next.releaseFrom != "" {
		// The release reported success and the home is still on a mismatched slot (it was
		// re-mounted for a newer Start). Looping here would release it again forever.
		err = fmt.Errorf("home is still attached to %s", next.releaseFrom)
	}
	if err != nil {
		return fail("could not place the home after releasing the old slot", err)
	}
	next.gen = p.gen
	if e.superseded(ctx, p) {
		return ec2Placement{}, false
	}
	return next, true
}

// converge is finishStart's body, plus one budgeted retry for the single failure a Start
// can fix by itself: the slot turned out to be lost, so the home was moved off it and
// there is nothing wrong with simply trying another box.
//
// retries is 1 for a normal Start and 0 for the replacement it spawns, so one Start can
// quarantine at most one extra slot. That bound is the point: a pool failing for a reason
// this code has not understood (a bad AMI, a broken cluster) would otherwise walk every
// box into quarantine one Start at a time, turning a degraded deployment into an empty
// one. Giving up after two leaves the workspace `stopped` with the reason in the log —
// a state the user can retry from, and the operator can read.
func (e *ecsEC2Runtime) converge(ctx context.Context, p ec2Placement, prep ec2Prep, retries int) {
	if p.releaseFrom != "" {
		next, ok := e.releaseAndReplace(ctx, p)
		if !ok {
			return
		}
		p = next
	}
	if e.startSuperseded(ctx, p) {
		return
	}
	if p.wake {
		e.setPhase("slot: waking")
		if err := e.wakeSlot(ctx, p.instanceID); err != nil {
			log.Printf("ecs-ec2 start: waking slot %s for %s failed: %v", p.instanceID, e.base.name, err)
			return
		}
	}
	if p.claimed {
		e.setPhase("slot: booting")
		if err := e.waitInstanceRunning(ctx, p.instanceID); err != nil {
			log.Printf("ecs-ec2 start: slot %s for %s never came up: %v", p.instanceID, e.base.name, err)
			return
		}
		if e.startSuperseded(ctx, p) {
			return
		}
		e.setPhase("home: attaching")
		if err := e.attachHome(ctx, p.volumeID, p.instanceID); err != nil {
			log.Printf("ecs-ec2 start: attaching %s to the new slot %s failed: %v", p.volumeID, p.instanceID, err)
			return
		}
	}
	err := e.launch(ctx, p, prep)
	if err == nil {
		return
	}
	if errors.Is(err, errStartSuperseded) {
		log.Printf("ecs-ec2 start: %s was stopped while it converged on slot %s; leaving it stopped", e.base.name, p.instanceID)
		return
	}
	if errors.Is(err, errSlotLost) && retries > 0 {
		// abandonLostSlot has already detached the home and taken the box out of the
		// pool, so placeHome now sees an ordinary homeless home and puts it somewhere
		// healthy — including growing the pool if that is what it takes.
		log.Printf("ecs-ec2 start: slot %s is not coming back; re-placing %s", p.instanceID, e.base.name)
		if e.startSuperseded(ctx, p) {
			return
		}
		e.setPhase("slot: replacing")
		next, perr := e.placeHome(ctx)
		next.gen = p.gen
		if perr == nil {
			perr = next.claimErr // this half runs in the background: see ec2Placement.claimErr
		}
		if perr != nil {
			log.Printf("ecs-ec2 start: re-placing %s after losing slot %s: %v", e.base.name, p.instanceID, perr)
			e.unclaimUnlessSuperseded(ctx, p)
			return
		}
		e.converge(ctx, next, prep, retries-1)
		return
	}
	log.Printf("ecs-ec2 start: %s did not converge on slot %s: %v", e.base.name, p.instanceID, err)
	if errors.Is(err, errSlotLost) {
		// Out of retries: the claim abandonLostSlot deliberately kept alive has no
		// replacement coming, so drop it now instead of pinning the workspace at
		// `starting` until it expires.
		e.unclaimUnlessSuperseded(ctx, p)
	}
}

// launch takes an attached volume to a running task: register the task definition and
// point the service at it, wait for the slot to be a usable ECS container instance,
// mount the home, and only then put the service at desiredCount 1.
//
// ⚠️ The ORDER of those two service calls is the whole point, and it is worth ~40
// seconds on 40% of Starts (docs/log/64 §64.39, measured on the production pool).
//
// This used to be one call — `UpdateService(desiredCount=1 + taskDefinition=new +
// forceNewDeployment)` — issued after the mount. ECS answers a task-definition change by
// demoting the current deployment to ACTIVE and making the new one PRIMARY, and it then
// satisfies the desiredCount increase from the ACTIVE (i.e. OLD) deployment first:
//
//   - if the old revision's slot is still alive, a task from the OLD task definition
//     (old image, old env) actually RUNS for a minute or two next to the real one, and
//     Service Connect load-balances across both — the same window that silently dropped
//     an in-flight OAuth flow_id (see lastTaskDef), except it opens on EVERY Start whose
//     task definition changed, not just on a forced deployment;
//   - if that slot is gone (terminated, rebuilt in another class), the old revision's
//     `ec2InstanceId` constraint matches nothing, ECS logs "MemberOf placement constraint
//     unsatisfied" naming some OTHER instance, and the real task waits out a ~41s retry.
//
// Splitting the call fixes both, and the wait it introduces is free because it hides
// behind work we already do. Measured on a throwaway cluster (§64.39.4): the old
// deployment stops accepting tasks as soon as it leaves ACTIVE — 10-23s — while the
// slot needs ~18s to register with ECS plus the mount. Waiting for it to DISAPPEAR
// (59s) or for rolloutState=COMPLETED (90s) would cost more than the bug.
func (e *ecsEC2Runtime) launch(ctx context.Context, p ec2Placement, prep ec2Prep) error {
	// Everything this needs — the instance the constraint pins, the AZ the ENI must land
	// in — is already decided, so it can all happen while the slot is still booting.
	taskDefArn, reused, err := e.reuseOrRegisterTaskDef(ctx, p, prep)
	if err != nil {
		return fmt.Errorf("register task def: %w", err)
	}
	// Best-effort: on failure `prepared` stays false and the tail below falls back to the
	// old single call, which is a slower Start rather than a broken one.
	prepared := !reused && e.pointServiceAt(ctx, taskDefArn, p)

	if e.startSuperseded(ctx, p) {
		return errStartSuperseded
	}
	e.setPhase("slot: joining the cluster")
	if err := e.waitSlotRegistered(ctx, p.instanceID); err != nil {
		if errors.Is(err, errSlotLost) {
			// The box is up and the cluster cannot reach it: no amount of waiting fixes
			// that. Get the home off it and out of the blast radius, then tell the caller
			// to try a different slot rather than reporting a failed Start.
			if e.startSuperseded(ctx, p) {
				// The wait outlived a Stop (and maybe a newer Start on this very slot): the
				// slot is not this Start's to tear down.
				return errStartSuperseded
			}
			e.abandonLostSlot(ctx, p, err)
			return err
		}
		return fmt.Errorf("slot %s not registered with the cluster: %w", p.instanceID, err)
	}
	if e.startSuperseded(ctx, p) {
		return errStartSuperseded
	}
	e.setPhase("home: mounting")
	if err := e.mountHome(ctx, p); err != nil {
		if errors.Is(err, errHomeLeftSlot) || errors.Is(err, errHomeLockLost) {
			// A release took the home off while this launch was on its way, or the home's
			// lock could not be held. The slot did nothing wrong; this launch's claim goes,
			// so the workspace reads stopped and the next Start places it again instead of
			// waiting out the claim TTL.
			e.unclaimIfOurs(ctx, p)
			return fmt.Errorf("mount home on %s: %w", p.instanceID, err)
		}
		// A slot that cannot mount is not a slow slot, it is a broken one, and leaving it
		// in the pool means the next Start picks it too (measured: it did, for every user
		// that followed). Take it out of the world before returning.
		if e.startSuperseded(ctx, p) {
			return errStartSuperseded
		}
		e.quarantineSlot(ctx, p, err)
		return fmt.Errorf("mount home on %s: %w", p.instanceID, err)
	}
	if p.wipe != "" {
		if err := e.wipeMountedHome(ctx, p); err != nil {
			// The mark is still on the home, so the next Start tries again; until then the
			// workspace does not start with what the member asked to have removed. Dropping
			// the claim lets State() say `stopped` now rather than when the claim expires.
			e.unclaimUnlessSuperseded(ctx, p)
			return fmt.Errorf("clear home on %s: %w", p.instanceID, err)
		}
	}
	// The home is on this box now, so the box is this person's for as long as it stays
	// there — stamp it for the bill. Deliberately AFTER the mount and best-effort: the
	// tag is read by Cost Explorer and by nothing else (ADR 0048 decision 3).
	e.tagSlotOwner(ctx, p.instanceID)
	// …and it is no longer free, so its free-pool clock stops. Same moment, opposite tag.
	e.clearSlotFree(ctx, p.instanceID)
	if e.startSuperseded(ctx, p) {
		return errStartSuperseded
	}
	e.setPhase("task: starting")
	if prepared {
		// No wait here any more, and that is the whole point of ec2SingleTaskDeployment:
		// the service cannot run two tasks, so raising the count past a not-yet-retired
		// deployment can no longer hand one to the old revision. Measured A/B on one
		// substrate, same sequence, same moment (docs/log/64 §64.39.10):
		//   maximumPercent 200 → 2 tasks, the FIRST from the old revision (2 of 2)
		//   maximumPercent 100 → 1 task, always the new revision  (2 of 2)
		// Before that this spot waited 53-55s for the old deployment to leave ACTIVE.
		err = e.underStart(ctx, p, e.scaleUpService)
	} else {
		err = e.underStart(ctx, p, func(c context.Context) error {
			return e.upsertService(c, taskDefArn, p, !reused)
		})
	}
	if errors.Is(err, errStartSuperseded) {
		return err
	}
	if err != nil {
		return fmt.Errorf("service: %w", err)
	}
	e.finishLaunch(ctx, p)
	return nil
}

// finishLaunch is what every successful launch ends with: the service carries the state
// now, so drop the claim and make sure the home is not counted as dormant while its task
// runs.
func (e *ecsEC2Runtime) finishLaunch(ctx context.Context, p ec2Placement) {
	if p.replacement {
		e.clearReplacesHome(ctx, p.instanceID)
	}
	// A Stop (or a newer Start) after the service was raised owns the claim and the
	// dormancy marks now; the check and the deletes share one short lock.
	e.ifOwnedStrict(ctx, p, func(c context.Context) {
		e.unclaim(c, p.volumeID)
		e.clearDormancy(c, p.volumeID)
	})
	e.base.watchReady(ctx)
}

// quarantineSlot takes a slot that failed to mount a home out of the pool, and frees the
// home so its owner can be placed somewhere that works.
//
// Order matters and every step is best-effort: this runs when something is ALREADY wrong,
// and the one outcome that must not happen is the box staying in the free list.
//
//  1. re-tag af-role → quarantined. Every slot query filters on that tag, so this single
//     write removes it from freeSlots, from poolSize (a replacement may be created) and
//     from placement.
//  2. unmount (best-effort, bounded) and detach the home. The volume is the user's; it has
//     to be able to attach elsewhere, and a failed af-mount can have mounted it already.
//  3. drop the claim, so the owner's next Start is immediate rather than waiting out the
//     claim TTL on a slot that will never work.
//  4. stop the instance. It cannot run tasks, and a wedged kernel is not something the CP
//     can repair — but it bills by the hour until an operator looks. Stopping keeps the
//     root volume (and the evidence) while ending the compute charge; terminating it is
//     still the operator's call. ⚠️ The sweeper's own terminate stage (slotTerminateAfter)
//     will NOT collect this box either: quarantineSlot re-tags it af-role=quarantined, and
//     both sweeper walks filter on af-role=slot. The evidence survives on purpose.
func (e *ecsEC2Runtime) quarantineSlot(ctx context.Context, p ec2Placement, cause error) {
	log.Printf("ecs-ec2: QUARANTINING slot %s — it could not mount %s for %s: %v",
		p.instanceID, p.volumeID, e.base.name, cause)
	// Its own bounded context: this runs when a launch has already failed, often with the
	// launch's context ending, and the stop below must still be sent.
	ctx, cancelAll := context.WithTimeout(context.WithoutCancel(ctx), quarantineCleanupBudget)
	defer cancelAll()
	// Every step that acts on the slot or its home runs through ifOwned: a Stop and a new
	// Start may have taken the slot since the mount failed, and the rest is theirs.
	if !e.ifOwned(ctx, p, func(c context.Context) { e.markQuarantined(c, p.instanceID, cause) }) {
		return
	}
	if p.volumeID != "" {
		// A failed af-mount may still have mounted the home (it fails after the mount when
		// the new filesystem cannot be written), so unmount before the detach. Bounded and
		// best-effort: a slot that failed because SSM never answered would otherwise hold
		// the home here for good, and the instance stop below unmounts on its way down.
		// Without the lock the home stays attached: a detach another replica's mount could
		// land beside is the corruption the lock exists for, and the stop below unmounts.
		lockCtx, cancelLock := context.WithTimeout(ctx, quarantineLockBudget)
		lctx, unlock, err := e.lockHome(lockCtx, homeLockMount)
		if err != nil {
			log.Printf("ecs-ec2: leaving %s attached to the quarantined slot %s: %v", p.volumeID, p.instanceID, err)
		} else {
			owned := e.ifOwned(lctx, p, func(c context.Context) {
				uctx, cancel := context.WithTimeout(c, quarantineUmountBudget)
				if err := e.umountHome(uctx, p.instanceID); err != nil {
					log.Printf("ecs-ec2: unmounting %s on the quarantined slot %s before the detach: %v", p.volumeID, p.instanceID, err)
				}
				cancel()
				// Both locks: the mount lock keeps releases and mounts out, the claim lock (c)
				// is what a Stop on another replica waits on. Losing either leaves the home
				// attached for whoever holds the operation now.
				if err := homeGuardOf(lctx).check(); err != nil {
					log.Printf("ecs-ec2: leaving %s attached to the quarantined slot %s: %v", p.volumeID, p.instanceID, err)
				} else if err := homeGuardOf(c).check(); err != nil {
					log.Printf("ecs-ec2: leaving %s attached to the quarantined slot %s (claim lock): %v", p.volumeID, p.instanceID, err)
				} else if _, err := e.ec2.DetachVolume(c, &ec2.DetachVolumeInput{
					VolumeId:   aws.String(p.volumeID),
					InstanceId: aws.String(p.instanceID),
				}); err != nil {
					log.Printf("ecs-ec2: detaching %s from the quarantined slot %s: %v", p.volumeID, p.instanceID, err)
				}
			})
			unlock()
			if !owned {
				cancelLock()
				return
			}
		}
		cancelLock()
		e.unclaimUnlessSuperseded(ctx, p)
	}
	e.ifOwned(ctx, p, func(c context.Context) {
		if _, err := e.ec2.StopInstances(c, &ec2.StopInstancesInput{InstanceIds: []string{p.instanceID}}); err != nil {
			log.Printf("ecs-ec2: stopping the quarantined slot %s: %v", p.instanceID, err)
		}
	})
}

// markQuarantined is step 1 of both quarantine paths: the single tag write that takes a
// box out of the world, plus the owner tags that must not keep billing somebody for it.
// Split out because abandonLostSlot has to do it in a different ORDER from the rest.
func (e *ecsEC2Runtime) markQuarantined(ctx context.Context, instanceID string, cause error) {
	reason := cause.Error()
	if len(reason) > 200 {
		reason = reason[:200]
	}
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleQuarantined)},
			{Key: aws.String(ec2TagQuarantineReason), Value: aws.String(reason)},
			{Key: aws.String(ec2TagQuarantineAt), Value: aws.String(e.now().UTC().Format(time.RFC3339))},
		},
	}); err != nil {
		// This is the step that stops the bleeding; say so loudly when it fails, because
		// nothing else here prevents the next user from landing on the same box.
		log.Printf("ecs-ec2: could not quarantine slot %s (it will be offered again): %v", instanceID, err)
	}
	// A quarantined box is nobody's. On the mount-failure path it reaches here before
	// launch stamps the owner, so normally there is nothing to remove — but a previous
	// tenancy whose release failed would otherwise keep billing a person for a box they
	// can no longer use.
	e.untagSlotOwner(ctx, instanceID)
}

// abandonLostSlot is quarantineSlot for a box whose OPERATING SYSTEM is gone rather than
// one that merely failed a mount, and the difference is why it cannot reuse that path:
// there, nothing is running on the slot yet and the kernel still answers. Here a task is
// still nominally on it, the ECS agent is unreachable, and every step of the ordinary
// teardown quietly does the wrong thing (docs/log/64 §64.40, reproduced by hand during the
// incident before it was written down):
//
//	quarantineSlot                     what a wedged box actually does
//	─────────────────────────────────  ────────────────────────────────────────────────
//	DetachVolume on a running box      hangs in `detaching` forever — the kernel can
//	                                   never flush and unmount the filesystem
//	StopInstances (graceful)           waits out an ACPI shutdown nobody is listening for
//	StopInstances while a task ENI     the box comes back MULTI-ENI and silently loses
//	  is still attached                its egress (decision 3-3) — the very trap the sweeper
//	                                   refuses to go near
//
// So the order is inverted and one step is added at the front:
//
//  1. tag af-role → quarantined. First, as always: one write and the box is out of
//     freeSlots, out of poolSize and out of placement, whatever happens below.
//  2. DEREGISTER the container instance, forced. This is the new step and the one that
//     breaks the deadlock: ECS cannot stop a task whose agent is gone, so it holds the
//     task — and its ENI — indefinitely. Forcing the deregistration makes ECS give up on
//     it, and the ENI then detaches on its own. Safe to force here because a slot holds
//     exactly one home and therefore at most one workspace's task: the only task this
//     can kill is the caller's own, and it is already unreachable.
//  3. wait for the task ENI to go, THEN stop the box — forced, because there is nothing
//     alive in it to shut down politely.
//  4. only once it is off, detach the home. A powered-down instance gives the volume
//     back cleanly; a forced detach is the fallback for when it will not stop.
//
// The claim is deliberately LEFT IN PLACE (and refreshed): the caller is about to place
// this home somewhere else, and State() reads the claim to keep saying `starting` while
// that happens. Dropping it here would flash `stopped` at the Console mid-recovery and
// invite a second, racing Start. If the replacement never happens the claim expires on
// its own (claimTTL) and the workspace becomes startable again — the same safety valve
// State() already documents.
func (e *ecsEC2Runtime) abandonLostSlot(ctx context.Context, p ec2Placement, cause error) {
	log.Printf("ecs-ec2: ABANDONING slot %s — it is running but the cluster cannot reach it, so %s is being moved off it: %v",
		p.instanceID, e.base.name, cause)
	// Each step that acts on the slot or its home runs through ifOwned (the waits between
	// them do not): a Stop and a new Start can land during the minutes this takes, and the
	// new Start may be using this very slot.
	if !e.ifOwned(ctx, p, func(c context.Context) {
		e.markQuarantined(c, p.instanceID, cause)
		e.deregisterSlot(c, p.instanceID)
	}) {
		return
	}
	if err := e.waitTaskENIsGone(ctx, p.instanceID); err != nil {
		// Not fatal, but it changes what we may do next: stopping now is the multi-ENI
		// trap, so leave the box running and let the operator (or the sweeper, once ECS
		// does release the ENI) finish. The home still has to come off, forcibly.
		log.Printf("ecs-ec2: slot %s still holds a task ENI (%v); leaving it running and forcing the home off",
			p.instanceID, err)
		e.forceDetachHome(ctx, p)
		return
	}
	if !e.ifOwned(ctx, p, func(c context.Context) {
		if _, err := e.ec2.StopInstances(c, &ec2.StopInstancesInput{
			InstanceIds: []string{p.instanceID},
			// A graceful stop asks the OS to shut down; that is precisely what is broken.
			Force: aws.Bool(true),
		}); err != nil {
			log.Printf("ecs-ec2: force-stopping the lost slot %s: %v", p.instanceID, err)
		}
	}) {
		return
	}
	if err := e.waitInstanceStopped(ctx, p.instanceID); err != nil {
		log.Printf("ecs-ec2: lost slot %s did not stop (%v); forcing the home off it", p.instanceID, err)
		e.forceDetachHome(ctx, p)
		return
	}
	// The box is off: an ordinary detach now, which is the one that cannot corrupt
	// anything — nothing is holding the filesystem open any more.
	if p.volumeID != "" {
		var detachErr error
		if !e.ifOwned(ctx, p, func(c context.Context) {
			_, detachErr = e.ec2.DetachVolume(c, &ec2.DetachVolumeInput{
				VolumeId:   aws.String(p.volumeID),
				InstanceId: aws.String(p.instanceID),
			})
		}) {
			return
		}
		if detachErr != nil {
			log.Printf("ecs-ec2: detaching %s from the stopped lost slot %s: %v", p.volumeID, p.instanceID, detachErr)
			e.forceDetachHome(ctx, p)
		}
	}
}

// forceDetachHome is the fallback when the box could not be powered off first. A forced
// detach is a yanked cable — ext4 replays its journal on the next mount — and it is still
// the better outcome, because the alternative is a home bolted to a machine that will
// never run anything again.
func (e *ecsEC2Runtime) forceDetachHome(ctx context.Context, p ec2Placement) {
	if p.volumeID == "" {
		return
	}
	e.ifOwned(ctx, p, func(c context.Context) {
		if _, err := e.ec2.DetachVolume(c, &ec2.DetachVolumeInput{
			VolumeId:   aws.String(p.volumeID),
			InstanceId: aws.String(p.instanceID),
			Force:      aws.Bool(true),
		}); err != nil {
			log.Printf("ecs-ec2: force-detaching %s from the lost slot %s: %v", p.volumeID, p.instanceID, err)
		}
	})
}

// waitTaskENIsGone polls until ECS has taken its task ENIs off the instance. Same check
// the sweeper makes before stopping a slot, and for the same reason (decision 3-3).
func (e *ecsEC2Runtime) waitTaskENIsGone(ctx context.Context, instanceID string) error {
	return e.waitTaskENIsGoneWithin(ctx, instanceID, 60)
}

// waitTaskENIsGoneWithin polls every 2s, at most `polls` times.
func (e *ecsEC2Runtime) waitTaskENIsGoneWithin(ctx context.Context, instanceID string, polls int) error {
	for i := 0; i < polls; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err == nil && !instancesHoldTaskENI(out) {
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("task ENIs still attached to %s", instanceID)
}

func (e *ecsEC2Runtime) waitInstanceStopped(ctx context.Context, instanceID string) error {
	for i := 0; i < 90; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err == nil {
			for _, r := range out.Reservations {
				for _, inst := range r.Instances {
					if inst.State != nil && (inst.State.Name == ec2types.InstanceStateNameStopped ||
						inst.State.Name == ec2types.InstanceStateNameTerminated) {
						return nil
					}
				}
			}
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("slot %s did not reach `stopped`", instanceID)
}

// instancesHoldTaskENI reports whether any listed instance still carries an ENI that ECS
// attached for a task. The description is the only marker AWS gives us — the sweeper's
// taskENIsAttached has read it this way since decision 3-3 — so both callers share it rather
// than keeping two copies of the same string match.
func instancesHoldTaskENI(out *ec2.DescribeInstancesOutput) bool {
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			for _, ni := range inst.NetworkInterfaces {
				if strings.Contains(aws.ToString(ni.Description), ":ecs:") {
					return true
				}
			}
		}
	}
	return false
}
