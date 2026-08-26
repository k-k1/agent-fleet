package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// ecsEC2Runtime is the `ecs-ec2` Runtime adapter: ECS on the EC2 launch type, with a
// POOL of general-purpose slots and one PERSISTENT EBS volume per user that gets
// swapped between them (docs/64 §64.12 / §64.15, ADR 0045 決定 8 / 決定 10).
//
// Why it exists next to the Fargate adapter rather than inside it: Fargate has no
// "fast and persistent" home (ADR 0044 決定 4) and cannot go below ~105s on a warm
// Start, while a hot slot + volume swap measures 22–27s. The price is that one
// workspace stops being "a service + two EFS access points" and becomes six kinds of
// resource, on an ECS substrate with zero production mileage (ADR 0045 決定 2-3). So
// this ships as a SEPARATE profile: AF_RUNTIME=ecs-ec2 opts a deployment in, and
// runtime_ecs.go (Fargate) is not touched — rolling back is one line of profile, not
// a revert.
//
// The three invariants everything below leans on:
//
//   - A slot is EXCLUSIVE to one user (ADR 0045 決定 8). EC2 on-demand pricing is
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
	homeGiB      int32

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
	// Empty — which is every workspace a person owns — means ec2RoleGolden.
	//
	// The golden baker sets it to ec2RoleGoldenCandidate on its probe, and that is the
	// entire mechanism by which a freshly baked golden is proven to boot BEFORE anyone
	// else can be given it (docs/64 §64.28.3). Set once by the baker on a runtime for
	// its own reserved membership, before that workspace is ever started; no session,
	// PAT or admin path resolves that membership, so nothing else reads it.
	seedRole string
}

var _ Runtime = (*ecsEC2Runtime)(nil)

// ec2SingleTaskDeployment is what makes "one workspace, one task" a property of the
// SERVICE rather than of our call ordering.
//
// maximumPercent 100 with desiredCount 1 means ECS may never run a second task for
// this service — so the demoted deployment cannot be handed one, which is the entire
// mechanism of docs/64 §64.39. minimumHealthyPercent 0 is the other half: at max 100%
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
// ⚠️ This broke production on 0.12.3 (docs/64 §64.39.12), and it broke ONLY the
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
	// on every slot. This single constant IS the slot allocator (docs/64 §64.15.2):
	// AWS refuses a second AttachVolume on a device name that is already taken, so
	// "this slot is free" and "AttachVolume succeeded" are the same statement. Two
	// workspaces racing for the last hot slot are separated by EC2 itself — there is
	// no CP-side free-list to get out of sync with reality, and the exclusivity of
	// ADR 0045 決定 8 is enforced by the API rather than by convention.
	ec2HomeDevice = "/dev/sdf"

	// ec2HomeMountBase is where a slot mounts a user's volume. The task bind-mounts the
	// `dev` subdirectory (owned by uid 1000) as /home/dev, so the filesystem root stays
	// out of the container — same layout the sandbox harness measured (docs/64 §64.14).
	ec2HomeMountBase = "/af-home"

	// Where the credentials-only EFS access point lands inside the task. home now lives
	// on a single-AZ EBS volume, so the auth/identity set is kept on EFS as well and the
	// entrypoint symlinks it back into ~ (ADR 0045 決定 3-6 のハイブリッド).
	ec2KeepPath = "/var/lib/af/keep"

	ec2TagPool       = "af-pool"
	ec2TagRole       = "af-role"
	ec2TagMembership = "af-membership"
	ec2TagWorkspace  = "af-workspace"
	ec2TagSlotSize   = "af-slot-size"
	// ec2TagTenant is the owning tenant's SLUG, and it exists only for the bill
	// (docs/67, ADR 0048 決定 3). Nothing in the pool logic reads it: the CP can already
	// derive the tenant from af-membership through its own DB, so an opaque tenant id
	// here would buy nothing. A slug can be read straight out of Cost Explorer, which
	// is the whole point — grouping the invoice by tenant without the Console.
	//
	// ⚠️ The value is a slug rather than the tenant id ON PURPOSE, and it is safe to put
	// in billing data for the same reason af-workspace is NOT activated as a cost
	// allocation tag: a slug names an organisation, af-workspace names a person
	// (it is built from their email).
	ec2TagTenant = "af-tenant"
	// ec2TagClaim marks a home volume as "a slot is being launched for this user".
	// It exists for exactly one window: RunInstances returns a PENDING instance, and
	// AttachVolume only accepts running|stopped, so for those few seconds the volume
	// is the only place that can say "this workspace is starting". Without it State()
	// would answer `stopped`, the Console would offer Start again, and the second
	// Start would create a second slot.
	ec2TagClaim   = "af-claim"
	ec2TagClaimAt = "af-claim-at"
	// ec2TagIdleSince marks a home that is still ATTACHED to its slot but whose
	// workspace is stopped. It is what makes "the same user gets the same slot" work
	// without the CP remembering anything: the attachment IS the affinity, and this tag
	// is how long it has been dormant (for the idle-stop and for picking a victim).
	ec2TagIdleSince = "af-idle-since"
	// ec2TagSlotIdleSince is af-idle-since's counterpart for a slot that holds NO home:
	// the moment the box went back to the free pool. It is on the INSTANCE because there
	// is nowhere else to put it — a free slot has no volume, and the CP holds no state
	// (ADR 0012). Two replicas and a restarted CP therefore read the same answer.
	//
	// ★ Why it has to exist at all (docs/64 §64.31): the sleep test used to live only in
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
	// ec2TagHibernating marks a home that has entered hibernation (§64.18.2) and the
	// moment it did. It exists because hibernation spans several sweeps and cannot lean
	// on af-idle-since: releaseSlot — hibernation's own first step — clears that tag.
	//
	// The timestamp is load-bearing, not decoration. It is what tells "the snapshot that
	// captures this dormancy" apart from "a snapshot of the same volume taken during an
	// EARLIER one", and mistaking the second for the first deletes a volume holding work
	// that was never captured. Only a snapshot started after this mark counts.
	ec2TagHibernating = "af-hibernating"

	ec2RoleHome = "home"
	ec2RoleSlot = "slot"
	// ec2RoleQuarantined is a slot that could not mount a home and must never be offered
	// again. It is the SAME tag every pool query already filters on, so re-stamping it
	// removes the box from the free list, from the cap, and from placement in one write —
	// no second concept, no CP-side state (ADR 0012).
	//
	// Measured, 2026-08-16 (docs/64 §64.24): a workspace container whose home was
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
	// ec2RoleGolden is the ONE shared snapshot new homes are built from: a home that has
	// already paid boot-install (48s) and warmed its caches (ADR 0045 決定 9). One per
	// pool, no membership tag — deleting it by mistake would cost every future user
	// their first two minutes, which is why nothing that walks per-membership resources
	// can ever match it.
	ec2RoleGolden = "golden"
	// ec2RoleGoldenCandidate is a golden that has been BAKED but not yet proven to
	// boot (docs/64 §64.28.3). Nothing seeds an ordinary home from it — the whole
	// point is that it is invisible to goldenSnapshot()'s default lookup, so a
	// candidate that turns out to be unbootable can never reach a real user. The
	// baker's probe asks for this role explicitly, and only a probe that comes up
	// healthy gets it renamed to ec2RoleGolden.
	//
	// ★ The failure this exists for: a golden whose home cannot boot looks like a
	// complete success right up to "snapshot completed". What breaks is the NEXT new
	// user, and the only symptom is a task that restarts forever. Measured on a live
	// deployment — the first golden ever baked from the product's own path was
	// unbootable, and nothing before the user's Console said so.
	ec2RoleGoldenCandidate = "golden-candidate"
	// ec2RoleGoldenRejected is a candidate whose probe did not come up. Kept rather
	// than deleted: it is the evidence for why this image has no golden, and it is
	// also the memo that stops the baker retrying the same broken image every tick.
	ec2RoleGoldenRejected = "golden-rejected"
	// ec2TagBakeStarted is when the baker began waiting on a step, by the CP's clock.
	// It is the deadline anchor for "the probe never became healthy" — without it a
	// crash-looping probe would hold a slot forever, since nothing else about it ever
	// changes. Same discipline as ec2TagHibernating: the state lives in AWS, so a CP
	// that restarts mid-bake resumes instead of starting over (ADR 0012).
	ec2TagBakeStarted = "af-bake-started"
	// ec2TagBakeReason records why a candidate was rejected, for the operator.
	ec2TagBakeReason = "af-bake-reason"
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
	// same as losing the work (ADR 0045 決定 17). It is a THIRD kind of snapshot and it is
	// tagged as its own role on purpose: every existing lookup filters on af-role, so
	// backups are invisible to the restore path, to hibernation's superseded-capture
	// sweep, and to the golden lookup. Nothing gets to mistake a backup for the copy it
	// was waiting for.
	ec2RoleBackup = "backup"
	// ec2TagBackupAt is when a backup was STARTED, by the CP's clock. EBS reports its own
	// StartTime, but the schedule is decided against this: a snapshot's StartTime is what
	// AWS did, and the question here is when this deployment last asked.
	ec2TagBackupAt = "af-backup-at"
	// ec2TagImage stamps the golden snapshot with the workspace image it was baked from.
	// A golden that predates an image or CLI-pin bump would start new users on the OLD
	// tools, silently and only for them — so the CP compares and refuses a stale one
	// instead of trusting that somebody remembered to re-bake (決定 9).
	ec2TagImage = "af-image"
	// ec2TagImageFP stamps the CONTENT the image reference above resolved to when the
	// golden was baked — `sha256:<hex>` over imageFingerprint()'s per-platform manifest
	// digests (goldenIdentity).
	//
	// ★ Why a second tag at all: ec2TagImage is a REFERENCE, and a reference is not an
	// identity. Measured on the real deployment (docs/72 §72.6.4): re-tagging the
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
	// class, from their very first start (docs/70 §70.6). The image tag alone cannot
	// catch it: both goldens are baked from the same image reference.
	//
	// Absent on goldens baked before this existed. Those are x86_64 by construction —
	// there was no other kind of slot — so an empty tag reads as x86_64.
	ec2TagArch = "af-arch"
)

// --- narrow AWS client ports (only the calls this adapter makes), so it is unit
// testable against fakes. The real *ec2.Client / *ssm.Client / *ecs.Client satisfy
// these. The ECS calls are a SECOND interface rather than an extension of ecsAPI so
// that runtime_ecs.go and its fakes stay untouched. ---

type ec2API interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	CreateVolume(context.Context, *ec2.CreateVolumeInput, ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error)
	DeleteVolume(context.Context, *ec2.DeleteVolumeInput, ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
	AttachVolume(context.Context, *ec2.AttachVolumeInput, ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error)
	DetachVolume(context.Context, *ec2.DetachVolumeInput, ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error)
	CreateTags(context.Context, *ec2.CreateTagsInput, ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	DeleteTags(context.Context, *ec2.DeleteTagsInput, ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error)
	RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	StopInstances(context.Context, *ec2.StopInstancesInput, ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	// TerminateInstances is the LAST step of the dormancy series and the only one that
	// gives the root volume back (slotTerminateAfter). It is deliberately reachable from
	// exactly one place — the sweeper — so that "a box disappeared" always has the same
	// explanation. See terminateSlot.
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	// Snapshots serve two features that share one mechanism: hibernating a long-unused
	// home (ADR 0045 決定 4) and seeding a new one from the golden image (決定 9).
	DescribeSnapshots(context.Context, *ec2.DescribeSnapshotsInput, ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error)
	CreateSnapshot(context.Context, *ec2.CreateSnapshotInput, ...func(*ec2.Options)) (*ec2.CreateSnapshotOutput, error)
	DeleteSnapshot(context.Context, *ec2.DeleteSnapshotInput, ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error)
}

type ssmCommandAPI interface {
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

type ecsContainerInstanceAPI interface {
	ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error)
	DescribeContainerInstances(context.Context, *ecs.DescribeContainerInstancesInput, ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error)
	DeregisterContainerInstance(context.Context, *ecs.DeregisterContainerInstanceInput, ...func(*ecs.Options)) (*ecs.DeregisterContainerInstanceOutput, error)
}

// ec2PoolConfig is the deployment-wide shape of the slot pool, read once at boot from
// AF_ECS_EC2_*. The substrate (deploy/aws/ecs/cfn/40-ec2-pool.yaml) owns the launch
// template: AMI, user-data (cluster join, the shortened task-cleanup window, the
// af-mount/af-umount helpers), instance profile and security group all live there, so
// the CP only ever says "run one of these, in this AZ, at this size".
type ec2PoolConfig struct {
	launchTemplate string // AF_ECS_EC2_LAUNCH_TEMPLATE (id or name)
	// amiArm64 is AF_ECS_EC2_AMI_ARM64, required only when a declared class says
	// arm64: the launch template pins the x86_64 ECS-optimized AMI, and an arm64
	// instance type cannot boot it (docs/70 §70.8).
	//
	// An AMI ID rather than a second launch template, and resolved by CLOUDFORMATION
	// rather than by the CP. Everything else about a slot — instance profile, security
	// group, root volume, and the user-data that joins the cluster and installs
	// af-mount/af-umount — is architecture-neutral, so a second template would be a
	// ~90-line copy of the first whose only difference was one field, kept in step by
	// hope. RunInstances lets a request parameter override the template, which is the
	// same one field expressed once.
	//
	// ⚠️ Not read from SSM by the CP. That would put ssm:GetParameter on the task role
	// for a value that changes only when the pool stack is redeployed, and it would
	// break the rule that patching a slot's AMI IS redeploying that stack (ADR 0045
	// 決定 7). The 40-ec2-pool stack resolves the public parameter and passes the id.
	amiArm64     string
	pool         string         // AF_ECS_EC2_POOL tag value (defaults to the cluster name)
	classes      []ec2SlotClass // AF_ECS_EC2_SLOT_TYPES, in declared order
	defaultClass string         // AF_ECS_EC2_DEFAULT_SLOT_CLASS (defaults to the first)
	maxSlots     int            // AF_ECS_EC2_MAX_SLOTS: cap on instances this pool may run
	homeGiB      int32          // AF_ECS_EC2_HOME_GB: default per-user home volume size
	tmpfsMiB     int32          // AF_ECS_EC2_TMP_MB: size cap of the /tmp tmpfs
	tmpfsOpts    []string       // AF_ECS_EC2_TMP_OPTS
	claimTTL     time.Duration
	releaseGrace time.Duration
	// slotSleepAfter is how long a slot may sit with no running task before the sweeper
	// STOPS the instance (never terminates it — the image cache lives on its root
	// volume, and a stopped instance costs only that volume).
	//
	// This is a SECOND, lower-level timer, and it is not the idle-stop the product
	// already had. The reaper (AF_WS_IDLE_TIMEOUT / per-tenant ws_idle_timeout) watches
	// the PERSON and stops their WORKSPACE; every runtime has that. This one starts
	// counting only once the workspace is already stopped, and it acts on the BOX the
	// workspace was using. The two run in series: person goes away → (reaper) workspace
	// stops → (this) slot sleeps.
	//
	// It governs BOTH kinds of dormant slot, from the same value (docs/64 §64.31):
	//   - a slot still holding its owner's home (af-idle-since on the volume) — sweepVolume;
	//   - a slot holding nothing at all (af-slot-idle-since on the instance) — sweepFreeSlots.
	// The second used to have no timer of any kind, which made the "~$9.6 instead of ~$95"
	// this parameter is sold on untrue for every slot that had been released.
	//
	// 0 = OFF (slots never sleep), matching hibernateAfter and what the parameter has
	// always been documented to mean. ⚠️ It used to mean "stop at the very first sweep" —
	// the exact opposite — because the test was a bare `idle < slotSleepAfter`. Same shape
	// as the AF_WS_IDLE_TIMEOUT=0 trap in §64.26: an operator's explicit "off" has to be
	// read as off.
	slotSleepAfter time.Duration
	// slotTerminateAfter is the step AFTER slotSleepAfter on the same clock, and the only
	// one that ends the ROOT volume charge: past it the box is TERMINATED rather than left
	// stopped (AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC).
	//
	// Why it has to exist: nothing else in this adapter ever removes a box, so the number
	// of retained roots only ever grows, and its ceiling is maxSlots. A deployment that
	// raises maxSlots to serve more people is therefore also signing up for maxSlots × the
	// root volume, permanently — 30 × 40 GiB × $0.096/GB-month ≈ $115/month of slots that
	// may all be stopped and idle. Measured on the live deployment (docs/64 §64.32): the
	// only way to get those back was an operator terminating boxes by hand.
	//
	// What it costs the user is 25 seconds, once, and only for the first arrival:
	//   dormant box, home still attached, woken   → 110s  (StartInstances → re-register)
	//   no box at all, built from scratch         → 135s  (RunInstances → boot → … → task)
	// The image cache on the root volume saves the 32s cold pull; instance boot (19s) plus
	// ECS re-registration spends it again. That is the whole trade this timer makes.
	//
	// ⚠️ NO WARM FLOOR, deliberately — "keep N stopped boxes" does not buy what a warm pool
	// usually buys. A dormant box is only reusable BY ITS OWNER (occupiedInstances reads the
	// attachment, so freeSlots never offers it to anyone else; only evictLongestIdle can
	// take it, and only at the cap). Making one generic instead means the next user pays
	// attach + the mount SSM round trip on top of the wake — 123–143s against 135s for a new
	// box — so a shared warm box is worth ≈0. A floor would therefore hold specific people's
	// boxes forever, including through a shutdown when nothing is running at all. The 92s
	// that IS worth having belongs to a RUNNING free slot, which costs ~$95/month, not $3.84.
	//
	// 0 = OFF (boxes are never terminated), which is the default and the behaviour every
	// deployment had before this existed. Read the same way as slotSleepAfter and
	// hibernateAfter: an operator's explicit "off" is off (§64.31.4).
	slotTerminateAfter time.Duration
	// hibernateAfter is the THIRD timer in the same series, and the only one that
	// touches the user's data: once a home has been dormant this long it is snapshotted
	// and its volume deleted (ADR 0045 決定 4 + 決定 13-2). The next Start restores it —
	// 122s and a slower first day, against $4.80 → $1.00 a month for a 20 GiB home
	// nobody has opened.
	//
	// 0 = OFF, and that is the default ON PURPOSE. This is the only automatic path in
	// the product that removes a user's home from where it was, so a deployment has to
	// ask for it. It stays reversible for exactly that reason: hibernation never
	// destroys (which is what makes it safe to run unattended at all).
	//
	// ⚠️ This copy is the DEPLOYMENT DEFAULT, and this layer no longer decides WHEN.
	// The trigger lives in the reaper, which is the only place that can see a tenant's
	// home_hibernate_after (the sweeper starts from EC2 tags and has no view of the CP
	// database — ADR 0012). What is left here is the resume path: the sweeper advances
	// a hibernation ALREADY under way, ungated, so switching the feature off never
	// strands a home half-way. The value is kept so the pool screen can say what the
	// deployment default is.
	hibernateAfter time.Duration
	// backupKeep is how many completed backup copies of a home to keep
	// (AF_ECS_EC2_BACKUP_KEEP). How OFTEN to take one is a tenant answer and lives in the
	// reaper; how many to pay for is the operator's, and lives here. Snapshots are
	// incremental — the second copy of a home costs only the blocks that changed — so the
	// default is small rather than one.
	backupKeep int
	sweepEvery time.Duration
	ghostAfter time.Duration
	// waitBudget bounds every background convergence (slot boot → ECS registration →
	// mount → task). Past it the attempt gives up and leaves the state for the sweeper.
	waitBudget time.Duration
}

// ec2Slot is one purchasable slot size.
//
// vcpu is DECLARED by the operator, not looked up: it exists only so the Console can
// say "you land on m7i.xlarge (4 vCPU / 16 GiB)". Asking EC2 (DescribeInstanceTypes)
// would be authoritative, but it means adding an IAM action to the CP task role and
// redeploying the platform stack for a label — and the operator writing the ladder
// already knows the number (ADR 0045 決定 21). 0 = not declared → not shown.
type ec2Slot struct {
	instanceType string
	memMiB       int64
	vcpu         int
}

// CPU architectures a class may declare. They are the ECS/EC2 spellings, so the
// value goes straight into RuntimePlatform without a second vocabulary.
const (
	ec2ArchX86 = "x86_64"
	ec2ArchArm = "arm64"
)

// ec2SlotClass is one named ladder: which KIND of machine, as opposed to how big.
//
// The architecture is DECLARED, never derived from the instance type's name. "a
// family ending in g is Graviton" reads well until m7gd, x2gd or g4dn, and a wrong
// guess here does not misprint a label — it boots the wrong AMI. The operator
// writing the ladder already knows (the same reasoning ADR 0045 決定 21 used for
// vCPU), and newECSEC2Factory refuses to start when a declared arm64 class has no
// launch template to run on.
type ec2SlotClass struct {
	id    string
	label string
	arch  string
	slots []ec2Slot // ascending by memory
}

// ecsEC2Factory is the `ecs-ec2` RuntimeFactory.
type ecsEC2Factory struct {
	base *ecsFactory
	ec2  ec2API
	ssmc ssmCommandAPI
	ci   ecsContainerInstanceAPI
	pool ec2PoolConfig

	subnetMu sync.Mutex
	subnetAZ map[string]string // subnet-id -> AZ, resolved once
}

var _ RuntimeFactory = (*ecsEC2Factory)(nil)

// WorkspaceImage passes the base factory's answer through (same deployment, same image).
func (f *ecsEC2Factory) WorkspaceImage() string { return f.base.WorkspaceImage() }

// MaxSlots exposes the pool cap to the CP layer, which has the DATABASE this adapter
// deliberately does not (ADR 0012) and therefore is the only place that can compare the
// cap against what the tenants are allowed to run at once (poolBudget).
func (f *ecsEC2Factory) MaxSlots() int { return f.pool.maxSlots }

// newECSEC2Factory builds the EC2-pool Runtime factory. It reuses the Fargate
// factory's AWS config plumbing (region, cluster, subnets, EFS, Service Connect, log
// group, image) and adds the EC2/SSM clients plus the pool settings; then it starts
// the drift sweeper (docs/64 §64.15.6), which is what makes the "no CP-side state"
// choice survivable — every unfinished teardown is re-derived from tags.
func newECSEC2Factory(m *manager) (RuntimeFactory, error) {
	baseFactory, err := newECSFactory(m)
	if err != nil {
		return nil, err
	}
	base, ok := baseFactory.(*ecsFactory)
	if !ok {
		return nil, fmt.Errorf("ecs-ec2: unexpected base factory %T", baseFactory)
	}
	ac, err := awsConfigFor(context.Background(), base.cfg.region)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	pool := ec2PoolConfig{
		launchTemplate: os.Getenv("AF_ECS_EC2_LAUNCH_TEMPLATE"),
		amiArm64:       os.Getenv("AF_ECS_EC2_AMI_ARM64"),
		pool:           envOr("AF_ECS_EC2_POOL", base.cfg.cluster),
		classes:        parseSlotClasses(envOr("AF_ECS_EC2_SLOT_TYPES", "m7i.large:8192,m7i.xlarge:16384,m7i.2xlarge:32768")),
		defaultClass:   os.Getenv("AF_ECS_EC2_DEFAULT_SLOT_CLASS"),
		maxSlots:       envInt("AF_ECS_EC2_MAX_SLOTS", 8),
		homeGiB:        int32(envInt("AF_ECS_EC2_HOME_GB", 50)),
		tmpfsMiB:       int32(envInt("AF_ECS_EC2_TMP_MB", 2048)),
		// noexec is deliberately NOT in the default set. ADR 0045 決定 8 names
		// noexec,nosuid,nodev, but this is a developer container: installers, test
		// runners and build tools routinely exec out of /tmp, and a noexec /tmp turns
		// that into a confusing "Permission denied". The two properties the decision
		// actually wanted from tmpfs — nothing lands on the shared root volume, and the
		// size is capped — hold without it. Deployments that want it back can set
		// AF_ECS_EC2_TMP_OPTS=noexec,nosuid,nodev.
		tmpfsOpts: splitCSV(envOr("AF_ECS_EC2_TMP_OPTS", "nosuid,nodev")),
		// A launch that has not produced a running service within this window is dead,
		// and the workspace must become startable again — the claim is what would
		// otherwise keep answering `starting` forever.
		claimTTL:       time.Duration(envInt("AF_ECS_EC2_CLAIM_TTL_SEC", 300)) * time.Second,
		releaseGrace:   time.Duration(envInt("AF_ECS_EC2_RELEASE_GRACE_SEC", 600)) * time.Second,
		slotSleepAfter: time.Duration(envInt("AF_ECS_EC2_SLOT_SLEEP_SEC", 900)) * time.Second,
		// Default 0 = never terminate. See the field comment: this is the step that gives
		// the root volume back, and it is the pre-existing behaviour, so it is opt-in.
		slotTerminateAfter: time.Duration(envInt("AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC", 0)) * time.Second,
		// Default 0 = no hibernation. See the field comment: this is the one automatic
		// path that moves a user's home, so it is opt-in.
		hibernateAfter: time.Duration(envInt("AF_ECS_EC2_HIBERNATE_AFTER_SEC", 0)) * time.Second,
		backupKeep:     envInt("AF_ECS_EC2_BACKUP_KEEP", 3),
		sweepEvery:     time.Duration(envInt("AF_ECS_EC2_SWEEP_SEC", 300)) * time.Second,
		ghostAfter:     time.Duration(envInt("AF_ECS_EC2_GHOST_AFTER_SEC", 3600)) * time.Second,
		waitBudget:     time.Duration(envInt("AF_ECS_EC2_WAIT_SEC", 600)) * time.Second,
	}
	if err := pool.validate(); err != nil {
		return nil, err
	}
	f := &ecsEC2Factory{
		base: base,
		ec2:  ec2.NewFromConfig(ac),
		ssmc: ssm.NewFromConfig(ac),
		ci:   ecs.NewFromConfig(ac),
		pool: pool,
	}
	log.Printf("runtime=ecs-ec2 pool=%s launch-template=%s arm64-ami=%s classes=%s default-class=%s max=%d home=%dGiB",
		pool.pool, pool.launchTemplate, envOr("AF_ECS_EC2_AMI_ARM64", "(none)"),
		describeSlotClasses(pool.classes), pool.defaultClass, pool.maxSlots, pool.homeGiB)
	go f.sweepLoop(context.Background())
	return f, nil
}

func (f *ecsEC2Factory) New(ws Workspace, secretKey string, extraEnv []string) Runtime {
	base, ok := f.base.New(ws, secretKey, extraEnv).(*ecsRuntime)
	if !ok { // unreachable: ecsFactory.New always returns *ecsRuntime
		panic("ecs-ec2: base factory did not return *ecsRuntime")
	}
	instanceType, arch := f.pool.slotTypeFor(ws.SlotClass, ws.MemBytes)
	return &ecsEC2Runtime{
		base:         base,
		ec2:          f.ec2,
		ssmc:         f.ssmc,
		ci:           f.ci,
		pool:         f.pool,
		instanceType: instanceType,
		arch:         arch,
		homeGiB:      f.homeGiB(ws),
		azOfSubnet:   f.subnetAZs,
		bg:           backgroundWithin(f.pool.waitBudget),
		now:          time.Now,
		sleep:        sleepCtx,
	}
}

// homeGiB is the persistent home size for a workspace: its own disk request, else the
// deployment default. Unlike Fargate's ephemeral storage there is no free tier and no
// 200 GiB ceiling here — the volume is billed as provisioned and grows online with
// ModifyVolume (docs/64 §64.4.5).
func (f *ecsEC2Factory) homeGiB(ws Workspace) int32 {
	if ws.DiskGB > 0 {
		return int32(ws.DiskGB)
	}
	return f.pool.homeGiB
}

// classFor resolves a class id to its declared ladder, falling back to the default
// class (and then to the first) so an id that no longer exists never fails a Start.
// The CP is not the place to enforce that a stored id is still declared — the
// operator can delete a class at any redeploy, and the person whose row still names
// it must keep working. resolveSlotClass reports the substitution; this is the last
// line of defence, not the check.
func (p ec2PoolConfig) classFor(id string) ec2SlotClass {
	for _, c := range p.classes {
		if c.id == id {
			return c
		}
	}
	for _, c := range p.classes {
		if c.id == p.defaultClass {
			return c
		}
	}
	return p.classes[0]
}

// slotTypeFor picks the smallest slot IN THE GIVEN CLASS that holds the workspace's
// memory request, and reports the class's architecture with it. Sizing on EC2 is a
// choice of instance type, not Fargate's 74 discrete (cpu, memory) pairs (docs/64
// §64.4.5): the task reserves neither cpu nor memory, so the user gets the whole box
// (ADR 0045 決定 8).
//
// ⚠️ The memory argument is a REQUIREMENT, not a cap — "fit me into a box this big" —
// and the person then gets whatever that box has. It is also why 0 (unset) lands on
// the SMALLEST slot here, while 0 on Fargate means the deployment's task size and on
// docker means WS_MEMORY. The Console says which of the three it is (ADR 0045 決定 21).
//
// The class only chooses the LADDER. Memory still chooses the rung, in every class,
// which is what keeps "8 GB" meaning the same thing after a class change.
func (p ec2PoolConfig) slotTypeFor(classID string, memBytes int64) (instanceType, arch string) {
	c := p.classFor(classID)
	want := memBytes / mib
	for _, s := range c.slots {
		if want <= s.memMiB {
			return s.instanceType, c.arch
		}
	}
	return c.slots[len(c.slots)-1].instanceType, c.arch
}

// parseSlotClasses reads AF_ECS_EC2_SLOT_TYPES.
//
// Two shapes, and the older one is not deprecated:
//
//	m7i.large:8192:2,m7i.xlarge:16384:4          → one class, id "default", x86_64
//	id|label|arch|<ladder>[; id|label|arch|…]    → one class per entry, in order
//
// ⚠️ The bare form must keep parsing, unchanged, forever. Every deployed
// 30-ingress stack passes it, and an operator upgrading the CP does not touch CFN
// parameters — a spec that stopped parsing would leave the pool with no ladder at
// all, which newECSEC2Factory turns into a refusal to boot.
//
// Entries are separated by ";" or a newline (a CFN parameter is one line, an env file
// is easier to read multi-line). A class with no usable rung is dropped; a class with
// an unknown architecture is dropped rather than defaulted, because defaulting it to
// x86_64 would boot the wrong AMI for a typo'd "aarch64".
func parseSlotClasses(spec string) []ec2SlotClass {
	if !strings.Contains(spec, "|") {
		if slots := parseSlotSizes(spec); len(slots) > 0 {
			return []ec2SlotClass{{id: "default", arch: ec2ArchX86, slots: slots}}
		}
		return nil
	}
	var out []ec2SlotClass
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "|", 4)
		if len(parts) != 4 {
			log.Printf("ecs-ec2: ignoring slot class %q (want id|label|arch|type:memMiB[:vcpu],…)", entry)
			continue
		}
		id, label := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		arch := strings.TrimSpace(parts[2])
		if id == "" {
			continue
		}
		if arch != ec2ArchX86 && arch != ec2ArchArm {
			log.Printf("ecs-ec2: ignoring slot class %q: arch %q is not %s or %s", id, arch, ec2ArchX86, ec2ArchArm)
			continue
		}
		slots := parseSlotSizes(parts[3])
		if len(slots) == 0 {
			log.Printf("ecs-ec2: ignoring slot class %q: no usable rung in %q", id, parts[3])
			continue
		}
		if label == "" {
			label = id
		}
		out = append(out, ec2SlotClass{id: id, label: label, arch: arch, slots: slots})
	}
	return out
}

