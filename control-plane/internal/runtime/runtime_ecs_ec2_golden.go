// runtime_ecs_ec2_golden.go — the `ecs-ec2` half of the automatic golden bake
// (ADR 0045 決定 9 / docs/log/64 §64.28). The state machine itself lives in
// golden_bake.go and knows nothing about EC2; what is here is the two narrow
// capabilities it needs, implemented once, on the adapter that owns the tags.
//
// Everything below follows hibernation's discipline: **the state lives in AWS**, not
// in the CP (ADR 0012). A bake is a series of steps, each of which is a no-op when it
// has already happened, so a CP that dies half way through resumes on the next tick
// instead of stranding a slot, a volume or a snapshot nobody will ever look at again.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// GoldenBakePool is what the baker needs from the deployment's runtime profile. Only
// ecs-ec2 implements it; on every other profile the baker simply never runs, which is
// correct — nothing else seeds new homes from a shared snapshot.
type GoldenBakePool interface {
	// WorkspaceImage is the image string a golden must be stamped with to be usable.
	WorkspaceImage() string
	// PoolLabel identifies the slot pool, for logs.
	PoolLabel() string
	// BakeArches lists the CPU architectures this deployment needs a golden for —
	// one per distinct architecture among the declared slot classes, the default
	// class's first. A golden is a home full of BINARIES, so there is no such thing
	// as one golden for two architectures (docs/log/70 §70.6).
	BakeArches() []string
	// GoldenFor returns the newest COMPLETED snapshot carrying this role for the
	// running image AND this architecture, plus anything still pending.
	// ("", false) means neither.
	GoldenFor(ctx context.Context, role, arch string) (snap GoldenSnap, found bool, err error)
	// BakeBlocked reports that starting a NEW bake would take the last slot from a
	// real user. It gates only the first step: abandoning a bake half way through
	// would strand the seed's slot, which is the opposite of the intent.
	BakeBlocked(ctx context.Context) (bool, string, error)
	// SnapshotHome captures a detached-or-stopped home as a golden CANDIDATE for arch.
	SnapshotHome(ctx context.Context, volumeID, workspace, arch string) (string, error)
	// SetGoldenRole moves a snapshot between the golden roles (candidate → golden on a
	// passing probe, candidate → rejected on a failing one). reason is recorded when
	// non-empty.
	SetGoldenRole(ctx context.Context, snapshotID, role, reason string) error
	// DropSupersededGoldens deletes every published golden AND every leftover candidate
	// OF THIS ARCHITECTURE except keepID. Called only after a candidate has been
	// promoted, so the pool never pays for two — and so that a second CP replica that
	// baked its own candidate in the same window does not leave it billing forever.
	//
	// ⚠️ The arch scope is load-bearing, not tidiness: without it, publishing the arm64
	// golden would delete the x86_64 one, and every new x86_64 member would silently
	// go back to an empty home.
	DropSupersededGoldens(ctx context.Context, keepID, arch string) error
	// RejectedAttempts counts the candidates this image has already burned. It is the
	// give-up counter: a bake that fails for a reason that is not going to change
	// (an image whose home genuinely cannot boot) must stop taking a slot every tick.
	RejectedAttempts(ctx context.Context, arch string) (int, error)
	// SeedClassFor is a slot class that runs on arch, for the seed and probe
	// workspaces to be placed with. "" when the deployment declared no classes.
	SeedClassFor(arch string) string
	// SweepOrphans deletes the AWS side of a reserved bake workspace that the database
	// has no row for — the service (only while it is carrying no task) and its detached
	// home volume — and returns what it removed, empty when there was nothing. The
	// caller has already established that nothing owns the name.
	SweepOrphans(ctx context.Context, workspace string) ([]string, error)
}

// GoldenSnap is the little that the state machine needs to know about a snapshot.
type GoldenSnap struct {
	ID        string
	Completed bool
	Failed    bool
	// Started is the CP-stamped af-bake-started, NOT the EBS StartTime: the deadline
	// being measured is "how long has this deployment been waiting", and a snapshot
	// that AWS restarted internally must not reset it.
	Started time.Time
}

