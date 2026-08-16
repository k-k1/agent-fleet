package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// TestECSEC2LiveLifecycle drives the real ecs-ec2 adapter against real AWS: one cold
// start (new volume + new slot), a hot swap to a second workspace on the same slot, and
// a return to the first with its home intact. The unit tests prove the decisions; this
// proves the parts no fake can: that ECS accepts a task definition pinned with
// `ec2InstanceId ==` while using awsvpc + Service Connect, that an EBS volume mounted by
// the CP over SSM really becomes the container's /home/dev, and that the EFS
// credentials access points still mount on the EC2 launch type (docs/64 §64.9 listed
// that last one as 未実測).
//
// Opt-in, because it creates billable resources and needs a substrate:
//
//	source ~/af-ec2c/state.env && go test -run TestECSEC2Live -v -timeout 75m ./...
//
// The substrate is ~/af-ec2c/setup.sh (which stands up the REAL deploy/aws/ecs/cfn/
// 40-ec2-pool.yaml), and ~/af-ec2c/teardown.sh removes everything. Run all three in one
// session and check that nothing is left — a forgotten slot bills by the hour.
func TestECSEC2LiveLifecycle(t *testing.T) {
	if os.Getenv("AF_ECS_EC2_LIVE") != "1" {
		t.Skip("set AF_ECS_EC2_LIVE=1 (and source the harness state.env) to run the live EC2 pool test")
	}
	ctx := context.Background()
	factory, err := newECSEC2Factory(&manager{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	f := factory.(*ecsEC2Factory)

	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(f.base.cfg.region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	live := &liveEC2{t: t, ctx: ctx, ec2: ec2.NewFromConfig(ac), ecs: ecs.NewFromConfig(ac), ssm: ssm.NewFromConfig(ac), cluster: f.base.cfg.cluster}

	// ECS refuses to recreate a service whose name was deleted moments ago
	// ("Create service is not idempotent"), so a re-run after a failed run needs fresh
	// names: AF_ECS_EC2_LIVE_SUFFIX=b.
	sfx := os.Getenv("AF_ECS_EC2_LIVE_SUFFIX")
	name1, name2 := "af-ec2c-u1"+sfx, "af-ec2c-u2"+sfx
	u1 := f.New(Workspace{ContainerName: name1, MembershipID: "m-u1", AgentToken: "tok-u1"}, "", nil).(*ecsEC2Runtime)
	u2 := f.New(Workspace{ContainerName: name2, MembershipID: "m-u2", AgentToken: "tok-u2"}, "", nil).(*ecsEC2Runtime)

	// --- 1. first start. Whether this is a真 cold start depends on what a previous run
	// left behind, so record it: a home that already exists skips CreateVolume + mkfs +
	// the first boot-install, and a warm pool skips the instance boot. ---
	pre, _ := u1.homeVolume(ctx)
	preSlots := live.poolSize(f)
	t.Logf("starting conditions: home volume exists=%v, pool slots=%d", pre != nil, preSlots)
	wasRunning := u1.State(ctx) == "running"
	t0 := time.Now()
	if err := u1.Start(ctx); err != nil {
		t.Fatalf("u1 Start: %v", err)
	}
	// Only meaningful when this Start actually launches something: a re-run against a
	// workspace that is already up answers `running` immediately, which is correct.
	if s := u1.State(ctx); !wasRunning && s != "starting" {
		t.Errorf("u1 State right after Start = %q, want starting (a cold start must not look stopped)", s)
	}
	live.waitState(u1, "running", 12*time.Minute)
	coldStart := time.Since(t0)
	t.Logf("MEASURED start #1 = %.1fs (home existed=%v, slots before=%d)", coldStart.Seconds(), pre != nil, preSlots)

	slot1 := live.slotOf(u1)
	t.Logf("u1 landed on slot %s", slot1)
	live.assertTaskPinnedTo(u1, slot1)
	live.assertHomeMounted(u1, slot1)

	// The container's /home/dev must BE the mounted volume: the entrypoint's writes show
	// up on the host under the slot's mount point.
	out := live.run(slot1, "ls -a /af-home/m-u1/dev | head -20; stat -c '%U %a' /af-home/m-u1/dev; mountpoint /af-home/m-u1")
	t.Logf("host view of u1 home:\n%s", out)
	if !strings.Contains(out, "is a mountpoint") {
		t.Errorf("u1 home is not a mountpoint on the slot:\n%s", out)
	}
	live.run(slot1, "echo af-ec2c-marker-u1 > /af-home/m-u1/dev/af-marker.txt; chown 1000:1000 /af-home/m-u1/dev/af-marker.txt")

	// --- 2. Stop drains the task but keeps the home where it is (lazy release). ---
	t1 := time.Now()
	if err := u1.Stop(ctx); err != nil {
		t.Fatalf("u1 Stop: %v", err)
	}
	live.waitTasksGone(u1, 5*time.Minute)
	t.Logf("MEASURED stop → task drained = %.1fs", time.Since(t1).Seconds())
	if s := u1.State(ctx); s != "stopped" {
		t.Errorf("u1 State after stop = %q, want stopped", s)
	}

	// --- 3. lazy release: Stop must leave the home on the slot (that attachment IS the
	// affinity), and the return must be the cheap path. ---
	if inst := live.slotOf(u1); inst != slot1 {
		t.Fatalf("Stop detached the home (now on %q); lazy release means it stays on %s", inst, slot1)
	}
	if ec2TagValue(live.volumeOf(u1).Tags, ec2TagIdleSince) == "" {
		t.Error("Stop did not record when the home went dormant")
	}
	t2 := time.Now()
	if err := u1.Start(ctx); err != nil {
		t.Fatalf("u1 warm return: %v", err)
	}
	live.waitState(u1, "running", 8*time.Minute)
	warmReturn := time.Since(t2)
	t.Logf("MEASURED warm return (home never left the slot) = %.1fs", warmReturn.Seconds())
	if inst := live.slotOf(u1); inst != slot1 {
		t.Errorf("came back on %s instead of its own slot %s", inst, slot1)
	}

	// --- 4. dormant: the sweeper stops the slot; the home stays attached and the owner
	// wakes it. This is the path that replaces "release and re-attach". ---
	if err := u1.Stop(ctx); err != nil {
		t.Fatalf("u1 Stop before sleeping: %v", err)
	}
	live.waitTasksGone(u1, 3*time.Minute)
	f.pool.slotSleepAfter = time.Second // do not wait 15 minutes in a test
	u1.pool.slotSleepAfter = time.Second
	live.eventually(2*time.Minute, "slot stopped", func() bool {
		if err := f.sweep(ctx); err != nil {
			t.Logf("sweep: %v", err)
		}
		running, err := u1.instanceRunning(ctx, slot1)
		return err == nil && !running
	})
	if inst := live.slotOf(u1); inst != slot1 {
		t.Errorf("the sweeper detached a dormant home (now %q); it should only stop the slot", inst)
	}
	t3 := time.Now()
	if err := u1.Start(ctx); err != nil {
		t.Fatalf("u1 wake: %v", err)
	}
	live.waitState(u1, "running", 10*time.Minute)
	wake := time.Since(t3)
	t.Logf("MEASURED wake of a dormant slot (StartInstances → task) = %.1fs", wake.Seconds())
	if got := live.run(slot1, "cat /af-home/m-u1/dev/af-marker.txt"); !strings.Contains(got, "af-ec2c-marker-u1") {
		t.Errorf("home did not survive the slot sleeping: %q", got)
	}

	// --- 5. eviction: at the cap, the longest-dormant occupant gives its slot up. ---
	if err := u1.Stop(ctx); err != nil {
		t.Fatalf("u1 Stop before eviction: %v", err)
	}
	live.waitTasksGone(u1, 3*time.Minute)
	// The pool may already hold slots from an earlier run, so "did it grow?" has to be
	// measured, not assumed to be 1. Hard-coding it made a warm re-run report a product
	// bug that was really leftover state.
	nBefore := live.poolSize(f)
	// Eviction only happens when there is NOWHERE else to go. Lowering the cap is not
	// enough: if a slot from an earlier run is sitting free, u2 takes it — correctly —
	// and nothing is evicted. Measure which world we are in instead of assuming.
	freeBefore, err := u2.freeSlots(ctx, "")
	if err != nil {
		t.Fatalf("list free slots: %v", err)
	}
	// Each runtime carries its OWN copy of the pool config (taken when it was built), so
	// lowering the cap on the factory alone changes nothing for u2 — it grew the pool
	// instead of reclaiming, and the test read that as a product bug.
	f.pool.maxSlots = 1
	u1.pool.maxSlots = 1
	u2.pool.maxSlots = 1
	t4 := time.Now()
	if err := u2.Start(ctx); err != nil {
		t.Fatalf("u2 Start (should evict u1): %v", err)
	}
	live.waitState(u2, "running", 10*time.Minute)
	t.Logf("MEASURED eviction + swap (u2 takes u1's slot at the cap) = %.1fs", time.Since(t4).Seconds())
	if len(freeBefore) == 0 {
		if inst := live.slotOf(u2); inst != slot1 {
			t.Errorf("u2 landed on %s, expected the reclaimed slot %s", inst, slot1)
		}
		if inst := live.slotOf(u1); inst != "" {
			t.Errorf("u1 is still attached to %s after being evicted", inst)
		}
	} else {
		// Not a pass for eviction — say so rather than letting a green run imply it.
		t.Logf("EVICTION NOT EXERCISED: %d free slot(s) were left over from an earlier run, "+
			"so u2 took one instead of reclaiming u1's. Start from an empty pool to cover it.", len(freeBefore))
		if inst := live.slotOf(u1); inst != slot1 {
			t.Errorf("u1 lost its slot (%q) although u2 had a free one to take", inst)
		}
	}
	if n := live.poolSize(f); n > nBefore {
		t.Errorf("pool grew to %d (from %d) instead of reclaiming at the cap", n, nBefore)
	}

	// --- 6. hibernation: u1's home (detached since the eviction) becomes a snapshot, the
	// volume goes, and the next Start rebuilds it. This is the only path in the product
	// that deletes a live home on its own, so the marker file is the whole assertion.
	// (ADR 0045 決定 4 / docs/64 §64.18.2.) ---
	vol1 := aws.ToString(live.volumeOf(u1).VolumeId)
	t5 := time.Now()
	live.eventually(20*time.Minute, "home hibernated (snapshot completed, volume deleted)", func() bool {
		if err := u1.hibernate(ctx); err != nil {
			t.Logf("hibernate step: %v", err)
			return false
		}
		v, err := u1.homeVolume(ctx)
		return err == nil && v == nil
	})
	t.Logf("MEASURED hibernate (%s → snapshot, volume deleted) = %.1fs", vol1, time.Since(t5).Seconds())
	snaps, err := u1.homeSnapshots(ctx)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("hibernation snapshots = %v (err %v), want exactly one", snaps, err)
	}
	if s := u1.State(ctx); s != "none" {
		t.Logf("State of a hibernated workspace = %q (no volume; Start restores it)", s)
	}
	// A restore has to land somewhere, and step 5 left the cap at 1 with u2 holding the
	// only slot. Stopping u2 makes it the longest-dormant occupant, so the restore comes
	// back through the eviction path rather than by growing the pool.
	if err := u2.Stop(ctx); err != nil {
		t.Fatalf("u2 Stop before the restore: %v", err)
	}
	live.waitTasksGone(u2, 3*time.Minute)
	t6 := time.Now()
	if err := u1.Start(ctx); err != nil {
		t.Fatalf("u1 Start after hibernation: %v", err)
	}
	live.waitState(u1, "running", 12*time.Minute)
	t.Logf("MEASURED restore from snapshot → running = %.1fs", time.Since(t6).Seconds())
	slotR := live.slotOf(u1)
	if got := live.run(slotR, "cat /af-home/m-u1/dev/af-marker.txt"); !strings.Contains(got, "af-ec2c-marker-u1") {
		t.Errorf("the home did not survive hibernation: %q", got)
	}
	if v := live.volumeOf(u1); aws.ToString(v.SnapshotId) == "" {
		t.Error("the restored home was built empty rather than from the snapshot")
	}

	// --- 7. golden snapshot: bake one by RUNNING deploy/aws/ecs/bake-golden.sh, then boot
	// a brand-new user on the home it seeds. Earlier rounds only made the same AWS calls
	// from Go, which left the operator-facing script itself unproven and stopped at "the
	// volume came from the right snapshot" — never at "a task starts on it". (ADR 0045
	// 決定 9 / docs/64 §64.19.4.)
	//
	// The hibernation snapshot from step 6 cannot be reused as the golden: a successful
	// restore deletes it, on purpose — otherwise the user pays for both the volume and a
	// stale copy of it. (Measured: this test used to reuse it and got InvalidSnapshot.NotFound.)
	if err := u1.Stop(ctx); err != nil {
		t.Fatalf("u1 Stop before baking the golden: %v", err)
	}
	live.waitTasksGone(u1, 3*time.Minute)
	if err := u1.releaseSlot(ctx); err != nil {
		t.Fatalf("release u1's slot before baking: %v", err)
	}
	script, err := filepath.Abs("../deploy/aws/ecs/bake-golden.sh")
	if err != nil {
		t.Fatalf("locate bake-golden.sh: %v", err)
	}
	bake := exec.CommandContext(ctx, "bash", script,
		"--workspace", u1.Name(), "--image", f.base.cfg.workspaceImage, "--pool", f.pool.pool)
	bake.Env = os.Environ()
	bakeOut, err := bake.CombinedOutput()
	t.Logf("bake-golden.sh:\n%s", bakeOut)
	if err != nil {
		t.Fatalf("bake-golden.sh: %v", err)
	}
	// Read the result back the way the CP does rather than parsing the script's output:
	// what matters is that the tags it wrote are the ones goldenSnapshot() looks for.
	golden := u1.goldenSnapshot(ctx)
	if golden == "" {
		t.Fatalf("bake-golden.sh finished but the CP does not see a usable golden for %s / %s",
			f.pool.pool, f.base.cfg.workspaceImage)
	}
	t.Logf("the CP picked up the baked golden %s", golden)
	// The golden belongs to the pool, not to u1 — Destroy below must leave it alone, and
	// the harness teardown removes it.
	defer func() {
		if _, err := f.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(golden)}); err != nil {
			t.Logf("cleaning up the golden snapshot %s: %v", golden, err)
		}
	}()

	// A brand-new user, started for real. u2 still holds the only slot at the cap set in
	// step 5, so raise it by one rather than evicting — this measures a new user's first
	// start, and an eviction in the middle of it would measure something else.
	f.pool.maxSlots = 2
	u1.pool.maxSlots, u2.pool.maxSlots = 2, 2
	u3 := f.New(Workspace{ContainerName: "af-ec2c-u3" + sfx, MembershipID: "m-u3", AgentToken: "tok-u3"}, "", nil).(*ecsEC2Runtime)
	u3.pool.maxSlots = 2
	t7 := time.Now()
	if err := u3.Start(ctx); err != nil {
		t.Fatalf("u3 (a brand-new user seeded from the golden) Start: %v", err)
	}
	live.waitState(u3, "running", 12*time.Minute)
	t.Logf("MEASURED first start of a NEW user seeded from the golden = %.1fs (compare start #1 = %.1fs, which built an empty home)",
		time.Since(t7).Seconds(), coldStart.Seconds())
	if src := live.snapshotOfVolume(aws.ToString(live.volumeOf(u3).VolumeId)); src != golden {
		t.Errorf("the new user's home was built from %q, want the golden snapshot %s", src, golden)
	}
	// The seed really arrived: u1's marker was written into the volume this golden was
	// taken from, so it has to be in a brand-new user's home — and readable from the task's
	// slot, not just present on a volume nobody mounted.
	slot3 := live.slotOf(u3)
	if got := live.run(slot3, "cat /af-home/m-u3/dev/af-marker.txt; ls /af-home/m-u3/dev/.local/bin 2>&1 | head -20"); !strings.Contains(got, "af-ec2c-marker-u1") {
		t.Errorf("a home seeded from the golden did not carry the seed's contents: %q", got)
	} else {
		t.Logf("host view of the golden-seeded home:\n%s", got)
	}

	// --- 8. leave nothing running; the volumes are deleted by Destroy. u1 was already
	// stopped and released in step 7. ---
	for _, rt := range []*ecsEC2Runtime{u2, u3} {
		if err := rt.Stop(ctx); err != nil {
			t.Errorf("final stop %s: %v", rt.Name(), err)
		}
		live.waitTasksGone(rt, 3*time.Minute)
	}
	for _, rt := range []*ecsEC2Runtime{u1, u2, u3} {
		leftovers, err := rt.Destroy(ctx)
		if err != nil {
			t.Errorf("Destroy %s: %v", rt.Name(), err)
		}
		// Destroy now folds the Fargate-side resources too (ADR 0045 決定 13-3): the
		// service, both EFS access points and both SSM parameters. Those are exactly the
		// things that survived every earlier run and had to be swept up by teardown.sh.
		// ECS and EFS both delete asynchronously — DescribeServices keeps returning the
		// service (DRAINING, then INACTIVE) and DescribeAccessPoints keeps listing the
		// access points (deleting) for a while after the call returns. Poll, or the
		// assertion measures the API's bookkeeping rather than the teardown (measured:
		// it reported a leak while everything was already on its way out).
		rt := rt
		live.eventually(3*time.Minute, "the workspace's cloud resources are gone", func() bool {
			_, ok, err := rt.base.describeService(ctx)
			return err == nil && !ok && live.accessPointsOf(rt) == 0 && live.paramsOf(rt) == 0
		})
		// The EFS directories are the one thing Destroy cannot remove (docs/64
		// §64.18.4). Report them so the harness' teardown check stays honest about
		// what is left on the filesystem.
		if len(leftovers) > 0 {
			t.Logf("Destroy %s left behind (expected, EFS dirs need a mount): %v", rt.Name(), leftovers)
		}
	}
	t.Logf("SUMMARY start#1=%.1fs warmReturn=%.1fs wake=%.1fs", coldStart.Seconds(), warmReturn.Seconds(), wake.Seconds())
}