// parseSlotSizes reads "m7i.large:8192,m7i.xlarge:16384:4" into an ascending list.
// The third field (vCPU) is OPTIONAL and display-only, so every ladder written before
// it existed keeps parsing unchanged; a malformed one drops just the vCPU, never the
// slot — a rung silently vanishing from the ladder would change placement.
func parseSlotSizes(spec string) []ec2Slot {
	var out []ec2Slot
	for _, part := range splitCSV(spec) {
		name, rest, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		memStr, cpuStr, _ := strings.Cut(rest, ":")
		mem, err := strconv.ParseInt(strings.TrimSpace(memStr), 10, 64)
		if err != nil || mem <= 0 || strings.TrimSpace(name) == "" {
			continue
		}
		slot := ec2Slot{instanceType: strings.TrimSpace(name), memMiB: mem}
		if cpu, err := strconv.Atoi(strings.TrimSpace(cpuStr)); err == nil && cpu > 0 {
			slot.vcpu = cpu
		}
		out = append(out, slot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].memMiB < out[j].memMiB })
	return out
}

// subnetAZs maps the deployment's task subnets to their AZ, once. EBS volumes are
// AZ-bound, so nearly every placement decision starts here.
func (f *ecsEC2Factory) subnetAZs(ctx context.Context) (map[string]string, error) {
	f.subnetMu.Lock()
	defer f.subnetMu.Unlock()
	if f.subnetAZ != nil {
		return f.subnetAZ, nil
	}
	out, err := f.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: f.base.cfg.subnets})
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, s := range out.Subnets {
		m[aws.ToString(s.SubnetId)] = aws.ToString(s.AvailabilityZone)
	}
	f.subnetAZ = m
	return m, nil
}

// startGen counts Starts per workspace WITHIN THIS PROCESS. It exists for one race:
// the recreate / clean-home handlers call Stop and then Start immediately, while the
// teardown Stop scheduled — unmount and detach — is still on its way. Releasing a slot
// out from under a workspace that has just been started would drop the task's home
// mount, and the entrypoint would then write into the slot's root disk instead.
//
// The counter is process-local scratch, not state: nothing is authoritative here, and
// losing it on restart only means the sweeper (which re-checks the service anyway) is
// the one that notices. The service's desiredCount is still the primary check — this
// closes the window where Start has begun but has not reached UpdateService yet.
var startGen sync.Map // workspace name -> *atomic.Int64

// startPhase is what the CP is DOING for a workspace right now: "slot: creating",
// "home: restoring", … It exists because on this runtime the first minutes of a start
// are infrastructure work — a new EC2 slot, a 50 GiB volume, an SSM mount — and the
// only thing the Console could say about them was the native runtime's line about
// installing agent CLIs, which is not what is happening (and not what is slow).
//
// Same shape as startGen for the same reason: the Runtime object is rebuilt per
// request, so the phase cannot live on it. Process-local scratch, keyed by workspace
// name; a CP restart or the other replica answering just means no phase, which the
// Console renders as the generic line.
var startPhase sync.Map // workspace name -> string

// setPhase publishes (or with "" clears) the current provisioning step. Clearing is
// not optional: the Console keeps its "starting" dialog open for as long as a phase is
// reported, so a phase left behind by a failed start would never go away.
func (e *ecsEC2Runtime) setPhase(p string) {
	if p == "" {
		startPhase.Delete(e.base.name)
		return
	}
	startPhase.Store(e.base.name, p)
}

// BootPhase satisfies the optional interface GET /api/workspace probes for (the one
// the native rootfs uses for boot-install). Empty = nothing in flight.
func (e *ecsEC2Runtime) BootPhase() string {
	if v, ok := startPhase.Load(e.base.name); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return ""
}

func (e *ecsEC2Runtime) generation() *atomic.Int64 {
	v, _ := startGen.LoadOrStore(e.base.name, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// --- Runtime port ---

func (e *ecsEC2Runtime) Token() string    { return e.base.Token() }
func (e *ecsEC2Runtime) Name() string     { return e.base.Name() }
func (e *ecsEC2Runtime) Endpoint() string { return e.base.Endpoint() }

// State maps the THREE-part truth — home volume, slot, service — onto the port's four
// words (ADR 0045 決定 3-5). The service's desired/running alone cannot tell a booting
// slot from a stopped workspace, and calling a starting workspace `stopped` invites the
// caller to Start it a second time.
//
//	no volume                                  → none
//	live claim (attached or not)               → starting — a Start is converging
//	volume attached, task RUNNING              → running
//	volume attached, service desired 1         → starting
//	volume attached, service desired 0 or gone → stopped
//	volume available, no claim                 → stopped
//
// The claim tag — not the attachment — is what says "starting". An earlier revision
// reported "volume attached but no service yet" as `starting`, and that was a trap:
// Start returns early on `starting`, so a workspace whose launch died between the
// attach and the CreateService could never be started again by anyone. Measured on real
// AWS. Now the same tag that covers the pre-attach window covers the whole launch, and
// it EXPIRES, so a dead launch always falls back to `stopped`.
func (e *ecsEC2Runtime) State(ctx context.Context) string {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		// A cancelled context is the caller leaving (a Console poll aborted because the
		// tab closed or the next poll superseded it), not a fault. Logging it printed an
		// error line per abandoned poll on a real deployment — noise that makes the log
		// harder to read exactly when somebody is reading it for a real failure.
		if ctx.Err() == nil {
			log.Printf("ecs-ec2 state: describe home volume for %s: %v", e.base.name, err)
		}
		return "none"
	}
	if vol == nil {
		return "none"
	}
	if e.claimLive(vol) {
		return "starting"
	}
	if attachedInstance(vol) == "" {
		return "stopped"
	}
	s, ok, err := e.base.describeService(ctx)
	if err != nil {
		log.Printf("ecs-ec2 state: describe service %s: %v", e.base.name, err)
		return "starting" // attached but unknown: never report a live slot as gone
	}
	if !ok {
		// Attached with no service and no claim: an abandoned attachment. Say stopped so
		// Start can pick it up again (it reuses the attachment) and the sweeper can
		// release it.
		return "stopped"
	}
	switch {
	// serviceRolledOut (runtime_ecs.go): launch() re-registers a task definition
	// and forces a new deployment on every Start (2074/2106), including the
	// "reattach to the same slot" case — so a RUNNING task can still share the
	// service with an old task that Service Connect hasn't stopped routing to
	// yet. Reporting "running" before that settles let a client's OAuth
	// start/complete land on two different Agent processes and lose the
	// in-memory flow_id (confirmed 2026-08-19 on the dev deployment).
	case s.DesiredCount >= 1 && s.RunningCount >= 1 && serviceRolledOut(s):
		e.clearBlockedPhase()
		return "running"
	case s.DesiredCount >= 1:
		e.notePlacementBlocked(s)
		return "starting"
	default:
		return "stopped"
	}
}

// notePlacementBlocked surfaces "the scheduler cannot place this task" as a phase, so a
// start that is never going to converge says WHY instead of spinning.
//
// ⚠️ This is the one place State() writes something, which is worth the exception.
// `starting` here means only "desired >= 1 and nothing is running yet", and it has no
// timeout — a task ECS refuses to place holds that forever (docs/70 §70.14.6: a task
// definition declaring ARM64 while pinned to an x86_64 slot). ECS says exactly why in
// the service events; the CP was throwing that away and reporting a bare `starting`,
// so the only way to find out was `aws ecs describe-services` by hand. Nothing else in
// the read path can reach the events — BootPhase() takes no context and cannot call
// AWS — so it is written from here or not at all.
//
// No extra API call: the events come from the DescribeServices the caller already made.
func (e *ecsEC2Runtime) notePlacementBlocked(s ecstypes.Service) {
	why := ecsPlacementBlocked(s)
	if why == "" {
		// Still coming up normally. Do NOT clear the phase here: an ordinary Start is
		// concurrently writing its own progress ("slot: creating", "home: attaching")
		// and this poll must not wipe it.
		return
	}
	phase := blockedPhasePrefix + why
	if e.BootPhase() == phase {
		return // already said, and this runs every 4s
	}
	e.setPhase(phase)
	log.Printf("ecs-ec2: %s cannot be placed and will stay `starting` until this is fixed: %s", e.base.name, why)
}

// clearBlockedPhase removes a blocked phase once the task is actually running. Scoped to
// the prefix on purpose: any other phase belongs to a Start that is still in flight, and
// clearing that from a poll would blank the starting dialog mid-boot.
func (e *ecsEC2Runtime) clearBlockedPhase() {
	if strings.HasPrefix(e.BootPhase(), blockedPhasePrefix) {
		e.setPhase("")
	}
}

// blockedPhasePrefix is the contract with the Console: WsStartingDialog maps this
// prefix to a localized headline and prints the rest — the raw ECS sentence — beneath
// it. The raw text is the valuable half; it names the constraint that failed.
const blockedPhasePrefix = "blocked: "

// ecsPlacementBlocked returns the ECS event explaining why the CURRENT deployment cannot
// be placed, or "" if there is no such event.
//
// Scoped to events newer than the PRIMARY deployment: a service that was wedged, got
// fixed and redeployed still carries the old complaint in its event list, and reporting
// that would turn a normal cold start into a fake diagnosis.
//
// ⚠️ ECS de-duplicates its own events, so the message appears once and then goes quiet
// for a long while. That is exactly why this is matched against the deployment rather
// than "was there an event recently": the wedge is permanent but the event is not
// repeated (measured — the run that prompted this had a single event and then silence).
func ecsPlacementBlocked(s ecstypes.Service) string {
	var since time.Time
	for _, d := range s.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" && d.CreatedAt != nil {
			since = *d.CreatedAt
		}
	}
	for _, ev := range s.Events {
		if ev.CreatedAt == nil || ev.CreatedAt.Before(since) {
			continue
		}
		if msg := aws.ToString(ev.Message); strings.Contains(msg, "unable to place a task") {
			return strings.TrimSpace(msg)
		}
	}
	return ""
}

// Start brings the workspace up on a slot. Everything that can be slow is pushed off
// the caller's thread: Start runs inside an HTTP request behind a 60s-idle ALB
// (docs/62 §62.5), so only the hot path — attach 3s + mount 4s + a few API calls —
// may run synchronously. Booting a slot (19s) or creating one (8s to running, then
// ~20s to register) hands off to a background goroutine and State() says `starting`.
func (e *ecsEC2Runtime) Start(ctx context.Context) error {
	switch e.State(ctx) {
	case "running":
		return nil
	case "starting":
		// Already converging. Re-entering would attach a second slot / restart the
		// service deployment from zero, exactly as on Fargate.
		return nil
	}
	// Mark that a Start has begun, so a teardown still draining from the Stop that the
	// recreate / clean-home handlers issued a moment ago aborts instead of pulling this
	// workspace's home out from under it.
	e.generation().Add(1)
	e.setPhase("preparing")
	prep, err := e.prepare(ctx)
	if err != nil {
		e.setPhase("")
		return err
	}
	place, err := e.placeHome(ctx)
	if err != nil {
		e.setPhase("")
		return err
	}
	if place.deferred {
		// The slot is not attachable yet (pending instance) or not registered with ECS
		// yet (just started). Finish in the background; the claim tag / the attachment
		// is what keeps State() at `starting` until it lands.
		e.bg(ctx, func(c context.Context) { e.finishStart(c, place, prep) })
		return nil
	}
	defer e.setPhase("")
	return e.launch(ctx, place, prep)
}

// Stop scales the service to zero and LEAVES THE HOME ATTACHED — "lazy release".
//
// The first implementation unmounted and detached right away, which handed the slot
// back in ~13s but made the common case pay for it: the same person coming back after
// lunch had to find a slot, attach, mkfs-check and mount again. Keeping the attachment
// makes that return the cheapest path there is (measured 13.2s: no attach, no mount,
// just the service going back to desiredCount 1), and it gives the affinity for free —
// **the attachment IS "this user's slot"**, so nothing has to be remembered anywhere.
//
// Two things keep it from turning into hoarding, both in the sweeper:
//
//   - the slot is STOPPED after AF_ECS_EC2_SLOT_SLEEP_SEC (default 15m). A stopped slot
//     costs only its root volume (~$9.6/month vs ~$95 running) and keeps the image
//     cache, so the return is ~90s instead of 135s.
//   - when someone else needs a slot and the pool is at its cap, the longest-idle
//     occupant is evicted (placeHome → evict).
//
// Nothing is unmounted here, so nothing can be corrupted here. The teardown order
// (umount before detach) still lives in releaseSlot, which is now reached only by
// eviction, Destroy and drift repair.
func (e *ecsEC2Runtime) Stop(ctx context.Context) error {
	if _, ok, err := e.base.describeService(ctx); err != nil {
		return err
	} else if ok {
		if _, err := e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster:      aws.String(e.base.cfg.cluster),
			Service:      aws.String(e.base.name),
			DesiredCount: aws.Int32(0),
		}); err != nil {
			return err
		}
	}
	// Record when the dormancy started. If this write is lost (CP restart mid-Stop) the
	// sweeper stamps it the first time it sees an idle attachment, so the worst case is
	// that the slot sleeps one sweep later.
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil || attachedInstance(vol) == "" {
		return nil
	}
	e.markIdle(ctx, aws.ToString(vol.VolumeId))
	return nil
}

// markIdle / clearIdle move the home in and out of the dormant set.
func (e *ecsEC2Runtime) markIdle(ctx context.Context, volumeID string) {
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagIdleSince), Value: aws.String(e.now().UTC().Format(time.RFC3339))}},
	}); err != nil {
		log.Printf("ecs-ec2: marking %s idle failed (the sweeper will stamp it): %v", volumeID, err)
	}
}

func (e *ecsEC2Runtime) clearIdle(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagIdleSince)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the idle mark on %s failed: %v", volumeID, err)
	}
}

// clearDormancy is what a START calls: the owner is back, so both the dormancy clock and
// any in-flight hibernation are off. It is deliberately NOT the same thing as clearIdle —
// hibernation's own first step is releaseSlot, which clears the idle mark, and if that
// also dropped the hibernation mark the operation would forget itself one line after
// starting.
//
// A snapshot already running is left to finish rather than cancelled: the next
// hibernation drops it as superseded (it started before that one's mark), and
// DeleteSnapshot racing a completing capture buys nothing.
func (e *ecsEC2Runtime) clearDormancy(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagIdleSince)}, {Key: aws.String(ec2TagHibernating)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the dormancy marks on %s failed: %v", volumeID, err)
	}
}

// idleSince reports how long a home has been dormant, and whether it is dormant at all.
func idleSince(vol *ec2types.Volume, now time.Time) (time.Duration, bool) {
	v := ec2TagValue(vol.Tags, ec2TagIdleSince)
	if v == "" {
		return 0, false
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0, false
	}
	return now.Sub(at), true
}

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
	// keep-list is under 100 MiB (ADR 0045 決定 3-6). The entrypoint links ~ into it.
	if p.keepAP, err = e.ensureAccessPoint(ctx, "keep-ec2", "/home-keep/"+e.base.membershipID); err != nil {
		return p, fmt.Errorf("efs keep access point: %w", err)
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
	out, err := e.base.efs.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(e.base.cfg.efsFileSystem),
	})
	if err != nil {
		return "", err
	}
	for _, ap := range out.AccessPoints {
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
}