// GoldenHome is the seed's home volume as the state machine sees it.
type GoldenHome struct {
	VolumeID string
	// Capturable is "not attached to a running slot" — the same rule bake-golden.sh
	// applies, for the same reason (§64.28.2).
	Capturable bool
	// Baked is af-bake-ready: boot-install finished on this home.
	Baked bool
	// Created anchors the seed's deadline. The volume's own CreateTime, so a CP
	// restart cannot reset the clock by forgetting when it started.
	Created time.Time
}

// GoldenSeedRuntime is what the baker needs from the seed workspace's own runtime,
// beyond the ordinary Runtime interface (Start / Stop / State).
type GoldenSeedRuntime interface {
	// SeedFromCandidate makes THIS workspace build its new home from an unpublished
	// candidate instead of the published golden. Set on the probe only.
	SeedFromCandidate()
	// HomeForBake reports the workspace's home volume, whether it is safe to snapshot
	// right now (i.e. not attached to a RUNNING slot), whether boot-install has been
	// confirmed on it (MarkHomeBaked), and when the volume was created — the anchor
	// for the seed's own deadline. volumeID is "" when there is no home yet.
	HomeForBake(ctx context.Context) (h GoldenHome, err error)
	// MarkHomeBaked records that this home has finished boot-install and is worth
	// capturing. Called once, after the seed's Agent answers.
	MarkHomeBaked(ctx context.Context, volumeID string) error
	// ReleaseForBake unmounts and detaches the home. This is the CP-side equivalent of
	// the 15-minute wait an operator running bake-golden.sh has to sit through
	// (§64.28.2): in here the umount is a call, not a shutdown to wait for.
	ReleaseForBake(ctx context.Context) error
}

var (
	_ GoldenBakePool    = (*ecsEC2Factory)(nil)
	_ GoldenSeedRuntime = (*ecsEC2Runtime)(nil)
)

// --- golden の同一性（docs/log/72 §72.6.4） ------------------------------------------

// goldenIdentity answers "is this golden a golden for the image we are about to run".
//
// image is the reference (what the deployment is configured with); fp is the content it
// resolved to, or "" when that could not be established. The asymmetry is the whole
// design: the fingerprint decides when BOTH sides have one, and everything else falls
// back to the reference — a golden baked before this existed, a deployment whose image
// is not in ECR, an ECR call that failed. Unknown means "carry on as before", never
// "mismatch"; the cost of the former is a stale-looking golden, the cost of the latter
// is every existing deployment rebuilding every golden from scratch on upgrade.
type goldenIdentity struct {
	image string
	fp    string
}

// goldenFPCacheKey is deliberately NOT ecrFingerprintKey: that cache holds the RAW
// fingerprint for the restart badge, and this one holds the hashed form that fits in an
// EC2 tag. Same underlying ECR read, one extra call per TTL — worth it to keep the two
// values from being mistaken for each other.
func goldenFPCacheKey(image string) string { return "golden-img-fp:" + image }

// goldenIdentityFor resolves the running workspace image to its identity, memoised for
// ecsStaleTTL. Called on the Start path (goldenSnapshot) and on every baker tick, so it
// must not turn into an ECR call per workspace start.
func goldenIdentityFor(ctx context.Context, api ecrAPI, image string) goldenIdentity {
	id := goldenIdentity{image: image}
	if api == nil || image == "" {
		return id
	}
	id.fp = Freshness.get(goldenFPCacheKey(image), ecsStaleTTL, func() string {
		return hashImageFingerprint(ecrImageFingerprint(ctx, api, image))
	})
	return id
}

// hashImageFingerprint compresses the fingerprint into a tag value. The raw form is
// `linux/amd64=sha256:… linux/arm64=sha256:…`, ~180 characters for two architectures —
// under the 256-character EC2 tag limit today and over it at four. Hashing also makes
// the value opaque, which is right: it is only ever compared for equality.
func hashImageFingerprint(fp string) string {
	if fp == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fp))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// matches is the comparison, and the order of the two branches is the contract above.
func (id goldenIdentity) matches(tags []ec2types.Tag) bool {
	if id.fp != "" {
		if got := ec2TagValue(tags, ec2TagImageFP); got != "" {
			return got == id.fp
		}
	}
	return ec2TagValue(tags, ec2TagImage) == id.image
}

