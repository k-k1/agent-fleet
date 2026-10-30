package runtime

import (
	"context"
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
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// ec2PoolConfig is the deployment-wide shape of the slot pool, read once at boot from
// AF_ECS_EC2_*. The substrate (deploy/aws/ecs/cfn/40-ec2-pool.yaml) owns the launch
// template: AMI, user-data (cluster join, the shortened task-cleanup window, the
// af-mount/af-umount helpers), instance profile and security group all live there, so
// the CP only ever says "run one of these, in this AZ, at this size".
type ec2PoolConfig struct {
	launchTemplate string // AF_ECS_EC2_LAUNCH_TEMPLATE (id or name)
	// noAutoReplace is AF_ECS_EC2_AUTO_REPLACE_OUTDATED_SLOTS=false: a slot below the launch
	// template's $Latest is then replaced only when an operator reserves it (#1473). The zero
	// value is the default — automatic replacement ON (#1934) — so a pool built without the
	// env keeps the new behaviour.
	noAutoReplace bool
	// amiArm64 is AF_ECS_EC2_AMI_ARM64, required only when a declared class says
	// arm64: the launch template pins the x86_64 ECS-optimized AMI, and an arm64
	// instance type cannot boot it (docs/log/70 §70.8).
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
	// decision 7). The 40-ec2-pool stack resolves the public parameter and passes the id.
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
	// It governs BOTH kinds of dormant slot, from the same value (docs/log/64 §64.31):
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
	// may all be stopped and idle. Measured on the live deployment (docs/log/64 §64.32): the
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
	// and its volume deleted (ADR 0045 decision 4 + decision 13-2). The next Start restores it —
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
	// slotLostAfter is how long a slot may be EC2-`running` while the CLUSTER cannot
	// reach its agent before the CP declares the box lost and rebuilds the workspace
	// somewhere else (AF_ECS_EC2_SLOT_LOST_AFTER_SEC, default 5m).
	//
	// 🔥 The incident it exists for (<prod-deployment>, 2026-08-27, docs/log/64 §64.40): something in
	// one workspace took several GB of anonymous memory, and with no swap the kernel's only
	// way to reclaim was to throw away page cache — so every process on the box spent the
	// next three hours re-reading its own executable pages off disk. The root volume holds
	// 8.0 GB of data in total and was read at 39.34 GB every five minutes: the whole disk,
	// five times over, per bucket, for three hours. Everything that needed a disk starved,
	// and the box's daemons died within fifteen seconds of each other — the EFS
	// transit-encryption stunnel (`nfs: server 127.0.0.1 not responding`), the ECS agent,
	// SSM. EC2 still said `running` and every status check still passed; the box was simply
	// gone as far as the cluster was concerned.
	//
	// ⚠️ The OOM killer never fired, and that is not evidence against memory being the
	// cause — in this state it CANNOT fire, because reclaim keeps succeeding (by destroying
	// the cache). Containing that blast radius is a separate fix (a cgroup memory limit on
	// the workspace container, which ecs-ec2 alone does not apply). What this field bounds
	// is only the CP's reaction to a slot that has stopped answering, which is the same
	// problem whichever resource ran out.
	//
	// Nothing could recover from that on its own, and each layer was individually correct:
	//   - ECS could not stop the ghost task (no agent), so its ENI never detached;
	//   - the sweeper refuses to stop a slot that still holds a task ENI (multi-ENI/egress
	//     loss, decision 3-3), so it logged "leaving it running for now" every 5 minutes forever;
	//   - Start reused the slot by affinity and waited for a registration that was never
	//     coming, which the ingress cut at 60s — the user got a 504 per attempt, for hours.
	// So the missing piece was a TIME LIMIT on "not registered yet": past it the box is not
	// slow, it is lost, and the home has to be moved rather than waited on (abandonLostSlot).
	//
	// Sized well above every legitimate wait — a hot slot answers on the first poll, a woken
	// one in ~20s, a brand-new one in ~35s from RunInstances — because the cost of being
	// wrong is asymmetric: too eager throws away a healthy box mid-boot, too patient only
	// makes a doomed Start take a few minutes longer than it had to.
	//
	// 0 = OFF, read the same way as the other timers (§64.31.4): wait as long as the
	// caller's budget allows, i.e. exactly the behaviour before this existed.
	slotLostAfter time.Duration
	// hostReserveMiB is how much of a slot is held back from the workspace container so
	// the box's own daemons cannot be starved by it (AF_ECS_EC2_HOST_RESERVE_MB).
	// See workspaceMemCapMiB for what it buys and how the default was sized.
	//
	//	unset / "auto"  → ec2HostReserveAuto: a flat 2 GiB (the default)
	//	"off"           → ec2HostReserveOff: no cap at all, the pre-2026-08 behaviour
	//	<n>             → exactly n MiB; 0 reserves nothing and caps at the full rung
	//
	// Only ec2HostReserveAuto, ec2HostReserveOff and values >= 0 are valid; parseHostReserveMiB
	// never returns anything else.
	hostReserveMiB int64
}

const (
	// ec2HostReserveOff is hostReserveMiB's "leave the workspace uncapped" sentinel.
	// Negative rather than 0 because 0 is a meaningful answer ("reserve nothing, cap at
	// the full rung"), and an operator's explicit off has to be distinguishable from it.
	ec2HostReserveOff = -1
	// ec2HostReserveAuto is the "use the flat floor below" sentinel, likewise distinct
	// from an explicit 0: an operator who sets 0 must not get 2 GiB reserved.
	ec2HostReserveAuto = -2
	// The auto reserve (and so its floor), flat at every rung. The daemons were measured at ~1.2-1.4 GiB
	// with a workspace and its Service Connect sidecars running, and the rung overstates
	// the real MemTotal by ~5% on top; a fifth of an 8 GiB rung left ~1230 MiB and a box
	// thrashed (ECS and SSM agents went dark). Flat, because a proportional reserve
	// shrinks again just above 8 GiB, and the daemons' cost does not grow with the box.
	ec2HostReserveFloorMiB = 2048
	// ec2MinWorkspaceMemMiB is the soft reservation buildTaskDef sets; a capped workspace
	// must be left with MORE than it, because ECS rejects Memory <= MemoryReservation.
	// At or below it the rung is simply too small to share, and the cap is dropped
	// rather than applied.
	ec2MinWorkspaceMemMiB = 512
)

// ec2Slot is one purchasable slot size.
//
// vcpu is DECLARED by the operator, not looked up: it exists only so the Console can
// say "you land on m7i.xlarge (4 vCPU / 16 GiB)". Asking EC2 (DescribeInstanceTypes)
// would be authoritative, but it means adding an IAM action to the CP task role and
// redeploying the platform stack for a label — and the operator writing the ladder
// already knows the number (ADR 0045 decision 21). 0 = not declared → not shown.
type ec2Slot struct {
	instanceType string
	memMiB       int64
	vcpu         int
}

// CPU architectures a class may declare. They are the ECS/EC2 spellings, so the
// value goes straight into RuntimePlatform without a second vocabulary.
const (
	EC2ArchX86 = "x86_64"
	EC2ArchArm = "arm64"
)

// ec2SlotClass is one named ladder: which KIND of machine, as opposed to how big.
//
// The architecture is DECLARED, never derived from the instance type's name. "a
// family ending in g is Graviton" reads well until m7gd, x2gd or g4dn, and a wrong
// guess here does not misprint a label — it boots the wrong AMI. The operator
// writing the ladder already knows (the same reasoning ADR 0045 decision 21 used for
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
	// leases is Config.HomeLeases, handed to every runtime the factory builds.
	leases HomeLeaseStore
	// onAutoReplace is Config.OnSlotAutoReplace.
	onAutoReplace func(context.Context, SlotAutoReplace)
	// bg / sleep replace the probe runtime's background runner and sleeper when set; only
	// tests do, to drive ForceStopSlot's delayed re-stop without waiting for it.
	bg    func(context.Context, func(context.Context))
	sleep func(context.Context, time.Duration) error

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
// the drift sweeper (docs/log/64 §64.15.6), which is what makes the "no CP-side state"
// choice survivable — every unfinished teardown is re-derived from tags.
func newECSEC2Factory(mcfg Config) (RuntimeFactory, error) {
	baseFactory, err := newECSFactory(mcfg)
	if err != nil {
		return nil, err
	}
	base, ok := baseFactory.(*ecsFactory)
	if !ok {
		return nil, fmt.Errorf("ecs-ec2: unexpected base factory %T", baseFactory)
	}
	ac, err := AWSConfigFor(context.Background(), base.cfg.region)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	pool := ec2PoolConfig{
		launchTemplate: os.Getenv("AF_ECS_EC2_LAUNCH_TEMPLATE"),
		amiArm64:       os.Getenv("AF_ECS_EC2_AMI_ARM64"),
		noAutoReplace:  !envBoolDefault("AF_ECS_EC2_AUTO_REPLACE_OUTDATED_SLOTS", true),
		pool:           envOr("AF_ECS_EC2_POOL", base.cfg.cluster),
		classes:        parseSlotClasses(envOr("AF_ECS_EC2_SLOT_TYPES", "m7i.large:8192,m7i.xlarge:16384,m7i.2xlarge:32768")),
		defaultClass:   os.Getenv("AF_ECS_EC2_DEFAULT_SLOT_CLASS"),
		maxSlots:       EnvInt("AF_ECS_EC2_MAX_SLOTS", 8),
		homeGiB:        int32(EnvInt("AF_ECS_EC2_HOME_GB", 50)),
		tmpfsMiB:       int32(EnvInt("AF_ECS_EC2_TMP_MB", 2048)),
		// noexec is deliberately NOT in the default set. ADR 0045 decision 8 names
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
		claimTTL:       time.Duration(EnvInt("AF_ECS_EC2_CLAIM_TTL_SEC", 300)) * time.Second,
		releaseGrace:   time.Duration(EnvInt("AF_ECS_EC2_RELEASE_GRACE_SEC", 600)) * time.Second,
		slotSleepAfter: time.Duration(EnvInt("AF_ECS_EC2_SLOT_SLEEP_SEC", 900)) * time.Second,
		// Default 0 = never terminate. See the field comment: this is the step that gives
		// the root volume back, and it is the pre-existing behaviour, so it is opt-in.
		slotTerminateAfter: time.Duration(EnvInt("AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC", 0)) * time.Second,
		// Default 0 = no hibernation. See the field comment: this is the one automatic
		// path that moves a user's home, so it is opt-in.
		hibernateAfter: time.Duration(EnvInt("AF_ECS_EC2_HIBERNATE_AFTER_SEC", 0)) * time.Second,
		backupKeep:     EnvInt("AF_ECS_EC2_BACKUP_KEEP", 3),
		sweepEvery:     time.Duration(EnvInt("AF_ECS_EC2_SWEEP_SEC", 300)) * time.Second,
		ghostAfter:     time.Duration(EnvInt("AF_ECS_EC2_GHOST_AFTER_SEC", 3600)) * time.Second,
		waitBudget:     time.Duration(EnvInt("AF_ECS_EC2_WAIT_SEC", 600)) * time.Second,
		// Default ON, unlike the other timers here: this one does not change what the
		// product does in the normal case, it only puts an end to a wait that was
		// previously unbounded in practice. See the field comment for the incident.
		slotLostAfter:  time.Duration(EnvInt("AF_ECS_EC2_SLOT_LOST_AFTER_SEC", 300)) * time.Second,
		hostReserveMiB: parseHostReserveMiB(envOr("AF_ECS_EC2_HOST_RESERVE_MB", "auto")),
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
		// A factory with no store keeps the process-local locks; log it, because with two
		// replicas that is the race #1601 closed.
		leases: mcfg.HomeLeases,

		onAutoReplace: mcfg.OnSlotAutoReplace,
	}
	if f.leases == nil {
		log.Printf("ecs-ec2: no store for home leases; a home is serialised within this Control Plane only")
	}
	log.Printf("runtime=ecs-ec2 pool=%s launch-template=%s arm64-ami=%s classes=%s default-class=%s max=%d home=%dGiB",
		pool.pool, pool.launchTemplate, envOr("AF_ECS_EC2_AMI_ARM64", "(none)"),
		describeSlotClasses(pool.classes), pool.defaultClass, pool.maxSlots, pool.homeGiB)
	go f.sweepLoop(context.Background())
	return f, nil
}

func (f *ecsEC2Factory) New(ws Workspace, keys SecretKeys, extraEnv []string) Runtime {
	base, ok := f.base.New(ws, keys, extraEnv).(*ecsRuntime)
	if !ok { // unreachable: ecsFactory.New always returns *ecsRuntime
		panic("ecs-ec2: base factory did not return *ecsRuntime")
	}
	rung, class := f.pool.rungFor(ws.SlotClass, ws.MemBytes)
	return &ecsEC2Runtime{
		base:         base,
		ec2:          f.ec2,
		ssmc:         f.ssmc,
		ci:           f.ci,
		pool:         f.pool,
		instanceType: rung.instanceType,
		arch:         class.arch,
		memCapMiB:    f.pool.workspaceMemCapMiB(rung.memMiB),
		homeGiB:      f.homeGiB(ws),
		slotMemMiB:   rung.memMiB,
		slotVCPU:     rung.vcpu,
		classID:      class.id,
		classLabel:   class.label,
		azOfSubnet:   f.subnetAZs,
		bg:           backgroundWithin(f.pool.waitBudget),
		now:          time.Now,
		sleep:        sleepCtx,
		leases:       f.leases,

		tenantID: ws.TenantID, workspaceID: ws.ID, onAutoReplace: f.onAutoReplace,
	}
}

// homeGiB is the persistent home size for a workspace: its own disk request, else the
// deployment default. Unlike Fargate's ephemeral storage there is no free tier and no
// 200 GiB ceiling here — the volume is billed as provisioned and grows online with
// ModifyVolume (docs/log/64 §64.4.5).
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
// choice of instance type, not Fargate's 74 discrete (cpu, memory) pairs (docs/log/64
// §64.4.5): the task reserves neither cpu nor memory, so the user gets the whole box
// (ADR 0045 decision 8).
//
// ⚠️ The memory argument is a REQUIREMENT, not a cap — "fit me into a box this big" —
// and the person then gets whatever that box has. It is also why 0 (unset) lands on
// the SMALLEST slot here, while 0 on Fargate means the deployment's task size and on
// docker means WS_MEMORY. The Console says which of the three it is (ADR 0045 decision 21).
//
// The class only chooses the LADDER. Memory still chooses the rung, in every class,
// which is what keeps "8 GB" meaning the same thing after a class change.
func (p ec2PoolConfig) slotTypeFor(classID string, memBytes int64) (instanceType, arch string) {
	t, a, _ := p.slotRungFor(classID, memBytes)
	return t, a
}

// slotRungFor is slotTypeFor plus the rung's DECLARED memory, which the workspace's
// memory cap is derived from (workspaceMemCapMiB).
func (p ec2PoolConfig) slotRungFor(classID string, memBytes int64) (instanceType, arch string, slotMemMiB int64) {
	s, c := p.rungFor(classID, memBytes)
	return s.instanceType, c.arch, s.memMiB
}

// rungFor is the whole answer slotRungFor narrows: the rung itself (including the
// declared vCPU the Console prints) and the class it was taken from. Every caller goes
// through here so that "which box does this member get" is decided in exactly one place —
// a second copy of the rule would drift from placement, and the screen would then promise
// a box the pool never asks for.
func (p ec2PoolConfig) rungFor(classID string, memBytes int64) (ec2Slot, ec2SlotClass) {
	c := p.classFor(classID)
	want := memBytes / mib
	for _, s := range c.slots {
		if want <= s.memMiB {
			return s, c
		}
	}
	return c.slots[len(c.slots)-1], c
}

// workspaceMemCapMiB is the hard memory limit put on the workspace container: the
// slot's declared memory, less a reserve kept for the box's own daemons. 0 = no cap
// (the behaviour every deployment had before this existed).
//
// 🔥 Why a cap exists at all (<prod-deployment>, 2026-08-27, docs/log/64 §64.40): a workspace
// took several GB of anonymous memory, and with no swap the kernel's only way to
// reclaim was to throw away page cache — so every process on the box spent three hours
// re-reading its own executable pages off disk (the root volume holds 8.0 GB of data
// in total and was read at 39.34 GB per five minutes). The workspace's OWN pain is not
// what this fixes; it is the collateral: dockerd, containerd, the ECS agent, SSM and
// the EFS stunnel all starved and died within fifteen seconds of each other, and the
// box became unmanageable — ECS could not stop the task, SSM could not run a command,
// and the owner got a 504 on every Start for hours.
//
// A cgroup limit is the fix because of WHERE the pressure then lands: reclaim for an
// over-limit cgroup happens INSIDE that cgroup, so the daemons keep their pages
// whatever the workspace does. The runaway gets OOM-killed in its own cgroup — visible
// as OOMKilled, which is a diagnosis rather than a mystery.
//
// ⚠️ Derived from the LADDER's declared MiB, not from what the box really has, and the
// two differ by more than you would guess: an "8192 MiB" m7i.large reports MemTotal
// 7784 MiB (measured). The reserve therefore has to absorb that ~5% as well as the
// daemons, which is why the default is 2 GiB rather than the ~1.2-1.4 GiB the daemons
// were measured at:
//
//	nominal 8192 → reserve 2048 → cap 6144 MiB, real headroom 7784-6144 = 1640 MiB ✅
//	nominal 8192 → reserve 1638 → cap 6554 MiB, real headroom 7784-6554 = 1230 MiB ✗ (thrashed)
//	nominal 8192 → reserve 1024 → cap 7168 MiB, real headroom 7784-7168 =  616 MiB ✗
//
// Reading the true figure is possible (DescribeContainerInstances reports the
// registered MEMORY) but it is not worth it here: the task definition is built BEFORE
// the slot is known to be registered, so using it would mean reordering launch() for
// an accuracy this reserve already covers.
//
// The auto reserve is flat: the daemons cost the same on every size.
// parseHostReserveMiB reads AF_ECS_EC2_HOST_RESERVE_MB. Anything unrecognised is
// "auto" rather than an error: this knob decides how much RAM a workspace gets, and a
// typo must not be the reason a deployment stops capping (or stops starting).
func parseHostReserveMiB(s string) int64 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ec2HostReserveAuto
	case "off", "none", "false":
		return ec2HostReserveOff
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		log.Printf("ecs-ec2: AF_ECS_EC2_HOST_RESERVE_MB=%q is not auto|off|<MiB>; using auto", s)
		return ec2HostReserveAuto
	}
	return n
}

