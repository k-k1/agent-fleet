package main

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	t0 := time.Now()
	if err := u1.Start(ctx); err != nil {
		t.Fatalf("u1 Start: %v", err)
	}
	if s := u1.State(ctx); s != "starting" {
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
	if inst := live.slotOf(u2); inst != slot1 {
		t.Errorf("u2 landed on %s, expected the reclaimed slot %s", inst, slot1)
	}
	if inst := live.slotOf(u1); inst != "" {
		t.Errorf("u1 is still attached to %s after being evicted", inst)
	}
	if n := live.poolSize(f); n != 1 {
		t.Errorf("pool grew to %d instead of reclaiming at the cap", n)
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
	golden := aws.ToString(snaps[0].SnapshotId) // reused below as the golden source
	if s := u1.State(ctx); s != "none" {
		t.Logf("State of a hibernated workspace = %q (no volume; Start restores it)", s)
	}
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

	// --- 7. golden snapshot: tag a snapshot the way bake-golden.sh does and check that a
	// BRAND-NEW user's home is created from it. Only the volume is created here (no boot)
	// — what needs proving on real AWS is the tag lookup and the image match, not another
	// task launch. (ADR 0045 決定 9.) ---
	if _, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{golden},
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleGolden)},
			{Key: aws.String(ec2TagPool), Value: aws.String(f.pool.pool)},
			{Key: aws.String(ec2TagImage), Value: aws.String(f.base.cfg.workspaceImage)},
			{Key: aws.String(ec2TagMembership), Value: aws.String("")},
		},
	}); err != nil {
		t.Fatalf("tag %s as golden: %v", golden, err)
	}
	u3 := f.New(Workspace{ContainerName: "af-ec2c-u3" + sfx, MembershipID: "m-u3", AgentToken: "tok-u3"}, "", nil).(*ecsEC2Runtime)
	newHome, err := u3.createHomeVolume(ctx, live.azOf(u1))
	if err != nil {
		t.Fatalf("createHomeVolume for a new user: %v", err)
	}
	newID := aws.ToString(newHome.VolumeId)
	if src := live.snapshotOfVolume(newID); src != golden {
		t.Errorf("a new user's home was built from %q, want the golden snapshot %s", src, golden)
	} else {
		t.Logf("a new user's home came from the golden snapshot %s", golden)
	}
	if _, err := f.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(newID)}); err != nil {
		t.Errorf("cleaning up the new user's volume %s: %v", newID, err)
	}
	// The golden belongs to the pool, not to u1 — Destroy below must leave it alone, and
	// the harness teardown removes it.
	defer func() {
		if _, err := f.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(golden)}); err != nil {
			t.Logf("cleaning up the golden snapshot %s: %v", golden, err)
		}
	}()

	// --- 8. leave nothing running; the volumes are deleted by Destroy. ---
	if err := u1.Stop(ctx); err != nil {
		t.Errorf("u1 final stop: %v", err)
	}
	live.waitTasksGone(u1, 3*time.Minute)
	if err := u2.Stop(ctx); err != nil {
		t.Errorf("final stop: %v", err)
	}
	live.waitTasksGone(u2, 3*time.Minute)
	for _, rt := range []*ecsEC2Runtime{u1, u2} {
		leftovers, err := rt.Destroy(ctx)
		if err != nil {
			t.Errorf("Destroy %s: %v", rt.Name(), err)
		}
		// Destroy now folds the Fargate-side resources too (ADR 0045 決定 13-3): the
		// service, both EFS access points and both SSM parameters. Those are exactly the
		// things that survived every earlier run and had to be swept up by teardown.sh.
		if _, ok, err := rt.base.describeService(ctx); err == nil && ok {
			t.Errorf("Destroy left the ECS service %s behind", rt.Name())
		}
		if n := live.accessPointsOf(rt); n != 0 {
			t.Errorf("Destroy left %d EFS access points behind for %s", n, rt.Name())
		}
		if n := live.paramsOf(rt); n != 0 {
			t.Errorf("Destroy left %d SSM parameters behind for %s", n, rt.Name())
		}
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