// stampTags is what a newly baked snapshot carries. Both are written: the reference so
// a human (and every existing tool, including bake-golden.sh and dev-deploy.sh) can see
// what it was baked from, the fingerprint so the CP compares content.
func (id goldenIdentity) stampTags() []ec2types.Tag {
	tags := []ec2types.Tag{{Key: aws.String(ec2TagImage), Value: aws.String(id.image)}}
	if id.fp != "" {
		tags = append(tags, ec2types.Tag{Key: aws.String(ec2TagImageFP), Value: aws.String(id.fp)})
	}
	return tags
}

// --- pool side -----------------------------------------------------------------

func (f *ecsEC2Factory) PoolLabel() string { return f.pool.pool }

// imageIdentity is workspaceImage() plus the content it currently resolves to.
func (f *ecsEC2Factory) imageIdentity(ctx context.Context) goldenIdentity {
	return goldenIdentityFor(ctx, f.base.ecr, f.WorkspaceImage())
}

// BakeArches is the declared classes' distinct architectures, default class first
// so the arch most members land on gets its golden soonest.
func (f *ecsEC2Factory) BakeArches() []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	add(f.pool.classFor(f.pool.defaultClass).arch)
	for _, c := range f.pool.classes {
		add(c.arch)
	}
	return out
}

// SeedClassFor picks a class on the given architecture. The FIRST declared one, not
// the cheapest or the biggest: the seed exists to run boot-install once, and which
// rung it does that on changes nothing about the home it produces.
func (f *ecsEC2Factory) SeedClassFor(arch string) string {
	for _, c := range f.pool.classes {
		if c.arch == arch {
			return c.id
		}
	}
	return ""
}

// snapshotArch reads a snapshot's architecture. An untagged snapshot is x86_64:
// goldens baked before classes existed could not have been anything else, and
// treating them as "unknown" would orphan every deployment's existing golden on
// upgrade (docs/log/70 §70.6).
func snapshotArch(s ec2types.Snapshot) string {
	return archOrX86(ec2TagValue(s.Tags, ec2TagArch))
}

// archOrX86 applies the same rule to the READING side. A pool with no classes declared
// is x86_64 (parseSlotClasses' bare form says so, and it drops an entry whose arch it
// does not recognise rather than defaulting it), so an empty arch here means the same
// thing an untagged snapshot does. Keeping the two symmetric is the point: compare a
// normalised arch against a normalised arch, or a legacy deployment matches nothing
// and every home is built empty.
func archOrX86(a string) string {
	if a == "" {
		return EC2ArchX86
	}
	return a
}

func (f *ecsEC2Factory) GoldenFor(ctx context.Context, role, arch string) (GoldenSnap, bool, error) {
	out, err := f.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, role),
		},
	})
	if err != nil {
		return GoldenSnap{}, false, err
	}
	id := f.imageIdentity(ctx)
	var best GoldenSnap
	found := false
	for i := range out.Snapshots {
		s := out.Snapshots[i]
		// The architecture is matched HERE and not in the API filter: an EC2 tag filter
		// cannot say "af-arch is x86_64 or absent", and absent is what every golden
		// baked before this change looks like. The image is matched here for a second
		// reason — the identity is "af-image-fp if both sides have one, else af-image",
		// which no single tag filter can express (goldenIdentity).
		if snapshotArch(s) != arch || !id.matches(s.Tags) {
			continue
		}
		g := GoldenSnap{
			ID:        aws.ToString(s.SnapshotId),
			Completed: s.State == ec2types.SnapshotStateCompleted,
			Failed:    s.State == ec2types.SnapshotStateError,
		}
		if ts := ec2TagValue(s.Tags, ec2TagBakeStarted); ts != "" {
			g.Started, _ = time.Parse(time.RFC3339, ts)
		}
		if g.Started.IsZero() && s.StartTime != nil {
			g.Started = *s.StartTime
		}
		// A completed one always wins over a pending one; among equals, the newest.
		if !found || (g.Completed && !best.Completed) || (g.Completed == best.Completed && g.Started.After(best.Started)) {
			best, found = g, true
		}
	}
	return best, found, nil
}

