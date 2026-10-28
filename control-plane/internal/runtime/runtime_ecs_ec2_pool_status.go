package runtime

import (
	"context"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// --- pool status for the admin UI (docs/log/64 §64.18.6) ---

// ec2SlotView / ec2HomeView / EC2PoolStatus are what an operator needs to answer the three
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
	// (decision 20). It is shown rather than hidden because it keeps billing until somebody
	// terminates it, and because "the pool shrank by one" is not an explanation.
	Quarantined      bool   `json:"quarantined"`
	QuarantineReason string `json:"quarantine_reason"`
	// TemplateVersion is the launch template version the slot was launched from, and
	// TemplateOutdated whether that is older than $Latest (slotTemplateOutdated) — user data
	// is read only at launch, so an outdated slot runs the old one until it is replaced.
	TemplateVersion  string `json:"template_version,omitempty"`
	TemplateOutdated bool   `json:"template_outdated,omitempty"`
	// ReplaceReserved: its workspace's next Start moves to a new slot (#1473).
	ReplaceReserved   bool   `json:"replace_reserved,omitempty"`
	ReplaceReservedAt string `json:"replace_reserved_at,omitempty"`
	// StoppingTask is the ECS status of a task asked to stop on this slot that its agent has
	// not reported (STOPPING, …), only read for a slot whose agent is not connected. The
	// clean-up is to start the instance once so the agent reports; the screen says so and
	// nothing automates it.
	StoppingTask        string `json:"stopping_task,omitempty"`
	StoppingTaskMinutes int    `json:"stopping_task_minutes,omitempty"`
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
	// Backups (decision 17). Reported per home rather than as a pool total, because the
	// question an operator actually has is "would THIS person lose work if their AZ went
	// away", and an average cannot answer it. BackupAgeMinutes is -1 when there is none.
	Backups          int `json:"backups"`
	BackupAgeMinutes int `json:"backup_age_minutes"`
}

type EC2PoolStatus struct {
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
	// deployment's classes declare (docs/log/70 §70.6). The six scalars above are this
	// list's FIRST entry — the default class's architecture — kept so nothing that
	// reads them has to change on a deployment that has only one.
	Goldens []EC2GoldenView `json:"goldens,omitempty"`
	// SlotClasses names the declared classes for the pool screen. Absent when the
	// deployment declared a single unnamed ladder.
	SlotClasses []WorkspaceSlotClass `json:"slot_classes,omitempty"`
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
	Budget *PoolBudget `json:"budget,omitempty"`

	// TemplateLatest is the slot launch template's $Latest version number, "" when it could
	// not be read — in which case no slot is reported outdated.
	TemplateLatest string `json:"template_latest,omitempty"`
	// AutoReplaceOutdated is whether a slot below TemplateLatest is replaced at its workspace's
	// next Start without anyone reserving it (#1934; AF_ECS_EC2_AUTO_REPLACE_OUTDATED_SLOTS).
	AutoReplaceOutdated bool `json:"auto_replace_outdated,omitempty"`
}

