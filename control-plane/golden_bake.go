// golden_bake.go — keeping the pool's golden snapshot in step with the workspace
// image, without anybody remembering to (ADR 0045 決定 9 / docs/64 §64.28).
//
// The manual procedure works — §64.28 got one baked on a real deployment — but it is
// a procedure, and the thing it guards against is a release nobody re-baked for. The
// CP already knows when a golden has gone stale, because it is the CP that refuses to
// use it; this turns that knowledge into the action it implies.
//
// # Shape
//
// The same shape as hibernation, for the same reasons (ADR 0012): **one step per tick,
// with the state in AWS.** A bake spans minutes — a seed boot, a snapshot, a probe boot
// — and nothing may sit on a loop waiting for it. Every step is a no-op when it has
// already happened, so a CP that restarts half way through resumes rather than starting
// over, and never strands a slot.
//
//	no candidate, no golden   → make sure the seed is up, let it finish boot-install
//	seed booted               → stop it, release its slot, snapshot the home
//	candidate completed       → boot a PROBE from it — a membership that has never
//	                            existed before, so it gets a fresh keep volume
//	probe healthy             → promote the candidate to golden, delete the old one,
//	                            destroy the seed and the probe
//	probe never came up       → reject the candidate; new homes stay empty, which is
//	                            slow but correct, and the reason is on the snapshot
//
// # Why the probe is not optional, and why it must be a NEW membership
//
// A golden whose home cannot boot looks like a total success right up to "snapshot
// completed"; what breaks is the next new user, and the only symptom is a task that
// restarts forever (§64.28.3, measured — the first golden ever baked this way was
// unbootable). So nothing is published until something has actually started from it.
//
// It has to be a membership with **no history**, because the failure that made this
// rule exists only there: the seed's identity dirs already exist on ITS keep volume, so
// re-booting the seed from its own golden proves nothing. Destroying the probe's
// workspace takes its EFS access points with it, so the next probe genuinely starts
// from nothing.
//
// # What it will not do
//
//   - take the last slots (bakeBlocked). A missing golden costs a slow first start.
//     Evicting somebody to avoid that is not a trade this is allowed to make.
//   - re-bake an image it has already failed on twice (rejectedAttempts).
//   - run anywhere but ecs-ec2, or when AF_ECS_EC2_GOLDEN_AUTOBAKE=0.
package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// The reserved tenant and the two reserved members. They are ordinary rows — the
// workspace has to be built by the ordinary code path or the golden would be a copy of
// something the product does not actually produce (which is the mistake bake-golden.sh
// warns about in its own header) — but they are named so that nobody mistakes them for
// people, and they live in their own tenant so they never appear in anyone's picker.
const (
	goldenTenantSlug = "af-golden"
	goldenTenantName = "golden snapshot (system)"
	goldenSeedKey    = "af-golden-seed"
	goldenProbeKey   = "af-golden-probe"
)

// goldenBaker drives the series above. One per CP; the reaper ticks it.
type goldenBaker struct {
	mgr   *manager
	pool  goldenBakePool
	wsAPI workspaceAPI
	// seedBudget bounds "the seed should have booted by now" and probeBudget the same
	// for the probe. Generous on purpose: the number they are compared against is a
	// cold EC2 launch plus an image pull (127s measured, §64.28.4), and failing a bake
	// because a slot was slow would burn one of the two rejectedAttempts.
	seedBudget  time.Duration
	probeBudget time.Duration
	now         func() time.Time
	// loggedBlocked keeps "the pool is full" to one line per reason instead of one per
	// tick — the reaper runs every minute and this can be true for hours.
	loggedBlocked string
}

func newGoldenBaker(mgr *manager, pool goldenBakePool) *goldenBaker {
	return &goldenBaker{
		mgr:         mgr,
		pool:        pool,
		wsAPI:       newWorkspaceAPI(mgr, true),
		seedBudget:  20 * time.Minute,
		probeBudget: 20 * time.Minute,
		now:         time.Now,
	}
}

// goldenBakerFor returns a baker when this deployment can have one, or nil. The nil is
// the normal answer everywhere except ecs-ec2, and callers check it rather than the
// runtime profile string — the capability is the condition.
func goldenBakerFor(mgr *manager, enabled bool) *goldenBaker {
	if !enabled {
		return nil
	}
	pool, ok := mgr.rtFactory.(goldenBakePool)
	if !ok {
		return nil
	}
	return newGoldenBaker(mgr, pool)
}