type liveEC2 struct {
	t       *testing.T
	ctx     context.Context
	ec2     *ec2.Client
	ecs     *ecs.Client
	ssm     *ssm.Client
	cluster string
}

func (l *liveEC2) waitState(rt *ecsEC2Runtime, want string, budget time.Duration) {
	l.t.Helper()
	deadline := time.Now().Add(budget)
	last := ""
	for time.Now().Before(deadline) {
		if s := rt.State(l.ctx); s != last {
			l.t.Logf("%s state=%s (+%.0fs)", rt.Name(), s, budget.Seconds()-time.Until(deadline).Seconds())
			last = s
		}
		if last == want {
			return
		}
		time.Sleep(3 * time.Second)
	}
	l.t.Fatalf("%s never reached %q (stuck at %q)", rt.Name(), want, last)
}

// eventually polls until cond() holds, failing the test if it never does.
func (l *liveEC2) eventually(budget time.Duration, what string, cond func() bool) {
	l.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Second)
	}
	l.t.Fatalf("timed out waiting for %s", what)
}

// waitTasksGone waits until the service has drained. With lazy release there is no
// detach to watch for any more, so this is what "stopped" looks like from outside.
func (l *liveEC2) waitTasksGone(rt *ecsEC2Runtime, budget time.Duration) {
	l.t.Helper()
	l.eventually(budget, rt.Name()+" tasks gone", func() bool {
		s, ok, err := rt.base.describeService(l.ctx)
		return err == nil && (!ok || (s.RunningCount == 0 && s.PendingCount == 0))
	})
}