func (p ec2PoolConfig) workspaceMemCapMiB(slotMemMiB int64) int64 {
	if slotMemMiB <= 0 || p.hostReserveMiB == ec2HostReserveOff {
		return 0
	}
	reserve := p.hostReserveMiB
	if reserve == ec2HostReserveAuto {
		reserve = ec2HostReserveFloorMiB
	}
	cap := slotMemMiB - reserve
	if cap <= ec2MinWorkspaceMemMiB {
		// A rung too small to leave both a workspace and its daemons room. Capping it
		// to something unusable would break the deployment; say so and leave it alone.
		log.Printf("ecs-ec2: slot rung %d MiB is too small to reserve %d MiB for the host; leaving the workspace uncapped",
			slotMemMiB, reserve)
		return 0
	}
	return cap
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
			return []ec2SlotClass{{id: "default", arch: EC2ArchX86, slots: slots}}
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
		if arch != EC2ArchX86 && arch != EC2ArchArm {
			log.Printf("ecs-ec2: ignoring slot class %q: arch %q is not %s or %s", id, arch, EC2ArchX86, EC2ArchArm)
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

// subnetAZs maps the deployment's subnets (AF_ECS_SUBNETS and AF_ECS_WORKSPACE_SUBNETS) to
// their AZ, once. EBS volumes are AZ-bound, so nearly every placement decision starts here.
func (f *ecsEC2Factory) subnetAZs(ctx context.Context) (map[string]string, error) {
	f.subnetMu.Lock()
	defer f.subnetMu.Unlock()
	if f.subnetAZ != nil {
		return f.subnetAZ, nil
	}
	ids := append(append([]string(nil), f.base.cfg.subnets...), f.base.cfg.wsSubnets...)
	out, err := f.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: ids})
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

// LaunchBudget satisfies LaunchBudgeter. Both clocks start at Start: the claim keeps
// State() at `starting`, and backgroundWithin cancels the background launch at waitBudget.
func (e *ecsEC2Runtime) LaunchBudget() time.Duration {
	return max(e.pool.claimTTL, e.pool.waitBudget)
}

// Without it the CP's start deadline could stop a launch its background work then revives.
var _ LaunchBudgeter = (*ecsEC2Runtime)(nil)

// RunningTasks satisfies TaskCounter: the service is the base adapter's.
func (e *ecsEC2Runtime) RunningTasks(ctx context.Context) (int, error) {
	return e.base.RunningTasks(ctx)
}

var _ TaskCounter = (*ecsEC2Runtime)(nil)

func (e *ecsEC2Runtime) generation() *atomic.Int64 {
	v, _ := startGen.LoadOrStore(e.localKey(), &atomic.Int64{})
	return v.(*atomic.Int64)
}