// BakeBlocked keeps the baker off the last slot. A pool at its cap means the next
// person to arrive would be evicting somebody; taking that slot for housekeeping is
// not a trade this feature gets to make (the cost of not baking is a slow first start,
// which is exactly what the golden is for and therefore nobody's emergency).
func (f *ecsEC2Factory) BakeBlocked(ctx context.Context) (bool, string, error) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return false, "", err
	}
	n := 0
	for _, r := range out.Reservations {
		n += len(r.Instances)
	}
	// One free slot is not enough: the bake needs one for the seed AND, later, one for
	// the probe, and a bake that gets half way and then cannot place its probe would
	// hold the seed's slot across every following tick.
	blocked, why := bakeCapacityBlocked(n, f.pool.maxSlots)
	return blocked, why, nil
}

// bakeCapacityBlocked is the arithmetic above, on its own so that the pool SCREEN can
// answer "why is nothing being baked" with the same rule the baker actually applies
// (docs/log/64 §64.30). Two copies of "needs two free" would drift, and the screen's job is
// precisely to explain the baker's decisions.
func bakeCapacityBlocked(inUse, maxSlots int) (bool, string) {
	if inUse+BakeReservedSlots > maxSlots {
		return true, fmt.Sprintf("%d/%d slots in use; a bake needs two free", inUse, maxSlots)
	}
	return false, ""
}

// BakeReservedSlots is the "two free" above as a number other code can subtract. The
// tenant-quota check (poolBudget) has to leave the same room, and a deployment that
// allocated every slot would never re-bake its golden — a failure whose only symptom is
// "new members start slowly", noticed weeks later if at all.
const BakeReservedSlots = 2

// --- what the pool screen shows about a bake in flight (docs/log/64 §64.30) -----------

// ec2BakeHome is one reserved workspace's home as the SCREEN needs it: the two facts
// the ordinary home view does not carry, both of which the baker itself steers by.
type ec2BakeHome struct {
	VolumeID   string
	InstanceID string    // "" once the slot has been released
	Baked      bool      // af-bake-ready: boot-install finished on this home
	Created    time.Time // the anchor the seed's own deadline uses
}

// describeBake works out how far this architecture's bake has got, from what the pool
// call has already read — no extra AWS calls, and no state in the CP (ADR 0012).
//
// The order of the cases is the order of the state machine in golden_bake.go, read
// backwards: the furthest thing that exists is the phase we are in. A candidate exists
// only after the home was captured, a captured home only after boot-install finished,
// and so on, so the first case that matches is the truthful one even when a previous
// step's leftovers are still around.
func describeBake(g *EC2GoldenView, arch string, homes map[string]ec2BakeHome, slotRunning map[string]bool,
	cand ec2types.SnapshotState, blocked bool, slotsInUse int) {
	seedWS, seed, haveSeed := findBakeHome(homes, GoldenSeedKey, arch)
	if haveSeed {
		g.Seed = &ec2BakeWorkspaceView{Workspace: seedWS, VolumeID: seed.VolumeID, InstanceID: seed.InstanceID}
	}
	if probeWS, probe, ok := findBakeHome(homes, GoldenProbeKey, arch); ok {
		g.Probe = &ec2BakeWorkspaceView{Workspace: probeWS, VolumeID: probe.VolumeID, InstanceID: probe.InstanceID}
	}
	switch {
	case g.SnapshotID != "" && !g.Stale:
		g.Phase = ec2BakePhasePublished
	case g.Candidate != "" && cand == ec2types.SnapshotStateError:
		// The copy itself failed. The baker rejects it on its next tick; reporting
		// "snapshotting" until then would show a bake that is already over.
		g.Phase = ec2BakePhaseRejected
	case g.Candidate != "" && cand != ec2types.SnapshotStateCompleted:
		g.Phase = ec2BakePhaseSnapshot
	case g.Candidate != "":
		// Completed. verify() does nothing until this point, so a completed candidate
		// means the probe is what is being waited for — whether or not it exists yet.
		g.Phase = ec2BakePhaseProbe
	case haveSeed && seed.Baked:
		g.Phase, g.PhaseSince = ec2BakePhaseCapture, bakeStamp(seed.Created)
	case haveSeed && slotRunning[seed.InstanceID]:
		g.Phase, g.PhaseSince = ec2BakePhaseBoot, bakeStamp(seed.Created)
	case haveSeed:
		// A home with no running slot under it: the seed is being placed, or its slot is
		// still coming up. ⚠️ Before the home volume exists there is nothing in AWS to
		// see at all, so the first moments of a bake still read as "idle" — one tick.
		g.Phase, g.PhaseSince = ec2BakePhaseSeed, bakeStamp(seed.Created)
	case g.Attempts >= 2:
		g.Phase = ec2BakePhaseGaveUp
	case g.Rejected != "":
		g.Phase = ec2BakePhaseRejected
	case blocked:
		g.Phase, g.SlotsInUse = EC2BakePhaseBlocked, slotsInUse
	default:
		g.Phase = EC2BakePhaseIdle
	}
}