func (l *liveEC2) volumeOf(rt *ecsEC2Runtime) *ec2types.Volume {
	l.t.Helper()
	vol, err := rt.homeVolume(l.ctx)
	if err != nil || vol == nil {
		l.t.Fatalf("home volume of %s: %v", rt.Name(), err)
	}
	return vol
}

func (l *liveEC2) slotOf(rt *ecsEC2Runtime) string {
	l.t.Helper()
	vol, err := rt.homeVolume(l.ctx)
	if err != nil || vol == nil {
		l.t.Fatalf("home volume of %s: %v", rt.Name(), err)
	}
	return attachedInstance(vol)
}

// assertTaskPinnedTo checks the running task is on the slot we attached the volume to —
// i.e. that the `ec2InstanceId ==` placement constraint on the TASK DEFINITION really
// steers placement — and that Service Connect attached on the EC2 launch type.
func (l *liveEC2) assertTaskPinnedTo(rt *ecsEC2Runtime, instanceID string) {
	l.t.Helper()
	list, err := l.ecs.ListTasks(l.ctx, &ecs.ListTasksInput{Cluster: aws.String(l.cluster), ServiceName: aws.String(rt.Name())})
	if err != nil || len(list.TaskArns) == 0 {
		l.t.Fatalf("list tasks of %s: %v", rt.Name(), err)
	}
	tasks, err := l.ecs.DescribeTasks(l.ctx, &ecs.DescribeTasksInput{Cluster: aws.String(l.cluster), Tasks: list.TaskArns})
	if err != nil || len(tasks.Tasks) == 0 {
		l.t.Fatalf("describe tasks of %s: %v", rt.Name(), err)
	}
	task := tasks.Tasks[0]
	ci, err := l.ecs.DescribeContainerInstances(l.ctx, &ecs.DescribeContainerInstancesInput{
		Cluster: aws.String(l.cluster), ContainerInstances: []string{aws.ToString(task.ContainerInstanceArn)},
	})
	if err != nil || len(ci.ContainerInstances) == 0 {
		l.t.Fatalf("describe container instance: %v", err)
	}
	if got := aws.ToString(ci.ContainerInstances[0].Ec2InstanceId); got != instanceID {
		l.t.Errorf("task placed on %s but the home is attached to %s — the placement constraint did not hold", got, instanceID)
	}
	sc := false
	for _, a := range task.Attachments {
		if strings.Contains(strings.ToLower(aws.ToString(a.Type)), "serviceconnect") {
			sc = true
			l.t.Logf("service connect attachment status=%s", aws.ToString(a.Status))
		}
	}
	if !sc {
		l.t.Errorf("no Service Connect attachment on the task; Endpoint() would not resolve")
	}
}