// EC2GoldenView is one architecture's golden situation, including how far along a bake
// that is under way has got (docs/log/64 §64.30).
type EC2GoldenView struct {
	Arch       string `json:"arch"`
	SnapshotID string `json:"snapshot_id"`
	Image      string `json:"image"`
	Stale      bool   `json:"stale"`
	Baking     bool   `json:"baking"`
	Rejected   string `json:"rejected"`
	Reason     string `json:"reason"`

	// --- how far the bake has got (docs/log/64 §64.30) ---
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
	EC2BakePhaseIdle      = "idle"      // nothing under way; the next tick starts one
	EC2BakePhaseBlocked   = "blocked"   // a bake needs two free slots and cannot have them
	ec2BakePhaseRejected  = "rejected"  // the last candidate could not boot; one attempt left
	ec2BakePhaseGaveUp    = "gave_up"   // two candidates burned on this image — no more tries
	EC2BakePhaseOff       = "off"       // AF_ECS_EC2_GOLDEN_AUTOBAKE=0
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
func (f *ecsEC2Factory) PoolStatus(ctx context.Context) (EC2PoolStatus, error) {
	// One throwaway runtime as the library for the tag queries — the same trick the
	// sweeper uses.
	probe := f.probeRuntime()
	st := EC2PoolStatus{
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
			tagFilter(EC2TagPool, f.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
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
			Workspace:   ec2TagValue(v.Tags, EC2TagWorkspace),
			SizeGiB:     aws.ToInt32(v.Size),
			AZ:          aws.ToString(v.AvailabilityZone),
			AttachedTo:  attachedInstance(v),
			IdleMinutes: int(idle.Minutes()),
			Hibernating: ec2TagValue(v.Tags, EC2TagHibernating) != "",
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
	// see (decision 20).
	insts, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			{Name: aws.String("tag:" + EC2TagRole), Values: []string{ec2RoleSlot, ec2RoleQuarantined}},
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
	st.AutoReplaceOutdated = !f.pool.noAutoReplace
	lt, err := f.launchTemplateLatest(ctx)
	if err != nil {
		log.Printf("ecs-ec2 pool status: slot launch template unreadable: %v", err)
	} else {
		st.TemplateLatest = strconv.FormatInt(lt.latest, 10)
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
			if ec2TagValue(inst.Tags, EC2TagRole) == ec2RoleQuarantined {
				s.Quarantined = true
				s.QuarantineReason = ec2TagValue(inst.Tags, ec2TagQuarantineReason)
			}
			s.TemplateVersion, s.TemplateOutdated, _ = slotTemplateOutdated(inst.Tags, lt)
			s.ReplaceReservedAt = ec2TagValue(inst.Tags, ec2TagSlotReplace)
			s.ReplaceReserved = s.ReplaceReservedAt != ""
			st.Slots = append(st.Slots, s)
		}
	}
	// A task still STOPPING on a slot whose agent is not connected is what blocks the next
	// Start of its workspace (#2447); read only for those slots, so a healthy pool costs
	// nothing. Unreadable is silence, not an error: the screen opens when things are broken.
	var dark []string
	for _, s := range st.Slots {
		if !s.Registered {
			dark = append(dark, s.InstanceID)
		}
	}
	if len(dark) > 0 {
		stopping, err := probe.stoppingTasksOn(ctx, dark)
		if err != nil {
			log.Printf("ecs-ec2 pool status: tasks stopping on slots unreadable: %v", err)
		}
		for i := range st.Slots {
			if t, ok := stopping[st.Slots[i].InstanceID]; ok {
				st.Slots[i].StoppingTask = t.status
				if !t.since.IsZero() {
					st.Slots[i].StoppingTaskMinutes = int(now.Sub(t.since).Minutes())
				}
			}
		}
	}
	sort.SliceStable(st.Slots, func(i, j int) bool { return st.Slots[i].InstanceID < st.Slots[j].InstanceID })
	sort.SliceStable(st.Homes, func(i, j int) bool { return st.Homes[i].Workspace < st.Homes[j].Workspace })

	// Hibernation snapshots, matched back onto the homes they belong to, plus the golden.
	snaps, err := f.ec2.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
		Filters:  []ec2types.Filter{tagFilter(EC2TagPool, f.pool.pool)},
	})
	if err != nil {
		log.Printf("ecs-ec2 pool status: snapshots unreadable: %v", err)
		return st, nil
	}
	// One golden per architecture (docs/log/70 §70.6), so the screen answers "is every
	// class covered" rather than "is there a golden". byArch is keyed by the snapshot's
	// af-arch — absent means x86_64, the only kind that existed before classes.
	byArch := map[string]*EC2GoldenView{}
	candidateState := map[string]ec2types.SnapshotState{}
	// The screen has to agree with the Start path about what "for the running image"
	// means, or it reports a golden as stale that goldenSnapshot happily uses (and the
	// operator re-bakes for nothing). Same identity, same rule (goldenIdentity).
	imgID := f.imageIdentity(ctx)
	goldenMatched := map[string]bool{}
	golden := func(arch string) *EC2GoldenView {
		g, ok := byArch[arch]
		if !ok {
			g = &EC2GoldenView{Arch: arch}
			byArch[arch] = g
		}
		return g
	}
	for _, s := range snaps.Snapshots {
		arch := snapshotArch(s)
		switch ec2TagValue(s.Tags, EC2TagRole) {
		case EC2RoleGolden:
			// A matching golden wins over a stale one, so the screen shows what the Start
			// path would actually use — and shows the stale one only when that is all
			// there is, which is exactly when the operator needs to be told to re-bake.
			g := golden(arch)
			img := ec2TagValue(s.Tags, ec2TagImage)
			if match := imgID.matches(s.Tags); g.SnapshotID == "" || match {
				g.SnapshotID, g.Image = aws.ToString(s.SnapshotId), img
				goldenMatched[arch] = match
			}
		case EC2RoleGoldenCandidate:
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
		case EC2RoleGoldenRejected:
			if imgID.matches(s.Tags) {
				g := golden(arch)
				g.Rejected = aws.ToString(s.SnapshotId)
				g.Reason = ec2TagValue(s.Tags, EC2TagBakeReason)
				// The same count the baker gives up on (RejectedAttempts), derived from
				// the snapshots this call already read rather than asked for again.
				g.Attempts++
			}
		case ec2RoleBackup:
			ws := ec2TagValue(s.Tags, EC2TagWorkspace)
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
			ws := ec2TagValue(s.Tags, EC2TagWorkspace)
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
			continue // the same set BakeBlocked counts: af-role=slot, quarantine excluded
		}
		slotsInUse++
		slotRunning[s.InstanceID] = s.State == "running"
	}
	blocked, _ := bakeCapacityBlocked(slotsInUse, f.pool.maxSlots)
	for _, arch := range f.BakeArches() {
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

// orphanReplacementAfter is how old an unclaimed replacement slot must be before the sweeper
// retires it: twice the claim window, and never under ten minutes.
func (f *ecsEC2Factory) orphanReplacementAfter() time.Duration {
	d := 2 * f.pool.claimTTL
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}