// run ticks the state machine. It has its OWN ticker rather than riding the idle-stop
// reaper's: the reaper can be switched off deployment-wide (AF_IDLE_SWEEP_INTERVAL=0),
// and "the operator turned off idle-stop" is not a statement about golden snapshots.
// A feature that quietly stops working because of an unrelated switch is a feature
// nobody can rely on.
func (b *goldenBaker) run(ctx context.Context, every time.Duration) {
	log.Printf("golden: auto-bake on (pool=%s image=%s, checking every %s)",
		b.pool.poolLabel(), b.pool.workspaceImage(), every)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.step(ctx)
		}
	}
}

// step advances the bake by at most one move. Errors are logged and dropped: a bake is
// housekeeping, and there is no caller who could do anything about a failed step that
// the next tick will not do anyway.
func (b *goldenBaker) step(ctx context.Context) {
	image := b.pool.workspaceImage()

	// 1. Already published and current? Then the only thing left is to make sure a
	//    previous round did not leave its seed or probe behind (a CP that died between
	//    "promote" and "destroy" would otherwise pay for two homes forever).
	if _, ok, err := b.pool.goldenFor(ctx, ec2RoleGolden); err != nil {
		log.Printf("golden: reading the published golden failed: %v", err)
		return
	} else if ok {
		b.tidy(ctx)
		return
	}

	// 2. Have we already burned this image? Twice is the give-up point: once can be an
	//    AZ having a bad day, twice is the image.
	if n, err := b.pool.rejectedAttempts(ctx); err != nil {
		log.Printf("golden: counting rejected candidates failed: %v", err)
		return
	} else if n >= 2 {
		b.tidy(ctx)
		return
	}

	cand, haveCand, err := b.pool.goldenFor(ctx, ec2RoleGoldenCandidate)
	if err != nil {
		log.Printf("golden: reading the candidate failed: %v", err)
		return
	}
	if haveCand {
		b.verify(ctx, cand, image)
		return
	}
	b.bake(ctx, image)
}

// --- baking ---------------------------------------------------------------------

func (b *goldenBaker) bake(ctx context.Context, image string) {
	// The capacity gate belongs HERE and only here: once a seed exists, abandoning it
	// because the pool filled up would leave its slot held across every later tick.
	seedRes, seedExists, err := b.existing(ctx, goldenSeedKey)
	if err != nil {
		log.Printf("golden: resolving the seed failed: %v", err)
		return
	}
	if !seedExists {
		blocked, why, err := b.pool.bakeBlocked(ctx)
		if err != nil {
			log.Printf("golden: reading the pool failed: %v", err)
			return
		}
		if blocked {
			if b.loggedBlocked != why {
				log.Printf("golden: no golden for %s, but not baking one now — %s", image, why)
				b.loggedBlocked = why
			}
			return
		}
		b.loggedBlocked = ""
		log.Printf("golden: no golden for %s; baking one (ADR 0045 決定 9)", image)
		seedRes, err = b.create(ctx, goldenSeedKey)
		if err != nil {
			log.Printf("golden: creating the seed workspace failed: %v", err)
			return
		}
	}
	seed, ok := seedRes.rt.(goldenSeedRuntime)
	if !ok {
		return // not ecs-ec2 after all; goldenBakerFor should have prevented this
	}

	home, err := seed.homeForBake(ctx)
	if err != nil {
		log.Printf("golden: reading the seed's home failed: %v", err)
		return
	}
	// The seed took too long to get anywhere. Tear it down rather than hold a slot;
	// the next tick starts a clean one, and rejectedAttempts is untouched because
	// nothing was baked — this is not evidence about the image.
	if home.VolumeID != "" && !home.Baked && b.expired(home.Created, b.seedBudget) {
		log.Printf("golden: the seed did not finish booting within %s; tearing it down and retrying later", b.seedBudget)
		b.destroy(ctx, goldenSeedKey)
		return
	}

	switch {
	case !home.Baked:
		// Still needs to boot (or has never been started). ensureWorkspaceStarted is a
		// no-op on a workspace that is already running, so this is safe every tick.
		if state := seedRes.rt.State(ctx); state == "running" {
			if _, err := b.mgr.agentSessions(ctx, seedRes.rt); err != nil {
				return // still coming up; the deadline above is what bounds this
			}
			if home.VolumeID == "" {
				return // running but no home yet visible — next tick
			}
			if err := seed.markHomeBaked(ctx, home.VolumeID); err != nil {
				log.Printf("golden: marking the seed's home baked failed: %v", err)
				return
			}
			log.Printf("golden: the seed finished boot-install; stopping it to capture the home")
			b.stop(ctx, seedRes)
			return
		}
		if aerr := b.wsAPI.ensureWorkspaceStartedRT(ctx, seedRes, seedRes.rt); aerr != nil {
			log.Printf("golden: starting the seed failed: %s", aerr.message)
		}
	case !home.Capturable:
		// Booted and stopped, but the home is still on a running slot — a Stop keeps it
		// there on purpose (§64.17), and nothing in the ordinary lifecycle ever takes it
		// off. In the CP that is one call rather than the operator's 15-minute wait.
		if state := seedRes.rt.State(ctx); state == "running" || state == "starting" {
			b.stop(ctx, seedRes)
			return
		}
		log.Printf("golden: releasing the seed's slot before the snapshot")
		if err := seed.releaseForBake(ctx); err != nil {
			log.Printf("golden: releasing the seed's slot failed: %v", err)
		}
	default:
		if _, err := b.pool.snapshotHome(ctx, home.VolumeID, seedRes.ws.ContainerName); err != nil {
			log.Printf("golden: snapshotting the seed's home failed: %v", err)
		}
	}
}