// placeHome resolves the volume and the slot, attaching the two together when it can.
// The order follows the AZ (docs/64 §64.15.3): the volume pins the AZ, the AZ picks
// the candidate slots, and only a user with no volume yet is free to follow the pool.
// slotTypeMatches reports whether an instance is still the size AND class this
// workspace needs. One DescribeInstances, no caching — it is asked once per Start.
//
// An instance that has vanished (terminated between the volume read and here) counts
// as NOT matching, so the caller releases and re-places rather than pinning a task to
// a box that is gone.
func (e *ecsEC2Runtime) slotTypeMatches(ctx context.Context, instanceID string) (bool, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		if isAWSNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("describe slot %s: %w", instanceID, err)
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			return string(inst.InstanceType) == e.instanceType, nil
		}
	}
	return false, nil
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
			// Measured on a live deployment (docs/70 §70.14.5): a member moved from
			// the saver class (m6i, x86_64) to arm (m8g, arm64) and their workspace
			// could not start again. So the match is checked before the affinity is
			// honoured, and a stale slot is released rather than reused.
			matches, err := e.slotTypeMatches(ctx, inst)
			if err != nil {
				return ec2Placement{}, err
			}
			if !matches {
				log.Printf("ecs-ec2: %s now needs %s but its home is on %s; releasing that slot first",
					e.base.name, e.instanceType, inst)
				if err := e.releaseSlot(ctx); err != nil {
					return ec2Placement{}, fmt.Errorf("release the slot after a size/class change: %w", err)
				}
				// Re-read and fall through: the volume is detached now, so the code below
				// places it like any other homeless home. Note it keeps its AZ — an EBS
				// volume can never leave the one it was created in — so the search below
				// is correctly restricted to that AZ.
				if vol, err = e.homeVolume(ctx); err != nil {
					return ec2Placement{}, fmt.Errorf("describe home volume after release: %w", err)
				}
			} else {
				// The home is still on a slot — the normal case now that Stop leaves it
				// there (lazy release). This is both the affinity ("the same user gets the
				// same slot") and the cheapest path: no attach, no mount to redo.
				volID := aws.ToString(vol.VolumeId)
				if err := e.claim(ctx, volID, inst); err != nil {
					log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, err)
				}
				e.clearDormancy(ctx, volID)
				running, err := e.instanceRunning(ctx, inst)
				if err != nil {
					return ec2Placement{}, err
				}
				// A dormant slot keeps both the image cache and the attachment, so waking it
				// is the ~90s path rather than the 135s of building a new one.
				return ec2Placement{
					volumeID: volID, instanceID: inst, az: aws.ToString(vol.AvailabilityZone),
					deferred: !running, wake: !running,
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
	// It used to be created first, from anyAZ(). That is the same answer whenever a slot is
	// free, but it made the two growth paths below unable to look anywhere else: a capacity
	// shortfall in that one AZ could only be answered by deleting the volume and starting
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
		if err := e.waitVolumeAttachable(ctx, volID); err != nil {
			return ec2Placement{}, err
		}
		if err := e.attachHomeWithRetry(ctx, volID, s.id); err != nil {
			log.Printf("ecs-ec2 start: slot %s did not take %s (%v); trying the next one", s.id, volID, err)
			continue
		}
		// Claim it for the whole launch, not just until the attach: State() reads this
		// tag to say `starting`, and Start early-returns on `starting`.
		if err := e.claim(ctx, volID, s.id); err != nil {
			log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, err)
		}
		// A hot, already-registered slot is the only case that can finish inline.
		return ec2Placement{
			volumeID: volID, instanceID: s.id, az: azFilter,
			deferred: !(s.running && s.registered), wake: !s.running,
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
			// (docs/64 §64.33). Falls through to growPool below.
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
			if err := e.waitVolumeAttachable(ctx, volID); err != nil {
				return ec2Placement{}, err
			}
			if err := e.attachHomeWithRetry(ctx, volID, victim); err != nil {
				return ec2Placement{}, fmt.Errorf("attach %s to the reclaimed slot %s: %w", volID, victim, err)
			}
			if err := e.claim(ctx, volID, victim); err != nil {
				log.Printf("ecs-ec2 start: could not mark %s as converging: %v", volID, err)
			}
			e.clearDormancy(ctx, volID)
			running, err := e.instanceRunning(ctx, victim)
			if err != nil {
				return ec2Placement{}, err
			}
			return ec2Placement{volumeID: volID, instanceID: victim, az: azFilter, deferred: true, wake: !running}, nil
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
	return ec2Placement{volumeID: volID, instanceID: inst, az: azFilter, deferred: true, claimed: true}, nil
}

// finishStart is the background half: wait for the slot, attach if the claim path left
// that undone, then launch. Everything it does is idempotent and re-derivable, so a CP
// restart in the middle costs a retry, not a wedged workspace.
func (e *ecsEC2Runtime) finishStart(ctx context.Context, p ec2Placement, prep ec2Prep) {
	// Whatever happens — converged, failed, or gave up — the workspace is no longer
	// mid-provisioning, and the Console's dialog keys off exactly this.
	defer e.setPhase("")
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
		e.setPhase("home: attaching")
		if err := e.attachHome(ctx, p.volumeID, p.instanceID); err != nil {
			log.Printf("ecs-ec2 start: attaching %s to the new slot %s failed: %v", p.volumeID, p.instanceID, err)
			return
		}
	}
	if err := e.launch(ctx, p, prep); err != nil {
		log.Printf("ecs-ec2 start: %s did not converge on slot %s: %v", e.base.name, p.instanceID, err)
	}
}

// launch takes an attached volume to a running task: register the task definition and
// point the service at it, wait for the slot to be a usable ECS container instance,
// mount the home, and only then put the service at desiredCount 1.
//
// ⚠️ The ORDER of those two service calls is the whole point, and it is worth ~40
// seconds on 40% of Starts (docs/64 §64.39, measured on the production pool).
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

	e.setPhase("slot: joining the cluster")
	if err := e.waitSlotRegistered(ctx, p.instanceID); err != nil {
		return fmt.Errorf("slot %s not registered with the cluster: %w", p.instanceID, err)
	}
	e.setPhase("home: mounting")
	if err := e.mountHome(ctx, p); err != nil {
		// A slot that cannot mount is not a slow slot, it is a broken one, and leaving it
		// in the pool means the next Start picks it too (measured: it did, for every user
		// that followed). Take it out of the world before returning.
		e.quarantineSlot(ctx, p, err)
		return fmt.Errorf("mount home on %s: %w", p.instanceID, err)
	}
	// The home is on this box now, so the box is this person's for as long as it stays
	// there — stamp it for the bill. Deliberately AFTER the mount and best-effort: the
	// tag is read by Cost Explorer and by nothing else (ADR 0048 決定 3).
	e.tagSlotOwner(ctx, p.instanceID)
	// …and it is no longer free, so its free-pool clock stops. Same moment, opposite tag.
	e.clearSlotFree(ctx, p.instanceID)
	e.setPhase("task: starting")
	if prepared {
		// No wait here any more, and that is the whole point of ec2SingleTaskDeployment:
		// the service cannot run two tasks, so raising the count past a not-yet-retired
		// deployment can no longer hand one to the old revision. Measured A/B on one
		// substrate, same sequence, same moment (docs/64 §64.39.10):
		//   maximumPercent 200 → 2 tasks, the FIRST from the old revision (2 of 2)
		//   maximumPercent 100 → 1 task, always the new revision  (2 of 2)
		// Before that this spot waited 53-55s for the old deployment to leave ACTIVE.
		if err := e.scaleUpService(ctx); err != nil {
			return fmt.Errorf("service: %w", err)
		}
	} else if err := e.upsertService(ctx, taskDefArn, p, !reused); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	e.finishLaunch(ctx, p)
	return nil
}

// finishLaunch is what every successful launch ends with: the service carries the state
// now, so drop the claim and make sure the home is not counted as dormant while its task
// runs.
func (e *ecsEC2Runtime) finishLaunch(ctx context.Context, p ec2Placement) {
	e.unclaim(ctx, p.volumeID)
	e.clearDormancy(ctx, p.volumeID)
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
//  2. detach the home. The volume is the user's; it has to be able to attach elsewhere,
//     and on the failure this was written for it was never actually opened here.
//  3. drop the claim, so the owner's next Start is immediate rather than waiting out the
//     claim TTL on a slot that will never work.
//  4. stop the instance. It cannot run tasks, and a wedged kernel is not something the CP
//     can repair — but it bills by the hour until an operator looks. Stopping keeps the
//     root volume (and the evidence) while ending the compute charge; terminating it is
//     still the operator's call. ⚠️ The sweeper's own terminate stage (slotTerminateAfter)
//     will NOT collect this box either: quarantineSlot re-tags it af-role=quarantined, and
//     both sweeper walks filter on af-role=slot. The evidence survives on purpose.
func (e *ecsEC2Runtime) quarantineSlot(ctx context.Context, p ec2Placement, cause error) {
	reason := cause.Error()
	if len(reason) > 200 {
		reason = reason[:200]
	}
	log.Printf("ecs-ec2: QUARANTINING slot %s — it could not mount %s for %s: %v",
		p.instanceID, p.volumeID, e.base.name, cause)
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{p.instanceID},
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleQuarantined)},
			{Key: aws.String(ec2TagQuarantineReason), Value: aws.String(reason)},
			{Key: aws.String(ec2TagQuarantineAt), Value: aws.String(e.now().UTC().Format(time.RFC3339))},
		},
	}); err != nil {
		// This is the step that stops the bleeding; say so loudly when it fails, because
		// nothing else here prevents the next user from landing on the same box.
		log.Printf("ecs-ec2: could not quarantine slot %s (it will be offered again): %v", p.instanceID, err)
	}
	// A quarantined box is nobody's. It reaches here before launch stamps the owner, so
	// normally there is nothing to remove — but a previous tenancy whose release failed
	// would otherwise keep billing a person for a box they can no longer use.
	e.untagSlotOwner(ctx, p.instanceID)
	if p.volumeID != "" {
		if _, err := e.ec2.DetachVolume(ctx, &ec2.DetachVolumeInput{
			VolumeId:   aws.String(p.volumeID),
			InstanceId: aws.String(p.instanceID),
		}); err != nil {
			log.Printf("ecs-ec2: detaching %s from the quarantined slot %s: %v", p.volumeID, p.instanceID, err)
		}
		e.unclaim(ctx, p.volumeID)
	}
	if _, err := e.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{p.instanceID}}); err != nil {
		log.Printf("ecs-ec2: stopping the quarantined slot %s: %v", p.instanceID, err)
	}
}

// --- volumes ---

// homeVolume returns this workspace's persistent home volume, or nil. Tag lookup only:
// this is what keeps the adapter stateless (ADR 0012).
func (e *ecsEC2Runtime) homeVolume(ctx context.Context) (*ec2types.Volume, error) {
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagMembership, e.base.membershipID),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return nil, err
	}
	for i := range out.Volumes {
		if out.Volumes[i].State == ec2types.VolumeStateDeleting || out.Volumes[i].State == ec2types.VolumeStateDeleted {
			continue
		}
		return &out.Volumes[i], nil
	}
	return nil, nil
}

// createHomeVolume creates the user's home in the given AZ. Tags go in the SAME call
// (TagSpecifications), never as a follow-up CreateTags: an untagged volume is an
// invisible volume — State() would say `none`, the next Start would make another one,
// and the first would be billed forever with nothing pointing at it.
//
// "Create" is really three cases, tried in this order:
//
//  1. this user's home was HIBERNATED (§64.18.3) → rebuild it from that snapshot, so the
//     workspace cannot tell it ever went away;
//  2. a GOLDEN snapshot matches the running image (決定 9) → a new user skips
//     boot-install (48s) and a cold npm cache;
//  3. neither → an empty volume, which is correct, just slow.
//
// The order is the point: the user's own home always wins. Handing somebody the golden
// image when their real home merely failed to be found would be silent data loss.
func (e *ecsEC2Runtime) createHomeVolume(ctx context.Context, az string) (*ec2types.Volume, error) {
	snapshotID, err := e.restoreSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	restored := snapshotID != ""
	if restored {
		// Two different waits with two different explanations: restoring somebody's
		// hibernated home is not the same as making a new one, and the Console should
		// not call both "starting".
		e.setPhase("home: restoring")
		log.Printf("ecs-ec2: restoring %s from %s", e.base.name, snapshotID)
	} else if golden := e.goldenSnapshot(ctx); golden != "" {
		snapshotID = golden
		e.setPhase("home: creating")
		log.Printf("ecs-ec2: seeding a new home for %s from the golden snapshot %s", e.base.name, golden)
	} else {
		e.setPhase("home: creating")
	}
	var snapshot *string
	if snapshotID != "" {
		snapshot = aws.String(snapshotID)
	}
	out, err := e.ec2.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(az),
		SnapshotId:       snapshot,
		Size:             aws.Int32(e.homeGiB),
		VolumeType:       ec2types.VolumeTypeGp3,
		Encrypted:        aws.Bool(true),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVolume,
			Tags: e.ownedTags([]ec2types.Tag{
				{Key: aws.String(ec2TagMembership), Value: aws.String(e.base.membershipID)},
				{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
				{Key: aws.String(ec2TagWorkspace), Value: aws.String(e.base.name)},
				{Key: aws.String(ec2TagPool), Value: aws.String(e.pool.pool)},
				{Key: aws.String("Name"), Value: aws.String(e.base.name + "-home")},
			}),
		}},
	})
	if err != nil {
		return nil, err
	}
	log.Printf("ecs-ec2: created %s (%d GiB, %s) for %s", aws.ToString(out.VolumeId), e.homeGiB, az, e.base.name)
	if restored {
		// ONLY after restoring the user's OWN home. The golden snapshot is shared by every
		// future user and must never be swept up here — deleteHomeSnapshots filters on
		// af-membership + af-role=home and so cannot match it, but the intent is worth
		// stating twice for a call that deletes things.
		//
		// The home is the volume again, so the snapshot is now a stale copy that bills.
		// In the background and only once the volume is usable — and if it fails, the
		// next hibernation drops it as superseded rather than trusting it.
		volID := aws.ToString(out.VolumeId)
		e.bg(ctx, func(ctx context.Context) {
			if err := e.waitVolumeAttachable(ctx, volID); err != nil {
				log.Printf("ecs-ec2: %s restored but not usable yet; keeping %s: %v", volID, snapshotID, err)
				return
			}
			if err := e.deleteHomeSnapshots(ctx); err != nil {
				log.Printf("ecs-ec2: could not drop the restored-from snapshot of %s: %v", e.base.name, err)
			}
		})
	}
	return &ec2types.Volume{
		VolumeId:         out.VolumeId,
		AvailabilityZone: out.AvailabilityZone,
		State:            out.State,
	}, nil
}

// waitVolumeAttachable polls until the volume leaves `creating`. Measured at ~3s for a
// fresh volume (docs/64 §64.7), which is why this can sit in the synchronous path.
func (e *ecsEC2Runtime) waitVolumeAttachable(ctx context.Context, volumeID string) error {
	for i := 0; ; i++ {
		out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
		if err != nil {
			return err
		}
		if len(out.Volumes) == 0 {
			return fmt.Errorf("volume %s disappeared", volumeID)
		}
		switch out.Volumes[0].State {
		case ec2types.VolumeStateAvailable, ec2types.VolumeStateInUse:
			return nil
		case ec2types.VolumeStateError, ec2types.VolumeStateDeleting, ec2types.VolumeStateDeleted:
			return fmt.Errorf("volume %s is %s", volumeID, out.Volumes[0].State)
		}
		if i >= 30 {
			return fmt.Errorf("volume %s still %s", volumeID, out.Volumes[0].State)
		}
		if err := e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// attachHomeWithRetry keeps trying while the only problem is that the slot's attachment
// point has not been given back yet.
//
// Measured on real AWS: DetachVolume answers, the volume reads `available`, and an
// attach to the same slot is STILL refused ~7s later with "Attachment point /dev/sdf is
// already in use". Without this retry the next Start reads that as "no free slot" and
// grows the pool — paying for an instance and ~90s to avoid a wait of a few seconds,
// which is precisely the cost the pool exists to avoid. A genuine race with another
// workspace (the device really is taken now) simply exhausts the window and moves on to
// the next candidate.
func (e *ecsEC2Runtime) attachHomeWithRetry(ctx context.Context, volumeID, instanceID string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = e.attachHome(ctx, volumeID, instanceID); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "already in use") {
			return err
		}
		if serr := e.sleep(ctx, 2*time.Second); serr != nil {
			return serr
		}
	}
	return err
}

func (e *ecsEC2Runtime) attachHome(ctx context.Context, volumeID, instanceID string) error {
	_, err := e.ec2.AttachVolume(ctx, &ec2.AttachVolumeInput{
		Device:     aws.String(ec2HomeDevice),
		InstanceId: aws.String(instanceID),
		VolumeId:   aws.String(volumeID),
	})
	return err
}

// claim / unclaim mark the one window where a workspace is starting but has nothing
// attached yet (see ec2TagClaim).
func (e *ecsEC2Runtime) claim(ctx context.Context, volumeID, instanceID string) error {
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{volumeID},
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagClaim), Value: aws.String(instanceID)},
			{Key: aws.String(ec2TagClaimAt), Value: aws.String(e.now().UTC().Format(time.RFC3339))},
		},
	})
	return err
}

func (e *ecsEC2Runtime) unclaim(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagClaim)}, {Key: aws.String(ec2TagClaimAt)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the claim on %s failed (sweeper will): %v", volumeID, err)
	}
}

// ownedTags appends `af-tenant` when this workspace knows its tenant slug. Nothing in
// the pool reads it — it exists so the invoice can be grouped by tenant (ADR 0048 決定 3).
// An unknown slug appends nothing rather than an empty value: an empty cost allocation
// tag is a real group in the bill, and "tenant = (blank)" reads like a bug.
func (e *ecsEC2Runtime) ownedTags(tags []ec2types.Tag) []ec2types.Tag {
	if e.base.tenantSlug == "" {
		return tags
	}
	return append(tags, ec2types.Tag{Key: aws.String(ec2TagTenant), Value: aws.String(e.base.tenantSlug)})
}

// tagSlotOwner / untagSlotOwner put this workspace's owner tags on the INSTANCE, and
// take them off again when the slot goes back to the pool.
//
// Why the instance and not just the home volume (docs/67 §67.4): measured on the live
// deployment, 91% of the cost that can be attributed to a person at all is the slot's
// instance-hours, and the instance carried af-role/af-pool/af-slot-size but NO
// af-membership. That is correct as pool logic — a slot belongs to nobody until a home
// is attached to it — but it means the cost allocation tag had nothing to attach to.
//
// ⚠️ Neither call may fail a Start or a Stop. These tags are read by the bill and by
// nothing else; the sweeper repairs whatever a crash leaves behind (sweepSlotOwnerTags).
// A pool that stops working because a billing tag could not be written would be a far
// worse bug than a mis-attributed hour.
//
// ⚠️ Untag on release is what keeps a WARM POOL slot out of somebody's bill. Without it
// the last user of a box keeps paying for it while it sits idle waiting for the next
// person — and "idle pool" is exactly the shared cost the operator needs to see as
// shared (ADR 0048 決定 4).
func (e *ecsEC2Runtime) tagSlotOwner(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	tags := e.ownedTags([]ec2types.Tag{
		{Key: aws.String(ec2TagMembership), Value: aws.String(e.base.membershipID)},
	})
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      tags,
	}); err != nil {
		log.Printf("ecs-ec2: stamping the owner of slot %s failed (billing only): %v", instanceID, err)
	}
}

func (e *ecsEC2Runtime) untagSlotOwner(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership)},
			{Key: aws.String(ec2TagTenant)},
		},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the owner of slot %s failed (sweeper will): %v", instanceID, err)
	}
}

// markSlotFree / clearSlotFree move a SLOT in and out of the free set's dormancy clock,
// exactly as markIdle / clearIdle do for a home. They are the instance-side pair because
// a free slot has no volume to carry the mark (see ec2TagSlotIdleSince).
//
// ⚠️ Best-effort like the owner tags, and for the same reason: neither a Start nor a Stop
// may fail because a bookkeeping tag could not be written. The sweeper re-stamps a missing
// mark on its next pass, which costs one sweep of extra running time and nothing else.
func (e *ecsEC2Runtime) markSlotFree(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{{
			Key: aws.String(ec2TagSlotIdleSince), Value: aws.String(e.now().UTC().Format(time.RFC3339)),
		}},
	}); err != nil {
		log.Printf("ecs-ec2: marking slot %s free failed (the sweeper will stamp it): %v", instanceID, err)
	}
}

// clearSlotFree is called when a workspace takes the slot. Without it a box that is used
// for less than one sweep interval keeps the mark from BEFORE that tenancy, and the next
// release would look older than it is — the slot would sleep with no grace at all.
func (e *ecsEC2Runtime) clearSlotFree(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagSlotIdleSince)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the free mark on slot %s failed (sweeper will): %v", instanceID, err)
	}
}

// claimLive reports whether the volume carries a claim that has not expired. The TTL
// matters: a claim that outlives its failed launch pins the workspace at `starting`
// forever, and Start returns early on `starting`, so the user could never recover.
func (e *ecsEC2Runtime) claimLive(vol *ec2types.Volume) bool {
	if ec2TagValue(vol.Tags, ec2TagClaim) == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, ec2TagValue(vol.Tags, ec2TagClaimAt))
	if err != nil {
		return false
	}
	return e.now().Sub(at) < e.pool.claimTTL
}

// --- slots ---

type ec2SlotCandidate struct {
	id         string
	az         string
	running    bool
	registered bool
}

// slotsOfMyType lists the pool's slots THIS workspace could actually run on: the right
// instance type, and (when az is set) the AZ its home is pinned to. Both placement paths
// start here — freeSlots for an empty box, evictLongestIdle for one it has to take back.
//
// ⚠️ It exists because those two did NOT agree. freeSlots filtered on instance-type from
// the start; evictLongestIdle filtered only by AZ and by "not me", so at the cap a member
// on a bigger rung could take a SMALLER box and be attached to it. ECS then refuses to
// place the task — "no container instance met all of its requirements" — and the service
// stays at desiredCount 1 / runningCount 0 forever, which is a stuck workspace rather than
// a slow one. Across architectures placeHome's own header records the same symptom
// measured on a live deployment (docs/70 §70.14.5). Two copies of one rule is how that
// happened, so there is now one.
//
// az == "" means the whole pool: a home that does not exist yet is not pinned anywhere,
// and where a slot can be had is what decides its AZ.
func (e *ecsEC2Runtime) slotsOfMyType(ctx context.Context, az string) (*ec2.DescribeInstancesOutput, error) {
	filters := []ec2types.Filter{
		tagFilter(ec2TagPool, e.pool.pool),
		tagFilter(ec2TagRole, ec2RoleSlot),
		{Name: aws.String("instance-state-name"), Values: []string{"running", "stopped"}},
		{Name: aws.String("instance-type"), Values: []string{e.instanceType}},
	}
	if az != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("availability-zone"), Values: []string{az}})
	}
	return e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filters})
}

// freeSlots lists the pool's slots that nobody's home is on, hot ones first (22–27s to
// swap) and stopped ones after (~50s).
//
// Occupancy is read from the VOLUMES, not from the instances' BlockDeviceMappings. Both
// are eventually consistent, but measured on real AWS the instance view lags: seconds
// after a detach had completed (volume `available`), the instance still listed /dev/sdf,
// so the freed slot looked busy and the next Start grew the pool instead of swapping
// onto it — the exact behaviour this design exists to avoid. The volume side is also the
// same source State() and releaseSlot() already trust, so there is one story about who
// is on which slot instead of two.
//
// Claims cover the rest: a slot some other workspace is launching onto has no attachment
// yet, and only its claim says so.
func (e *ecsEC2Runtime) freeSlots(ctx context.Context, az string) ([]ec2SlotCandidate, error) {
	out, err := e.slotsOfMyType(ctx, az)
	if err != nil {
		return nil, err
	}
	busy, err := e.occupiedInstances(ctx)
	if err != nil {
		return nil, err
	}
	registered, err := e.registeredSlots(ctx)
	if err != nil {
		return nil, err
	}
	var cands []ec2SlotCandidate
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			if busy[id] {
				continue
			}
			cands = append(cands, ec2SlotCandidate{
				id:         id,
				az:         aws.ToString(inst.Placement.AvailabilityZone),
				running:    inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning,
				registered: registered[id],
			})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].running != cands[j].running {
			return cands[i].running
		}
		if cands[i].registered != cands[j].registered {
			return cands[i].registered
		}
		return cands[i].id < cands[j].id
	})
	return cands, nil
}

// occupiedInstances is the set of slots that are taken: someone's home is attached to
// them, or someone is launching onto them (claim). One DescribeVolumes answers both.
func (e *ecsEC2Runtime) occupiedInstances(ctx context.Context) (map[string]bool, error) {
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return nil, err
	}
	busy := map[string]bool{}
	for i := range out.Volumes {
		if inst := attachedInstance(&out.Volumes[i]); inst != "" {
			busy[inst] = true
		}
		if inst := ec2TagValue(out.Volumes[i].Tags, ec2TagClaim); inst != "" && e.claimLive(&out.Volumes[i]) {
			busy[inst] = true
		}
	}
	return busy, nil
}

// registeredSlots is the set of EC2 instance ids the cluster currently accepts tasks
// on (ACTIVE + agentConnected). An unregistered slot can hold a volume but cannot run
// the task yet, so it sorts behind the hot ones rather than being skipped.
func (e *ecsEC2Runtime) registeredSlots(ctx context.Context) (map[string]bool, error) {
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return map[string]bool{}, err
	}
	ready := map[string]bool{}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(e.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return nil, err
		}
		for _, ci := range out.ContainerInstances {
			if aws.ToString(ci.Status) == "ACTIVE" && ci.AgentConnected {
				ready[aws.ToString(ci.Ec2InstanceId)] = true
			}
		}
	}
	return ready, nil
}