func (l *liveEC2) assertHomeMounted(rt *ecsEC2Runtime, instanceID string) {
	l.t.Helper()
	vol, _ := rt.homeVolume(l.ctx)
	att := attachment(vol)
	if att == nil || aws.ToString(att.Device) != ec2HomeDevice {
		l.t.Fatalf("home of %s is not attached at %s: %+v", rt.Name(), ec2HomeDevice, att)
	}
	if aws.ToString(att.InstanceId) != instanceID {
		l.t.Fatalf("home attached to %s, task slot is %s", aws.ToString(att.InstanceId), instanceID)
	}
}

func (l *liveEC2) poolSize(f *ecsEC2Factory) int {
	l.t.Helper()
	out, err := l.ec2.DescribeInstances(l.ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + ec2TagPool), Values: []string{f.pool.pool}},
			{Name: aws.String("tag:" + ec2TagRole), Values: []string{ec2RoleSlot}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		l.t.Fatalf("pool size: %v", err)
	}
	n := 0
	for _, r := range out.Reservations {
		n += len(r.Instances)
	}
	return n
}

// run executes a shell command on a slot through SSM and returns its output, so the
// test can look at the host's view of the mount (the one thing the adapter's own SSM
// helper deliberately does not expose).
func (l *liveEC2) run(instanceID, command string) string {
	l.t.Helper()
	var cmdID string
	for i := 0; i < 20; i++ {
		out, err := l.ssm.SendCommand(l.ctx, &ssm.SendCommandInput{
			DocumentName: aws.String("AWS-RunShellScript"),
			InstanceIds:  []string{instanceID},
			Parameters:   map[string][]string{"commands": {command}},
		})
		if err == nil {
			cmdID = aws.ToString(out.Command.CommandId)
			break
		}
		time.Sleep(3 * time.Second)
	}
	if cmdID == "" {
		l.t.Fatalf("ssm send to %s failed", instanceID)
	}
	for i := 0; i < 40; i++ {
		time.Sleep(2 * time.Second)
		inv, err := l.ssm.GetCommandInvocation(l.ctx, &ssm.GetCommandInvocationInput{
			CommandId: aws.String(cmdID), InstanceId: aws.String(instanceID),
		})
		if err != nil {
			continue
		}
		switch inv.Status {
		case "Success", "Failed":
			return fmt.Sprintf("%s%s", aws.ToString(inv.StandardOutputContent), aws.ToString(inv.StandardErrorContent))
		}
	}
	l.t.Fatalf("ssm command %q on %s never finished", command, instanceID)
	return ""
}