// --- verifying ------------------------------------------------------------------

func (b *goldenBaker) verify(ctx context.Context, cand goldenSnap, image string) {
	if cand.Failed {
		b.reject(ctx, cand, "the snapshot itself failed")
		return
	}
	if !cand.Completed {
		return // EBS is still copying blocks; nothing to do but wait
	}
	probeRes, exists, err := b.existing(ctx, goldenProbeKey)
	if err != nil {
		log.Printf("golden: resolving the probe failed: %v", err)
		return
	}
	if !exists {
		log.Printf("golden: %s is baked; booting a probe from it before publishing it", cand.ID)
		probeRes, err = b.create(ctx, goldenProbeKey)
		if err != nil {
			log.Printf("golden: creating the probe workspace failed: %v", err)
			return
		}
	}
	// ★ Every tick, and before ANY start. Not once at creation: destroying the previous
	// probe evicted its memoized runtime (destroyWorkspaceByMembership →
	// evictMembershipCache), so what buildResolved hands back here is a FRESH runtime
	// with seedRole cleared. A probe that silently read the published golden — or, with
	// none published, an empty home — would come up perfectly and prove nothing, which
	// is the same "looks fine, tested nothing" shape this phase exists to remove.
	seed, ok := probeRes.rt.(goldenSeedRuntime)
	if !ok {
		return
	}
	seed.seedFromCandidate()

	if state := probeRes.rt.State(ctx); state == "running" {
		if _, err := b.mgr.agentSessions(ctx, probeRes.rt); err == nil {
			b.publish(ctx, cand, image)
			return
		}
	}
	// Not healthy yet. The deadline is anchored on the candidate's own af-bake-started,
	// so a CP restart in the middle does not hand the probe a fresh budget.
	if b.expired(cand.Started, b.probeBudget) {
		b.reject(ctx, cand, "a workspace built from it did not come up within "+b.probeBudget.String())
		return
	}
	if aerr := b.wsAPI.ensureWorkspaceStartedRT(ctx, probeRes, probeRes.rt); aerr != nil {
		log.Printf("golden: starting the probe failed: %s", aerr.message)
	}
}

func (b *goldenBaker) publish(ctx context.Context, cand goldenSnap, image string) {
	if err := b.pool.setGoldenRole(ctx, cand.ID, ec2RoleGolden, ""); err != nil {
		log.Printf("golden: publishing %s failed: %v", cand.ID, err)
		return
	}
	log.Printf("golden: %s is now the golden for %s — a probe started from it cleanly", cand.ID, image)
	// Only after the promotion succeeded: deleting the old one first would leave the
	// pool with no golden at all if the CreateTags above had failed.
	if err := b.pool.dropSupersededGoldens(ctx, cand.ID); err != nil {
		log.Printf("golden: dropping the superseded golden failed: %v", err)
	}
	b.tidy(ctx)
}

// reject leaves the candidate in place under a role nothing seeds from. It is not
// deleted: it is the answer to "why does this deployment have no golden", and the CP
// log line scrolls away long before an operator goes looking.
func (b *goldenBaker) reject(ctx context.Context, cand goldenSnap, reason string) {
	if err := b.pool.setGoldenRole(ctx, cand.ID, ec2RoleGoldenRejected, reason); err != nil {
		log.Printf("golden: rejecting %s failed: %v", cand.ID, err)
		return
	}
	log.Printf("golden: REJECTED the candidate %s — %s. New homes stay empty (slow first start, "+
		"nothing broken) until this is looked at; the reason is on the snapshot as %s.",
		cand.ID, reason, ec2TagBakeReason)
	b.tidy(ctx)
}

// --- shared plumbing --------------------------------------------------------------