func (e *ecsEC2Runtime) listContainerInstanceARNs(ctx context.Context) ([]string, error) {
	var arns []string
	var next *string
	for {
		out, err := e.ci.ListContainerInstances(ctx, &ecs.ListContainerInstancesInput{
			Cluster:   aws.String(e.base.cfg.cluster),
			NextToken: next,
		})
		if err != nil {
			return nil, err
		}
		arns = append(arns, out.ContainerInstanceArns...)
		if next = out.NextToken; next == nil {
			return arns, nil
		}
	}
}

// runSlot grows the pool by one instance in the given AZ. The launch template owns
// everything about what a slot IS (AMI, user-data, instance profile, security group);
// the CP only chooses size and placement.
// growPool adds a slot and reports which AZ it landed in.
//
// The az argument is a CONSTRAINT, not a preference. An existing home cannot move, so a
// slot anywhere else is useless to it and a capacity failure there is a real failure. A
// home that does not exist yet passes "" and may go wherever there is room — which is the
// only reason to try more than one AZ.
//
// Without this, one AZ running out of the slot type stops every NEW user in the deployment
// (everybody already placed keeps working, so it does not look like an outage) — and the
// AZ that ran out is the one AZ the adapter ever picks, because anyAZ is deterministic.
// docs/64 §64.20.4.
func (e *ecsEC2Runtime) growPool(ctx context.Context, az string) (string, string, error) {
	if az != "" {
		id, err := e.runSlot(ctx, az)
		return id, az, err
	}
	azs, err := e.spreadAZs(ctx)
	if err != nil {
		return "", "", err
	}
	if len(azs) == 0 {
		return "", "", fmt.Errorf("no usable subnet/AZ configured (AF_ECS_SUBNETS)")
	}
	var lastErr error
	for _, candidate := range azs {
		id, err := e.runSlot(ctx, candidate)
		if err == nil {
			return id, candidate, nil
		}
		// Only a "there is no room here" answer is worth asking somewhere else. A bad
		// launch template or a hit quota fails identically in every AZ, and retrying it
		// three times just buries the real message under the last one.
		if !isEC2CapacityError(err) {
			return "", "", err
		}
		log.Printf("ecs-ec2: %s cannot take a %s right now (%v); trying the next AZ", candidate, e.instanceType, err)
		lastErr = err
	}
	return "", "", fmt.Errorf("no configured AZ (%s) could take a new %s slot: %w",
		strings.Join(azs, ", "), e.instanceType, lastErr)
}

// isEC2CapacityError reports whether RunInstances failed for a reason that another AZ
// might not have. Matched on the message like isAWSNotFound, because that is how this
// package reads AWS error codes.
func isEC2CapacityError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"InsufficientInstanceCapacity",
		"InsufficientHostCapacity",
		"InsufficientCapacity",
		"Unsupported", // the instance type is not offered in that AZ at all
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// validate refuses a pool configuration that cannot work, at BOOT, and fills in the
// default class. Every failure here would otherwise surface as an error on somebody's
// first Start — long after the operator stopped watching the logs — so this follows
// the line AF_ECS_EC2_LAUNCH_TEMPLATE already drew: a CP that cannot run workspaces
// says so instead of starting and running none.
func (p *ec2PoolConfig) validate() error {
	if p.launchTemplate == "" {
		return fmt.Errorf("AF_ECS_EC2_LAUNCH_TEMPLATE is required for AF_RUNTIME=ecs-ec2")
	}
	if len(p.classes) == 0 {
		return fmt.Errorf("AF_ECS_EC2_SLOT_TYPES has no usable entry (want type:memMiB,... or id|label|arch|type:memMiB,...)")
	}
	if p.amiArm64 == "" {
		for _, c := range p.classes {
			if c.arch == ec2ArchArm {
				return fmt.Errorf("slot class %q is %s but AF_ECS_EC2_AMI_ARM64 is empty "+
					"(redeploy cfn/40-ec2-pool.yaml and pass its SlotAmiIdArm64 output)", c.id, c.arch)
			}
		}
	}
	if p.defaultClass == "" {
		p.defaultClass = p.classes[0].id
		return nil
	}
	if p.classFor(p.defaultClass).id != p.defaultClass {
		return fmt.Errorf("AF_ECS_EC2_DEFAULT_SLOT_CLASS=%q is not one of the declared classes", p.defaultClass)
	}
	return nil
}

// amiFor is the AMI to override the launch template's with, or "" to use the
// template's own. An empty arch (a deployment that never declared classes) is x86_64,
// which is what the template has always pinned.
func (p ec2PoolConfig) amiFor(arch string) string {
	if arch == ec2ArchArm {
		return p.amiArm64
	}
	return ""
}

// launchTemplateSpec accepts either an id (lt-…) or a name, the way every
// AF_ECS_EC2_LAUNCH_TEMPLATE* value may be written.
func launchTemplateSpec(ref string) *ec2types.LaunchTemplateSpecification {
	lt := &ec2types.LaunchTemplateSpecification{Version: aws.String("$Latest")}
	if strings.HasPrefix(ref, "lt-") {
		lt.LaunchTemplateId = aws.String(ref)
	} else {
		lt.LaunchTemplateName = aws.String(ref)
	}
	return lt
}

// describeSlotClasses renders the parsed ladders for the one boot log line — the
// operator's only chance to see that the spec they wrote parsed the way they meant.
func describeSlotClasses(cs []ec2SlotClass) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		types := make([]string, 0, len(c.slots))
		for _, s := range c.slots {
			types = append(types, s.instanceType)
		}
		parts = append(parts, fmt.Sprintf("%s(%s:%s)", c.id, c.arch, strings.Join(types, "/")))
	}
	return strings.Join(parts, " ")
}

func (e *ecsEC2Runtime) runSlot(ctx context.Context, az string) (string, error) {
	total, err := e.poolSize(ctx)
	if err != nil {
		return "", err
	}
	if total >= e.pool.maxSlots {
		return "", fmt.Errorf("slot pool is full (%d/%d); raise AF_ECS_EC2_MAX_SLOTS", total, e.pool.maxSlots)
	}
	subnet, err := e.subnetIn(ctx, az)
	if err != nil {
		return "", err
	}
	lt := launchTemplateSpec(e.pool.launchTemplate)
	out, err := e.ec2.RunInstances(ctx, &ec2.RunInstancesInput{
		LaunchTemplate: lt,
		// Overrides the template's ImageId on arm64 and is nil everywhere else, so an
		// x86_64 launch is byte-for-byte the call it has always been.
		ImageId:      strOrNil(e.pool.amiFor(e.arch)),
		InstanceType: ec2types.InstanceType(e.instanceType),
		SubnetId:     aws.String(subnet),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags: []ec2types.Tag{
				{Key: aws.String(ec2TagPool), Value: aws.String(e.pool.pool)},
				{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleSlot)},
				{Key: aws.String(ec2TagSlotSize), Value: aws.String(e.instanceType)},
				{Key: aws.String("Name"), Value: aws.String("af-slot-" + e.instanceType)},
			},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("run slot: %w", err)
	}
	if len(out.Instances) == 0 {
		return "", fmt.Errorf("run slot: no instance returned")
	}
	id := aws.ToString(out.Instances[0].InstanceId)
	log.Printf("ecs-ec2: grew the pool with slot %s (%s, %s) for %s", id, e.instanceType, az, e.base.name)
	return id, nil
}

// instanceRunning reports whether a slot is up. A dormant slot is stopped, not gone, so
// "which of the two" decides between the 13s path and the ~90s one.
func (e *ecsEC2Runtime) instanceRunning(ctx context.Context, instanceID string) (bool, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, err
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if inst.State == nil {
				continue
			}
			switch inst.State.Name {
			case ec2types.InstanceStateNameRunning:
				return true, nil
			case ec2types.InstanceStateNameTerminated, ec2types.InstanceStateNameShuttingDown:
				return false, fmt.Errorf("slot %s is %s", instanceID, inst.State.Name)
			}
			return false, nil
		}
	}
	return false, fmt.Errorf("slot %s no longer exists", instanceID)
}

func (e *ecsEC2Runtime) poolFull(ctx context.Context) (bool, error) {
	n, err := e.poolSize(ctx)
	if err != nil {
		return false, err
	}
	return n >= e.pool.maxSlots, nil
}

// evictLongestIdle takes a slot back from the workspace that has been dormant longest and
// returns the freed instance id AND its AZ — the caller may not have an AZ yet (a home
// that has not been created), and the reclaimed slot is what decides it.
//
// az is a filter, not a preference: pass the AZ an existing home is pinned to, or "" to
// consider the whole pool.
//
// ⚠️ A victim has to be a box this workspace can actually RUN on, which means the same
// instance type — see slotsOfMyType for what taking the wrong one does. The
// longest-dormant box overall is therefore not always the victim: a member on the xlarge
// rung skips past a large that has been asleep for days.
//
// Only dormant homes are candidates (the af-idle-since tag, written by Stop), and
// releaseSlot refuses any victim whose service is not actually at desiredCount 0 — so a
// workspace that woke up between the pick and the release keeps its slot and we simply
// report that there was nothing to take.
func (e *ecsEC2Runtime) evictLongestIdle(ctx context.Context, az string) (string, string, error) {
	usable, err := e.slotsOfMyType(ctx, az)
	if err != nil {
		return "", "", err
	}
	canRunHere := map[string]bool{}
	for _, r := range usable.Reservations {
		for _, inst := range r.Instances {
			canRunHere[aws.ToString(inst.InstanceId)] = true
		}
	}
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return "", "", err
	}
	var best *ec2types.Volume
	var bestIdle time.Duration
	now := e.now()
	for i := range out.Volumes {
		v := &out.Volumes[i]
		if inst := attachedInstance(v); inst == "" || !canRunHere[inst] {
			continue
		}
		if az != "" && aws.ToString(v.AvailabilityZone) != az {
			continue
		}
		if aws.ToString(v.VolumeId) == "" || ec2TagValue(v.Tags, ec2TagMembership) == e.base.membershipID {
			continue
		}
		idle, ok := idleSince(v, now)
		if !ok || (best != nil && idle <= bestIdle) {
			continue
		}
		best, bestIdle = v, idle
	}
	if best == nil {
		// Say which size, because "full" and "full of the wrong size" call for different
		// actions: the first is raise the cap, the second is wait for a box of this size
		// to fall dormant (or for Ec2SlotTerminateAfterSec to collect one and free room to
		// build the right one).
		return "", "", fmt.Errorf("slot pool is full (%d) and no dormant %s slot can be reclaimed; raise AF_ECS_EC2_MAX_SLOTS",
			e.pool.maxSlots, e.instanceType)
	}
	victimSlot := attachedInstance(best)
	victimAZ := aws.ToString(best.AvailabilityZone)
	victim := e.siblingFor(best)
	if victim == nil {
		return "", "", fmt.Errorf("slot %s holds an unidentifiable home %s", victimSlot, aws.ToString(best.VolumeId))
	}
	log.Printf("ecs-ec2: reclaiming slot %s in %s from %s (dormant %.0fm) for %s",
		victimSlot, victimAZ, victim.base.name, bestIdle.Minutes(), e.base.name)
	if err := victim.releaseSlot(ctx); err != nil {
		return "", "", fmt.Errorf("reclaim slot %s: %w", victimSlot, err)
	}
	return victimSlot, victimAZ, nil
}

// makeRoom frees ONE place under the cap by TERMINATING a dormant box of a size this
// workspace cannot use, so that growPool can build one it can. It is the last thing tried
// before a Start fails at the cap, and it exists because the alternative is a member who
// simply cannot work — the one outcome this deployment's operator ruled out.
//
// ⚠️ Terminate rather than take the box over, because an instance type is not something a
// running box can change. evictLongestIdle has already had first refusal on every box of
// the RIGHT size, so whatever is left is a box no amount of reuse can turn into what this
// workspace needs — while it still holds one of the maxSlots places. Before this, that
// state failed the Start and went on failing until an operator noticed and terminated
// something by hand (docs/64 §64.33).
//
// ⚠️ NOT filtered by AZ, unlike evictLongestIdle, and the difference is the whole point:
// there the box is REUSED, so it has to be where the volume is pinned; here it is
// destroyed to buy a place under a cap that counts the entire pool. A box freed in another
// AZ is worth exactly as much, and refusing to look there would leave the requester stuck
// for nothing.
//
// ⚠️ Deliberately NOT gated on slotTerminateAfter. That knob is about idle COST — how long
// to keep paying for a box nobody is using — and its 0 means "keep them". This is a
// capacity deadlock, it only ever runs when the alternative is a failed Start, and what it
// costs the victim is the image cache (their return goes from ~110s to ~135s), never their
// home: releaseSlot detaches it first, and the volume is DeleteOnTermination=false either
// way. Reading "never terminate" as "leave the deadlock in place" would answer a question
// the parameter was never asked.
//
// Empty boxes are taken before occupied ones — those spend nobody's affinity at all.
func (e *ecsEC2Runtime) makeRoom(ctx context.Context) (bool, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			// af-role=slot leaves quarantined boxes alone, as everywhere else: those are
			// evidence an operator has not looked at yet (決定 20).
			tagFilter(ec2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"running", "stopped"}},
		},
	})
	if err != nil {
		return false, err
	}
	vols, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return false, err
	}
	// The ECS side as well as the EC2 side, for the reason sweepFreeSlots gives: a task can
	// be running on a box no home is attached to at all (the golden baker's probe).
	tasks, err := e.slotTaskCounts(ctx)
	if err != nil {
		return false, err
	}
	now := e.now()
	type occupant struct {
		vol     *ec2types.Volume
		idle    time.Duration
		dormant bool
	}
	occupied, claimed := map[string]occupant{}, map[string]bool{}
	for i := range vols.Volumes {
		v := &vols.Volumes[i]
		// A box a Start is landing on holds no attachment yet; only the claim says so.
		if inst := ec2TagValue(v.Tags, ec2TagClaim); inst != "" && e.claimLive(v) {
			claimed[inst] = true
		}
		if inst := attachedInstance(v); inst != "" {
			idle, ok := idleSince(v, now)
			occupied[inst] = occupant{vol: v, idle: idle, dormant: ok}
		}
	}
	type candidate struct {
		id, itype string
		free      bool
		idle      time.Duration
		vol       *ec2types.Volume
	}
	var best *candidate
	take := func(c candidate) {
		switch {
		case best == nil:
		case best.free != c.free:
			if !c.free {
				return // an empty box always wins over somebody's dormant one
			}
		case c.idle <= best.idle:
			return
		}
		best = &c
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			if string(inst.InstanceType) == e.instanceType {
				continue // evictLongestIdle already had first refusal on this size
			}
			if claimed[id] || tasks[id] > 0 {
				continue
			}
			occ, taken := occupied[id]
			switch {
			case !taken:
				// Free, so the only clock is how long it has been free. An unreadable or
				// missing mark reads as 0 and simply loses every tie-break — this walk is
				// about capacity, not about timing, so a fresh box is still eligible when
				// it is the only one.
				var freeFor time.Duration
				if at, err := time.Parse(time.RFC3339, ec2TagValue(inst.Tags, ec2TagSlotIdleSince)); err == nil {
					freeFor = now.Sub(at)
				}
				take(candidate{id: id, itype: string(inst.InstanceType), free: true, idle: freeFor})
			case occ.dormant && ec2TagValue(occ.vol.Tags, ec2TagMembership) != e.base.membershipID:
				take(candidate{id: id, itype: string(inst.InstanceType), idle: occ.idle, vol: occ.vol})
			}
		}
	}
	if best == nil {
		return false, nil
	}
	if best.vol != nil {
		victim := e.siblingFor(best.vol)
		if victim == nil {
			return false, fmt.Errorf("slot %s holds an unidentifiable home %s", best.id, aws.ToString(best.vol.VolumeId))
		}
		log.Printf("ecs-ec2: the pool is full of slots %s cannot use; taking %s (%s, %s's, dormant %.0fm) out of it",
			e.instanceType, best.id, best.itype, victim.base.name, best.idle.Minutes())
		// Same order and the same reason as the sweeper's terminate stage: releaseSlot is
		// anchored to the victim's Start generation, so a Start racing this aborts the
		// release instead of losing its home to a box that is going away.
		if err := victim.releaseSlot(ctx); err != nil {
			return false, fmt.Errorf("release %s before terminating slot %s: %w",
				aws.ToString(best.vol.VolumeId), best.id, err)
		}
	} else {
		log.Printf("ecs-ec2: the pool is full of slots %s cannot use; taking the empty %s (%s) out of it",
			e.instanceType, best.id, best.itype)
	}
	e.terminateSlot(ctx, best.id, "made room for a "+e.instanceType)
	return true, e.waitSlotGone(ctx, best.id)
}

// waitSlotGone waits until a terminated box stops counting toward the cap.
// TerminateInstances returns as soon as the request is accepted and DescribeInstances can
// still answer `stopped` for a moment afterwards — runSlot re-reads poolSize, so without
// this the Start we just freed a place for would fail on the box we freed it with.
func (e *ecsEC2Runtime) waitSlotGone(ctx context.Context, instanceID string) error {
	for i := 0; i < 30; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err != nil {
			if isAWSNotFound(err) {
				return nil
			}
			return err
		}
		counted := false
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				if inst.State == nil {
					continue
				}
				switch inst.State.Name {
				case ec2types.InstanceStateNameShuttingDown, ec2types.InstanceStateNameTerminated:
				default:
					counted = true
				}
			}
		}
		if !counted {
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("slot %s still counts toward the pool after being terminated", instanceID)
}

// siblingFor builds the runtime of ANOTHER workspace from its home volume's tags — the
// same trick the sweeper uses. Only the identity and the clients matter here; the
// runtime is used exclusively to release that workspace's slot.
func (e *ecsEC2Runtime) siblingFor(vol *ec2types.Volume) *ecsEC2Runtime {
	membership := ec2TagValue(vol.Tags, ec2TagMembership)
	name := ec2TagValue(vol.Tags, ec2TagWorkspace)
	if membership == "" || name == "" {
		return nil
	}
	base := *e.base
	base.name = name
	base.membershipID = membership
	sib := *e
	sib.base = &base
	return &sib
}

// poolSize counts every slot that is not terminated, across sizes and AZs — the cap is
// about the deployment's blast radius and bill, not about one size.
func (e *ecsEC2Runtime) poolSize(ctx context.Context) (int, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			tagFilter(ec2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range out.Reservations {
		n += len(r.Instances)
	}
	return n, nil
}

func (e *ecsEC2Runtime) subnetIn(ctx context.Context, az string) (string, error) {
	azs, err := e.azOfSubnet(ctx)
	if err != nil {
		return "", err
	}
	subnets := append([]string(nil), e.base.cfg.subnets...)
	sort.Strings(subnets)
	for _, s := range subnets {
		if az == "" || azs[s] == az {
			return s, nil
		}
	}
	return "", fmt.Errorf("no configured subnet in %s (AF_ECS_SUBNETS)", az)
}

// poolAZs is every AZ the pool may use, in the order a new home tries them: the configured
// subnets sorted by SUBNET ID, deduplicated.
//
// ⚠️ Sorted by id, which is NOT the order they were written in AF_ECS_SUBNETS — an
// operator listing 1a first still gets 1c when its subnet id happens to be smaller
// (measured, docs/64 §64.20.4). The order is arbitrary but it must be STABLE: every new
// home going to the same AZ is what keeps a pool from scattering one workspace's slots
// away from where the free ones are.
func (e *ecsEC2Runtime) poolAZs(ctx context.Context) ([]string, error) {
	bySubnet, err := e.azOfSubnet(ctx)
	if err != nil {
		return nil, err
	}
	subnets := append([]string(nil), e.base.cfg.subnets...)
	sort.Strings(subnets)
	seen := map[string]bool{}
	var azs []string
	for _, s := range subnets {
		az := bySubnet[s]
		if az == "" || seen[az] {
			continue
		}
		seen[az] = true
		azs = append(azs, az)
	}
	return azs, nil
}

// spreadAZs is poolAZs ordered by how many homes are already in each — fewest first, ties
// broken by the stable poolAZs order. Only growPool uses it, and only for a home that has
// no AZ yet.
//
// Why spread at all, when the pool otherwise works hardest to keep homes and free slots in
// the same place: because "the same place" turned out to mean ONE AZ for everybody. New
// homes followed a deterministic first choice, so the blast radius of a single AZ going
// down was not half the deployment, it was all of it (docs/64 §64.21). An EBS home cannot
// be evacuated — the only lever is not putting everyone in the same AZ to begin with.
//
// ⚠️ It is not free. A home in 1a can only ever use a slot in 1a, so free slots in the
// other AZ are useless to it and the pool grows instead of reusing — more instances for
// the same number of people. That is the price of the blast radius, and it is why the
// FREE-SLOT-FIRST preference in placeHome is untouched: spreading only decides where a
// NEW slot goes, never whether an existing one gets reused.
func (e *ecsEC2Runtime) spreadAZs(ctx context.Context) ([]string, error) {
	azs, err := e.poolAZs(ctx)
	if err != nil || len(azs) < 2 {
		return azs, err
	}
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		// Balancing is an optimisation; the fixed order still works. Never fail a Start
		// over it.
		log.Printf("ecs-ec2: could not count homes per AZ (%v); falling back to the fixed AZ order", err)
		return azs, nil
	}
	homes := map[string]int{}
	for i := range out.Volumes {
		homes[aws.ToString(out.Volumes[i].AvailabilityZone)]++
	}
	ordered := append([]string(nil), azs...)
	rank := map[string]int{}
	for i, az := range azs {
		rank[az] = i
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if homes[ordered[i]] != homes[ordered[j]] {
			return homes[ordered[i]] < homes[ordered[j]]
		}
		return rank[ordered[i]] < rank[ordered[j]]
	})
	return ordered, nil
}

func (e *ecsEC2Runtime) anyAZ(ctx context.Context) (string, error) {
	azs, err := e.poolAZs(ctx)
	if err != nil {
		return "", err
	}
	if len(azs) == 0 {
		return "", fmt.Errorf("no usable subnet/AZ configured (AF_ECS_SUBNETS)")
	}
	return azs[0], nil
}

