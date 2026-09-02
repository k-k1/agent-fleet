// golden_bake.go — keeping the pool's golden snapshot in step with the workspace
// image, without anybody remembering to (ADR 0045 決定 9 / docs/log/64 §64.28).
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
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// The reserved tenant and the two reserved members. They are ordinary rows — the
// workspace has to be built by the ordinary code path or the golden would be a copy of
// something the product does not actually produce (which is the mistake bake-golden.sh
// warns about in its own header) — but they are named so that nobody mistakes them for
// people, and they live in their own tenant so they never appear in anyone's picker.
const (
	goldenTenantSlug = "af-golden"
	goldenTenantName = "golden snapshot (system)"
	// The two reserved member keys are declared by the adapters' package: the ecs-ec2
	// golden reader matches AWS resources against them, and it moved there.
	goldenSeedKey  = runtime.GoldenSeedKey
	goldenProbeKey = runtime.GoldenProbeKey
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
	// warned does the same for the cleanup path, per reserved key: see warn.
	warned map[string]string
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
		b.pool.PoolLabel(), b.pool.WorkspaceImage(), every)
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

// seedKey / probeKey name the reserved workspace for one architecture.
//
// ⚠️ x86_64 keeps the ORIGINAL, unsuffixed keys. Every deployment that has ever baked
// a golden has an af-golden-seed membership, and renaming it would orphan that row
// (and its home volume) with nothing left pointing at it — a workspace nobody can see
// and nothing will ever clean up.
func (b *goldenBaker) seedKey(arch string) string  { return archKey(goldenSeedKey, arch) }
func (b *goldenBaker) probeKey(arch string) string { return archKey(goldenProbeKey, arch) }

// archKey is declared by the adapters' package for the same reason as the two keys
// above — the golden reader on the ecs-ec2 side derives the same names from AWS tags.
var archKey = runtime.ArchKey

// step advances the bake by at most one move PER ARCHITECTURE. Errors are logged and
// dropped: a bake is housekeeping, and there is no caller who could do anything about
// a failed step that the next tick will not do anyway.
//
// The architectures do not need explicit serialisation: bakeBlocked counts the whole
// pool, so the second arch's seed simply is not created while the first one holds a
// slot, and it starts on a later tick.
func (b *goldenBaker) step(ctx context.Context) {
	arches := b.pool.BakeArches()
	if len(arches) == 0 {
		arches = []string{ec2ArchX86}
	}
	for _, arch := range arches {
		b.stepArch(ctx, arch)
	}
}

func (b *goldenBaker) stepArch(ctx context.Context, arch string) {
	image := b.pool.WorkspaceImage()

	// 1. Already published and current? Then the only thing left is to make sure a
	//    previous round did not leave its seed or probe behind (a CP that died between
	//    "promote" and "destroy" would otherwise pay for two homes forever).
	if _, ok, err := b.pool.GoldenFor(ctx, ec2RoleGolden, arch); err != nil {
		log.Printf("golden[%s]: reading the published golden failed: %v", arch, err)
		return
	} else if ok {
		b.tidy(ctx, arch)
		return
	}

	// 2. Have we already burned this image? Twice is the give-up point: once can be an
	//    AZ having a bad day, twice is the image.
	if n, err := b.pool.RejectedAttempts(ctx, arch); err != nil {
		log.Printf("golden[%s]: counting rejected candidates failed: %v", arch, err)
		return
	} else if n >= 2 {
		b.tidy(ctx, arch)
		return
	}

	cand, haveCand, err := b.pool.GoldenFor(ctx, ec2RoleGoldenCandidate, arch)
	if err != nil {
		log.Printf("golden[%s]: reading the candidate failed: %v", arch, err)
		return
	}
	if haveCand {
		b.verify(ctx, cand, image, arch)
		return
	}
	b.bake(ctx, image, arch)
}

// --- baking ---------------------------------------------------------------------