// findBakeHome picks the reserved workspace's home out of the pool's homes. Matched on
// the reserved USER KEY, which is what the workspace name ends with (workspaceNames:
// af-ws-<tenant>-<key>) — and per architecture, because arm64's seed is a different
// workspace from x86_64's (archKey).
func findBakeHome(homes map[string]ec2BakeHome, key, arch string) (string, ec2BakeHome, bool) {
	want := ArchKey(key, arch)
	for ws, h := range homes {
		if ws == want || strings.HasSuffix(ws, "-"+want) {
			return ws, h, true
		}
	}
	return "", ec2BakeHome{}, false
}

// snapshotProgress turns EBS's "63%" into 63. An unreadable value is 0, which the
// screen shows as no percentage at all rather than as "0% done".
func snapshotProgress(s ec2types.Snapshot) int {
	n, err := strconv.Atoi(strings.TrimSuffix(aws.ToString(s.Progress), "%"))
	if err != nil || n < 0 || n > 100 {
		return 0
	}
	return n
}

// bakeStartedAt is when THIS deployment started waiting on the candidate — the CP's own
// af-bake-started, the same value the probe deadline is measured from, so the elapsed
// time on the screen and the tear-down it explains cannot disagree.
func bakeStartedAt(s ec2types.Snapshot) string {
	if ts := ec2TagValue(s.Tags, ec2TagBakeStarted); ts != "" {
		return ts
	}
	if s.StartTime != nil {
		return bakeStamp(*s.StartTime)
	}
	return ""
}

func bakeStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (f *ecsEC2Factory) SnapshotHome(ctx context.Context, volumeID, workspace, arch string) (string, error) {
	// ⚠️ The identity is taken HERE, at capture time, and both halves are stamped in the
	// same CreateSnapshot call. Reading it again later would let an image that moved
	// mid-bake stamp a candidate with content it was not baked from.
	tags := []ec2types.Tag{
		{Key: aws.String(EC2TagPool), Value: aws.String(f.pool.pool)},
		{Key: aws.String(EC2TagRole), Value: aws.String(EC2RoleGoldenCandidate)},
		{Key: aws.String(ec2TagArch), Value: aws.String(arch)},
		{Key: aws.String(ec2TagBakeStarted), Value: aws.String(time.Now().UTC().Format(time.RFC3339))},
		{Key: aws.String("Name"), Value: aws.String("af-golden-candidate-" + arch)},
	}
	tags = append(tags, f.imageIdentity(ctx).stampTags()...)
	out, err := f.ec2.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String("agent-fleet golden home candidate (" + f.WorkspaceImage() + ", " + arch + ")"),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSnapshot,
			Tags:         tags,
		}},
	})
	if err != nil {
		return "", err
	}
	id := aws.ToString(out.SnapshotId)
	log.Printf("golden: baking %s from %s (image %s, %s)", id, workspace, f.WorkspaceImage(), arch)
	return id, nil
}

