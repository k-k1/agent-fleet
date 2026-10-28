package runtime

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ecsEC2Runtime is the `ecs-ec2` Runtime adapter: ECS on the EC2 launch type, with a
// POOL of general-purpose slots and one PERSISTENT EBS volume per user that gets
// swapped between them (docs/log/64 §64.12 / §64.15, ADR 0045 decision 8 / decision 10).
//
// Why it exists next to the Fargate adapter rather than inside it: Fargate has no
// "fast and persistent" home (ADR 0044 decision 4) and cannot go below ~105s on a warm
// Start, while a hot slot + volume swap measures 22–27s. The price is that one
// workspace stops being "a service + two EFS access points" and becomes six kinds of
// resource, on an ECS substrate with zero production mileage (ADR 0045 decision 2-3). So
// this ships as a SEPARATE profile: AF_RUNTIME=ecs-ec2 opts a deployment in, and
// runtime_ecs.go (Fargate) is not touched — rolling back is one line of profile, not
// a revert.
//
// The three invariants everything below leans on:
//
//   - A slot is EXCLUSIVE to one user (ADR 0045 decision 8). EC2 on-demand pricing is
//     perfectly linear in vCPU, so bin-packing users onto one box saves nothing but
//     costs a shared kernel and a shared root filesystem.
//   - The adapter holds NO CP-side state (ADR 0012). Volumes and slots are found by
//     tag, "which slot is this user on" is DERIVED from the volume's attachment, and
//     the placement is an `ec2InstanceId ==` constraint. No schema change.
//   - Slot allocation is decided by AWS, not by us — see ec2HomeDevice.
type ecsEC2Runtime struct {
	// base is the Fargate adapter instance for this same workspace, used as a library
	// for the parts that are identical on both launch types: the EFS access points,
	// the SSM SecureString secrets, the Service Connect endpoint and the background
	// readiness watch.
	//
	// COMPOSITION, NOT EMBEDDING, on purpose: embedding would promote base's Start /
	// Stop / State and silently satisfy the Runtime interface with the FARGATE
	// implementations if one of the overrides below were ever dropped. With a named
	// field the compiler makes that impossible (see the var _ Runtime assertion).
	base *ecsRuntime

	ec2  ec2API
	ssmc ssmCommandAPI
	ci   ecsContainerInstanceAPI
	pool ec2PoolConfig

	// instanceType is the slot size this workspace needs (its memory request resolved
	// against its CLASS's ladder), arch the CPU architecture that class runs on, and
	// homeGiB the size of its persistent home volume.
	//
	// arch is carried rather than re-derived because two different places need it and
	// neither can see the class: launchSlot picks the launch template (an arm64
	// instance type cannot boot the x86_64 AMI) and buildTaskDef declares
	// RuntimePlatform.
	instanceType string
	arch         string
	// memCapMiB is the hard memory limit the workspace container gets, resolved once
	// from the rung above (workspaceMemCapMiB). 0 = uncapped.
	memCapMiB int64
	homeGiB   int32
	// The rest of the rung and the class it came from, carried for MachineProfile()
	// alone: the Console names the box a member is on, and the answer has to be the same
	// resolution that placement uses rather than a second one computed elsewhere.
	slotMemMiB int64
	slotVCPU   int
	classID    string
	classLabel string

	// azOfSubnet resolves the deployment's subnets to their AZ (cached in the factory):
	// an EBS volume never leaves its AZ, so the volume pins the AZ and the AZ picks
	// both the slot candidates and the subnet the task's ENI is created in.
	azOfSubnet func(context.Context) (map[string]string, error)

	// bg runs the slow half of a lifecycle operation off the caller's thread. It is a
	// field, not a bare `go`, so tests can run that half inline (or drop it) instead of
	// racing a goroutine they cannot join.
	bg func(context.Context, func(context.Context))

	// now is time.Now, overridable in tests (claim expiry / release grace).
	now func() time.Time
	// sleep is the poll delay, overridable in tests so waits do not really sleep.
	sleep func(context.Context, time.Duration) error

	// seedRole is the snapshot ROLE a brand-new home for this workspace is built from.
	// Empty — which is every workspace a person owns — means EC2RoleGolden.
	//
	// The golden baker sets it to EC2RoleGoldenCandidate on its probe, and that is the
	// entire mechanism by which a freshly baked golden is proven to boot BEFORE anyone
	// else can be given it (docs/log/64 §64.28.3). Set once by the baker on a runtime for
	// its own reserved membership, before that workspace is ever started; no session,
	// PAT or admin path resolves that membership, so nothing else reads it.
	seedRole string

	// leases serialises this workspace's home across CP replicas (HomeLeaseStore); nil
	// leaves it to the process-local locks.
	leases HomeLeaseStore
	// tenantID, workspaceID and onAutoReplace let an automatic slot replacement reach the
	// CP's audit log (recordAutoReplace); onAutoReplace nil = not recorded.
	tenantID, workspaceID string
	onAutoReplace         func(context.Context, SlotAutoReplace)
	// replica separates the process-local maps of two CPs a test stands up in one
	// process (localKey). Always empty in production.
	replica string
	// leaseTTL and leasePoll override homeLeaseTTL and homeLeasePoll in tests.
	leaseTTL, leasePoll time.Duration
}

