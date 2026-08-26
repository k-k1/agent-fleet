package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// TestECSEC2LiveStartDeployments counts, on the REAL pool, how many tasks ECS actually
// creates per Start — the only way to check docs/64 §64.39 end to end.
//
// Unit tests can only assert the shape of the calls we make. What made this bug expensive
// is the shape of the ANSWER: one UpdateService carrying both a new task definition and
// desiredCount 1 makes ECS satisfy the count from the deployment it just demoted, so a
// second task — from the OLD revision — appears about 40 seconds later. Nothing in the
// CP's own call log shows that; it is visible only in the service's events.
//
//	source ~/af-ec2c/state.env
//	AF_ECS_EC2_LIVE_DEPLOY=1 go test -run TestECSEC2LiveStartDeployments -v -timeout 90m .
//
// ⚠️ Round 2 is a WITNESS and it does NOT gate the test, because the defect turned out to
// be a RACE rather than a consequence. Measured while writing this: the old combined call
// produced two tasks twice out of three times on Fargate and once out of two on the pool,
// and even deliberately raising desiredCount past a provably-ACTIVE deployment came back
// clean here. So a red witness proves the substrate reproduces; a green witness proves
// nothing either way, and failing on it would only make this test flaky.
//
// Read the rounds accordingly: they show the product no longer CREATES the condition
// (docs/64 §64.39), not that the condition was guaranteed to bite. The frequency evidence
// is the production pool's 40% of Starts, not this harness.
func TestECSEC2LiveStartDeployments(t *testing.T) {
	if os.Getenv("AF_ECS_EC2_LIVE") != "1" || os.Getenv("AF_ECS_EC2_LIVE_DEPLOY") != "1" {
		t.Skip("set AF_ECS_EC2_LIVE=1 AF_ECS_EC2_LIVE_DEPLOY=1 (and source the harness state.env)")
	}
	ctx := context.Background()
	useCPTaskRole(t)
	factory, err := newECSEC2Factory(&manager{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	f := factory.(*ecsEC2Factory)
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(f.base.cfg.region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	live := &liveEC2{t: t, ctx: ctx, ec2: ec2.NewFromConfig(ac), ecs: ecs.NewFromConfig(ac),
		ssm: ssm.NewFromConfig(ac), cluster: f.base.cfg.cluster}
	d := &liveDeploy{t: t, ctx: ctx, ecs: ecs.NewFromConfig(ac), cluster: f.base.cfg.cluster}

	name := "af-ec2c-d" + os.Getenv("AF_ECS_EC2_LIVE_SUFFIX")
	ws := Workspace{ContainerName: name, MembershipID: "m-d1", AgentToken: "tok-d1"}
	u := f.New(ws, "", nil).(*ecsEC2Runtime)

	// A second task shows up ~40s after the first, so "converged" is not far enough to
	// look: every round watches on past that before counting.
	const settle = 90 * time.Second

	stop := func() {
		t.Helper()
		if err := u.Stop(ctx); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		live.waitTasksGone(u, 5*time.Minute)
		d.settle(name, 4*time.Minute)
	}

	// --- 1. get to a known state: a converged workspace, stopped, with one settled
	// deployment. No assertion — this round only builds the substrate the next two read. ---
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	stop()

	// --- 2. WITNESS: raise desiredCount while the demoted deployment is still ACTIVE.
	//
	// ⚠️ Do NOT witness this with the old single combined call. That call only SOMETIMES
	// produces the second task — measured here: twice out of three tries on Fargate, once
	// out of two on the pool — because whether the count is applied before or after the
	// demotion settles is a race inside the ECS scheduler. A witness that flips a coin
	// cannot tell "the fix worked" from "the bug did not fire", which is exactly how the
	// first version of this test reported three meaningless green rounds.
	//
	// So reproduce the CONDITION instead of the call: change the revision, then raise the
	// count immediately, while the old deployment is provably still ACTIVE. That is also
	// precisely what the product did when its wait was capped too low, and it produced
	// two tasks ten seconds apart.
	legacy := d.copyRevision(d.serviceTaskDef(name))
	from := time.Now()
	if _, err := d.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(f.base.cfg.cluster), Service: aws.String(name),
		TaskDefinition: aws.String(legacy),
	}); err != nil {
		t.Fatalf("witness revision update: %v", err)
	}
	if !d.hasActiveDeployment(name) {
		t.Fatalf("the revision change did not leave an ACTIVE deployment behind; the witness cannot set up")
	}
	if _, err := d.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(f.base.cfg.cluster), Service: aws.String(name),
		DesiredCount: aws.Int32(1),
	}); err != nil {
		t.Fatalf("witness scale-up: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	time.Sleep(settle)
	witness, wb := d.countSince(name, from)
	t.Logf("[WITNESS: desiredCount raised while the demoted deployment was still ACTIVE] "+
		"tasks created = %d, 'unable to place' = %d", witness, wb)
	if witness < 2 && wb == 0 {
		t.Logf("NOTE: the witness came back clean (%d task, %d complaints). The race did not fire this run, "+
			"so the rounds below show the product's behaviour but do not, on their own, demonstrate the bug "+
			"they prevent.", witness, wb)
	}
	stop()

	// --- 3. the same situation through the PRODUCT, with a changed task definition:
	// the case that used to cost 40 seconds. A different env is the smallest honest
	// change (image, settings and slot all reach the fingerprint the same way). ---
	lastTaskDef.Delete(name)
	changed := f.New(ws, "", []string{"AF_LIVE_DEPLOY_PROBE=1"}).(*ecsEC2Runtime)
	u = changed
	revsBefore := d.revisions(name)
	from = time.Now()
	if err := changed.Start(ctx); err != nil {
		t.Fatalf("changed-taskdef Start: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	time.Sleep(settle)
	n, b := d.countSince(name, from)
	t.Logf("[FIXED: changed task definition] tasks created = %d, 'unable to place' = %d (witness above: %d/%d)",
		n, b, witness, wb)
	if n != 1 || b != 0 {
		t.Errorf("BUG NOT FIXED: a changed task definition created %d tasks and %d placement complaints, want 1/0", n, b)
	}
	if after := d.revisions(name); after <= revsBefore {
		t.Errorf("a changed env should have registered a new revision, still %d", after)
	}
	stop()

	// --- 4. the CP-restart case (§64.39.6.2). Nothing changed, but the process-local
	// cache is gone — what happens to every workspace on its first Start after a deploy.
	// It must not register a revision at all. ---
	lastTaskDef.Delete(name)
	revsBefore = d.revisions(name)
	from = time.Now()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("re-wake Start: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	time.Sleep(settle)
	n, b = d.countSince(name, from)
	t.Logf("[FIXED: re-wake after a CP restart, nothing changed] tasks created = %d, 'unable to place' = %d", n, b)
	if n != 1 || b != 0 {
		t.Errorf("a re-wake must create exactly one task and no placement complaint, got %d/%d", n, b)
	}
	if after := d.revisions(name); after != revsBefore {
		t.Errorf("a re-wake with nothing changed registered %d new revision(s); the fingerprint label should have been read back instead", after-revsBefore)
	} else {
		t.Logf("[FIXED: re-wake] no new task definition revision (still %d) — reuse survived the restart", after)
	}
	stop()
}

// TestECSFargateLiveStartDeployments asks the same question of the FARGATE adapter,
// which docs/64 §64.39.6.3 could only reason about: `ecsRuntime.upsertService` sends
// desiredCount 1 together with a new revision and ForceNewDeployment, and
// `registerTaskDef` registers unconditionally — so every Start after the first has the
// exact shape that made ECS launch a second task on the EC2 pool.
//
// It changes nothing (ADR 0045 keeps runtime_ecs.go frozen) and asserts nothing about
// what the number SHOULD be. It measures, so the doc can stop saying "probably".
//
//	AF_ECS_EC2_LIVE_DEPLOY=1 go test -run TestECSFargateLiveStartDeployments -v -timeout 60m .
func TestECSFargateLiveStartDeployments(t *testing.T) {
	if os.Getenv("AF_ECS_EC2_LIVE") != "1" || os.Getenv("AF_ECS_EC2_LIVE_DEPLOY") != "1" {
		t.Skip("set AF_ECS_EC2_LIVE=1 AF_ECS_EC2_LIVE_DEPLOY=1 (and source the harness state.env)")
	}
	ctx := context.Background()
	useCPTaskRole(t)
	factory, err := newECSFactory(&manager{})
	if err != nil {
		t.Fatalf("fargate factory: %v", err)
	}
	f := factory.(*ecsFactory)
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(f.cfg.region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	d := &liveDeploy{t: t, ctx: ctx, ecs: ecs.NewFromConfig(ac), cluster: f.cfg.cluster}

	name := "af-ec2c-fg" + os.Getenv("AF_ECS_EC2_LIVE_SUFFIX")
	rt := f.New(Workspace{ContainerName: name, MembershipID: "m-fg1", AgentToken: "tok-fg1"}, "", nil)

	waitFor := func(want string, budget time.Duration) {
		t.Helper()
		deadline := time.Now().Add(budget)
		for time.Now().Before(deadline) {
			if s := rt.State(ctx); s == want {
				return
			}
			time.Sleep(5 * time.Second)
		}
		t.Fatalf("fargate workspace never reached %q (now %q)", want, rt.State(ctx))
	}
	defer func() {
		// A forgotten desiredCount 1 bills for as long as it runs.
		if err := rt.Stop(ctx); err != nil {
			t.Logf("cleanup Stop: %v", err)
		}
	}()

	from := time.Now()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	waitFor("running", 15*time.Minute)
	time.Sleep(90 * time.Second)
	n, b := d.countSince(name, from)
	t.Logf("[fargate first start (CreateService)] tasks created = %d, 'unable to place' = %d", n, b)

	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for i := 0; i < 60 && rt.State(ctx) != "stopped"; i++ {
		time.Sleep(5 * time.Second)
	}
	// ⚠️ Same trap as the EC2 test: a service whose previous deployment has not settled
	// does not spawn the old-revision task, and skipping this made the first run report a
	// clean 1 that meant nothing.
	d.settle(name, 4*time.Minute)

	from = time.Now()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	waitFor("running", 15*time.Minute)
	time.Sleep(90 * time.Second)
	n, b = d.countSince(name, from)
	t.Logf("[fargate re-start (UpdateService: new revision + desiredCount 1 + force)] "+
		"RESULT tasks created = %d, 'unable to place' = %d", n, b)
	if n >= 2 {
		t.Logf("MEASURED: the Fargate adapter has the same defect the EC2 pool had — %d tasks for one "+
			"Start. Fargate keeps no image cache, so the extra task pays a full pull.", n)
	} else {
		t.Logf("MEASURED: the Fargate adapter created %d task(s) on a settled service; the EC2 pool's "+
			"second-task behaviour did NOT reproduce. docs/64 §64.39.6.3 must be corrected.", n)
	}
}

// liveDeploy reads what ECS did, with the deployer's own credentials — never the
// product's. The product's view is exactly what this test must not trust.
type liveDeploy struct {
	t       *testing.T
	ctx     context.Context
	ecs     *ecs.Client
	cluster string
}

// countSince counts task creations and placement complaints the service recorded after
// `since`. Service events are the only place a task ECS created on its own initiative
// (from a demoted deployment) is visible at all.
func (d *liveDeploy) countSince(service string, since time.Time) (tasks, blocked int) {
	d.t.Helper()
	s := d.describe(service)
	for _, ev := range s.Events {
		if ev.CreatedAt == nil || ev.CreatedAt.Before(since) {
			continue
		}
		msg := aws.ToString(ev.Message)
		switch {
		case strings.Contains(msg, "has started 1 tasks"):
			tasks++
			d.t.Logf("    event %s  %s", ev.CreatedAt.Format(time.TimeOnly), msg)
		case strings.Contains(msg, "unable to place a task"):
			blocked++
			d.t.Logf("    event %s  %s", ev.CreatedAt.Format(time.TimeOnly), msg)
		}
	}
	return tasks, blocked
}

// settle waits until the service is back to ONE deployment that has finished rolling
// out. That state is the precondition for the whole defect: only a settled PRIMARY can be
// demoted to ACTIVE by the next task-definition change and then be handed the
// desiredCount. Measured at ~90s on a stopped service (docs/64 §64.39.4).
func (d *liveDeploy) settle(service string, budget time.Duration) {
	d.t.Helper()
	deadline := time.Now().Add(budget)
	for {
		s := d.describe(service)
		if len(s.Deployments) == 1 && s.Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted {
			return
		}
		if !time.Now().Before(deadline) {
			var st []string
			for _, dep := range s.Deployments {
				st = append(st, aws.ToString(dep.Status)+"/"+string(dep.RolloutState))
			}
			d.t.Logf("WARNING: %s did not settle within %s (%v); the next round may not reproduce anything",
				service, budget, st)
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// hasActiveDeployment reports whether a demoted deployment is still around — the
// precondition the witness needs, checked rather than assumed.
func (d *liveDeploy) hasActiveDeployment(service string) bool {
	d.t.Helper()
	for _, dep := range d.describe(service).Deployments {
		if aws.ToString(dep.Status) == "ACTIVE" {
			return true
		}
	}
	return false
}

func (d *liveDeploy) describe(service string) ecstypes.Service {
	d.t.Helper()
	out, err := d.ecs.DescribeServices(d.ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(d.cluster), Services: []string{service},
	})
	if err != nil || len(out.Services) == 0 {
		d.t.Fatalf("describe %s: %v", service, err)
	}
	return out.Services[0]
}

func (d *liveDeploy) revisions(family string) int {
	d.t.Helper()
	var n int
	var next *string
	for {
		out, err := d.ecs.ListTaskDefinitions(d.ctx, &ecs.ListTaskDefinitionsInput{
			FamilyPrefix: aws.String(family), NextToken: next,
		})
		if err != nil {
			d.t.Fatalf("list task definitions for %s: %v", family, err)
		}
		n += len(out.TaskDefinitionArns)
		if next = out.NextToken; next == nil {
			return n
		}
	}
}

func (d *liveDeploy) serviceTaskDef(service string) string {
	return aws.ToString(d.describe(service).TaskDefinition)
}

// copyRevision re-registers a revision unchanged except that it DROPS the fingerprint
// label, so the copy is a different-but-equivalent task definition — exactly what the old
// call shape used to hand ECS. Dropping the label also keeps the copy out of the
// product's reuse path, so the witness cannot contaminate the rounds after it.
func (d *liveDeploy) copyRevision(arn string) string {
	d.t.Helper()
	got, err := d.ecs.DescribeTaskDefinition(d.ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
	})
	if err != nil {
		d.t.Fatalf("describe task definition %s: %v", arn, err)
	}
	td := got.TaskDefinition
	cds := append([]ecstypes.ContainerDefinition(nil), td.ContainerDefinitions...)
	for i := range cds {
		labels := map[string]string{"af.live.witness": "1"}
		for lk, lv := range cds[i].DockerLabels {
			if lk != afTaskDefFingerprintLabel {
				labels[lk] = lv
			}
		}
		cds[i].DockerLabels = labels
	}
	out, err := d.ecs.RegisterTaskDefinition(d.ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  td.Family,
		RequiresCompatibilities: td.RequiresCompatibilities,
		NetworkMode:             td.NetworkMode,
		RuntimePlatform:         td.RuntimePlatform,
		ExecutionRoleArn:        td.ExecutionRoleArn,
		TaskRoleArn:             td.TaskRoleArn,
		ContainerDefinitions:    cds,
		PlacementConstraints:    td.PlacementConstraints,
		Volumes:                 td.Volumes,
		Cpu:                     td.Cpu,
		Memory:                  td.Memory,
	})
	if err != nil {
		d.t.Fatalf("re-register %s: %v", arn, err)
	}
	return aws.ToString(out.TaskDefinition.TaskDefinitionArn)
}