// wakeSlot starts a dormant slot, waiting first for it to be startable.
//
// The wait is the whole point: the sweeper stops a slot the moment its workspace goes
// dormant, so a user who comes back seconds later finds it in `stopping`, and EC2
// answers StartInstances with "IncorrectInstanceState" (measured — it failed the live
// test). Retrying from `stopping` is not an error case, it is the normal race between a
// person and a 15-minute timer.
func (e *ecsEC2Runtime) wakeSlot(ctx context.Context, instanceID string) error {
	for i := 0; ; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err != nil {
			return err
		}
		state := ec2types.InstanceStateName("")
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				if inst.State != nil {
					state = inst.State.Name
				}
			}
		}
		switch state {
		case ec2types.InstanceStateNameRunning, ec2types.InstanceStateNamePending:
			return nil
		case ec2types.InstanceStateNameStopped:
			log.Printf("ecs-ec2: waking slot %s for %s (home still attached)", instanceID, e.base.name)
			_, err := e.ec2.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{instanceID}})
			return err
		case ec2types.InstanceStateNameTerminated, ec2types.InstanceStateNameShuttingDown:
			return fmt.Errorf("slot %s is %s", instanceID, state)
		}
		if i >= 60 {
			return fmt.Errorf("slot %s stuck in %s", instanceID, state)
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

func (e *ecsEC2Runtime) waitInstanceRunning(ctx context.Context, instanceID string) error {
	for {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err == nil {
			for _, r := range out.Reservations {
				for _, inst := range r.Instances {
					if inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning {
						return nil
					}
				}
			}
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// waitSlotRegistered waits until the slot is an ACTIVE, agent-connected container
// instance. Measured at ~20s after an instance start (docs/64 §64.12.1); a hot slot
// returns on the first poll.
func (e *ecsEC2Runtime) waitSlotRegistered(ctx context.Context, instanceID string) error {
	// Bounded as well as context-scoped. The caller's budget is the real limit in
	// production, but an unbounded "poll until" hangs anything that drives this with a
	// context that never expires.
	for i := 0; i < 400; i++ {
		ready, err := e.registeredSlots(ctx)
		if err == nil && ready[instanceID] {
			return nil
		}
		if err := e.sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("slot %s never registered with the cluster", instanceID)
}

// --- mount / umount (SSM) ---

func (e *ecsEC2Runtime) homeMountPoint() string {
	return ec2HomeMountBase + "/" + e.base.membershipID
}

// mountHome runs `af-mount <volume-id> <mountpoint> --mkfs` on the slot. The helper is
// placed by the launch template's user-data, resolves the NVMe device from the volume
// id (the device name we asked for is not what the kernel shows on Nitro), and only
// formats when blkid finds no filesystem — which is why --mkfs can be passed
// unconditionally and a retried mount never eats a home.
func (e *ecsEC2Runtime) mountHome(ctx context.Context, p ec2Placement) error {
	mp := e.homeMountPoint()
	return e.runOnSlot(ctx, p.instanceID, fmt.Sprintf("af-mount %s %s --mkfs", p.volumeID, mp))
}

func (e *ecsEC2Runtime) umountHome(ctx context.Context, instanceID string) error {
	return e.runOnSlot(ctx, instanceID, fmt.Sprintf("af-umount %s", e.homeMountPoint()))
}

// runOnSlot sends one shell command through SSM and waits for it. SendCommand is
// retried for a while because a freshly booted slot registers with SSM a little after
// it registers with ECS, and an InvalidInstanceId there is a timing artifact rather
// than a failure.
func (e *ecsEC2Runtime) runOnSlot(ctx context.Context, instanceID, command string) error {
	var cmdID string
	for attempt := 1; ; attempt++ {
		out, err := e.ssmc.SendCommand(ctx, &ssm.SendCommandInput{
			DocumentName: aws.String("AWS-RunShellScript"),
			InstanceIds:  []string{instanceID},
			Parameters:   map[string][]string{"commands": {command}},
			Comment:      aws.String("agent-fleet slot volume"),
		})
		if err == nil && out.Command != nil {
			cmdID = aws.ToString(out.Command.CommandId)
			break
		}
		// SAY SOMETHING. A slot whose SSM agent never came back swallows every mount and
		// unmount silently, and the workspace just sits at `starting` with no clue why —
		// which is exactly how a live run burned ten minutes before anyone could tell
		// that SSM, not ECS, was the thing that was stuck.
		if attempt%5 == 1 {
			log.Printf("ecs-ec2: slot %s is not answering SSM yet (attempt %d, %q): %v",
				instanceID, attempt, command, err)
		}
		if err := e.sleep(ctx, 3*time.Second); err != nil {
			return fmt.Errorf("ssm send %q to %s: %w", command, instanceID, err)
		}
	}
	// Poll on a ramp rather than a flat 2s, and ask EARLY. Measured on the production
	// deployment's pool (docs/64 §64.38, n=37 mounts over 5 days): `af-mount` itself runs
	// in 0.6s median / 3.0s max, and SSM queues it in another 0.2–0.7s — so the command
	// is almost always finished before the CP has even asked once. A flat 2s pre-sleep
	// therefore spent ~1.5s per call doing nothing, twice on the swap path (umount on the
	// old slot, mount on the new one). The ramp keeps the tail cheap: the poll widens to
	// the same 2s within a few rounds, so a mount that genuinely takes a minute (mkfs on
	// a fresh 50 GiB home) costs the same number of API calls it always did.
	//
	// ⚠️ This is a small win on purpose. The measurement it comes from is that the mount
	// was NEVER the 10–30s the earlier estimate charged it (§64.17.5); it is ~2–4s out of
	// a 110–147s Start. Do not expect a Start to get noticeably faster from this.
	delay := 300 * time.Millisecond
	for {
		if err := e.sleep(ctx, delay); err != nil {
			return err
		}
		if delay < 2*time.Second {
			if delay *= 2; delay > 2*time.Second {
				delay = 2 * time.Second
			}
		}
		inv, err := e.ssmc.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(cmdID),
			InstanceId: aws.String(instanceID),
		})
		if err != nil {
			continue // InvocationDoesNotExist right after SendCommand is normal
		}
		switch inv.Status {
		case "Success":
			return nil
		case "Failed", "Cancelled", "TimedOut":
			return fmt.Errorf("%q on %s: %s: %s", command, instanceID, inv.Status,
				strings.TrimSpace(aws.ToString(inv.StandardErrorContent)))
		}
	}
}

// --- task definition / service ---

// lastTaskDef caches the (instance, fingerprint, ARN) of the task definition each
// workspace last registered, so a Start that re-attaches to the SAME slot with
// otherwise-unchanged inputs can reuse it instead of registering a new revision and
// force-deploying. registerTaskDef + upsertService's ForceNewDeployment together
// retire the running task and start a fresh one — Service Connect load-balances
// across both for the ~1-2 minutes it takes to settle, which silently drops
// anything a caller stashed in the Agent process's memory (an OAuth flow_id;
// confirmed 2026-08-19 on the dev deployment). Most re-wakes change nothing (same image,
// same env, same secrets ARNs), so most re-wakes can skip that window entirely.
//
// Same shape as startGen/startPhase for the same reason: the Runtime object is
// rebuilt per request, so this cannot live on it. Process-local scratch, keyed by
// workspace name — so a CP restart or a second replica misses it, and that miss used
// to mean "register a redundant revision and roll the service for nothing", which
// every workspace paid on its first Start after every deploy. It is now only a miss of
// the FAST path: serviceTaskDefIfFingerprint asks AWS the same question by reading the
// fingerprint back off the revision the service is already on. Never
// incorrect (the fingerprint covers every per-Start-dynamic input, including the
// ws-settings/tenant-limits env that workspaceExtraEnv recomputes on every Start —
// see registerConnectionRoutes… no, see manager.workspaceExtraEnv), only sometimes
// a missed optimization.
var lastTaskDef sync.Map // workspace name -> ecTaskDefCacheEntry

type ecTaskDefCacheEntry struct {
	instanceID  string
	fingerprint string
	arn         string
}

// taskDefFingerprint hashes OUR OWN RegisterTaskDefinitionInput before it is sent —
// not ECS's read-back representation, which AWS is free to reorder or pad with
// defaults. Two calls that build byte-identical input (same code path, same
// arguments) always hash the same; anything that actually differs (env, secrets,
// image, instance) always hashes differently. That is all reuseOrRegisterTaskDef
// needs — it never has to understand what changed, only that something did.
func taskDefFingerprint(in *ecs.RegisterTaskDefinitionInput) string {
	b, err := json.Marshal(in)
	if err != nil {
		return "" // never matches a cached fingerprint; falls back to registering
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// reuseOrRegisterTaskDef returns the task definition to launch p on, reusing the
// last one registered for this workspace when the instance and the fingerprint
// both still match (see lastTaskDef), and registering a fresh revision otherwise.
// reused=false also covers the ordinary case of a genuinely new revision (image
// bump, changed settings, moved slot) — upsertService only needs to know whether
// to force a new deployment, and a fresh revision always needs one.
func (e *ecsEC2Runtime) reuseOrRegisterTaskDef(ctx context.Context, p ec2Placement, prep ec2Prep) (arn string, reused bool, err error) {
	in := e.buildTaskDef(ctx, p, prep)
	fp := taskDefFingerprint(in)
	if fp != "" {
		if v, ok := lastTaskDef.Load(e.base.name); ok {
			if entry, ok := v.(ecTaskDefCacheEntry); ok &&
				entry.instanceID == p.instanceID && entry.fingerprint == fp {
				return entry.arn, true, nil
			}
		}
		// Process-local miss, which is not the same thing as "something changed": the
		// map is empty after every CP restart and on whichever replica did not serve the
		// last Start. Ask AWS the same question before paying for a revision nobody
		// needs — see serviceTaskDefIfFingerprint for what that costs and buys.
		if arn := e.serviceTaskDefIfFingerprint(ctx, fp); arn != "" {
			lastTaskDef.Store(e.base.name, ecTaskDefCacheEntry{instanceID: p.instanceID, fingerprint: fp, arn: arn})
			return arn, true, nil
		}
		// ⚠️ AFTER the hash, never inside buildTaskDef. The label is the hash, so a
		// revision that carried it into the hash could never match the next Start's
		// freshly built (unlabelled) input, and reuse would be dead again — silently,
		// since the only symptom is a slower Start.
		stampTaskDefFingerprint(in, fp)
	}
	out, err := e.base.ecs.RegisterTaskDefinition(ctx, in)
	if err != nil {
		return "", false, err
	}
	arn = aws.ToString(out.TaskDefinition.TaskDefinitionArn)
	if fp != "" {
		lastTaskDef.Store(e.base.name, ecTaskDefCacheEntry{instanceID: p.instanceID, fingerprint: fp, arn: arn})
	}
	return arn, false, nil
}

// afTaskDefFingerprintLabel carries taskDefFingerprint on the revision it describes, so
// "have we already registered exactly this?" can be answered from AWS instead of only
// from lastTaskDef. Same trick and the same reason as afImageStampLabel
// (runtime_ecs_stale.go): a docker label rather than a task-definition tag, so reading it
// back needs neither include=TAGS nor ecs:TagResource.
const afTaskDefFingerprintLabel = "af.taskdef.fingerprint"

// stampTaskDefFingerprint writes fp onto the revision about to be registered. Must be
// called after taskDefFingerprint, never before — see the caller.
func stampTaskDefFingerprint(in *ecs.RegisterTaskDefinitionInput, fp string) {
	for i := range in.ContainerDefinitions {
		if in.ContainerDefinitions[i].DockerLabels == nil {
			in.ContainerDefinitions[i].DockerLabels = map[string]string{}
		}
		in.ContainerDefinitions[i].DockerLabels[afTaskDefFingerprintLabel] = fp
	}
}

// serviceTaskDefIfFingerprint returns the revision the service already points at, but
// only when that revision was registered from byte-identical input — i.e. only when
// registering again would produce the same thing. "" means "register a fresh one".
//
// This is what makes reuse survive a CP restart, and that matters more than it sounds:
// lastTaskDef is process-local, so before this every workspace's FIRST Start after a
// deploy registered a redundant revision, which made launch() split-and-wait (and, before
// §64.39, cost the full 40 seconds) for a task definition that had not changed at all.
// Everyone paid it, every deploy.
//
// Two AWS reads on the Start path, both already made elsewhere for other reasons, and
// only on a cache miss. Deliberately NOT wired into the /api/workspace poll.
//
// ⚠️ It must refuse an INACTIVE revision. Nothing here deregisters task definitions, but
// an operator's cleanup does, and UpdateService onto a deregistered revision fails — a
// reuse that turns a Start into an error is far worse than a redundant registration.
func (e *ecsEC2Runtime) serviceTaskDefIfFingerprint(ctx context.Context, fp string) string {
	s, ok, err := e.base.describeService(ctx)
	if err != nil || !ok {
		return ""
	}
	arn := aws.ToString(s.TaskDefinition)
	if arn == "" {
		return ""
	}
	out, err := e.base.ecs.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
	})
	if err != nil || out.TaskDefinition == nil ||
		out.TaskDefinition.Status != ecstypes.TaskDefinitionStatusActive {
		return ""
	}
	for _, c := range out.TaskDefinition.ContainerDefinitions {
		if c.DockerLabels[afTaskDefFingerprintLabel] == fp {
			return arn
		}
	}
	return ""
}

// buildTaskDef assembles (without submitting) a fresh EC2-launch-type revision
// pinned to one slot.
//
// Differences from the Fargate revision that matter:
//   - placementConstraints `ec2InstanceId == i-…` — the whole point. It lives on the
//     task definition rather than on the service, so moving a user to another slot
//     never touches the service's own shape.
//   - no task cpu / memory. On EC2 those are RESERVATIONS against the instance
//     (docs/64 §64.4.5); with one user per slot the right answer is to reserve nothing
//     and let them have the box. A small memoryReservation keeps the API happy.
//   - /tmp is a tmpfs. The root volume is shared with whoever had the slot before, so
//     a disk-backed /tmp would both leak (readable from the host) and let one user fill
//     the volume out from under the next (ADR 0045 決定 8 の代償 2). tmpfs is the one
//     tool Fargate did not have.
//   - home is a host bind of the freshly mounted EBS, not an EFS volume.
//
// It takes a ctx because the backend-drift stamp is probed from ECR here
// (runtime_ecs_stale.go). That stamp is part of what taskDefFingerprint hashes, and
// that is LOAD-BEARING rather than incidental: with a mutable tag (`:dev`) the image
// STRING does not change when the image does, so the stamp is the only field that
// tells reuseOrRegisterTaskDef the inputs moved. Excluding it would make a re-wake
// after an image push reuse the old revision — the task would still pull the new
// image, but it would carry the OLD stamp, and the workspace's 要再起動 badge would
// then never clear no matter how often the user restarted.
func (e *ecsEC2Runtime) buildTaskDef(ctx context.Context, p ec2Placement, prep ec2Prep) *ecs.RegisterTaskDefinitionInput {
	env := []ecstypes.KeyValuePair{
		{Name: aws.String("CLAUDE_CONFIG_DIR"), Value: aws.String("/var/lib/af/claude")},
		// Where the entrypoint keeps the auth/identity set (ADR 0045 決定 3-6).
		{Name: aws.String("AF_WS_KEEP"), Value: aws.String(ec2KeepPath)},
		{Name: aws.String("AGENT_STOP_GRACE_SEC"), Value: aws.String(strconv.Itoa(agentStopGraceSec()))},
		// NOTE: AF_WS_SCRATCH is deliberately absent (ADR 0045 決定 10-3). It exists to
		// keep small-file build output off EFS; here home IS local NVMe-backed EBS, so
		// the relocation would buy nothing and would move the user's caches onto a disk
		// that is wiped when the slot changes hands.
	}
	if e.base.cfg.sessionCmd != "" {
		env = append(env, ecstypes.KeyValuePair{Name: aws.String("AGENT_SESSION_CMD"), Value: aws.String(e.base.cfg.sessionCmd)})
	}
	for _, kv := range e.base.extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env = append(env, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
		}
	}
	container := ecstypes.ContainerDefinition{
		Name:      aws.String("agent"),
		Image:     aws.String(e.base.cfg.workspaceImage),
		Essential: aws.Bool(true),
		// Backend-drift stamp — same contract as the Fargate revision
		// (runtime_ecs_stale.go).
		DockerLabels: e.base.stampImage(ctx),
		// A soft reservation only: ECS requires a memory figure somewhere, and a hard
		// limit here would cap the user below the slot they are paying for.
		MemoryReservation: aws.Int32(512),
		PortMappings: []ecstypes.PortMapping{
			{ContainerPort: aws.Int32(ecsAgentPort), Name: aws.String("agent")},
		},
		Environment: env,
		Secrets:     prep.secrets,
		MountPoints: []ecstypes.MountPoint{
			{SourceVolume: aws.String("home"), ContainerPath: aws.String("/home/dev")},
			{SourceVolume: aws.String("claude"), ContainerPath: aws.String("/var/lib/af/claude")},
			{SourceVolume: aws.String("keep"), ContainerPath: aws.String(ec2KeepPath)},
		},
		StopTimeout: aws.Int32(int32(stopGraceSec())),
		LinuxParameters: &ecstypes.LinuxParameters{
			InitProcessEnabled: aws.Bool(true),
			Tmpfs: []ecstypes.Tmpfs{{
				ContainerPath: aws.String("/tmp"),
				Size:          e.pool.tmpfsMiB,
				MountOptions:  e.pool.tmpfsOpts,
			}},
		},
	}
	if e.base.cfg.logGroup != "" {
		container.LogConfiguration = &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         e.base.cfg.logGroup,
				"awslogs-region":        e.base.cfg.region,
				"awslogs-stream-prefix": "agent",
			},
		}
	}
	// The slot's CPU architecture, declared rather than left to chance.
	//
	// Measured: on an EC2-compatibility task definition, omitting runtimePlatform
	// leaves it null — it does NOT default to X86_64 the way it does on Fargate — so
	// this is not what makes an arm64 slot work (docs/70 §70.8). It is stated anyway
	// for two reasons: ECS then refuses to PLACE a task on a box of the wrong
	// architecture instead of starting it and letting the image fail to exec, and the
	// field is part of what taskDefFingerprint hashes, so a member moved to another
	// class provably gets a new revision rather than a reused one.
	arch := ecstypes.CPUArchitectureX8664
	if e.arch == ec2ArchArm {
		arch = ecstypes.CPUArchitectureArm64
	}
	in := &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(e.base.name),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityEc2},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RuntimePlatform: &ecstypes.RuntimePlatform{
			CpuArchitecture:       arch,
			OperatingSystemFamily: ecstypes.OSFamilyLinux,
		},
		ExecutionRoleArn:     strOrNil(e.base.cfg.execRole),
		TaskRoleArn:          strOrNil(e.base.cfg.taskRole),
		ContainerDefinitions: []ecstypes.ContainerDefinition{container},
		PlacementConstraints: []ecstypes.TaskDefinitionPlacementConstraint{{
			Type:       ecstypes.TaskDefinitionPlacementConstraintTypeMemberOf,
			Expression: aws.String(fmt.Sprintf("ec2InstanceId == %s", p.instanceID)),
		}},
		Volumes: []ecstypes.Volume{
			{
				Name: aws.String("home"),
				Host: &ecstypes.HostVolumeProperties{SourcePath: aws.String(e.homeMountPoint() + "/dev")},
			},
			efsVolume("claude", e.base.cfg.efsFileSystem, prep.claudeAP),
			efsVolume("keep", e.base.cfg.efsFileSystem, prep.keepAP),
		},
	}
	return in
}

// netConfig is the awsvpc configuration every service call carries. The ENI must land in
// the slot's own AZ, so it names only that AZ's subnet.
func (e *ecsEC2Runtime) netConfig(ctx context.Context, p ec2Placement) (*ecstypes.NetworkConfiguration, error) {
	subnet, err := e.subnetIn(ctx, p.az)
	if err != nil {
		return nil, err
	}
	return &ecstypes.NetworkConfiguration{
		AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets:        []string{subnet},
			SecurityGroups: []string{e.base.cfg.securityGroup},
			// Never AssignPublicIp on a slot: a task ENI that outlives a stop turns the
			// instance into a multi-ENI box that silently loses its auto-assigned public
			// IPv4, and the agent then cannot reach the control plane at all
			// (ADR 0045 決定 3-3, measured: 11 minutes offline).
			AssignPublicIp: ecstypes.AssignPublicIpDisabled,
		},
	}, nil
}

// pointServiceAt moves an EXISTING service onto taskDefArn WITHOUT touching
// desiredCount, so the deployment ECS spawns for the revision change can retire while
// the slot is still booting. See launch() for why the two halves must not be one call.
//
// It reports whether the caller may finish with a bare scaleUpService. Everything that
// makes that unsafe answers false and leaves the old single-call path in charge:
//   - no service yet (first ever Start) — CreateService has no old deployment to fight;
//   - the service already points here — no deployment is spawned, nothing to wait for;
//   - the call failed — say so and take the slow path rather than scaling up a service
//     that may still be on the previous revision.
//
// ⚠️ Leaving desiredCount alone is also what makes this safe to issue this early: it
// cannot start a workspace that is not being started, so a Start that dies before the
// mount just leaves a stopped service pointing at a revision nobody runs. The next Start
// recomputes it. Same reason it is safe when the mount later quarantines the slot.
func (e *ecsEC2Runtime) pointServiceAt(ctx context.Context, taskDefArn string, p ec2Placement) bool {
	s, ok, err := e.base.describeService(ctx)
	if err != nil || !ok || aws.ToString(s.Status) != "ACTIVE" {
		return false
	}
	if aws.ToString(s.TaskDefinition) == taskDefArn {
		return false
	}
	netCfg, err := e.netConfig(ctx, p)
	if err != nil {
		return false
	}
	if _, err := e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:                     aws.String(e.base.cfg.cluster),
		Service:                     aws.String(e.base.name),
		TaskDefinition:              aws.String(taskDefArn),
		NetworkConfiguration:        netCfg,
		DeploymentConfiguration:     ec2SingleTaskDeployment,
		AvailabilityZoneRebalancing: ec2NoAZRebalancing,
	}); err != nil {
		log.Printf("ecs-ec2 start: could not pre-point %s at %s (falling back to the slower single update): %v",
			e.base.name, taskDefArn, err)
		return false
	}
	return true
}

// scaleUpService is the second half of the split: desiredCount ONLY, on a service
// pointServiceAt has already moved onto the right revision. Sending the task definition
// again here would be harmless, but sending ForceNewDeployment would not — it spawns the
// very ACTIVE deployment the split exists to retire first.
func (e *ecsEC2Runtime) scaleUpService(ctx context.Context) error {
	_, err := e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(e.base.cfg.cluster),
		Service:      aws.String(e.base.name),
		DesiredCount: aws.Int32(1),
		// Sent here too: this is the ONLY call on the path that raises the count, so a
		// service that somehow missed the setting must not raise it at 200%.
		DeploymentConfiguration:     ec2SingleTaskDeployment,
		AvailabilityZoneRebalancing: ec2NoAZRebalancing,
	})
	return err
}

// upsertService creates or updates the workspace's service at desiredCount 1 on the EC2
// launch type. Service Connect stays exactly as on Fargate, which is why Endpoint() did
// not have to change (docs/64 §64.4.4).
// forceNewDeployment should be false only when taskDefArn is a task definition
// reuseOrRegisterTaskDef found unchanged from what this service is already running —
// specifying it without forcing lets ECS notice nothing actually changed and skip
// the rolling replacement, instead of retiring a perfectly good running task.
//
// ⚠️ Since §64.39 this is no longer the ordinary path for a CHANGED revision: launch()
// splits that into pointServiceAt + scaleUpService, and only falls back here when the
// service does not exist yet (CreateService, which has no old deployment to fight) or
// when the pre-point failed. Sending a new revision together with desiredCount 1 is the
// thing that costs 40 seconds, so do not route more traffic back through it.
func (e *ecsEC2Runtime) upsertService(ctx context.Context, taskDefArn string, p ec2Placement, forceNewDeployment bool) error {
	netCfg, err := e.netConfig(ctx, p)
	if err != nil {
		return err
	}
	s, ok, err := e.base.describeService(ctx)
	if err != nil {
		return err
	}
	if ok && aws.ToString(s.Status) == "ACTIVE" {
		_, err = e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster:                     aws.String(e.base.cfg.cluster),
			Service:                     aws.String(e.base.name),
			DesiredCount:                aws.Int32(1),
			TaskDefinition:              aws.String(taskDefArn),
			NetworkConfiguration:        netCfg,
			ForceNewDeployment:          forceNewDeployment,
			DeploymentConfiguration:     ec2SingleTaskDeployment,
			AvailabilityZoneRebalancing: ec2NoAZRebalancing,
		})
		return err
	}
	_, err = e.base.ecs.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:                     aws.String(e.base.cfg.cluster),
		ServiceName:                 aws.String(e.base.name),
		TaskDefinition:              aws.String(taskDefArn),
		DesiredCount:                aws.Int32(1),
		LaunchType:                  ecstypes.LaunchTypeEc2,
		NetworkConfiguration:        netCfg,
		DeploymentConfiguration:     ec2SingleTaskDeployment,
		AvailabilityZoneRebalancing: ec2NoAZRebalancing,
		ServiceConnectConfiguration: &ecstypes.ServiceConnectConfiguration{
			Enabled:   true,
			Namespace: strOrNil(e.base.cfg.namespaceArn),
			Services: []ecstypes.ServiceConnectService{{
				PortName:      aws.String("agent"),
				DiscoveryName: aws.String(e.base.name),
				ClientAliases: []ecstypes.ServiceConnectClientAlias{
					{DnsName: aws.String(e.base.name), Port: aws.Int32(ecsAgentPort)},
				},
			}},
		},
	})
	return err
}

// --- release / destroy ---

