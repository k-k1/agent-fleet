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
//	source ~/af-ec2c/state.env && go test -run TestECSEC2Live -v -timeout 40m ./...
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

	// --- 2. Stop releases the slot: task gone, umounted, detached. ---
	t1 := time.Now()
	if err := u1.Stop(ctx); err != nil {
		t.Fatalf("u1 Stop: %v", err)
	}
	live.waitVolumeDetached(u1, 5*time.Minute)
	t.Logf("MEASURED stop → slot released (task drain + umount + detach) = %.1fs", time.Since(t1).Seconds())
	if s := u1.State(ctx); s != "stopped" {
		t.Errorf("u1 State after release = %q, want stopped", s)
	}
	if out := live.run(slot1, "mountpoint /af-home/m-u1 || true; lsblk -o NAME,SERIAL | tail -5"); strings.Contains(out, "is a mountpoint") {
		t.Errorf("slot still has u1's home mounted after release:\n%s", out)
	}

	// --- 3. hot swap: a different user takes the slot that was just handed back. ---
	t2 := time.Now()
	if err := u2.Start(ctx); err != nil {
		t.Fatalf("u2 Start: %v", err)
	}
	live.waitState(u2, "running", 8*time.Minute)
	swap := time.Since(t2)
	t.Logf("MEASURED hot swap (Start u2 → task RUNNING on a warm slot) = %.1fs", swap.Seconds())
	slot2 := live.slotOf(u2)
	if slot2 != slot1 {
		t.Errorf("u2 landed on %s, expected the freed hot slot %s (a new instance means the swap did not happen)", slot2, slot1)
	}
	if n := live.poolSize(f); n != 1 {
		t.Errorf("pool grew to %d instances; the whole point is reusing the slot", n)
	}

	// --- 4. u1 comes back: its home must still be its home. ---
	if err := u2.Stop(ctx); err != nil {
		t.Fatalf("u2 Stop: %v", err)
	}
	live.waitVolumeDetached(u2, 5*time.Minute)
	t3 := time.Now()
	if err := u1.Start(ctx); err != nil {
		t.Fatalf("u1 restart: %v", err)
	}
	live.waitState(u1, "running", 8*time.Minute)
	t.Logf("MEASURED return of u1 (swap back) = %.1fs", time.Since(t3).Seconds())
	if got := live.run(live.slotOf(u1), "cat /af-home/m-u1/dev/af-marker.txt"); !strings.Contains(got, "af-ec2c-marker-u1") {
		t.Errorf("u1's home did not survive the swap: %q", got)
	} else {
		t.Log("u1 home survived the swap (marker intact)")
	}

	// --- 5. leave nothing running; the volumes are deleted by Destroy. ---
	if err := u1.Stop(ctx); err != nil {
		t.Errorf("final stop: %v", err)
	}
	live.waitVolumeDetached(u1, 5*time.Minute)
	for _, rt := range []*ecsEC2Runtime{u1, u2} {
		if err := rt.Destroy(ctx); err != nil {
			t.Errorf("Destroy %s: %v", rt.Name(), err)
		}
	}
	t.Logf("SUMMARY cold=%.1fs hotswap=%.1fs", coldStart.Seconds(), swap.Seconds())
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

func (l *liveEC2) waitVolumeDetached(rt *ecsEC2Runtime, budget time.Duration) {
	l.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		vol, err := rt.homeVolume(l.ctx)
		if err == nil && vol != nil && attachedInstance(vol) == "" {
			return
		}
		time.Sleep(3 * time.Second)
	}
	l.t.Fatalf("%s home was never detached", rt.Name())
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