// SetGoldenRole is a CreateTags, which OVERWRITES a key that is already there — the
// promotion and the rejection are both one call, and both are idempotent.
//
// ★ Never DeleteTags-then-CreateTags. A CP that died between the two would leave a
// snapshot with no af-role at all: invisible to every lookup, matched by no cleanup,
// and billed forever.
func (f *ecsEC2Factory) SetGoldenRole(ctx context.Context, snapshotID, role, reason string) error {
	tags := []ec2types.Tag{{Key: aws.String(EC2TagRole), Value: aws.String(role)}}
	switch role {
	case EC2RoleGolden:
		tags = append(tags, ec2types.Tag{Key: aws.String("Name"), Value: aws.String("af-golden")})
	case EC2RoleGoldenRejected:
		tags = append(tags, ec2types.Tag{Key: aws.String("Name"), Value: aws.String("af-golden-rejected")})
	}
	if reason != "" {
		tags = append(tags, ec2types.Tag{Key: aws.String(EC2TagBakeReason), Value: aws.String(reason)})
	}
	_, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{snapshotID}, Tags: tags})
	return err
}

func (f *ecsEC2Factory) RejectedAttempts(ctx context.Context, arch string) (int, error) {
	out, err := f.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, EC2RoleGoldenRejected),
		},
	})
	if err != nil {
		return 0, err
	}
	// Per architecture: an image whose home cannot boot on arm64 is not evidence about
	// x86_64, and counting them together would stop the healthy arch from ever baking.
	// Per image identity for the same reason the lookup uses it: the give-up counter
	// belongs to the CONTENT that failed, so re-tagging the same bytes must not reset
	// it, and new content under the same tag must not inherit it.
	id := f.imageIdentity(ctx)
	n := 0
	for i := range out.Snapshots {
		if snapshotArch(out.Snapshots[i]) == arch && id.matches(out.Snapshots[i].Tags) {
			n++
		}
	}
	return n, nil
}

func (f *ecsEC2Factory) DropSupersededGoldens(ctx context.Context, keepID, arch string) error {
	// ★ Rejected candidates are NOT swept. They are the record of why an image has no
	// golden, and they are also the give-up counter — deleting them would put the baker
	// straight back into retrying an image it has already proven cannot boot.
	for _, role := range []string{EC2RoleGolden, EC2RoleGoldenCandidate} {
		out, err := f.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
			OwnerIds: []string{"self"},
			Filters: []ec2types.Filter{
				tagFilter(EC2TagPool, f.pool.pool),
				tagFilter(EC2TagRole, role),
			},
		})
		if err != nil {
			return err
		}
		for i := range out.Snapshots {
			id := aws.ToString(out.Snapshots[i].SnapshotId)
			if id == keepID || snapshotArch(out.Snapshots[i]) != arch {
				continue
			}
			log.Printf("golden: deleting the superseded %s %s", role, id)
			if _, err := f.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(id)}); err != nil && !isAWSNotFound(err) {
				return fmt.Errorf("delete superseded %s %s: %w", role, id, err)
			}
		}
	}
	return nil
}

// --- seed / probe runtime side ---------------------------------------------------

func (e *ecsEC2Runtime) SeedFromCandidate() { e.seedRole = EC2RoleGoldenCandidate }

// HomeForBake answers the same question bake-golden.sh's guard asks, and with the same
// rule (§64.28.2): a home attached to a STOPPED slot is safe to capture, because the
// instance stop unmounted it on the way down; a home attached to a RUNNING slot is not.
// The baker normally gets there via ReleaseForBake — this is the check that makes the
// resume path safe when a previous tick already detached it.
func (e *ecsEC2Runtime) HomeForBake(ctx context.Context) (GoldenHome, error) {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return GoldenHome{}, err
	}
	h := GoldenHome{
		VolumeID: aws.ToString(vol.VolumeId),
		Baked:    ec2TagValue(vol.Tags, ec2TagBakeReady) != "",
	}
	if vol.CreateTime != nil {
		h.Created = *vol.CreateTime
	}
	inst := attachedInstance(vol)
	if inst == "" {
		h.Capturable = true
		return h, nil
	}
	running, err := e.instanceRunning(ctx, inst)
	if err != nil {
		return h, err
	}
	h.Capturable = !running
	return h, nil
}

func (e *ecsEC2Runtime) MarkHomeBaked(ctx context.Context, volumeID string) error {
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{volumeID},
		Tags: []ec2types.Tag{{
			Key: aws.String(ec2TagBakeReady), Value: aws.String(e.now().UTC().Format(time.RFC3339)),
		}},
	})
	return err
}