var _ Runtime = (*ecsEC2Runtime)(nil)

// ec2SingleTaskDeployment is what makes "one workspace, one task" a property of the
// SERVICE rather than of our call ordering.
//
// maximumPercent 100 with desiredCount 1 means ECS may never run a second task for
// this service — so the demoted deployment cannot be handed one, which is the entire
// mechanism of docs/log/64 §64.39. minimumHealthyPercent 0 is the other half: at max 100%
// there is no room to start a replacement before stopping the old one, so ECS has to
// be allowed to reach zero. For a single-user workspace that is the RIGHT semantics
// anyway — two tasks means two Agents behind one Service Connect alias, which is how
// an in-flight OAuth flow_id was lost (see lastTaskDef), never something we want.
//
// ⚠️ The default is 200/100, and 200 is what allows the second task. It is sent on
// every service call, not once at creation, because services created before this
// change would otherwise keep the default forever.
var ec2SingleTaskDeployment = &ecstypes.DeploymentConfiguration{
	MaximumPercent:        aws.Int32(100),
	MinimumHealthyPercent: aws.Int32(0),
}

// ec2NoAZRebalancing must travel WITH ec2SingleTaskDeployment on every call, because
// ECS refuses the two together in the other order:
//
//	InvalidParameterException: The service couldn't be updated because Availability
//	Zone Rebalancing does not support maximumPercent <= 100 % as deployment
//	configuration.
//
// ⚠️ This broke production on 0.12.3 (docs/log/64 §64.39.12), and it broke ONLY the
// services that already existed: `CreateService` quietly settles on DISABLED when the
// deployment configuration cannot support rebalancing, so every service the new build
// creates is fine — while every service created by an older build carries ENABLED and
// rejects the very first UpdateService the new build sends it. A pre-upgrade service is
// therefore not reproduced by pushing 200/100 back onto a new one: this attribute is
// decided at CREATE time and an update does not move it.
//
// DISABLED is also the honest value rather than a workaround. The task definition pins
// the task to one instance (`ec2InstanceId ==`), so there is no AZ for ECS to rebalance
// to; the home volume is in that instance's AZ and cannot follow it anyway (ADR 0045).
const ec2NoAZRebalancing = ecstypes.AvailabilityZoneRebalancingDisabled