// tidy removes the seed and the probe. Called on every terminal outcome AND on the
// happy no-op path, because "the CP died right after promoting" leaves exactly the same
// mess as "the CP died right after rejecting".
func (b *goldenBaker) tidy(ctx context.Context) {
	b.destroy(ctx, goldenSeedKey)
	b.destroy(ctx, goldenProbeKey)
}

func (b *goldenBaker) destroy(ctx context.Context, key string) {
	mem, ok, err := b.membership(ctx, key, false)
	if err != nil || !ok {
		return
	}
	if _, ok, err := b.mgr.store.GetWorkspaceByMembership(ctx, mem.MembershipID); err != nil || !ok {
		return
	}
	// The membership row itself stays. What has to be fresh for the next round is the
	// WORKSPACE — destroying it takes the home volume and both EFS access points with
	// it, which is precisely what makes the next probe a genuine new user.
	if _, err := b.mgr.destroyWorkspaceByMembership(ctx, mem.MembershipID); err != nil {
		log.Printf("golden: destroying %s failed: %v", key, err)
		return
	}
	log.Printf("golden: destroyed the %s workspace", key)
}

func (b *goldenBaker) stop(ctx context.Context, res *resolved) {
	if err := res.rt.Stop(ctx); err != nil {
		log.Printf("golden: stopping %s failed: %v", res.ws.ContainerName, err)
		return
	}
	_ = b.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "stopped")
}

// existing resolves one of the reserved workspaces WITHOUT creating it, so that the
// capacity gate can run before anything exists.
func (b *goldenBaker) existing(ctx context.Context, key string) (*resolved, bool, error) {
	mem, ok, err := b.membership(ctx, key, false)
	if err != nil || !ok {
		return nil, false, err
	}
	if _, ok, err := b.mgr.store.GetWorkspaceByMembership(ctx, mem.MembershipID); err != nil || !ok {
		return nil, false, err
	}
	res, err := b.create(ctx, key) // the workspace exists; this only builds the runtime
	return res, err == nil, err
}

func (b *goldenBaker) create(ctx context.Context, key string) (*resolved, error) {
	mem, _, err := b.membership(ctx, key, true)
	if err != nil {
		return nil, err
	}
	ident, ok, err := b.mgr.store.GetIdentityByUserKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("the reserved identity %s vanished between creating it and reading it back", key)
	}
	res, aerr := b.mgr.buildResolved(ctx, ident, mem)
	if aerr != nil {
		return nil, fmt.Errorf("build the %s workspace: %s", key, aerr.message)
	}
	return res, nil
}

// membership finds (or, with create, makes) one of the reserved memberships. The
// identity carries NO email: an address here would be a person who does not exist, and
// roleHintFor matches SUPER_ADMIN_EMAILS on the address alone.
func (b *goldenBaker) membership(ctx context.Context, key string, create bool) (MembershipView, bool, error) {
	t, ok, err := b.mgr.store.GetTenantBySlug(ctx, goldenTenantSlug)
	if err != nil {
		return MembershipView{}, false, err
	}
	if !ok {
		if !create {
			return MembershipView{}, false, nil
		}
		if t, err = b.mgr.store.CreateTenant(ctx, goldenTenantSlug, goldenTenantName); err != nil {
			return MembershipView{}, false, err
		}
	}
	ident, ok, err := b.mgr.store.GetIdentityByUserKey(ctx, key)
	if err != nil {
		return MembershipView{}, false, err
	}
	if !ok {
		if !create {
			return MembershipView{}, false, nil
		}
		if ident, err = b.mgr.store.UpsertIdentity(ctx, "", key, ""); err != nil {
			return MembershipView{}, false, err
		}
	}
	mem, ok, err := b.mgr.store.GetMembership(ctx, ident.ID, t.ID)
	if err != nil {
		return MembershipView{}, false, err
	}
	if !ok {
		if !create {
			return MembershipView{}, false, nil
		}
		if mem, err = b.mgr.store.EnsureMembership(ctx, ident.ID, t.ID, "member"); err != nil {
			return MembershipView{}, false, err
		}
	}
	if mem.Status != "active" && create {
		if err := b.mgr.store.SetMembershipStatus(ctx, mem.ID, "active"); err != nil {
			return MembershipView{}, false, err
		}
	}
	return MembershipView{
		MembershipID: mem.ID, TenantID: t.ID, TenantSlug: t.Slug,
		TenantName: t.Name, Role: mem.Role,
	}, true, nil
}

func (b *goldenBaker) expired(since time.Time, budget time.Duration) bool {
	// A zero anchor means "we cannot tell how long this has been going", and treating
	// that as expired would tear down a bake that just started. Wait for a real one.
	return !since.IsZero() && b.now().Sub(since) > budget
}