// SweepOrphans deletes what is left in AWS of a reserved bake workspace whose database
// row is gone. It exists because the two sides can come apart: the bake creates a
// service and a 50 GiB home under a name, and if the row that pointed at them is
// deleted while they survive — a destroy racing a re-create, a CP that died between the
// two — nothing ever looks at them again. The drift sweeper will not: it walks home
// volumes by tag but never DELETES one, because a detached home is exactly what every
// stopped workspace looks like (sweepVolume), and it cannot tell a person's home from a
// dead seed's. Only the baker can, because only the baker owns these two names.
//
// Measured on a live deployment (docs/log/64 §64.29.5): after a promoted bake the seed's
// service (desiredCount 0) and its home stayed behind and billed until an operator
// found them by hand.
//
// The safety is in the caller (the name is reserved, the database has just said no row
// owns it) plus two refusals here: a service still carrying a task is left alone, and so
// is a volume that is attached or that a snapshot is still reading — a bake in flight
// must not be swept out from under itself.
func (f *ecsEC2Factory) SweepOrphans(ctx context.Context, workspace string) ([]string, error) {
	var removed []string
	svc, err := f.base.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(f.base.cfg.cluster),
		Services: []string{workspace},
	})
	if err != nil {
		return nil, fmt.Errorf("describe service %s: %w", workspace, err)
	}
	for i := range svc.Services {
		s := svc.Services[i]
		if aws.ToString(s.Status) == "INACTIVE" {
			continue // already deleted; ECS keeps answering for a while
		}
		if s.DesiredCount > 0 || s.RunningCount > 0 || s.PendingCount > 0 {
			return removed, fmt.Errorf("%s has no workspace row but is running a task; leaving it alone", workspace)
		}
		// No Force, unlike Destroy: there is nothing to destroy on a deadline here, and
		// a service that still has a draining task is one the next tick can delete.
		if _, err := f.base.ecs.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(f.base.cfg.cluster),
			Service: aws.String(workspace),
		}); err != nil && !isAWSNotFound(err) {
			return removed, fmt.Errorf("delete service %s: %w", workspace, err)
		}
		removed = append(removed, "service "+workspace)
	}
	vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
			tagFilter(ec2TagWorkspace, workspace),
		},
	})
	if err != nil {
		return removed, fmt.Errorf("describe the home of %s: %w", workspace, err)
	}
	for i := range vols.Volumes {
		vol := vols.Volumes[i]
		volumeID := aws.ToString(vol.VolumeId)
		if vol.State != ec2types.VolumeStateAvailable {
			return removed, fmt.Errorf("the home %s of %s is %s, not detached; leaving it alone",
				volumeID, workspace, vol.State)
		}
		// A candidate being captured right now reads this volume. EBS lets the source be
		// deleted mid-copy, so this is a refusal on purpose rather than an API limit.
		pending, err := f.snapshotInProgress(ctx, volumeID)
		if err != nil {
			return removed, err
		}
		if pending {
			return removed, nil
		}
		if _, err := f.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)}); err != nil {
			return removed, fmt.Errorf("delete the home %s of %s: %w", volumeID, workspace, err)
		}
		removed = append(removed, "volume "+volumeID)
	}
	return removed, nil
}

// snapshotInProgress reports whether a snapshot of this volume is still copying blocks.
func (f *ecsEC2Factory) snapshotInProgress(ctx context.Context, volumeID string) (bool, error) {
	out, err := f.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters:  []ec2types.Filter{{Name: aws.String("volume-id"), Values: []string{volumeID}}},
	})
	if err != nil {
		return false, fmt.Errorf("describe the snapshots of %s: %w", volumeID, err)
	}
	for _, s := range out.Snapshots {
		if s.State == ec2types.SnapshotStatePending {
			return true, nil
		}
	}
	return false, nil
}

// ReleaseForBake is releaseSlot under a name that says why it is being called. The
// operator's script cannot do this at all — nothing in the normal lifecycle detaches a
// merely stopped home, which is what made the documented procedure a dead end
// (§64.28.2). In the CP the umount is one SSM call away, so the bake pays seconds
// instead of the slot-sleep timer, and the slot goes straight back to the pool.
func (e *ecsEC2Runtime) ReleaseForBake(ctx context.Context) error { return e.releaseSlot(ctx) }