// releaseSlot is the teardown half of a Stop, and the same routine the sweeper runs on
// anything it finds drifting: wait for the task to be gone, unmount, detach, hand the
// slot back to the pool. Idempotent — every step is a no-op when it has already
// happened, because the sweeper WILL run it again.
func (e *ecsEC2Runtime) releaseSlot(ctx context.Context) error {
	return e.releaseSlotSince(ctx, e.generation().Load())
}

// releaseSlotSince is releaseSlot anchored to a Start count taken by the CALLER — see
// startGen for why the anchor cannot be taken here.
func (e *ecsEC2Runtime) releaseSlotSince(ctx context.Context, gen int64) error {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return err
	}
	if vol == nil {
		return nil
	}
	instanceID := attachedInstance(vol)
	if instanceID == "" {
		return nil
	}
	if err := e.waitTasksGone(ctx); err != nil {
		return err
	}
	if e.generation().Load() != gen {
		return fmt.Errorf("%s was started again while releasing its slot; leaving it attached", e.base.name)
	}
	// umount first, ALWAYS — while the slot is running. A detach of a mounted filesystem
	// is how a home gets corrupted (ADR 0045 決定 8 の代償 3), so a failure here must stop
	// the detach.
	//
	// A STOPPED slot is the exception, and it has to be: SSM cannot reach it, and there
	// is nothing to unmount — the instance stop is an ordinary shutdown, which unmounts
	// filesystems on the way down. Waiting for an umount that can never run would leave
	// dormant slots unreclaimable.
	running, err := e.instanceRunning(ctx, instanceID)
	if err != nil {
		return err
	}
	if running {
		if err := e.umountHome(ctx, instanceID); err != nil {
			return fmt.Errorf("umount %s on %s: %w", e.homeMountPoint(), instanceID, err)
		}
	}
	volumeID := aws.ToString(vol.VolumeId)
	if e.generation().Load() != gen {
		// Re-mount rather than detach: the workspace is coming up and needs its home.
		log.Printf("ecs-ec2: %s restarted mid-release; re-mounting instead of detaching", e.base.name)
		return e.mountHome(ctx, ec2Placement{volumeID: volumeID, instanceID: instanceID})
	}
	if _, err := e.ec2.DetachVolume(ctx, &ec2.DetachVolumeInput{
		VolumeId:   aws.String(volumeID),
		InstanceId: aws.String(instanceID),
	}); err != nil {
		return fmt.Errorf("detach %s: %w", volumeID, err)
	}
	// Do not call the slot free until it really is: the attachment point outlives the
	// DetachVolume response (see attachHomeWithRetry), and a Start that lands in that
	// window would grow the pool instead of swapping onto this slot.
	if err := e.waitDeviceFree(ctx, instanceID); err != nil {
		log.Printf("ecs-ec2: %s detached from %s but the device is still held: %v", volumeID, instanceID, err)
	}
	e.clearIdle(ctx, volumeID)
	// The box is back in the pool, so it stops being this person's cost from here on —
	// and starts its OWN dormancy clock, which is what eventually stops it. Before this
	// existed a released slot simply left every path that could ever stop it (§64.31).
	e.untagSlotOwner(ctx, instanceID)
	e.markSlotFree(ctx, instanceID)
	log.Printf("ecs-ec2: released slot %s from %s", instanceID, e.base.name)
	return nil
}

// waitDeviceFree polls until the slot no longer lists the home device. The instance view
// is the conservative one here — it keeps reporting the device while the volume side
// already says `available` — which makes it the right thing to wait on.
func (e *ecsEC2Runtime) waitDeviceFree(ctx context.Context, instanceID string) error {
	started := e.now()
	for i := 0; i < 30; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err != nil {
			return err
		}
		held := false
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				for _, m := range inst.BlockDeviceMappings {
					if aws.ToString(m.DeviceName) == ec2HomeDevice {
						held = true
					}
				}
			}
		}
		if !held {
			if d := e.now().Sub(started); d > time.Second {
				log.Printf("ecs-ec2: slot %s gave back %s after %.0fs", instanceID, ec2HomeDevice, d.Seconds())
			}
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("slot %s still holds %s", instanceID, ec2HomeDevice)
}

// waitDetached blocks until the volume has really left the slot. DetachVolume ANSWERS
// while the volume is still `detaching`, so anything that acts on the volume next — a
// DeleteVolume, an attach elsewhere — has to wait for this or it fails with VolumeInUse
// (measured).
func (e *ecsEC2Runtime) waitDetached(ctx context.Context, volumeID string) error {
	for {
		out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
		if err != nil {
			return err
		}
		if len(out.Volumes) == 0 || attachedInstance(&out.Volumes[0]) == "" {
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// waitTasksGone waits until the service actually has no task left. Detaching while the
// container still holds the mount is the failure mode this exists to prevent.
func (e *ecsEC2Runtime) waitTasksGone(ctx context.Context) error {
	for {
		s, ok, err := e.base.describeService(ctx)
		if err != nil {
			return err
		}
		if !ok || (s.RunningCount == 0 && s.PendingCount == 0) {
			return nil
		}
		if s.DesiredCount > 0 {
			return fmt.Errorf("service %s is at desired %d; not releasing its slot", e.base.name, s.DesiredCount)
		}
		if err := e.sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// Destroy releases the slot, deletes the user's home volume, and then hands the shared
// (Fargate-side) resources to the base adapter. The slot itself is NOT terminated: it
// never belonged to this user.
//
// ⚠️ P0 left the second half out — it deleted the EBS volume but kept the ECS service,
// the two EFS access points and the two SSM parameters alive forever (ADR 0045 決定 13,
// docs/64 §64.18.1). The order matters: the slot has to come back to the pool and the
// volume has to be detached before the service goes away, because releaseSlot reads the
// service to prove nothing is running on it.
//
// Ordering rationale for the volume vs its snapshot: destroy is irreversible on purpose,
// so any hibernation snapshot (§64.18.2) goes too — otherwise a "deleted" user keeps
// billing at snapshot rates and their home is restorable by the next person who gets
// their membership id.
func (e *ecsEC2Runtime) Destroy(ctx context.Context) ([]string, error) {
	if err := e.Stop(ctx); err != nil {
		return nil, err
	}
	if err := e.releaseSlot(ctx); err != nil {
		return nil, err
	}
	if err := e.deleteHomeVolume(ctx); err != nil {
		return nil, err
	}
	if err := e.deleteHomeSnapshots(ctx); err != nil {
		return nil, err
	}
	// Backups are a different role and so are invisible to every other cleanup here.
	// Without this they would outlive the person and bill forever — the exact thing
	// Destroy exists to stop (docs/64 §64.18.1).
	if err := e.deleteBackups(ctx); err != nil {
		return nil, err
	}
	return e.base.Destroy(ctx)
}

// deleteHomeVolume detaches (if needed) and deletes this workspace's home volume.
// Absent volume = already done.
func (e *ecsEC2Runtime) deleteHomeVolume(ctx context.Context) error {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return err
	}
	volumeID := aws.ToString(vol.VolumeId)
	if err := e.waitDetached(ctx, volumeID); err != nil {
		return err
	}
	// DescribeVolumes can already report the volume detached while DeleteVolume still
	// answers VolumeInUse (measured — the two views converge separately). Retry rather
	// than leave a provisioned volume behind, which is the one leftover here that bills
	// forever.
	for i := 0; ; i++ {
		if _, err = e.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)}); err == nil {
			return nil
		}
		if i >= 15 || !strings.Contains(err.Error(), "VolumeInUse") {
			return err
		}
		if serr := e.sleep(ctx, 4*time.Second); serr != nil {
			return serr
		}
	}
}

// homeSnapshots returns this workspace's hibernation snapshots (§64.18.2), newest first
// is not guaranteed — callers that need one pick by state. Owner self-scoped so a
// public snapshot with a colliding tag can never be picked up.
func (e *ecsEC2Runtime) homeSnapshots(ctx context.Context) ([]ec2types.Snapshot, error) {
	out, err := e.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(ec2TagMembership, e.base.membershipID),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return nil, err
	}
	return out.Snapshots, nil
}

// hibernate is one STEP of putting a long-unused home to sleep (ADR 0045 決定 4, docs/64
// §64.18.2), not the whole thing. A snapshot of a 45 GiB home takes 30–40 minutes, and
// the sweeper cannot sit on that, so the operation is resumable: each sweep advances it
// by one step and the state lives in AWS, not in the CP (ADR 0012).
//
//	no snapshot          → release the slot, then start one
//	snapshot pending     → nothing to do; the next sweep looks again
//	snapshot completed   → delete the volume (the home is now the snapshot)
//
// The slot is released BEFORE the snapshot on purpose: releaseSlot unmounts and detaches,
// so what gets captured is a quiesced filesystem rather than a crash-consistent image of
// a live mount. It also returns the slot to the pool at the earliest moment, which is
// half of why hibernation is worth doing.
// BeginHibernate is the reaper's entry point into the series above (tier 3 — see
// hibernatingRuntime in reaper.go). It only ever STARTS one: the timing decision belongs
// to the reaper, which can see the tenant's home_hibernate_after, and every step after
// the first belongs to the pool sweeper, which can resume it after a CP restart.
//
// Two guards, both of which are the difference between "cheap" and "lost work":
//
//   - already marked ⇒ do nothing. Otherwise the reaper and the sweeper would both be
//     advancing the same hibernation, and two CreateSnapshot calls in the same window
//     leave a second, orphaned capture billing forever.
//   - a live service ⇒ do nothing. The reaper decided from the database that nobody has
//     opened this workspace in weeks; AWS is the authority on whether it is running right
//     now, and it is the one that would lose the mount.
var _ hibernatingRuntime = (*ecsEC2Runtime)(nil)

func (e *ecsEC2Runtime) BeginHibernate(ctx context.Context) error {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return err // no home here: already a snapshot, or never created
	}
	if at := ec2TagValue(vol.Tags, ec2TagHibernating); at != "" {
		return nil // under way; the pool sweeper carries it the rest of the way
	}
	s, ok, err := e.base.describeService(ctx)
	if err != nil {
		return err
	}
	if ok && (s.DesiredCount > 0 || s.RunningCount > 0 || s.PendingCount > 0) {
		return nil
	}
	log.Printf("ecs-ec2 hibernate: %s has not been opened for the tenant's retention window; putting the home away", e.base.name)
	return e.hibernate(ctx)
}

func (e *ecsEC2Runtime) hibernate(ctx context.Context) error {
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		return err
	}
	volumeID := aws.ToString(vol.VolumeId)
	mark, fresh, err := e.hibernationMark(ctx, vol)
	if err != nil {
		return err
	}
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		// Only a snapshot of THIS volume, started after THIS hibernation's mark, is the
		// capture we are waiting for. Everything else is a leftover — from a restore
		// whose cleanup did not finish, or from a dormancy the owner interrupted by
		// coming back — and taking one of those for the capture deletes a volume holding
		// work that was never captured.
		if aws.ToString(s.VolumeId) != volumeID || s.StartTime == nil || s.StartTime.Before(mark) {
			log.Printf("ecs-ec2 hibernate: dropping the superseded snapshot %s of %s",
				aws.ToString(s.SnapshotId), e.base.name)
			if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
				return err
			}
			continue
		}
		switch s.State {
		case ec2types.SnapshotStateCompleted:
			log.Printf("ecs-ec2 hibernate: %s captured in %s; deleting the volume",
				e.base.name, aws.ToString(s.SnapshotId))
			return e.deleteHomeVolume(ctx)
		case ec2types.SnapshotStateError:
			// A failed snapshot would otherwise pin the home in this state forever:
			// never "completed", so the volume never goes, and never absent, so no new
			// snapshot is ever started.
			log.Printf("ecs-ec2 hibernate: snapshot %s of %s failed; discarding it and retrying",
				aws.ToString(s.SnapshotId), e.base.name)
			if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
				return err
			}
			return nil
		default:
			return nil // pending: let it finish
		}
	}
	if err := e.releaseSlot(ctx); err != nil {
		// Take the mark back off if THIS call put it there. releaseSlot unmounts over SSM,
		// so it fails for as long as the slot is unreachable — an AZ having a bad day is
		// the case that matters — and a mark with nothing behind it is not free:
		//
		//   - the volume looks "hibernating" in the pool view while it is still attached
		//     and perfectly fine;
		//   - every sweep logs the same failure with no way to tell it apart from progress;
		//   - and the mark outlives the outage, so the FIRST snapshot taken afterwards is
		//     judged against a timestamp from before it.
		//
		// A mark that was already there is left alone: it belongs to a hibernation that is
		// genuinely under way, and the snapshot it validates may already exist.
		if fresh {
			e.unmarkHibernating(ctx, volumeID)
		}
		return fmt.Errorf("release the slot before snapshotting: %w", err)
	}
	out, err := e.ec2.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String("agent-fleet hibernated home for " + e.base.name),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSnapshot,
			Tags: e.ownedTags([]ec2types.Tag{
				{Key: aws.String(ec2TagMembership), Value: aws.String(e.base.membershipID)},
				{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
				{Key: aws.String(ec2TagWorkspace), Value: aws.String(e.base.name)},
				{Key: aws.String(ec2TagPool), Value: aws.String(e.pool.pool)},
				{Key: aws.String("Name"), Value: aws.String(e.base.name + "-home")},
			}),
		}},
	})
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", volumeID, err)
	}
	log.Printf("ecs-ec2 hibernate: %s → %s (the volume goes once it completes)",
		e.base.name, aws.ToString(out.SnapshotId))
	return nil
}

// hibernationMark returns the moment this hibernation began, stamping it if this is the
// first step, and whether THIS call is what stamped it. Written BEFORE the slot is
// released so a CP that dies in between resumes instead of starting over — and so the
// mark always predates the snapshot it validates.
//
// The "fresh" answer is what lets the caller take it back when the very next step fails:
// see hibernate.
func (e *ecsEC2Runtime) hibernationMark(ctx context.Context, vol *ec2types.Volume) (time.Time, bool, error) {
	if v := ec2TagValue(vol.Tags, ec2TagHibernating); v != "" {
		at, err := time.Parse(time.RFC3339, v)
		if err == nil {
			return at, false, nil
		}
		// An unparseable mark is worse than none: every snapshot would compare against a
		// zero time and be accepted. Re-stamp.
		log.Printf("ecs-ec2 hibernate: %s has an unreadable %s tag (%q); re-stamping",
			e.base.name, ec2TagHibernating, v)
	}
	// RFC3339Nano, not RFC3339: second granularity would round the mark DOWN, placing it
	// up to a second before it was actually written — long enough to accept a snapshot
	// from the dormancy this one replaces.
	at := e.now().UTC()
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{aws.ToString(vol.VolumeId)},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagHibernating), Value: aws.String(at.Format(time.RFC3339Nano))}},
	}); err != nil {
		return time.Time{}, false, fmt.Errorf("mark %s as hibernating: %w", aws.ToString(vol.VolumeId), err)
	}
	return at, true, nil
}

// unmarkHibernating removes a hibernation mark this CP just wrote and could not act on.
// Best effort: leaving it is untidy, not dangerous, and the caller is already returning
// the real error.
func (e *ecsEC2Runtime) unmarkHibernating(ctx context.Context, volumeID string) {
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagHibernating)}},
	}); err != nil {
		log.Printf("ecs-ec2 hibernate: could not take the mark back off %s: %v", volumeID, err)
	}
}

// restoreSnapshot returns the completed hibernation snapshot to build this home from, or
// "" when there is none. A pending one is deliberately NOT used: CreateVolume from an
// incomplete snapshot is rejected, and answering "" here means the user gets a fresh home
// while their real one is still being captured — data loss dressed up as a fast start.
// Returning an error instead makes the Start fail, which is the honest outcome.
func (e *ecsEC2Runtime) restoreSnapshot(ctx context.Context) (string, error) {
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return "", err
	}
	// NEWEST completed, not first: a restore whose snapshot cleanup failed leaves an
	// older one behind, and picking that would silently hand the user a home from two
	// hibernations ago.
	var newest *ec2types.Snapshot
	for i := range snaps {
		if snaps[i].State != ec2types.SnapshotStateCompleted {
			continue
		}
		if newest == nil || snapshotStartedAfter(snaps[i], *newest) {
			newest = &snaps[i]
		}
	}
	if newest != nil {
		return aws.ToString(newest.SnapshotId), nil
	}
	for _, s := range snaps {
		if s.State == ec2types.SnapshotStatePending {
			return "", fmt.Errorf("home of %s is being hibernated (%s); try again once it completes",
				e.base.name, aws.ToString(s.SnapshotId))
		}
	}
	return "", nil
}

// goldenSnapshot returns the pool's pre-baked home for the image this deployment runs, or
// "" to build an empty one. Best-effort by design: a new user with no golden gets a slow
// first start, which is a worse experience but not a wrong one, so nothing here is ever
// promoted to an error that blocks a Start.
//
// ★ The image stamp is checked, not assumed. Re-baking is a manual step tied to a release
// (決定 9), and the failure mode of forgetting it is invisible: only NEW users get it,
// they get old CLIs, and everything looks fine. A stale golden is therefore refused —
// loudly — rather than used.
func (e *ecsEC2Runtime) goldenSnapshot(ctx context.Context) string {
	// Everybody reads ec2RoleGolden. Only the baker's probe reads a candidate, and it
	// says so on itself — an unproven golden is not something a lookup should be able
	// to reach by accident (§64.28.3).
	role := e.seedRole
	if role == "" {
		role = ec2RoleGolden
	}
	out, err := e.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, e.pool.pool),
			tagFilter(ec2TagRole, role),
		},
	})
	if err != nil {
		log.Printf("ecs-ec2: looking up the golden snapshot failed; building an empty home: %v", err)
		return ""
	}
	want := e.base.cfg.workspaceImage
	// The comparison is by CONTENT where the content is knowable (goldenIdentity):
	// a golden re-stamped for a new tag that resolves to the same bytes is still ours,
	// and one stamped with our tag that no longer resolves to the same bytes is not.
	id := goldenIdentityFor(ctx, e.base.ecr, want)
	var stale []string
	var newest *ec2types.Snapshot
	for i := range out.Snapshots {
		s := out.Snapshots[i]
		if s.State != ec2types.SnapshotStateCompleted {
			continue
		}
		if !id.matches(s.Tags) {
			stale = append(stale, fmt.Sprintf("%s(%s)", aws.ToString(s.SnapshotId), ec2TagValue(s.Tags, ec2TagImage)))
			continue
		}
		// ⚠️ A golden of ANOTHER architecture is not a candidate for this home. The
		// pool bakes one per declared arch and they all carry the same image stamp, so
		// image alone stops discriminating the moment a second arch is declared — and
		// what is left is "newest wins", which is a coin toss. This is NOT a stale
		// golden and must not be reported as one: the other arch's golden is correct,
		// it just is not ours.
		//
		// Measured on the real deployment (docs/70 §70.14.5): with x86_64 and arm64
		// baking at the same time, the x86_64 probe was seeded from the arm64
		// candidate. Nothing broke — §70.5's self-heal wipes the wrong-arch bits and
		// re-runs boot-install — which is exactly why this had to be found in a log
		// rather than by a failure: it silently throws away the whole point of the
		// golden, and it makes the probe prove the wrong snapshot.
		if snapshotArch(s) != archOrX86(e.arch) {
			continue
		}
		if newest == nil || snapshotStartedAfter(s, *newest) {
			newest = &out.Snapshots[i]
		}
	}
	if newest != nil {
		return aws.ToString(newest.SnapshotId)
	}
	// A stale CANDIDATE is the baker's own bookkeeping, not an operator problem — it
	// only means the image moved while a bake was in flight, and the next tick starts a
	// fresh one. Only a stale published golden is worth the standing warning.
	if len(stale) > 0 && role == ec2RoleGolden {
		log.Printf("ecs-ec2: the golden snapshot %s was baked from another image; this deployment runs %s. "+
			"New homes are being built EMPTY (slow first start) until it is re-baked — ADR 0045 決定 9.",
			strings.Join(stale, ", "), want)
	}
	return ""
}

func snapshotStartedAfter(a, b ec2types.Snapshot) bool {
	if a.StartTime == nil {
		return false
	}
	return b.StartTime == nil || a.StartTime.After(*b.StartTime)
}

// deleteHomeSnapshots removes every hibernation snapshot of this home. Called by Destroy
// and, after a restore, by createHomeVolume — both are moments when the snapshot is
// definitively superseded. Hibernation itself must never call it: it would delete the
// snapshot it just took.
func (e *ecsEC2Runtime) deleteHomeSnapshots(ctx context.Context) error {
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{
			SnapshotId: s.SnapshotId,
		}); err != nil && !isAWSNotFound(err) {
			return fmt.Errorf("delete snapshot %s: %w", aws.ToString(s.SnapshotId), err)
		}
	}
	return nil
}

// --- periodic backups (ADR 0045 決定 17) ---

// backupSnapshots lists this membership's backup copies, newest first. Role-filtered, so
// a hibernation capture or the pool's golden can never be counted, pruned, or mistaken
// for one.
func (e *ecsEC2Runtime) backupSnapshots(ctx context.Context) ([]ec2types.Snapshot, error) {
	out, err := e.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters: []ec2types.Filter{
			tagFilter(ec2TagMembership, e.base.membershipID),
			tagFilter(ec2TagRole, ec2RoleBackup),
		},
	})
	if err != nil {
		return nil, err
	}
	snaps := out.Snapshots
	sort.SliceStable(snaps, func(i, j int) bool {
		return backupStamp(snaps[j]).Before(backupStamp(snaps[i]))
	})
	return snaps, nil
}

// backupStamp is when the CP asked for a backup — af-backup-at, falling back to what EBS
// reports. The tag is preferred because the schedule is a statement about this deployment
// ("when did we last ask"), not about how long AWS took to answer.
func backupStamp(s ec2types.Snapshot) time.Time {
	if v := ec2TagValue(s.Tags, ec2TagBackupAt); v != "" {
		if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return at
		}
	}
	if s.StartTime != nil {
		return *s.StartTime
	}
	return time.Time{}
}

// BackupHome keeps a copy of this home OUTSIDE its Availability Zone, because an EBS
// volume lives in exactly one and cannot be evacuated: losing that AZ is otherwise the
// same as losing the work. Snapshots are regional, so they are the only place a home can
// be that its own AZ is not (docs/64 §64.21, ADR 0045 決定 17).
//
// Three things this deliberately does NOT do:
//
//   - It does not unmount anything. The volume is captured where it is, mounted and in
//     use, so a backup is CRASH-CONSISTENT — the same guarantee as snapshotting a running
//     instance, and the same one a power cut gives. Quiescing would mean taking a working
//     person's home away on a timer, which is a worse product than a backup that may need
//     a filesystem check. (This is the opposite call from bake-golden.sh — §64.18.5 — for
//     the opposite reason: a golden is everyone's STARTING state and gets to be clean.)
//   - It does not restore. A backup is older than the home by construction, and handing
//     somebody a silently older home is the failure mode this file keeps designing around.
//     Restoring is an operator decision, with the runbook in §64.21.
//   - It keeps no state in the CP. Whether a backup is due is read from the newest one's
//     tag (ADR 0012), so a restart, a second replica and a redeploy all agree.
func (e *ecsEC2Runtime) BackupHome(ctx context.Context, every time.Duration) error {
	if every <= 0 {
		return nil
	}
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil {
		// No volume: either this workspace has never started, or the home is already a
		// hibernation snapshot — which is itself a regional copy, so there is nothing a
		// backup would add.
		return err
	}
	if ec2TagValue(vol.Tags, ec2TagHibernating) != "" {
		// A hibernation is capturing this volume and will DELETE it. A second capture of
		// the same blocks would pay twice for one moment in time.
		return nil
	}
	snaps, err := e.backupSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if s.State == ec2types.SnapshotStatePending {
			return nil // one at a time; a 45 GiB home takes 30–40 minutes
		}
	}
	if len(snaps) > 0 && e.now().Sub(backupStamp(snaps[0])) < every {
		return e.pruneBackups(ctx, snaps)
	}
	at := e.now().UTC()
	out, err := e.ec2.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    vol.VolumeId,
		Description: aws.String("agent-fleet backup of " + e.base.name),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSnapshot,
			Tags: e.ownedTags([]ec2types.Tag{
				{Key: aws.String(ec2TagMembership), Value: aws.String(e.base.membershipID)},
				{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleBackup)},
				{Key: aws.String(ec2TagWorkspace), Value: aws.String(e.base.name)},
				{Key: aws.String(ec2TagPool), Value: aws.String(e.pool.pool)},
				{Key: aws.String(ec2TagBackupAt), Value: aws.String(at.Format(time.RFC3339Nano))},
				{Key: aws.String("Name"), Value: aws.String(e.base.name + "-backup")},
			}),
		}},
	})
	if err != nil {
		return fmt.Errorf("back up %s: %w", aws.ToString(vol.VolumeId), err)
	}
	log.Printf("ecs-ec2 backup: %s → %s", e.base.name, aws.ToString(out.SnapshotId))
	// Re-read rather than pruning the list we had a moment ago: the copy just created is
	// part of the retention count, and pruning the stale list would keep backupKeep+1
	// forever — one more than the operator agreed to pay for, every time.
	fresh, err := e.backupSnapshots(ctx)
	if err != nil {
		return err
	}
	return e.pruneBackups(ctx, fresh)
}