const (
	// ec2HomeDevice is the ONE device name every user's home volume is attached at,
	// on every slot. This single constant IS the slot allocator (docs/log/64 §64.15.2):
	// AWS refuses a second AttachVolume on a device name that is already taken, so
	// "this slot is free" and "AttachVolume succeeded" are the same statement. Two
	// workspaces racing for the last hot slot are separated by EC2 itself — there is
	// no CP-side free-list to get out of sync with reality, and the exclusivity of
	// ADR 0045 decision 8 is enforced by the API rather than by convention.
	ec2HomeDevice = "/dev/sdf"

	// ec2HomeMountBase is where a slot mounts a user's volume. The task bind-mounts the
	// `dev` subdirectory (owned by uid 1000) as /home/dev, so the filesystem root stays
	// out of the container — same layout the sandbox harness measured (docs/log/64 §64.14).
	ec2HomeMountBase = "/af-home"

	// Where the credentials-only EFS access point lands inside the task. home now lives
	// on a single-AZ EBS volume, so the auth/identity set is kept on EFS as well and the
	// entrypoint symlinks it back into ~ (the ADR 0045 decision 3-6 hybrid).
	ec2KeepPath = "/var/lib/af/keep"

	EC2TagPool       = "af-pool"
	EC2TagRole       = "af-role"
	EC2TagMembership = "af-membership"
	EC2TagWorkspace  = "af-workspace"
	EC2TagSlotSize   = "af-slot-size"
	// EC2TagTenant is the owning tenant's SLUG, and it exists only for the bill
	// (docs/log/67, ADR 0048 decision 3). Nothing in the pool logic reads it: the CP can already
	// derive the tenant from af-membership through its own DB, so an opaque tenant id
	// here would buy nothing. A slug can be read straight out of Cost Explorer, which
	// is the whole point — grouping the invoice by tenant without the Console.
	//
	// ⚠️ The value is a slug rather than the tenant id ON PURPOSE, and it is safe to put
	// in billing data for the same reason af-workspace is NOT activated as a cost
	// allocation tag: a slug names an organisation, af-workspace names a person
	// (it is built from their email).
	EC2TagTenant = "af-tenant"
	// EC2TagClaim marks a home volume as "a slot is being launched for this user".
	// It exists for exactly one window: RunInstances returns a PENDING instance, and
	// AttachVolume only accepts running|stopped, so for those few seconds the volume
	// is the only place that can say "this workspace is starting". Without it State()
	// would answer `stopped`, the Console would offer Start again, and the second
	// Start would create a second slot.
	EC2TagClaim   = "af-claim"
	ec2TagClaimAt = "af-claim-at"
	// EC2TagIdleSince marks a home that is still ATTACHED to its slot but whose
	// workspace is stopped. It is what makes "the same user gets the same slot" work
	// without the CP remembering anything: the attachment IS the affinity, and this tag
	// is how long it has been dormant (for the idle-stop and for picking a victim).
	EC2TagIdleSince = "af-idle-since"
	// ec2TagSlotIdleSince is af-idle-since's counterpart for a slot that holds NO home:
	// the moment the box went back to the free pool. It is on the INSTANCE because there
	// is nowhere else to put it — a free slot has no volume, and the CP holds no state
	// (ADR 0012). Two replicas and a restarted CP therefore read the same answer.
	//
	// ★ Why it has to exist at all (docs/log/64 §64.31): the sleep test used to live only in
	// sweepVolume, which walks HOMES. A slot whose home was released — an eviction, a
	// class change, the golden baker's seed and probe — left that walk entirely and no
	// other path stopped a running instance. Measured on the live deployment: three empty
	// m*.large sat running for over 24h with zero tasks, at ~$95/month each, while the
	// parameter that was supposed to stop them promised ~$9.6.
	//
	// Written by releaseSlot (the moment it knows) and re-stamped by the sweeper when it
	// is missing — the same "the doer writes it, the sweeper repairs it" split af-idle-since
	// already uses. Cleared when a workspace takes the slot, so a short tenancy does not
	// leave the box looking free since before it was busy.
	ec2TagSlotIdleSince = "af-slot-idle-since"
	// EC2TagHibernating marks a home that has entered hibernation (§64.18.2) and the
	// moment it did. It exists because hibernation spans several sweeps and cannot lean
	// on af-idle-since: releaseSlot — hibernation's own first step — clears that tag.
	//
	// The timestamp is load-bearing, not decoration. It is what tells "the snapshot that
	// captures this dormancy" apart from "a snapshot of the same volume taken during an
	// EARLIER one", and mistaking the second for the first deletes a volume holding work
	// that was never captured. Only a snapshot started after this mark counts.
	EC2TagHibernating = "af-hibernating"
	// ec2TagHomeWipePrefix + a HomeWipe marks a member's Recreate or Clean home that has
	// not happened yet: the next Start performs it on the slot, after the mount and before
	// the task (ADR 0045 decision 32). It lives on the home — the volume, and the
	// hibernation snapshots that stand in for it — because that is the one thing every
	// later Start reads, whichever CP runs it and whatever became of the slot meanwhile.
	// One key per kind: see homeWipeKey.
	ec2TagHomeWipePrefix = "af-home-wipe-"

	ec2RoleHome = "home"
	ec2RoleSlot = "slot"
	// ec2RoleQuarantined is a slot that could not mount a home and must never be offered
	// again. It is the SAME tag every pool query already filters on, so re-stamping it
	// removes the box from the free list, from the cap, and from placement in one write —
	// no second concept, no CP-side state (ADR 0012).
	//
	// Measured, 2026-08-16 (docs/log/64 §64.24): a workspace container whose home was
	// detached under it left processes in uninterruptible sleep, XFS flushing to a device
	// that was gone, and the kernel holding the dead volume's NVMe namespace. The next
	// user's volume then never appeared under /dev at all, so `af-mount` failed with
	// "device not found" — and because the slot still looked free, EVERY later Start
	// picked the same box and failed the same way. One wedged kernel became an outage for
	// everyone, silently.
	ec2RoleQuarantined = "quarantined"
	// Why the slot was quarantined, and when — the operator has to decide whether to
	// terminate it, and "some slot went away" is not a report.
	ec2TagQuarantineReason = "af-quarantine-reason"
	ec2TagQuarantineAt     = "af-quarantine-at"
	// EC2RoleGolden is the ONE shared snapshot new homes are built from: a home that has
	// already paid boot-install (48s) and warmed its caches (ADR 0045 decision 9). One per
	// pool, no membership tag — deleting it by mistake would cost every future user
	// their first two minutes, which is why nothing that walks per-membership resources
	// can ever match it.
	EC2RoleGolden = "golden"
	// EC2RoleGoldenCandidate is a golden that has been BAKED but not yet proven to
	// boot (docs/log/64 §64.28.3). Nothing seeds an ordinary home from it — the whole
	// point is that it is invisible to goldenSnapshot()'s default lookup, so a
	// candidate that turns out to be unbootable can never reach a real user. The
	// baker's probe asks for this role explicitly, and only a probe that comes up
	// healthy gets it renamed to EC2RoleGolden.
	//
	// ★ The failure this exists for: a golden whose home cannot boot looks like a
	// complete success right up to "snapshot completed". What breaks is the NEXT new
	// user, and the only symptom is a task that restarts forever. Measured on a live
	// deployment — the first golden ever baked from the product's own path was
	// unbootable, and nothing before the user's Console said so.
	EC2RoleGoldenCandidate = "golden-candidate"
	// EC2RoleGoldenRejected is a candidate whose probe did not come up. Kept rather
	// than deleted: it is the evidence for why this image has no golden, and it is
	// also the memo that stops the baker retrying the same broken image every tick.
	EC2RoleGoldenRejected = "golden-rejected"
	// ec2TagBakeStarted is when the baker began waiting on a step, by the CP's clock.
	// It is the deadline anchor for "the probe never became healthy" — without it a
	// crash-looping probe would hold a slot forever, since nothing else about it ever
	// changes. Same discipline as EC2TagHibernating: the state lives in AWS, so a CP
	// that restarts mid-bake resumes instead of starting over (ADR 0012).
	ec2TagBakeStarted = "af-bake-started"
	// EC2TagBakeReason records why a candidate was rejected, for the operator.
	EC2TagBakeReason = "af-bake-reason"
	// ec2TagBakeReady marks a SEED's home volume as having finished boot-install — the
	// difference between "this home is worth capturing" and "this home merely exists".
	//
	// ★ Without it the baker cannot tell a seed it booted from a seed whose volume was
	// created by a Start that then failed. Capturing the second one produces a golden
	// that is EMPTY — and an empty home boots perfectly, so the probe would pass it and
	// every new user would silently get no benefit at all. The one failure mode a
	// boot check cannot catch is the one where booting is not the problem.
	ec2TagBakeReady = "af-bake-ready"
	// ec2RoleBackup is a periodic copy of a home, kept so that losing the AZ is not the
	// same as losing the work (ADR 0045 decision 17). It is a THIRD kind of snapshot and it is
	// tagged as its own role on purpose: every existing lookup filters on af-role, so
	// backups are invisible to the restore path, to hibernation's superseded-capture
	// sweep, and to the golden lookup. Nothing gets to mistake a backup for the copy it
	// was waiting for.
	ec2RoleBackup = "backup"
	// EC2TagBackupAt is when a backup was STARTED, by the CP's clock. EBS reports its own
	// StartTime, but the schedule is decided against this: a snapshot's StartTime is what
	// AWS did, and the question here is when this deployment last asked.
	EC2TagBackupAt = "af-backup-at"
	// efsTagErasedVolumes records, on a member's keep access point, the home volumes an
	// administrator's Clean home deleted (EraseHome). A hibernation snapshot names the
	// volume it was taken from, so a snapshot of a recorded volume is a copy of an erased
	// home, and restoreSnapshot never restores one — whenever it was taken and whether or
	// not the erase's listings saw it. A snapshot of any other volume, such as the home
	// created after the erase, is never affected. It lives on the access point because that
	// is what outlives the erase. See eraseRecord for the two kinds of word it holds.
	efsTagErasedVolumes = "af-home-erased-volumes"
	// ec2TagImage stamps the golden snapshot with the workspace image it was baked from.
	// A golden that predates an image or CLI-pin bump would start new users on the OLD
	// tools, silently and only for them — so the CP compares and refuses a stale one
	// instead of trusting that somebody remembered to re-bake (decision 9).
	ec2TagImage = "af-image"
	// ec2TagImageFP stamps the CONTENT the image reference above resolved to when the
	// golden was baked — `sha256:<hex>` over imageFingerprint()'s per-platform manifest
	// digests (goldenIdentity).
	//
	// ★ Why a second tag at all: ec2TagImage is a REFERENCE, and a reference is not an
	// identity. Measured on the real deployment (docs/log/72 §72.6.4): re-tagging the
	// workspace image so that CP and workspace share one ImageTag left the digest
	// byte-identical (`sha256:497ca29360ed…` on both tags) and the CP still said
	// `no golden for …; baking one`, then spent ~10 minutes and two EC2 slots
	// rebuilding a home it already had. The error runs the other way too, and that one
	// is not merely expensive: push new content over a MUTABLE tag (`:dev`) and the
	// string still matches, so every new member is seeded from a home baked out of the
	// old image — silently, and only for them.
	//
	// Absent on goldens baked before this existed, and unknowable when the image is
	// not in ECR or ECR cannot be read. Both fall back to comparing ec2TagImage, i.e.
	// exactly the old behaviour: unknown must never be read as "does not match", which
	// would throw away every existing golden on upgrade.
	ec2TagImageFP = "af-image-fp"
	// ec2TagArch stamps the CPU architecture a golden snapshot was baked ON.
	//
	// ★ A golden is a HOME that has already paid boot-install — which means it is full
	// of binaries: ~/.local/bin/{rtk,agy,cursor-agent,kiro}, the npm CLIs, nvm's node,
	// the Chromium it downloaded. Handing an x86_64 golden to an arm64 slot produces a
	// home that mounts perfectly and cannot exec anything, for every new user of that
	// class, from their very first start (docs/log/70 §70.6). The image tag alone cannot
	// catch it: both goldens are baked from the same image reference.
	//
	// Absent on goldens baked before this existed. Those are x86_64 by construction —
	// there was no other kind of slot — so an empty tag reads as x86_64.
	ec2TagArch = "af-arch"
)