// azOf is the AZ a workspace's home lives in — an EBS volume never leaves it, so a new
// volume created for a comparison has to be made there too.
func (l *liveEC2) azOf(rt *ecsEC2Runtime) string {
	v := l.volumeOf(rt)
	if v == nil {
		return ""
	}
	return aws.ToString(v.AvailabilityZone)
}

// waitSnapshot blocks until a snapshot completes. A near-empty 8 GiB home took ~60s
// measured; a real 45 GiB one is 30–40 minutes, which is exactly why the product advances
// hibernation one sweep at a time instead of waiting like this.
func (l *liveEC2) waitSnapshot(id string, budget time.Duration) {
	l.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		out, err := l.ec2.DescribeSnapshots(l.ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{id}})
		if err == nil && len(out.Snapshots) == 1 {
			switch out.Snapshots[0].State {
			case ec2types.SnapshotStateCompleted:
				return
			case ec2types.SnapshotStateError:
				l.t.Fatalf("snapshot %s failed", id)
			}
		}
		time.Sleep(10 * time.Second)
	}
	l.t.Fatalf("snapshot %s did not complete within %s", id, budget)
}

func (l *liveEC2) snapshotOfVolume(volumeID string) string {
	out, err := l.ec2.DescribeVolumes(l.ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	if err != nil || len(out.Volumes) == 0 {
		l.t.Logf("describe %s: %v", volumeID, err)
		return ""
	}
	return aws.ToString(out.Volumes[0].SnapshotId)
}

// accessPointsOf / paramsOf count the per-membership resources on the Fargate side, so
// the Destroy assertions can say "nothing is left" about the things no unit test can see.
func (l *liveEC2) accessPointsOf(rt *ecsEC2Runtime) int {
	out, err := rt.base.efs.DescribeAccessPoints(l.ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(rt.base.cfg.efsFileSystem),
	})
	if err != nil {
		l.t.Logf("describe access points: %v", err)
		return 0
	}
	n := 0
	for _, ap := range out.AccessPoints {
		// EFS deletes asynchronously: an access point sits in `deleting` for a while and
		// is still listed. Counting those made a complete teardown look like a leak
		// (measured — this assertion failed while the resources were in fact going away).
		if st := string(ap.LifeCycleState); st == "deleting" || st == "deleted" {
			continue
		}
		if tagValue(ap.Tags, "af-membership") == rt.base.membershipID {
			n++
		}
	}
	return n
}