// pruneBackups keeps the newest backupKeep COMPLETED copies and deletes the rest.
//
// Completed only, on purpose: a capture in flight has already been paid for and deleting
// it buys nothing. It also means the count never drops below the retention while a new
// backup is still running — the oldest goes when its replacement is actually usable.
func (e *ecsEC2Runtime) pruneBackups(ctx context.Context, snaps []ec2types.Snapshot) error {
	keep := e.pool.backupKeep
	if keep < 1 {
		keep = 1
	}
	seen := 0
	for _, s := range snaps {
		if s.State != ec2types.SnapshotStateCompleted {
			continue
		}
		seen++
		if seen <= keep {
			continue
		}
		log.Printf("ecs-ec2 backup: dropping %s of %s (keeping %d)",
			aws.ToString(s.SnapshotId), e.base.name, keep)
		if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
			return fmt.Errorf("prune backup %s: %w", aws.ToString(s.SnapshotId), err)
		}
	}
	return nil
}

// deleteBackups removes every backup of this membership. Only Destroy calls it: a backup
// outliving its home is the entire point, so nothing short of "this person is being
// removed for good" may take one.
func (e *ecsEC2Runtime) deleteBackups(ctx context.Context) error {
	snaps, err := e.backupSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if _, err := e.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !isAWSNotFound(err) {
			return fmt.Errorf("delete backup %s: %w", aws.ToString(s.SnapshotId), err)
		}
	}
	return nil
}

// --- drift sweeper (docs/64 §64.15.6) ---

// sweepLoop re-derives the world from tags every sweepEvery and finishes whatever a
// crashed CP left half-done. Without it, "hold no state" would mean "lose the teardown
// when the process dies": a volume stuck on a slot no one is using, a claim that pins a
// workspace at `starting`, a ghost container instance the scheduler keeps trying to
// place onto.
func (f *ecsEC2Factory) sweepLoop(ctx context.Context) {
	t := time.NewTicker(f.pool.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := f.sweep(ctx); err != nil {
				log.Printf("ecs-ec2 sweep: %v", err)
			}
		}
	}
}

func (f *ecsEC2Factory) sweep(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, f.pool.waitBudget)
	defer cancel()
	vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, f.pool.pool),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return fmt.Errorf("describe home volumes: %w", err)
	}
	for i := range vols.Volumes {
		f.sweepVolume(ctx, &vols.Volumes[i])
	}
	// Repair the billing tags BEFORE the volume sweep's own releases are re-read: the
	// truth is "who is attached right now", and that is exactly what `vols` holds.
	f.sweepSlotOwnerTags(ctx, vols.Volumes)
	f.sweepFreeSlots(ctx, vols.Volumes)
	return f.sweepGhostInstances(ctx)
}

// sweepSlotOwnerTags takes af-membership/af-tenant off any slot that is not actually
// holding that person's home. It is the repair half of tagSlotOwner: a CP that dies
// between "detach" and "delete the tag" would otherwise keep charging the last user for
// a box that is back in the free pool, and nothing else would ever notice — the pool
// logic does not read these tags, so a stale one is invisible except on the invoice.
//
// The attached volumes ARE the answer to who owns what (the same source freeSlots and
// releaseSlot already trust), so this compares against them rather than asking ECS.
// It repairs BOTH directions, because they are different failures:
//   - a slot tagged for somebody whose home is not on it → clear (a crash between the
//     detach and the untag; the box is back in the pool and its hours are shared cost);
//   - a slot holding a home but not tagged for it → stamp (a crash between the attach
//     and the stamp, or a box that predates this code) — copied FROM THE VOLUME, which
//     is the only place that knows the owner without a DB read.
func (f *ecsEC2Factory) sweepSlotOwnerTags(ctx context.Context, homes []ec2types.Volume) {
	type ownerTags struct{ membership, tenant string }
	owner := map[string]ownerTags{} // instance id -> who is actually on it
	for i := range homes {
		if inst := attachedInstance(&homes[i]); inst != "" {
			owner[inst] = ownerTags{
				membership: ec2TagValue(homes[i].Tags, ec2TagMembership),
				tenant:     ec2TagValue(homes[i].Tags, ec2TagTenant),
			}
		}
	}
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, f.pool.pool),
			// A terminated box still answers DescribeInstances for a while and cannot be
			// tagged; excluding it keeps the log free of failures nobody can act on.
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("ecs-ec2 sweep: reading slot owner tags: %v", err)
		return
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			stamped := ec2TagValue(inst.Tags, ec2TagMembership)
			want := owner[id]
			switch {
			case stamped == want.membership:
				continue // already right (including "nobody on it, no tag")
			case want.membership == "":
				log.Printf("ecs-ec2 sweep: slot %s is billed to %q but holds no home; clearing", id, stamped)
				if _, err := f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
					Resources: []string{id},
					Tags:      []ec2types.Tag{{Key: aws.String(ec2TagMembership)}, {Key: aws.String(ec2TagTenant)}},
				}); err != nil {
					log.Printf("ecs-ec2 sweep: clearing the owner of slot %s failed: %v", id, err)
				}
			default:
				log.Printf("ecs-ec2 sweep: slot %s holds %q but is billed to %q; restamping", id, want.membership, stamped)
				tags := []ec2types.Tag{{Key: aws.String(ec2TagMembership), Value: aws.String(want.membership)}}
				if want.tenant != "" {
					tags = append(tags, ec2types.Tag{Key: aws.String(ec2TagTenant), Value: aws.String(want.tenant)})
				}
				if _, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{id}, Tags: tags}); err != nil {
					log.Printf("ecs-ec2 sweep: restamping the owner of slot %s failed: %v", id, err)
				}
				// The new owner's tenant is unknown but the previous one's may still be
				// on the box; leaving it would bill this person's hours to a tenant they
				// are not in, which is worse than no tenant at all.
				if want.tenant == "" && ec2TagValue(inst.Tags, ec2TagTenant) != "" {
					if _, err := f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
						Resources: []string{id},
						Tags:      []ec2types.Tag{{Key: aws.String(ec2TagTenant)}},
					}); err != nil {
						log.Printf("ecs-ec2 sweep: clearing a stale tenant tag on %s failed: %v", id, err)
					}
				}
			}
		}
	}
}

func (f *ecsEC2Factory) sweepVolume(ctx context.Context, vol *ec2types.Volume) {
	rt := f.runtimeForVolume(vol)
	if rt == nil {
		return
	}
	volumeID := aws.ToString(vol.VolumeId)
	if ec2TagValue(vol.Tags, ec2TagClaim) != "" && !rt.claimLive(vol) {
		log.Printf("ecs-ec2 sweep: clearing an expired claim on %s", volumeID)
		rt.unclaim(ctx, volumeID)
	}
	att := attachment(vol)
	if att == nil {
		// A hibernation already under way: the slot went back to the pool at its first
		// step (which is also what cleared af-idle-since, so the dormancy test below
		// cannot be what resumes it). Keep advancing it — nothing else will, and stopping
		// here leaves BOTH a snapshot and a volume billing. Deliberately not gated on
		// hibernateAfter: turning the feature off must not strand a home half-way.
		if ec2TagValue(vol.Tags, ec2TagHibernating) != "" {
			if err := rt.hibernate(ctx); err != nil {
				log.Printf("ecs-ec2 sweep: hibernating %s failed: %v", rt.base.name, err)
			}
		}
		return
	}
	// A Start is in flight on this home. Everything below reads "no live service" as
	// "dormant", and for those few seconds that is exactly wrong (docs/64 §64.31.6):
	// placeHome clears the dormancy marks FIRST and upsertService raises desiredCount
	// LAST, with the mount — an SSM round trip, 10–30s — in between. A sweep landing in
	// that window stamps af-idle-since back onto a workspace that is coming UP.
	//
	// It never caused a premature stop (re-stamping moves the clock forward, not back),
	// but the mark then outlives the launch: a RUNNING workspace wears a dormancy mark
	// until its next Stop, which makes the pool screen report it as idle and makes
	// evictLongestIdle pick it as a victim — and releaseSlot refusing a live service
	// turns that into a failed Start at the cap rather than a move to the next victim.
	//
	// The free-slot walk has honoured live claims from the start; this is the same rule
	// on the other half of the same sweep. Deliberately AFTER the hibernation branch
	// above: a claim must never be able to strand a capture half-way. An EXPIRED claim
	// falls through (claimLive parses af-claim-at rather than trusting the tag's
	// presence), so a launch that died cannot pin this walk either.
	if rt.claimLive(vol) {
		return
	}
	instanceID := aws.ToString(att.InstanceId)
	s, ok, err := rt.base.describeService(ctx)
	if err != nil {
		return
	}
	if ok && (s.DesiredCount > 0 || s.RunningCount > 0 || s.PendingCount > 0) {
		return // live workspace: leave it alone
	}
	if !ok {
		// Attached but the workspace has no service at all — drift (a launch that never
		// got that far, or a workspace that was removed). This is the only case where
		// the sweeper still takes the home off its slot; a merely STOPPED workspace
		// keeps its slot on purpose (lazy release).
		//
		// The grace is on the ATTACH time and belongs to THIS branch only: a Start
		// attaches before it creates the service, so without it a sweep landing in that
		// window would tear down a launch in progress. The idle-stop below must NOT be
		// gated this way — a long-lived attachment is exactly what it is looking for.
		if att.AttachTime != nil && time.Since(*att.AttachTime) < f.pool.releaseGrace {
			return
		}
		log.Printf("ecs-ec2 sweep: %s is attached to %s but has no service; releasing", volumeID, instanceID)
		if err := rt.releaseSlot(ctx); err != nil {
			log.Printf("ecs-ec2 sweep: releasing %s failed: %v", volumeID, err)
		}
		return
	}
	// Dormant workspace. Stamp it if the Stop lost the mark, then put the slot to sleep
	// once it has been dormant long enough: a stopped slot keeps the image cache and the
	// attachment (so the owner comes back in ~90s instead of 135s) and costs only its
	// root volume instead of a running instance.
	idle, marked := idleSince(vol, time.Now())
	if !marked {
		rt.markIdle(ctx, volumeID)
		return
	}
	// Hibernation is the third and last step of the same series (reaper stops the
	// workspace → the slot sleeps → the home becomes a snapshot), but it is NOT started
	// here: how long a tenant's homes may sit before they are put away is a database
	// answer and this loop has no database (ADR 0012). The reaper stamps af-hibernating
	// and starts the capture; from then on the branch above (att == nil) advances it.
	// A hibernation whose first step has run therefore never reaches this point.

	// Past slotTerminateAfter the box goes away entirely. This is checked BEFORE the sleep
	// test, not after: the two are stages of one clock, and by the time this fires the box
	// is normally already stopped — but a deployment with slotSleepAfter=0 must still be
	// able to reclaim roots, and there the box is running and skips straight to here.
	if f.pool.slotTerminateAfter > 0 && idle >= f.pool.slotTerminateAfter {
		// The home comes OFF first, through releaseSlot, rather than by letting
		// TerminateInstances drop it (the home is DeleteOnTermination=false, so it would
		// survive either way). Both reasons are about what the OWNER sees if they Start
		// during the ~60s a terminate takes:
		//
		//   - releaseSlot is anchored to the Start generation, so a Start racing this
		//     aborts the release — and re-mounts — instead of losing its home mid-launch.
		//     Nothing about TerminateInstances can be taken back.
		//   - placeHome derives placement from the ATTACHMENT. A volume still attached to a
		//     shutting-down instance reads as branch ①: it would claim that box, find its
		//     instance type still matching, and then fail to wake it.
		//
		// releaseSlot also skips the umount on an already-stopped box (SSM cannot reach
		// one) and refuses while any task remains, which is exactly the guard this branch
		// would otherwise have to repeat. On failure we leave the box alone: a slot that
		// could not be released is not one to terminate.
		if err := rt.releaseSlot(ctx); err != nil {
			log.Printf("ecs-ec2 sweep: releasing %s before terminating slot %s: %v", volumeID, instanceID, err)
			return
		}
		rt.terminateSlot(ctx, instanceID, fmt.Sprintf("%s dormant %.0fm", rt.base.name, idle.Minutes()))
		return
	}
	if f.pool.slotSleepAfter <= 0 || idle < f.pool.slotSleepAfter {
		return // 0 = never sleep (see slotSleepAfter)
	}
	running, err := rt.instanceRunning(ctx, instanceID)
	if err != nil || !running {
		return
	}
	// Never stop a slot that still carries a task ENI. ECS detaches those a little after
	// the task is gone, and an instance stopped in that window comes back MULTI-ENI —
	// which is how it silently loses its auto-assigned public IPv4 and, on a deployment
	// without NAT, its egress: the agent then never reconnects (ADR 0045 決定 3-3, and
	// reproduced through this very code path in the live test). Skipping just means the
	// next sweep stops it.
	if held, err := f.taskENIsAttached(ctx, instanceID); err != nil || held {
		if held {
			log.Printf("ecs-ec2 sweep: slot %s still has a task ENI; leaving it running for now", instanceID)
		}
		return
	}
	log.Printf("ecs-ec2 sweep: %s has been dormant %.0fm; stopping slot %s (home stays attached)",
		rt.base.name, idle.Minutes(), instanceID)
	if _, err := f.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{instanceID}}); err != nil {
		log.Printf("ecs-ec2 sweep: stopping %s failed: %v", instanceID, err)
	}
}

// sweepFreeSlots is the sleep test for slots that hold NO home — the half sweepVolume
// structurally cannot do, because it walks homes and a released slot has none
// (docs/64 §64.31, ADR 0045 決定 22).
//
// ★ The bug it fixes: Ec2SlotSleepSec promises "a slot with no running task is STOPPED,
// ~$9.6/month instead of ~$95". That was only ever true for a slot with a home still
// attached. The moment releaseSlot ran — an eviction, a size/class change, a Destroy, the
// golden baker finishing with its seed and its probe — the box left every path that could
// stop it and ran until an operator noticed. On the live deployment three of them had been
// up for over a day with zero tasks.
//
// The shape mirrors sweepVolume deliberately: stamp on first sighting, act only on the
// NEXT pass. That is what makes it safe across CP restarts and across the two replicas a
// rolling deploy always overlaps — both read the same tag and reach the same verdict, and
// StopInstances is idempotent, so a double call is a no-op rather than a race.
//
// ⚠️ No warm spare is kept, and that is not an omission (§64.17.2, "プレウォームは入れない").
// A free RUNNING slot buys the next arrival 43s instead of 110s, and costs ~$95/month to
// hold. A free STOPPED one buys almost nothing: waking it and then attaching and mounting
// somebody's home is 123–143s against the 135s of building a box from scratch, because the
// 32s of cold pull it saves is spent again on the boot and the mount SSM round trip. So a
// free slot is stopped after slotSleepAfter and TERMINATED after slotTerminateAfter, and
// the second timer is the one that gives its root volume back (docs/64 §64.32).
//
// This walk sees BOTH states for that reason. It used to list only `running` boxes, which
// was consistent while stopping was the last thing that could happen to one — but it means
// a slot leaves this walk forever the moment it is stopped, so the terminate stage would
// never see the boxes it exists for.
func (f *ecsEC2Factory) sweepFreeSlots(ctx context.Context, homes []ec2types.Volume) {
	if f.pool.slotSleepAfter <= 0 && f.pool.slotTerminateAfter <= 0 {
		return // both off (see slotSleepAfter / slotTerminateAfter)
	}
	probe := f.probeRuntime()
	busy := map[string]bool{}
	for i := range homes {
		if inst := attachedInstance(&homes[i]); inst != "" {
			busy[inst] = true
		}
		// A slot some other workspace is LAUNCHING onto holds no attachment yet; only the
		// claim says so. Missing it would stop a box mid-Start.
		if inst := ec2TagValue(homes[i].Tags, ec2TagClaim); inst != "" && probe.claimLive(&homes[i]) {
			busy[inst] = true
		}
	}
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, f.pool.pool),
			// af-role=slot excludes quarantined boxes on purpose: quarantineSlot already
			// stopped those, and re-stopping them would only add noise to the log.
			tagFilter(ec2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"running", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("ecs-ec2 sweep: listing free slots: %v", err)
		return
	}
	now := time.Now()
	type candidate struct {
		id        string
		idle      time.Duration
		terminate bool
	}
	var due []candidate
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			stamp := ec2TagValue(inst.Tags, ec2TagSlotIdleSince)
			if busy[id] {
				// Occupied. Repair the other direction too — a mark left on a taken slot
				// would otherwise stop it the moment its home is released next time, with
				// no grace at all.
				if stamp != "" {
					probe.clearSlotFree(ctx, id)
				}
				continue
			}
			at, err := time.Parse(time.RFC3339, stamp)
			if stamp == "" || err != nil {
				// First sighting (or a mark nobody can read): stamp it and let the NEXT
				// sweep decide. releaseSlot normally does this; this is the repair path for
				// a CP that died between the detach and the tag, and for the slots that
				// predate this code — which is exactly what the live deployment had.
				probe.markSlotFree(ctx, id)
				continue
			}
			idle := now.Sub(at)
			running := inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning
			switch {
			case f.pool.slotTerminateAfter > 0 && idle >= f.pool.slotTerminateAfter:
				due = append(due, candidate{id: id, idle: idle, terminate: true})
			case running && f.pool.slotSleepAfter > 0 && idle >= f.pool.slotSleepAfter:
				due = append(due, candidate{id: id, idle: idle})
			}
		}
	}
	if len(due) == 0 {
		return
	}
	// Re-read occupancy immediately before acting. `homes` was read at the top of the
	// sweep and a Start takes ~seconds to attach; without this the window between the two
	// is the whole sweep, and stopping a box a launch has just landed on strands that
	// workspace until the release grace expires.
	fresh, err := probe.occupiedInstances(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: re-reading slot occupancy before sleeping free slots: %v", err)
		return // unknown occupancy is never a reason to stop something
	}
	// The ECS side of the same question. A task can be RUNNING on a slot whose home is
	// not in `homes` at all (the baker's probe, or drift), and the instance's own view of
	// its ENIs lags — so both are asked, and either one answering "busy" is enough.
	tasks, err := probe.slotTaskCounts(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: reading task counts before sleeping free slots: %v", err)
		return
	}
	for _, c := range due {
		if fresh[c.id] {
			continue // taken between the two reads
		}
		if tasks[c.id] > 0 {
			log.Printf("ecs-ec2 sweep: free slot %s still runs %d task(s); leaving it", c.id, tasks[c.id])
			continue
		}
		// Never stop a slot that still carries a task ENI — the same guard the occupied
		// path uses, and for the same reason: an instance stopped inside that window comes
		// back MULTI-ENI and silently loses its auto-assigned public IPv4 and its egress
		// (ADR 0045 決定 3-3, reproduced on real hardware). Skipping just means the next
		// sweep stops it.
		if held, err := f.taskENIsAttached(ctx, c.id); err != nil || held {
			if held {
				log.Printf("ecs-ec2 sweep: free slot %s still has a task ENI; leaving it running for now", c.id)
			}
			continue
		}
		if c.terminate {
			// Nothing to release: this walk is BY DEFINITION the slots holding no home, and
			// the three checks above have just re-confirmed it against volumes, ECS tasks
			// and the instance's own ENIs.
			probe.terminateSlot(ctx, c.id, fmt.Sprintf("free for %.0fm", c.idle.Minutes()))
			continue
		}
		log.Printf("ecs-ec2 sweep: slot %s has held no home for %.0fm; stopping it", c.id, c.idle.Minutes())
		if _, err := f.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{c.id}}); err != nil {
			log.Printf("ecs-ec2 sweep: stopping the free slot %s failed: %v", c.id, err)
		}
	}
}

// terminateSlot ends a dormant box for good: out of the cluster first, then out of EC2.
// It is the only caller of TerminateInstances, and the last step of the dormancy series
// (see slotTerminateAfter).
//
// ⚠️ The ECS half is not optional and not automatic. A terminated instance stays in the
// cluster as ACTIVE with agentConnected=false (ADR 0045 決定 3-2, and observed again on
// the live deployment when the three boxes were terminated by hand) — and a ghost that
// looks ACTIVE still satisfies placement constraints, so ECS can pick it for a task that
// then never starts. sweepGhostInstances is the repair path for boxes that vanished some
// other way; when WE are the one removing it, saying so up front is cheaper and leaves no
// window. Deregistering first also means a failed TerminateInstances degrades into the
// case sweepGhostInstances already handles, rather than into a live box ECS won't use.
//
// The caller owns the safety argument (no tasks, no home, not claimed); this function
// only carries it out.
func (e *ecsEC2Runtime) terminateSlot(ctx context.Context, instanceID, why string) {
	e.deregisterSlot(ctx, instanceID)
	if _, err := e.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	}); err != nil {
		log.Printf("ecs-ec2 sweep: terminating slot %s failed: %v", instanceID, err)
		return
	}
	log.Printf("ecs-ec2 sweep: terminated slot %s (%s); its root volume goes with it", instanceID, why)
}

// deregisterSlot takes one box out of the ECS cluster by its EC2 id. Force, because the
// point of the call is that nothing may be placed here again — and by the time we ask,
// the box is about to stop existing, so a graceful drain has nothing to drain.
//
// A box the cluster does not know about is not an error: a slot that never finished
// registering is exactly the kind we terminate.
func (e *ecsEC2Runtime) deregisterSlot(ctx context.Context, instanceID string) {
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return
	}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(e.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			log.Printf("ecs-ec2 sweep: looking up the container instance of %s: %v", instanceID, err)
			return
		}
		for _, ci := range out.ContainerInstances {
			if aws.ToString(ci.Ec2InstanceId) != instanceID {
				continue
			}
			if _, err := e.ci.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
				Cluster:           aws.String(e.base.cfg.cluster),
				ContainerInstance: ci.ContainerInstanceArn,
				Force:             aws.Bool(true),
			}); err != nil {
				// Not fatal: sweepGhostInstances re-tries this once the EC2 side is gone.
				log.Printf("ecs-ec2 sweep: deregistering %s before terminating it failed: %v", instanceID, err)
			}
			return
		}
	}
}

// slotTaskCounts maps EC2 instance id → tasks ECS believes are on it (running + pending).
// A slot the cluster does not know about is simply absent, which reads as 0 — correct,
// since nothing can be placed on it.
func (e *ecsEC2Runtime) slotTaskCounts(ctx context.Context) (map[string]int, error) {
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return map[string]int{}, err
	}
	counts := map[string]int{}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(e.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return nil, err
		}
		for _, ci := range out.ContainerInstances {
			counts[aws.ToString(ci.Ec2InstanceId)] = int(ci.RunningTasksCount + ci.PendingTasksCount)
		}
	}
	return counts, nil
}

// probeRuntime is a throwaway ecsEC2Runtime used purely as the library for pool-wide tag
// queries. It is bound to no workspace, so only the pool-wide calls are valid on it.
func (f *ecsEC2Factory) probeRuntime() *ecsEC2Runtime {
	return &ecsEC2Runtime{
		base: &ecsRuntime{cfg: f.base.cfg, ecs: f.base.ecs}, ci: f.ci, ec2: f.ec2, pool: f.pool,
		bg: backgroundWithin(f.pool.waitBudget), now: time.Now, sleep: sleepCtx,
	}
}