func (b *goldenBaker) bake(ctx context.Context, image, arch string) {
	// The capacity gate belongs HERE and only here: once a seed exists, abandoning it
	// because the pool filled up would leave its slot held across every later tick.
	seedRes, seedExists, err := b.existing(ctx, b.seedKey(arch), arch)
	if err != nil {
		log.Printf("golden[%s]: resolving the seed failed: %v", arch, err)
		return
	}
	if !seedExists {
		blocked, why, err := b.pool.BakeBlocked(ctx)
		if err != nil {
			log.Printf("golden: reading the pool failed: %v", err)
			return
		}
		if blocked {
			if b.loggedBlocked != why {
				log.Printf("golden[%s]: no golden for %s, but not baking one now — %s", arch, image, why)
				b.loggedBlocked = why
			}
			return
		}
		b.loggedBlocked = ""
		log.Printf("golden[%s]: no golden for %s; baking one (ADR 0045 決定 9)", arch, image)
		seedRes, err = b.create(ctx, b.seedKey(arch), arch)
		if err != nil {
			log.Printf("golden[%s]: creating the seed workspace failed: %v", arch, err)
			return
		}
	}
	seed, ok := seedRes.rt.(goldenSeedRuntime)
	if !ok {
		return // not ecs-ec2 after all; goldenBakerFor should have prevented this
	}

	home, err := seed.HomeForBake(ctx)
	if err != nil {
		log.Printf("golden[%s]: reading the seed's home failed: %v", arch, err)
		return
	}
	// The seed took too long to get anywhere. Tear it down rather than hold a slot;
	// the next tick starts a clean one, and rejectedAttempts is untouched because
	// nothing was baked — this is not evidence about the image.
	//
	// ★ Two anchors, because there are two ways to be stuck and only one of them has a
	// volume to date. A Start that fails BEFORE createHomeVolume (no capacity, no slot,
	// an AWS error) leaves a workspace row and nothing else, and measuring from a volume
	// that does not exist would retry that forever — a seed row nobody ever cleans up,
	// re-Started once a minute for the life of the deployment.
	since := home.Created
	if since.IsZero() {
		since, _ = time.Parse(time.RFC3339, seedRes.ws.CreatedAt)
	}
	if !home.Baked && b.expired(since, b.seedBudget) {
		log.Printf("golden[%s]: the seed did not finish booting within %s; tearing it down and retrying later", arch, b.seedBudget)
		b.destroy(ctx, b.seedKey(arch))
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
			if err := seed.MarkHomeBaked(ctx, home.VolumeID); err != nil {
				log.Printf("golden[%s]: marking the seed's home baked failed: %v", arch, err)
				return
			}
			log.Printf("golden[%s]: the seed finished boot-install; stopping it to capture the home", arch)
			b.stop(ctx, seedRes)
			return
		}
		if aerr := b.wsAPI.ensureWorkspaceStartedRT(ctx, seedRes, seedRes.rt); aerr != nil {
			log.Printf("golden[%s]: starting the seed failed: %s", arch, aerr.message)
		}
	case !home.Capturable:
		// Booted and stopped, but the home is still on a running slot — a Stop keeps it
		// there on purpose (§64.17), and nothing in the ordinary lifecycle ever takes it
		// off. In the CP that is one call rather than the operator's 15-minute wait.
		if state := seedRes.rt.State(ctx); state == "running" || state == "starting" {
			b.stop(ctx, seedRes)
			return
		}
		log.Printf("golden[%s]: releasing the seed's slot before the snapshot", arch)
		if err := seed.ReleaseForBake(ctx); err != nil {
			log.Printf("golden[%s]: releasing the seed's slot failed: %v", arch, err)
		}
	default:
		if _, err := b.pool.SnapshotHome(ctx, home.VolumeID, seedRes.ws.ContainerName, arch); err != nil {
			log.Printf("golden[%s]: snapshotting the seed's home failed: %v", arch, err)
		}
	}
}

// --- verifying ------------------------------------------------------------------