func (l *liveEC2) paramsOf(rt *ecsEC2Runtime) int {
	n := 0
	for _, suffix := range []string{"agent-token", "secret-key"} {
		name := "/af-ws/" + rt.base.name + "/" + suffix
		if _, err := l.ssm.GetParameter(l.ctx, &ssm.GetParameterInput{Name: aws.String(name)}); err == nil {
			n++
		}
	}
	return n
}

// TestECSEC2LiveScale is the second live test: everything the lifecycle test does with one
// user at a time, done with several at once and left running past the timers. Three things
// had never been exercised before it (docs/64 §64.20):
//
//	 - concurrent Starts. Slot allocation is a race by design — it is decided by AWS
//	   accepting exactly one AttachVolume on a fixed device name — and one user at a time
//	   never puts two Starts on the same free slot.
//	 - a second AZ. An EBS home is pinned to its AZ, so "this user's home is in 1c" has to
//	   produce a slot in 1c; with a single subnet configured that path is dead code.
//	 - the sweep loop actually looping, over several users, for longer than its own timers.
//	   Every earlier run called sweep() by hand a handful of times.
//
// Opt-in on top of the lifecycle test's own gate, because it holds four instances at once
// and runs for well over half an hour:
//
//	AF_ECS_EC2_LIVE=1 AF_ECS_EC2_LIVE_SCALE=1 go test -run TestECSEC2LiveScale -v -timeout 90m .
func TestECSEC2LiveScale(t *testing.T) {
	if os.Getenv("AF_ECS_EC2_LIVE") != "1" || os.Getenv("AF_ECS_EC2_LIVE_SCALE") != "1" {
		t.Skip("set AF_ECS_EC2_LIVE=1 AF_ECS_EC2_LIVE_SCALE=1 (and source the harness state.env) for the multi-user soak")
	}
	ctx := context.Background()
	factory, err := newECSEC2Factory(&manager{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	f := factory.(*ecsEC2Factory)
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(f.base.cfg.region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	live := &liveEC2{t: t, ctx: ctx, ec2: ec2.NewFromConfig(ac), ecs: ecs.NewFromConfig(ac), ssm: ssm.NewFromConfig(ac), cluster: f.base.cfg.cluster}

	if n := live.poolSize(f); n != 0 {
		t.Fatalf("the pool already holds %d slot(s). Start from an empty pool, or this measures "+
			"how an earlier run's leftovers get reused rather than how %d users are placed", n, 3)
	}
	if f.pool.maxSlots < 4 {
		t.Fatalf("AF_ECS_EC2_MAX_SLOTS=%d; this test needs 4 (three users plus one in the second AZ)", f.pool.maxSlots)
	}
	sfx := os.Getenv("AF_ECS_EC2_LIVE_SUFFIX")
	newUser := func(n string) *ecsEC2Runtime {
		return f.New(Workspace{ContainerName: "af-ec2c-" + n + sfx, MembershipID: "m-" + n, AgentToken: "tok-" + n}, "", nil).(*ecsEC2Runtime)
	}

	// --- 1. three users start AT THE SAME TIME. The interesting outcome is not that they
	// come up but that they come up on three DIFFERENT slots: the allocation has no lock,
	// it relies on AttachVolume being the arbiter (docs/64 §64.15.2). ---
	users := []*ecsEC2Runtime{newUser("s1"), newUser("s2"), newUser("s3")}
	var wg sync.WaitGroup
	errs := make([]error, len(users))
	t0 := time.Now()
	for i, u := range users {
		wg.Add(1)
		go func(i int, u *ecsEC2Runtime) {
			defer wg.Done()
			errs[i] = u.Start(ctx)
		}(i, u)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s Start: %v", users[i].Name(), err)
		}
	}
	for _, u := range users {
		live.waitState(u, "running", 15*time.Minute)
	}
	t.Logf("MEASURED three concurrent cold starts, all running = %.1fs", time.Since(t0).Seconds())

	slots := map[string]string{}
	azs := map[string]string{}
	for _, u := range users {
		s := live.slotOf(u)
		if s == "" {
			t.Fatalf("%s is running with no slot", u.Name())
		}
		if other, dup := slots[s]; dup {
			t.Fatalf("%s and %s are both on slot %s — two homes on one box is the thing "+
				"the fixed device name is supposed to make impossible", other, u.Name(), s)
		}
		slots[s] = u.Name()
		live.assertTaskPinnedTo(u, s)
		live.assertHomeMounted(u, s)
		azs[u.Name()] = live.azOf(u)
		live.run(s, fmt.Sprintf("echo marker-%s > /af-home/%s/dev/af-marker.txt", u.base.membershipID, u.base.membershipID))
	}
	if n := live.poolSize(f); n != 3 {
		t.Errorf("pool holds %d slots for 3 users, want exactly 3", n)
	}
	// Say what the AZ numbers mean instead of letting three-in-one-AZ read as a failure or
	// three-across-AZs read as a feature: a NEW home follows anyAZ(), which is the
	// lowest-sorted configured subnet, deterministically. There is no spreading.
	t.Logf("AZs of the three new homes: %v (all the same is EXPECTED — a new home takes the "+
		"first configured subnet's AZ; the adapter does not spread)", azs)

	// --- 2. the second AZ. An EBS volume never leaves its AZ, so a user whose home is
	// there must get a slot there — including growing one, since every existing slot is in
	// the other AZ and offering one would fail the attach. ---
	subnetB := os.Getenv("AF_HARNESS_SUBNET_B")
	var u4 *ecsEC2Runtime
	if subnetB == "" {
		t.Log("AZ COVERAGE SKIPPED: AF_HARNESS_SUBNET_B is unset, so only one AZ is configured. " +
			"Re-run against a harness built with two private subnets to cover it.")
	} else {
		azB := live.azOfSubnet(subnetB)
		if azB == "" {
			t.Fatalf("cannot resolve the AZ of %s", subnetB)
		}
		if azB == azs[users[0].Name()] {
			t.Fatalf("AF_HARNESS_SUBNET_B (%s) is in %s, the same AZ as the other homes", subnetB, azB)
		}
		u4 = newUser("s4")
		if _, err := u4.createHomeVolume(ctx, azB); err != nil {
			t.Fatalf("create a home in %s: %v", azB, err)
		}
		t4 := time.Now()
		if err := u4.Start(ctx); err != nil {
			t.Fatalf("s4 (home in %s) Start: %v", azB, err)
		}
		live.waitState(u4, "running", 15*time.Minute)
		t.Logf("MEASURED first start of a user whose home is in the second AZ = %.1fs", time.Since(t4).Seconds())
		slot4 := live.slotOf(u4)
		if _, reused := slots[slot4]; reused {
			t.Fatalf("s4 landed on %s, a slot in the other AZ — its home could not have attached there", slot4)
		}
		if got := live.instanceAZ(slot4); got != azB {
			t.Errorf("s4's slot is in %s, its home is in %s", got, azB)
		}
		live.assertTaskPinnedTo(u4, slot4)
		live.assertHomeMounted(u4, slot4)
		live.run(slot4, "echo marker-m-s4 > /af-home/m-s4/dev/af-marker.txt")
		slots[slot4] = u4.Name()
	}

	// --- 3. the soak. Two users go away, one stays. The sweep LOOP (not a hand-called
	// sweep) then runs for longer than its own timers, over every home in the pool. What is
	// being watched for is the quiet kind of breakage: a live workspace disturbed, a slot
	// leaked, a home detached — or a hibernation starting on its own, which is exactly what
	// the sweeper stopped being allowed to do (ADR 0045 決定 14). ---
	for _, u := range users[1:] {
		if err := u.Stop(ctx); err != nil {
			t.Fatalf("%s Stop before the soak: %v", u.Name(), err)
		}
		live.waitTasksGone(u, 5*time.Minute)
	}
	f.pool.slotSleepAfter = 2 * time.Minute
	f.pool.sweepEvery = 45 * time.Second
	f.pool.hibernateAfter = time.Minute // a trap: the LOOP must still never start one
	// A second loop on purpose: newECSEC2Factory already started one, but it took its
	// interval from AF_ECS_EC2_SWEEP_SEC when the ticker was created (an hour in the
	// harness), so it would fire once at most in this window. Both read the same f.pool.
	soakCtx, stopSoak := context.WithCancel(ctx)
	go f.sweepLoop(soakCtx)
	soak := 18 * time.Minute
	t.Logf("soaking for %s with the sweep loop running every %s (slot sleep %s)", soak, f.pool.sweepEvery, f.pool.slotSleepAfter)
	deadline := time.Now().Add(soak)
	asleep := map[string]bool{}
	for time.Now().Before(deadline) {
		time.Sleep(90 * time.Second)
		if s := users[0].State(ctx); s != "running" {
			stopSoak()
			t.Fatalf("the sweeper disturbed the one workspace that was still in use: state=%q", s)
		}
		if n := live.poolSize(f); n != len(slots) {
			stopSoak()
			t.Fatalf("pool size drifted to %d, want %d", n, len(slots))
		}
		for _, u := range users[1:] {
			vol, err := u.homeVolume(ctx)
			if err != nil || vol == nil {
				stopSoak()
				t.Fatalf("%s lost its home volume during the soak (err %v) — the sweep loop "+
					"started a hibernation, which is no longer its job", u.Name(), err)
			}
			if inst := attachedInstance(vol); inst == "" {
				stopSoak()
				t.Fatalf("%s's home was detached from its slot; lazy release means it stays", u.Name())
			} else if live.instanceState(inst) == "stopped" {
				asleep[inst] = true
			}
		}
		t.Logf("soak +%.0fm: pool=%d asleep=%d", soak.Minutes()-time.Until(deadline).Minutes(), live.poolSize(f), len(asleep))
	}
	stopSoak()
	if len(asleep) < len(users)-1 {
		t.Errorf("only %d of the %d dormant slots were ever put to sleep during %s", len(asleep), len(users)-1, soak)
	}

	// The point of sleeping rather than releasing: the owner comes back to the same box,
	// with the same home on it.
	t6 := time.Now()
	if err := users[1].Start(ctx); err != nil {
		t.Fatalf("waking %s after the soak: %v", users[1].Name(), err)
	}
	live.waitState(users[1], "running", 12*time.Minute)
	t.Logf("MEASURED wake after an 18-minute soak = %.1fs", time.Since(t6).Seconds())
	if got := live.run(live.slotOf(users[1]), "cat /af-home/m-s2/dev/af-marker.txt"); !strings.Contains(got, "marker-m-s2") {
		t.Errorf("the home did not survive the soak: %q", got)
	}

	// --- 4. clean up: nothing may outlive this test. ---
	all := append([]*ecsEC2Runtime{}, users...)
	if u4 != nil {
		all = append(all, u4)
	}
	for _, u := range all {
		if err := u.Stop(ctx); err != nil {
			t.Errorf("final stop %s: %v", u.Name(), err)
		}
		live.waitTasksGone(u, 5*time.Minute)
	}
	for _, u := range all {
		leftovers, err := u.Destroy(ctx)
		if err != nil {
			t.Errorf("Destroy %s: %v", u.Name(), err)
		}
		u := u
		live.eventually(5*time.Minute, u.Name()+"'s cloud resources are gone", func() bool {
			_, ok, err := u.base.describeService(ctx)
			return err == nil && !ok && live.accessPointsOf(u) == 0 && live.paramsOf(u) == 0
		})
		if len(leftovers) > 0 {
			t.Logf("Destroy %s left behind (expected, EFS dirs need a mount): %v", u.Name(), leftovers)
		}
	}
	names := make([]string, 0, len(slots))
	for s := range slots {
		names = append(names, s)
	}
	sort.Strings(names)
	t.Logf("SUMMARY slots used: %v (the harness teardown terminates them)", names)
}

func (l *liveEC2) azOfSubnet(subnetID string) string {
	out, err := l.ec2.DescribeSubnets(l.ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{subnetID}})
	if err != nil || len(out.Subnets) == 0 {
		l.t.Logf("describe subnet %s: %v", subnetID, err)
		return ""
	}
	return aws.ToString(out.Subnets[0].AvailabilityZone)
}

func (l *liveEC2) instanceAZ(instanceID string) string {
	i := l.instance(instanceID)
	if i == nil || i.Placement == nil {
		return ""
	}
	return aws.ToString(i.Placement.AvailabilityZone)
}

func (l *liveEC2) instanceState(instanceID string) string {
	i := l.instance(instanceID)
	if i == nil || i.State == nil {
		return ""
	}
	return string(i.State.Name)
}

func (l *liveEC2) instance(instanceID string) *ec2types.Instance {
	out, err := l.ec2.DescribeInstances(l.ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil || len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		l.t.Logf("describe instance %s: %v", instanceID, err)
		return nil
	}
	return &out.Reservations[0].Instances[0]
}