// runtimeForVolume rebuilds just enough of an ecsEC2Runtime to act on a volume found by
// tag — the sweeper starts from AWS, not from the database, so it can clean up after a
// workspace the CP has not looked at since it restarted.
func (f *ecsEC2Factory) runtimeForVolume(vol *ec2types.Volume) *ecsEC2Runtime {
	membership := ec2TagValue(vol.Tags, ec2TagMembership)
	name := ec2TagValue(vol.Tags, ec2TagWorkspace)
	if membership == "" || name == "" {
		return nil
	}
	rt, ok := f.New(Workspace{ContainerName: name, MembershipID: membership}, "", nil).(*ecsEC2Runtime)
	if !ok {
		return nil
	}
	return rt
}

// sweepGhostInstances deregisters container instances whose EC2 instance is gone or
// long disconnected. ECS does NOT do this for stopped instances or disconnected agents
// even when they are terminated (ADR 0045 決定 3-2), and the ghosts satisfy placement
// constraints — the scheduler keeps aiming tasks at a box that is not there.
func (f *ecsEC2Factory) sweepGhostInstances(ctx context.Context) error {
	rt := f.probeRuntime()
	arns, err := rt.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return err
	}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := f.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(f.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return err
		}
		for _, ci := range out.ContainerInstances {
			if ci.AgentConnected || aws.ToString(ci.Status) != "ACTIVE" {
				continue
			}
			if ci.RegisteredAt != nil && time.Since(*ci.RegisteredAt) < f.pool.ghostAfter {
				continue
			}
			id := aws.ToString(ci.Ec2InstanceId)
			alive, err := f.instanceAlive(ctx, id)
			if err != nil || alive {
				continue
			}
			log.Printf("ecs-ec2 sweep: deregistering ghost container instance %s (ec2 %s is gone)",
				aws.ToString(ci.ContainerInstanceArn), id)
			if _, err := f.ci.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
				Cluster:           aws.String(f.base.cfg.cluster),
				ContainerInstance: ci.ContainerInstanceArn,
				Force:             aws.Bool(true),
			}); err != nil {
				log.Printf("ecs-ec2 sweep: deregistering %s failed: %v", id, err)
			}
		}
	}
	return nil
}

// taskENIsAttached reports whether ECS still has a task network interface on the slot.
// Task ENIs are described with the attachment ARN of the task that owns them, which is
// what distinguishes them from the instance's own primary interface.
func (f *ecsEC2Factory) taskENIsAttached(ctx context.Context, instanceID string) (bool, error) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, err
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			for _, ni := range inst.NetworkInterfaces {
				if strings.Contains(aws.ToString(ni.Description), ":ecs:") {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *ecsEC2Factory) instanceAlive(ctx context.Context, instanceID string) (bool, error) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return true, err // unknown: never deregister on a failed lookup
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if inst.State != nil && inst.State.Name != ec2types.InstanceStateNameTerminated {
				return true, nil
			}
		}
	}
	return false, nil
}

// --- small helpers ---

func tagFilter(key, value string) ec2types.Filter {
	return ec2types.Filter{Name: aws.String("tag:" + key), Values: []string{value}}
}

func ec2TagValue(tags []ec2types.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}
	return ""
}

func attachment(vol *ec2types.Volume) *ec2types.VolumeAttachment {
	for i := range vol.Attachments {
		switch vol.Attachments[i].State {
		case ec2types.VolumeAttachmentStateAttached, ec2types.VolumeAttachmentStateAttaching:
			return &vol.Attachments[i]
		}
	}
	return nil
}

func attachedInstance(vol *ec2types.Volume) string {
	if a := attachment(vol); a != nil {
		return aws.ToString(a.InstanceId)
	}
	return ""
}

func filterSlotsByAZ(slots []ec2SlotCandidate, az string) []ec2SlotCandidate {
	var out []ec2SlotCandidate
	for _, s := range slots {
		if s.az == az {
			out = append(out, s)
		}
	}
	return out
}

func chunkStrings(in []string, n int) [][]string {
	var out [][]string
	for len(in) > n {
		out = append(out, in[:n])
		in = in[n:]
	}
	if len(in) > 0 {
		out = append(out, in)
	}
	return out
}

// sleepCtx sleeps unless the context ends first — the poll primitive every wait above
// is built on, so a canceled Start stops polling AWS immediately.
// backgroundWithin is the production bg: detach from the request (whose cancelation
// would otherwise kill the convergence the moment the HTTP response is written) but
// keep a budget, so a wedged AWS call cannot leak a goroutine forever.
func backgroundWithin(budget time.Duration) func(context.Context, func(context.Context)) {
	return func(ctx context.Context, fn func(context.Context)) {
		go func() {
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
			defer cancel()
			fn(c)
		}()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- pool status for the admin UI (docs/64 §64.18.6) ---

// ec2SlotView / ec2HomeView / ec2PoolStatus are what an operator needs to answer the three
// questions this runtime introduces and no other one has: how many boxes am I paying for,
// which of them are asleep, and whose home is where. Everything is derived from AWS at
// call time — there is no pool state in the CP to show (ADR 0012).
type ec2SlotView struct {
	InstanceID   string `json:"instance_id"`
	InstanceType string `json:"instance_type"`
	AZ           string `json:"az"`
	State        string `json:"state"`      // running | stopped | pending | …
	Registered   bool   `json:"registered"` // ECS accepts tasks on it
	Workspace    string `json:"workspace"`  // occupant, "" = free
	IdleMinutes  int    `json:"idle_minutes"`
	// Quarantined: this box failed to mount a home and has been taken out of the pool
	// (決定 20). It is shown rather than hidden because it keeps billing until somebody
	// terminates it, and because "the pool shrank by one" is not an explanation.
	Quarantined      bool   `json:"quarantined"`
	QuarantineReason string `json:"quarantine_reason"`
}

type ec2HomeView struct {
	VolumeID      string `json:"volume_id"`
	Workspace     string `json:"workspace"`
	SizeGiB       int32  `json:"size_gib"`
	AZ            string `json:"az"`
	AttachedTo    string `json:"attached_to"` // "" = detached (hibernating, or drifted)
	IdleMinutes   int    `json:"idle_minutes"`
	Hibernating   bool   `json:"hibernating"`
	SnapshotID    string `json:"snapshot_id"`
	SnapshotState string `json:"snapshot_state"`
	// Backups (決定 17). Reported per home rather than as a pool total, because the
	// question an operator actually has is "would THIS person lose work if their AZ went
	// away", and an average cannot answer it. BackupAgeMinutes is -1 when there is none.
	Backups          int `json:"backups"`
	BackupAgeMinutes int `json:"backup_age_minutes"`
}

type ec2PoolStatus struct {
	Runtime     string `json:"runtime"`
	Pool        string `json:"pool"`
	MaxSlots    int    `json:"max_slots"`
	SleepAfterS int    `json:"slot_sleep_sec"`
	// TerminateAfterS is the stage after SleepAfterS: 0 = boxes are kept forever, which
	// is what makes MaxSlots double as "how many root volumes this deployment pays for".
	TerminateAfterS int           `json:"slot_terminate_sec"`
	HibernateS      int           `json:"hibernate_after_sec"`
	Slots           []ec2SlotView `json:"slots"`
	Homes           []ec2HomeView `json:"homes"`
	GoldenID        string        `json:"golden_id"`
	GoldenImage     string        `json:"golden_image"`
	GoldenStale     bool          `json:"golden_stale"`
	RunningImage    string        `json:"running_image"`
	// Baking / BakeRejected report what the automatic baker is doing, because the two
	// states an operator cannot otherwise account for are "there is no golden yet and
	// something is working on it" and "there is no golden because the last candidate
	// could not boot". Both otherwise exist only as a CP log line that has scrolled
	// away, and the second one is a standing condition, not an event.
	Baking       bool   `json:"baking"`
	BakeRejected string `json:"bake_rejected"` // snapshot id of the last refused candidate
	BakeReason   string `json:"bake_reason"`
	// Goldens is the same story per CPU architecture, one entry per architecture this
	// deployment's classes declare (docs/70 §70.6). The six scalars above are this
	// list's FIRST entry — the default class's architecture — kept so nothing that
	// reads them has to change on a deployment that has only one.
	Goldens []ec2GoldenView `json:"goldens,omitempty"`
	// SlotClasses names the declared classes for the pool screen. Absent when the
	// deployment declared a single unnamed ladder.
	SlotClasses []workspaceSlotClass `json:"slot_classes,omitempty"`
	// AutoBake is AF_ECS_EC2_GOLDEN_AUTOBAKE. Not derivable from AWS like everything
	// else here, and filled in by the manager rather than this adapter — but without it
	// "there is no golden and nothing is happening" has two very different meanings
	// (the next tick will start one / nothing ever will) and the screen cannot tell
	// them apart.
	AutoBake bool `json:"auto_bake"`
	// Budget is Σ(tenant max_workspaces) against MaxSlots, and it is present ONLY when
	// something is wrong with it (over-subscribed, or a tenant with no cap at all).
	// Filled in by the manager, which has the database this adapter does not.
	//
	// ⚠️ The two numbers count different things and the screen must not merge them:
	// max_workspaces bounds CONCURRENT workspaces, MaxSlots bounds boxes that EXIST, and
	// a stopped workspace still holds a box (lazy release) while counting toward neither
	// tenant's concurrency. See poolBudget.
	Budget *poolBudget `json:"budget,omitempty"`
}

// ec2GoldenView is one architecture's golden situation, including how far along a bake
// that is under way has got (docs/64 §64.30).
type ec2GoldenView struct {
	Arch       string `json:"arch"`
	SnapshotID string `json:"snapshot_id"`
	Image      string `json:"image"`
	Stale      bool   `json:"stale"`
	Baking     bool   `json:"baking"`
	Rejected   string `json:"rejected"`
	Reason     string `json:"reason"`

	// --- how far the bake has got (docs/64 §64.30) ---
	//
	// A bake takes ~11 minutes end to end and spends most of it in steps that produce
	// no snapshot at all (the seed's boot, boot-install, the slot release). Reporting
	// only "baking" — which was true only once a candidate snapshot existed — meant the
	// screen said "there is no golden" for the whole first half, i.e. exactly when an
	// operator wondering why a new member is slow goes looking.
	Phase string `json:"phase"`
	// PhaseSince anchors the elapsed time, and is deliberately the SAME anchor the
	// baker's own deadline uses: the seed's home volume while the seed is booting, the
	// candidate's af-bake-started once one exists. A screen that counted from something
	// else would disagree with the tear-down it is meant to explain.
	PhaseSince string `json:"phase_since,omitempty"`
	// Candidate is the snapshot being baked or verified — the one that becomes the
	// golden if the probe comes up.
	Candidate string `json:"candidate,omitempty"`
	// Progress is EBS's own copy percentage while the candidate is pending.
	Progress int `json:"progress,omitempty"`
	// Attempts counts the candidates this image has already burned on this
	// architecture. The baker gives up at 2, so 1 means "one more try left".
	Attempts int `json:"attempts,omitempty"`
	// SlotsInUse is what the capacity gate saw, reported only when it is what is
	// holding the bake up.
	SlotsInUse int `json:"slots_in_use,omitempty"`
	// Seed / Probe are the reserved workspaces while they exist. They are on the screen
	// because they hold a slot each: without them the pool table shows a box occupied by
	// af-ws-af-golden-… with nothing to connect it to.
	Seed  *ec2BakeWorkspaceView `json:"seed,omitempty"`
	Probe *ec2BakeWorkspaceView `json:"probe,omitempty"`
}

// The steps of a bake, in order, plus the four ways there can be no bake. The Console
// renders the first six as a progress line and the rest as a sentence.
const (
	ec2BakePhaseSeed      = "seed"      // the seed workspace is waiting for a slot
	ec2BakePhaseBoot      = "boot"      // boot-install is running on the seed's home
	ec2BakePhaseCapture   = "capture"   // boot-install done; the home is coming off its slot
	ec2BakePhaseSnapshot  = "snapshot"  // EBS is copying the candidate
	ec2BakePhaseProbe     = "probe"     // a probe workspace is being booted from the candidate
	ec2BakePhasePublished = "published" // a golden for the running image is in use
	ec2BakePhaseIdle      = "idle"      // nothing under way; the next tick starts one
	ec2BakePhaseBlocked   = "blocked"   // a bake needs two free slots and cannot have them
	ec2BakePhaseRejected  = "rejected"  // the last candidate could not boot; one attempt left
	ec2BakePhaseGaveUp    = "gave_up"   // two candidates burned on this image — no more tries
	ec2BakePhaseOff       = "off"       // AF_ECS_EC2_GOLDEN_AUTOBAKE=0
)

// ec2BakeWorkspaceView is one of the two reserved workspaces a bake stands up.
type ec2BakeWorkspaceView struct {
	Workspace  string `json:"workspace"`
	VolumeID   string `json:"volume_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// bakePhaseActive reports the phases in which something is actually being baked. It is
// what the legacy `baking` scalar now means: the old derivation (a candidate snapshot
// exists) called the first half of a bake "no golden, nothing happening".
func bakePhaseActive(phase string) bool {
	switch phase {
	case ec2BakePhaseSeed, ec2BakePhaseBoot, ec2BakePhaseCapture, ec2BakePhaseSnapshot, ec2BakePhaseProbe:
		return true
	}
	return false
}

// PoolStatus reports the live pool. Read-only and tolerant: a section that cannot be read
// comes back empty rather than failing the whole call, because the most likely time an
// operator opens this screen is when something is already wrong.
func (f *ecsEC2Factory) PoolStatus(ctx context.Context) (ec2PoolStatus, error) {
	// One throwaway runtime as the library for the tag queries — the same trick the
	// sweeper uses.
	probe := f.probeRuntime()
	st := ec2PoolStatus{
		Runtime: "ecs-ec2", Pool: f.pool.pool, MaxSlots: f.pool.maxSlots,
		SleepAfterS:     int(f.pool.slotSleepAfter.Seconds()),
		TerminateAfterS: int(f.pool.slotTerminateAfter.Seconds()),
		HibernateS:      int(f.pool.hibernateAfter.Seconds()),
		Slots:           []ec2SlotView{}, Homes: []ec2HomeView{},
		RunningImage: f.base.cfg.workspaceImage,
		SlotClasses:  f.SizingProfile().SlotClasses,
	}
	now := time.Now()

	vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, f.pool.pool),
			tagFilter(ec2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return st, err
	}
	occupant := map[string]*ec2HomeView{} // instance id → the home on it
	// bakeHomes is the seed's and the probe's home, kept aside as the baker sees them —
	// the af-bake-ready stamp and the creation time are not part of the ordinary home
	// view, and they are what says whether boot-install has finished and how long this
	// bake has been going.
	bakeHomes := map[string]ec2BakeHome{} // workspace name → facts
	for i := range vols.Volumes {
		v := &vols.Volumes[i]
		// A volume EC2 is still deleting is not a home any more, and listing it as one
		// tells the operator a destroyed workspace is still around (measured: a Destroy
		// left two volumes in `deleting` for ~40 minutes, and both showed on this screen
		// as detached homes). homeVolume() has always skipped these; the screen must too.
		if v.State == ec2types.VolumeStateDeleting || v.State == ec2types.VolumeStateDeleted {
			continue
		}
		idle, _ := idleSince(v, now)
		h := ec2HomeView{
			VolumeID:    aws.ToString(v.VolumeId),
			Workspace:   ec2TagValue(v.Tags, ec2TagWorkspace),
			SizeGiB:     aws.ToInt32(v.Size),
			AZ:          aws.ToString(v.AvailabilityZone),
			AttachedTo:  attachedInstance(v),
			IdleMinutes: int(idle.Minutes()),
			Hibernating: ec2TagValue(v.Tags, ec2TagHibernating) != "",
			// -1 rather than 0: "no copy at all" and "copied a moment ago" are opposite
			// answers and must not render as the same number.
			BackupAgeMinutes: -1,
		}
		st.Homes = append(st.Homes, h)
		if h.AttachedTo != "" {
			occupant[h.AttachedTo] = &st.Homes[len(st.Homes)-1]
		}
		bh := ec2BakeHome{VolumeID: h.VolumeID, InstanceID: h.AttachedTo, Baked: ec2TagValue(v.Tags, ec2TagBakeReady) != ""}
		if v.CreateTime != nil {
			bh.Created = *v.CreateTime
		}
		bakeHomes[h.Workspace] = bh
	}

	// Quarantined boxes are listed too, on purpose. They are out of the pool for
	// placement, but they still exist and still bill, and a slot that disappears from the
	// screen the moment it breaks is how an operator ends up paying for a box nobody can
	// see (決定 20).
	insts, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(ec2TagPool, f.pool.pool),
			{Name: aws.String("tag:" + ec2TagRole), Values: []string{ec2RoleSlot, ec2RoleQuarantined}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return st, err
	}
	registered, err := probe.registeredSlots(ctx)
	if err != nil {
		log.Printf("ecs-ec2 pool status: container instances unreadable: %v", err)
		registered = map[string]bool{}
	}
	for _, r := range insts.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			s := ec2SlotView{
				InstanceID: id, InstanceType: string(inst.InstanceType),
				AZ: aws.ToString(inst.Placement.AvailabilityZone), Registered: registered[id],
			}
			if inst.State != nil {
				s.State = string(inst.State.Name)
			}
			if h := occupant[id]; h != nil {
				s.Workspace, s.IdleMinutes = h.Workspace, h.IdleMinutes
			} else if at, err := time.Parse(time.RFC3339, ec2TagValue(inst.Tags, ec2TagSlotIdleSince)); err == nil {
				// A FREE slot has its own clock (af-slot-idle-since), and the operator's
				// question about a running box with no workspace on it is "how long has it
				// been like that" — which used to read as 0 whatever the answer was.
				s.IdleMinutes = int(now.Sub(at).Minutes())
			}
			if ec2TagValue(inst.Tags, ec2TagRole) == ec2RoleQuarantined {
				s.Quarantined = true
				s.QuarantineReason = ec2TagValue(inst.Tags, ec2TagQuarantineReason)
			}
			st.Slots = append(st.Slots, s)
		}
	}
	sort.SliceStable(st.Slots, func(i, j int) bool { return st.Slots[i].InstanceID < st.Slots[j].InstanceID })
	sort.SliceStable(st.Homes, func(i, j int) bool { return st.Homes[i].Workspace < st.Homes[j].Workspace })

	// Hibernation snapshots, matched back onto the homes they belong to, plus the golden.
	snaps, err := f.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters:  []ec2types.Filter{tagFilter(ec2TagPool, f.pool.pool)},
	})
	if err != nil {
		log.Printf("ecs-ec2 pool status: snapshots unreadable: %v", err)
		return st, nil
	}
	// One golden per architecture (docs/70 §70.6), so the screen answers "is every
	// class covered" rather than "is there a golden". byArch is keyed by the snapshot's
	// af-arch — absent means x86_64, the only kind that existed before classes.
	byArch := map[string]*ec2GoldenView{}
	candidateState := map[string]ec2types.SnapshotState{}
	// The screen has to agree with the Start path about what "for the running image"
	// means, or it reports a golden as stale that goldenSnapshot happily uses (and the
	// operator re-bakes for nothing). Same identity, same rule (goldenIdentity).
	imgID := f.imageIdentity(ctx)
	goldenMatched := map[string]bool{}
	golden := func(arch string) *ec2GoldenView {
		g, ok := byArch[arch]
		if !ok {
			g = &ec2GoldenView{Arch: arch}
			byArch[arch] = g
		}
		return g
	}
	for _, s := range snaps.Snapshots {
		arch := snapshotArch(s)
		switch ec2TagValue(s.Tags, ec2TagRole) {
		case ec2RoleGolden:
			// A matching golden wins over a stale one, so the screen shows what the Start
			// path would actually use — and shows the stale one only when that is all
			// there is, which is exactly when the operator needs to be told to re-bake.
			g := golden(arch)
			img := ec2TagValue(s.Tags, ec2TagImage)
			if match := imgID.matches(s.Tags); g.SnapshotID == "" || match {
				g.SnapshotID, g.Image = aws.ToString(s.SnapshotId), img
				goldenMatched[arch] = match
			}
		case ec2RoleGoldenCandidate:
			// Only a candidate for the image being RUN says a bake is under way. One
			// stamped with anything else is a leftover the baker will sweep.
			if imgID.matches(s.Tags) {
				g := golden(arch)
				g.Candidate = aws.ToString(s.SnapshotId)
				g.Progress = snapshotProgress(s)
				// The candidate's own state IS the difference between "EBS is still
				// copying" and "a probe is being booted from it" — the baker's verify
				// step does nothing at all until the copy completes.
				candidateState[arch] = s.State
				g.PhaseSince = bakeStartedAt(s)
			}
		case ec2RoleGoldenRejected:
			if imgID.matches(s.Tags) {
				g := golden(arch)
				g.Rejected = aws.ToString(s.SnapshotId)
				g.Reason = ec2TagValue(s.Tags, ec2TagBakeReason)
				// The same count the baker gives up on (rejectedAttempts), derived from
				// the snapshots this call already read rather than asked for again.
				g.Attempts++
			}
		case ec2RoleBackup:
			ws := ec2TagValue(s.Tags, ec2TagWorkspace)
			for i := range st.Homes {
				if st.Homes[i].Workspace != ws {
					continue
				}
				// Count every copy, but age only from COMPLETED ones: a capture still
				// running is not something anybody could restore from yet, and showing
				// its age would say "you are covered" a good half hour early.
				st.Homes[i].Backups++
				if s.State != ec2types.SnapshotStateCompleted {
					continue
				}
				age := int(now.Sub(backupStamp(s)).Minutes())
				if st.Homes[i].BackupAgeMinutes < 0 || age < st.Homes[i].BackupAgeMinutes {
					st.Homes[i].BackupAgeMinutes = age
				}
			}
		case ec2RoleHome:
			ws := ec2TagValue(s.Tags, ec2TagWorkspace)
			for i := range st.Homes {
				if st.Homes[i].Workspace == ws {
					st.Homes[i].SnapshotID = aws.ToString(s.SnapshotId)
					st.Homes[i].SnapshotState = string(s.State)
				}
			}
			// A home whose volume is already gone is not in Homes at all — it IS the
			// snapshot now, and an operator looking for "where did that user go" needs
			// to see it.
			if !hasHome(st.Homes, ws) {
				st.Homes = append(st.Homes, ec2HomeView{
					Workspace: ws, Hibernating: true, BackupAgeMinutes: -1,
					SnapshotID: aws.ToString(s.SnapshotId), SnapshotState: string(s.State),
				})
			}
		}
	}
	// Report the architectures this deployment declares, in bake order, so an arch
	// with NO golden at all appears as an empty row instead of not appearing — the
	// whole question the operator has is which classes are still slow.
	slotsInUse, slotRunning := 0, map[string]bool{}
	for _, s := range st.Slots {
		if s.Quarantined {
			continue // the same set bakeBlocked counts: af-role=slot, quarantine excluded
		}
		slotsInUse++
		slotRunning[s.InstanceID] = s.State == "running"
	}
	blocked, _ := bakeCapacityBlocked(slotsInUse, f.pool.maxSlots)
	for _, arch := range f.bakeArches() {
		g := golden(arch)
		g.Stale = g.SnapshotID != "" && !goldenMatched[arch]
		describeBake(g, arch, bakeHomes, slotRunning, candidateState[arch], blocked, slotsInUse)
		g.Baking = bakePhaseActive(g.Phase)
		st.Goldens = append(st.Goldens, *g)
	}
	// The scalar fields stay, as the DEFAULT architecture's answer: a Console built
	// before classes existed reads them, and a single-class deployment has nothing
	// else to say.
	if len(st.Goldens) > 0 {
		g := st.Goldens[0]
		st.GoldenID, st.GoldenImage, st.GoldenStale = g.SnapshotID, g.Image, g.Stale
		st.Baking, st.BakeRejected, st.BakeReason = g.Baking, g.Rejected, g.Reason
	}
	return st, nil
}

func hasHome(homes []ec2HomeView, workspace string) bool {
	for _, h := range homes {
		if h.Workspace == workspace && h.VolumeID != "" {
			return true
		}
	}
	return false
}