func (b *goldenBaker) verify(ctx context.Context, cand goldenSnap, image, arch string) {
	if cand.Failed {
		b.reject(ctx, cand, "the snapshot itself failed", arch)
		return
	}
	if !cand.Completed {
		return // EBS is still copying blocks; nothing to do but wait
	}
	probeRes, exists, err := b.existing(ctx, b.probeKey(arch), arch)
	if err != nil {
		log.Printf("golden[%s]: resolving the probe failed: %v", arch, err)
		return
	}
	if !exists {
		log.Printf("golden[%s]: %s is baked; booting a probe from it before publishing it", arch, cand.ID)
		probeRes, err = b.create(ctx, b.probeKey(arch), arch)
		if err != nil {
			log.Printf("golden[%s]: creating the probe workspace failed: %v", arch, err)
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
	seed.SeedFromCandidate()

	if state := probeRes.rt.State(ctx); state == "running" {
		if _, err := b.mgr.agentSessions(ctx, probeRes.rt); err == nil {
			b.publish(ctx, cand, image, arch)
			return
		}
	}
	// Not healthy yet. The deadline is anchored on the candidate's own af-bake-started,
	// so a CP restart in the middle does not hand the probe a fresh budget.
	if b.expired(cand.Started, b.probeBudget) {
		b.reject(ctx, cand, "a workspace built from it did not come up within "+b.probeBudget.String(), arch)
		return
	}
	if aerr := b.wsAPI.ensureWorkspaceStartedRT(ctx, probeRes, probeRes.rt); aerr != nil {
		log.Printf("golden[%s]: starting the probe failed: %s", arch, aerr.message)
	}
}

func (b *goldenBaker) publish(ctx context.Context, cand goldenSnap, image, arch string) {
	if err := b.pool.SetGoldenRole(ctx, cand.ID, ec2RoleGolden, ""); err != nil {
		log.Printf("golden[%s]: publishing %s failed: %v", arch, cand.ID, err)
		return
	}
	log.Printf("golden[%s]: %s is now the golden for %s — a probe started from it cleanly", arch, cand.ID, image)
	// Only after the promotion succeeded: deleting the old one first would leave the
	// pool with no golden at all if the CreateTags above had failed.
	if err := b.pool.DropSupersededGoldens(ctx, cand.ID, arch); err != nil {
		log.Printf("golden[%s]: dropping the superseded golden failed: %v", arch, err)
	}
	b.tidy(ctx, arch)
}

// reject leaves the candidate in place under a role nothing seeds from. It is not
// deleted: it is the answer to "why does this deployment have no golden", and the CP
// log line scrolls away long before an operator goes looking.
func (b *goldenBaker) reject(ctx context.Context, cand goldenSnap, reason, arch string) {
	if err := b.pool.SetGoldenRole(ctx, cand.ID, ec2RoleGoldenRejected, reason); err != nil {
		log.Printf("golden[%s]: rejecting %s failed: %v", arch, cand.ID, err)
		return
	}
	log.Printf("golden[%s]: REJECTED the candidate %s — %s. New %s homes stay empty (slow first start, "+
		"nothing broken) until this is looked at; the reason is on the snapshot as %s.",
		arch, cand.ID, reason, arch, ec2TagBakeReason)
	b.tidy(ctx, arch)
}

// --- shared plumbing --------------------------------------------------------------

// tidy removes the seed and the probe. Called on every terminal outcome AND on the
// happy no-op path, because "the CP died right after promoting" leaves exactly the same
// mess as "the CP died right after rejecting".
func (b *goldenBaker) tidy(ctx context.Context, arch string) {
	b.destroy(ctx, b.seedKey(arch))
	b.destroy(ctx, b.probeKey(arch))
}

// destroy removes one reserved workspace. Every way out of it says which one it took:
// "the database has nothing to destroy" and "the database could not be read" used to
// both be a bare return, so a tidy that cleaned NOTHING and a tidy that had nothing to
// clean produced the same log — no line at all. That is how a leaked seed stayed
// invisible on a real deployment (docs/log/64 §64.29.5): the probe's line was printed, the
// seed's absence was not, and its service and 50 GiB home billed on.
func (b *goldenBaker) destroy(ctx context.Context, key string) {
	mem, ok, err := b.membership(ctx, key, false)
	if err != nil {
		b.warn(key, "golden: looking up %s to clean it up failed: %v", key, err)
		return
	}
	if !ok {
		b.sweep(ctx, key) // no row can point at AWS any more; go by the tags
		return
	}
	if _, ok, err := b.mgr.store.GetWorkspaceByMembership(ctx, mem.MembershipID); err != nil {
		b.warn(key, "golden: reading the %s workspace to clean it up failed: %v", key, err)
		return
	} else if !ok {
		b.sweep(ctx, key)
		return
	}
	// The membership row itself stays. What has to be fresh for the next round is the
	// WORKSPACE — destroying it takes the home volume and both EFS access points with
	// it, which is precisely what makes the next probe a genuine new user.
	if _, err := b.mgr.destroyWorkspaceByMembership(ctx, mem.MembershipID); err != nil {
		log.Printf("golden: destroying %s failed: %v", key, err)
		return
	}
	b.quiet(key)
	log.Printf("golden: destroyed the %s workspace", key)
}

// sweep is the cleanup for the case destroy cannot handle: the database has no
// workspace to destroy, but the SERVICE and the HOME VOLUME of one are still in AWS.
//
// They are reachable only by tag, and the ordinary drift sweeper deliberately never
// deletes a home — a detached home is what every stopped workspace looks like. So the
// reserved bake workspaces need their own pass, and it is safe precisely because of the
// two conditions that hold here and nowhere else: the name is one this file reserves,
// and the database has just said nobody owns it.
//
// Silent when there is nothing to remove: tidy runs on every tick once a golden is
// current, and a line a minute for "still nothing" is how a log stops being read.
func (b *goldenBaker) sweep(ctx context.Context, key string) {
	name, _, _ := b.mgr.workspaceNames(goldenTenantSlug, key)
	removed, err := b.pool.SweepOrphans(ctx, name)
	if err != nil {
		b.warn(key, "golden: cleaning up what is left of %s failed: %v", name, err)
		return
	}
	b.quiet(key)
	if len(removed) > 0 {
		log.Printf("golden: %s had no workspace row but was still in AWS; removed %s",
			name, strings.Join(removed, ", "))
	}
}

// warn logs a repeating failure once per distinct message. tidy runs every minute in
// the steady state, and a cleanup that keeps failing for the same reason must be
// visible without printing 1440 identical lines a day.
func (b *goldenBaker) warn(key, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if b.warned == nil {
		b.warned = map[string]string{}
	}
	if b.warned[key] == msg {
		return
	}
	b.warned[key] = msg
	log.Print(msg)
}

// quiet forgets the last warning for a key, so the NEXT failure is printed even when it
// reads the same as one that has since been resolved.
func (b *goldenBaker) quiet(key string) { delete(b.warned, key) }

func (b *goldenBaker) stop(ctx context.Context, res *resolved) {
	if err := res.rt.Stop(ctx); err != nil {
		log.Printf("golden: stopping %s failed: %v", res.ws.ContainerName, err)
		return
	}
	_ = b.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "stopped")
}

// existing resolves one of the reserved workspaces WITHOUT creating it, so that the
// capacity gate can run before anything exists.
func (b *goldenBaker) existing(ctx context.Context, key, arch string) (*resolved, bool, error) {
	mem, ok, err := b.membership(ctx, key, false)
	if err != nil || !ok {
		return nil, false, err
	}
	if _, ok, err := b.mgr.store.GetWorkspaceByMembership(ctx, mem.MembershipID); err != nil || !ok {
		return nil, false, err
	}
	res, err := b.create(ctx, key, arch) // the workspace exists; this only builds the runtime
	return res, err == nil, err
}

func (b *goldenBaker) create(ctx context.Context, key, arch string) (*resolved, error) {
	mem, _, err := b.membership(ctx, key, true)
	if err != nil {
		return nil, err
	}
	// Pin the reserved member to a class on the architecture being baked, BEFORE the
	// runtime is built — buildResolved reads the quota and then memoizes the runtime,
	// so a class written afterwards would not reach this round's seed.
	//
	// It is written on every pass rather than once at creation: the operator can
	// rename or drop a class at any redeploy, and a seed left pinned to a class that
	// no longer exists would quietly bake on the default architecture instead — i.e.
	// publish an x86_64 home as the arm64 golden.
	if class := b.pool.SeedClassFor(arch); class != "" {
		cur, _, err := b.mgr.store.GetUserLimit(ctx, mem.MembershipID)
		if err != nil {
			return nil, err
		}
		if cur.SlotClass != class {
			cur.SlotClass = class
			if err := b.mgr.store.PutUserLimit(ctx, mem.MembershipID, cur.UserQuota); err != nil {
				return nil, err
			}
			b.mgr.evictMembershipCache(mem.MembershipID)
		}
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
