package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
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
// creates per Start — the only way to check docs/log/64 §64.39 end to end.
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
// (docs/log/64 §64.39), not that the condition was guaranteed to bite. The frequency evidence
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
// which docs/log/64 §64.39.6.3 could only reason about: `ecsRuntime.upsertService` sends
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
			"second-task behaviour did NOT reproduce. docs/log/64 §64.39.6.3 must be corrected.", n)
	}
}

// TestECSEC2LiveDeploymentConfig checks the ONE part of docs/log/64 §64.39.10 that unit tests
// cannot: that `ec2SingleTaskDeployment` (maximumPercent 100 / minimumHealthyPercent 0)
// actually reaches the real service — including a service that was created BEFORE this
// change and therefore still carries the AWS default 200/100, which is the state every
// service in the production pool is in right now.
//
//	source ~/af-ec2c/state.env
//	AF_ECS_EC2_LIVE_DEPLOY=1 go test -run TestECSEC2LiveDeploymentConfig -v -timeout 75m .
//
// The rounds, and what each is worth:
//
//  1. a service the product CREATES must carry 100/0.
//  2. an A/B of the setting ITSELF on this substrate, driven from the test side. Not the
//     40-second bug — the PROPERTY that made it possible: can this service ever run two
//     tasks at once? A revision swap on a service with a running task is the deterministic
//     way to ask (200/100 must start the replacement before stopping the old one; 100/0
//     has no room to and must stop first). ⚠️ Round 2 does not gate the test — see the
//     note on TestECSEC2LiveStartDeployments: the defect itself is a race, and the first
//     version of that test reported three meaningless greens because its substrate never
//     reproduced anything. This round exists so a green below is not read that way.
//  3. ★ the real question: force the service back to 200/100 (a pre-upgrade service), then
//     Start it through the PRODUCT with a changed task definition. It must come back at
//     100/0 — and, because that is what "is the first Start after the deploy still on the
//     old behaviour?" really asks, the 100 must be in place NO LATER than the moment the
//     desiredCount goes to 1. A watcher samples the service throughout to see the order.
//  4. the `!prepared` fallback (reuse → a single upsertService carrying the setting AND
//     the count), where nothing orders the two and only ECS's own atomicity is left.
func TestECSEC2LiveDeploymentConfig(t *testing.T) {
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
	// The test's own eyes and cleanup stay the deployer's; only the product runs as the
	// CP task role. A configuration this test READ with the product's client would be the
	// product's opinion of what it sent, which is the thing under test.
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(f.base.cfg.region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	live := &liveEC2{t: t, ctx: ctx, ec2: ec2.NewFromConfig(ac), ecs: ecs.NewFromConfig(ac),
		ssm: ssm.NewFromConfig(ac), cluster: f.base.cfg.cluster}
	d := &liveDeploy{t: t, ctx: ctx, ecs: ecs.NewFromConfig(ac), cluster: f.base.cfg.cluster}

	name := "af-ec2c-dc" + os.Getenv("AF_ECS_EC2_LIVE_SUFFIX")
	ws := Workspace{ContainerName: name, MembershipID: "m-dc1", AgentToken: "tok-dc1"}
	u := f.New(ws, "", nil).(*ecsEC2Runtime)

	// A second task shows up ~40s after the first, so "converged" is not far enough to
	// look: every round watches past that before counting.
	const settle = 90 * time.Second

	stop := func() {
		t.Helper()
		if err := u.Stop(ctx); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		live.waitTasksGone(u, 5*time.Minute)
		d.settle(name, 4*time.Minute)
	}
	// A left-behind desiredCount 1 bills for as long as it runs, and a failed round
	// t.Fatalf's straight past the stop() at the end of it.
	defer func() {
		if err := u.Stop(ctx); err != nil {
			t.Logf("cleanup Stop: %v", err)
		}
	}()

	// --- 1. a brand-new service (CreateService). ---
	w := d.watch(name)
	from := time.Now()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	time.Sleep(settle)
	r := w.finish()
	d.requireDeploymentConfig(name, 100, 0, "a service the product just CREATED")
	n, b := d.countSince(name, from)
	t.Logf("[NEW SERVICE] deploymentConfiguration = 100/0, tasks created = %d, 'unable to place' = %d, "+
		"most tasks seen at once = %d", n, b, r.maxTasks)
	for _, line := range d.tasksSince(name, from) {
		t.Logf("    task %s", line)
	}
	r.report(t)
	if n != 1 || b != 0 {
		t.Errorf("a first Start created %d tasks and %d placement complaints, want 1/0", n, b)
	}

	// --- 2. A/B of the SETTING on this substrate, with the service left running.
	//
	// A revision swap on a service that has a task running is the deterministic form of
	// "may this service run two tasks?": at 200/100 ECS MUST bring the replacement up
	// before it takes the old one down, at 100/0 it has nowhere to put it and must go
	// down to zero first. That is the same headroom the demoted deployment used in
	// production — asked directly, instead of hoping the scheduler race fires. ---
	ab := func(label string, max, min int32) int32 {
		t.Helper()
		d.setDeploymentConfig(name, max, min)
		d.requireDeploymentConfig(name, max, min, label+": the test's own setup")
		d.settle(name, 3*time.Minute)
		since := time.Now()
		wa := d.watch(name)
		if _, err := d.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: aws.String(f.base.cfg.cluster), Service: aws.String(name),
			TaskDefinition: aws.String(d.copyRevision(d.serviceTaskDef(name))),
		}); err != nil {
			t.Fatalf("%s: revision swap: %v", label, err)
		}
		time.Sleep(settle)
		res := wa.finish()
		created, blocked := d.countSince(name, since)
		t.Logf("[A/B %s] most tasks seen at once = %d, started = %d, 'unable to place' = %d",
			label, res.maxTasks, created, blocked)
		for _, line := range d.tasksSince(name, since) {
			t.Logf("    task %s", line)
		}
		res.report(t)
		// ⚠️ Do not hand the next round a service whose deployment is still rolling. The
		// first run of this test did, and round 3 then started from a state no production
		// Start is ever in: a PRIMARY deployment that had never placed a task. Leave the
		// substrate converged, so what the next round measures is its own.
		d.settle(name, 4*time.Minute)
		return res.maxTasks
	}
	atDefault := ab("maximumPercent 200 (the AWS default, i.e. every pre-upgrade service)", 200, 100)
	atFixed := ab("maximumPercent 100 (ec2SingleTaskDeployment)", 100, 0)
	switch {
	case atDefault >= 2 && atFixed < 2:
		t.Logf("A/B RESULT: 200%% ran %d tasks at once, 100%% ran %d — on this substrate the setting is "+
			"what decides whether a second task can exist at all (docs/log/64 §64.39.10).", atDefault, atFixed)
	case atDefault < 2:
		t.Logf("A/B RESULT: even at 200%% this substrate never showed two tasks at once (%d), so it does "+
			"NOT reproduce the production condition; the rounds below show what the product SENDS, not "+
			"that it prevented anything here.", atDefault)
	default:
		t.Errorf("A/B RESULT: 100%% still showed %d tasks at once — ec2SingleTaskDeployment does not do "+
			"what §64.39.10 says it does", atFixed)
	}
	stop()

	// --- 3. ★ the pre-upgrade service: forced back to 200/100, then started BY THE
	// PRODUCT with a changed task definition. A different env is the smallest honest
	// change (image, settings and slot all reach the fingerprint the same way).
	//
	// Run TWICE, with a different env each time. Once is a data point; the defect it
	// stands in for fired on 40% of production Starts, so a single green would sit
	// comfortably inside its miss rate. ---
	preUpgradeStart := func(round int, probe string) {
		t.Helper()
		d.setDeploymentConfig(name, 200, 100)
		d.requireDeploymentConfig(name, 200, 100, fmt.Sprintf("★%d: the pre-upgrade service this round needs", round))
		d.settle(name, 3*time.Minute)
		d.logDeployments(name, fmt.Sprintf("★%d: what the product is about to Start from", round))
		lastTaskDef.Delete(name)
		changed := f.New(ws, "", []string{"AF_LIVE_DC_PROBE=" + probe}).(*ecsEC2Runtime)
		u = changed
		revsBefore := d.revisions(name)
		w := d.watch(name)
		from := time.Now()
		if err := changed.Start(ctx); err != nil {
			t.Fatalf("★%d: changed-taskdef Start on a 200%% service: %v", round, err)
		}
		live.waitState(u, "running", 12*time.Minute)
		time.Sleep(settle)
		r := w.finish()
		n, b := d.countSince(name, from)
		d.requireDeploymentConfig(name, 100, 0,
			fmt.Sprintf("★%d: a service that was at 200/100 when the product Started it", round))
		t.Logf("[★%d PRE-UPGRADE SERVICE, changed task definition] tasks created = %d, 'unable to place' = %d, "+
			"most tasks seen at once = %d", round, n, b, r.maxTasks)
		for _, line := range d.tasksSince(name, from) {
			t.Logf("    task %s", line)
		}
		r.report(t)
		what := fmt.Sprintf("★%d: the FIRST Start after the deploy", round)
		r.requireOrdered(t, what)
		r.requireNoDemotedTask(t, what)
		if n != 1 || b != 0 {
			t.Errorf("%s: created %d tasks and %d placement complaints, want 1/0", what, n, b)
		}
		if after := d.revisions(name); after <= revsBefore {
			t.Errorf("%s: a changed env should have registered a new revision, still %d", what, after)
		}
		stop()
	}
	preUpgradeStart(1, "1")
	preUpgradeStart(2, "2")

	var revsBefore int

	// --- 4. the `!prepared` fallback: nothing changed, so reuseOrRegisterTaskDef reuses
	// and launch() goes through upsertService — one UpdateService carrying the setting and
	// desiredCount 1 together, the only place on the path where nothing orders them. ---
	d.setDeploymentConfig(name, 200, 100)
	d.requireDeploymentConfig(name, 200, 100, "the pre-upgrade service round 4 needs")
	d.settle(name, 3*time.Minute)
	d.logDeployments(name, "!prepared: what the product is about to Start from")
	revsBefore = d.revisions(name)
	w = d.watch(name)
	from = time.Now()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("re-wake Start on a 200%% service: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	time.Sleep(settle)
	r = w.finish()
	n, b = d.countSince(name, from)
	d.requireDeploymentConfig(name, 100, 0, "a re-wake of a 200/100 service")
	if after := d.revisions(name); after == revsBefore {
		t.Logf("[!prepared] no new revision (still %d): the task definition was reused, so this round did "+
			"go through upsertService's single call", after)
	} else {
		t.Logf("NOTE: this round registered %d new revision(s) — the slot changed, so the fingerprint did "+
			"and launch() took the split path again. The `!prepared` fallback was NOT exercised.", after-revsBefore)
	}
	t.Logf("[!prepared re-wake] tasks created = %d, 'unable to place' = %d, most tasks seen at once = %d",
		n, b, r.maxTasks)
	for _, line := range d.tasksSince(name, from) {
		t.Logf("    task %s", line)
	}
	r.report(t)
	r.requireOrdered(t, "the !prepared fallback")
	r.requireNoDemotedTask(t, "the !prepared fallback")
	if n != 1 || b != 0 {
		t.Errorf("a re-wake of a pre-upgrade service created %d tasks and %d placement complaints, want 1/0", n, b)
	}
	stop()
}

// TestECSEC2LivePreUpgradeService starts a workspace whose service was created by an
// OLDER BUILD, and is the round TestECSEC2LiveDeploymentConfig could not be:
//
//	source ~/af-ec2c/state.env
//	AF_ECS_EC2_LIVE_DEPLOY=1 go test -run TestECSEC2LivePreUpgradeService -v -timeout 40m .
//
// ⚠️ Why a separate test rather than one more round over there. That test made its
// "pre-upgrade" service by pushing 200/100 back onto a service the NEW code had created,
// and reported green twice. Production then broke on the first Start of every existing
// workspace: `availabilityZoneRebalancing` is decided at CREATE time and an UpdateService
// does not move it, so the service under test had DISABLED — the one value that makes
// maximumPercent 100 legal — while every real pre-upgrade service has ENABLED and answers
// 400 (§64.39.12).
//
// The lesson is bigger than the field: **a state you produced by editing the new thing is
// not the old thing.** The only way to test an upgrade is to build the old shape with the
// call that built it, which here means CreateService, not UpdateService.
func TestECSEC2LivePreUpgradeService(t *testing.T) {
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

	name := "af-ec2c-oz" + os.Getenv("AF_ECS_EC2_LIVE_SUFFIX")
	ws := Workspace{ContainerName: name, MembershipID: "m-oz1", AgentToken: "tok-oz1"}
	u := f.New(ws, "", nil).(*ecsEC2Runtime)
	defer func() {
		if err := u.Stop(ctx); err != nil {
			t.Logf("cleanup Stop: %v", err)
		}
	}()

	// --- 1. build the OLD shape, with CreateService, before the product ever sees this
	// name: 200/100 and Availability Zone rebalancing on — i.e. what an 0.12.2 control
	// plane left behind. desiredCount 0, so the placeholder revision never runs. ---
	d.createLegacyService(name, f.base.cfg.namespaceArn)
	s := d.describe(name)
	if s.AvailabilityZoneRebalancing != ecstypes.AvailabilityZoneRebalancingEnabled {
		t.Fatalf("the substrate is not a pre-upgrade service: availabilityZoneRebalancing = %q, want ENABLED",
			s.AvailabilityZoneRebalancing)
	}
	d.requireDeploymentConfig(name, 200, 100, "the service an older build would have left")
	t.Logf("pre-upgrade service ready: availabilityZoneRebalancing=%s, 200/100, desired=%d",
		s.AvailabilityZoneRebalancing, s.DesiredCount)

	// --- 2. WITNESS (does not gate): the exact call 0.12.3 made. maximumPercent 100
	// WITHOUT saying anything about rebalancing — which on an update means "keep what the
	// service has", i.e. ENABLED. This is the production 400, reproduced deliberately. ---
	_, werr := d.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(f.base.cfg.cluster), Service: aws.String(name),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MaximumPercent: aws.Int32(100), MinimumHealthyPercent: aws.Int32(0),
		},
	})
	switch {
	case werr == nil:
		t.Logf("NOTE: the witness call SUCCEEDED — ECS no longer rejects maximumPercent 100 while "+
			"rebalancing is on. This substrate no longer reproduces §64.39.12, so the round below "+
			"shows what the product sends rather than what it survives. (service is now %s)",
			d.describe(name).AvailabilityZoneRebalancing)
	case strings.Contains(werr.Error(), "Availability Zone Rebalancing"):
		t.Logf("[WITNESS] the 0.12.3 call is refused exactly as in production: %v", werr)
	default:
		t.Fatalf("the witness failed for some OTHER reason, so the substrate is not what this test "+
			"thinks it is: %v", werr)
	}

	// --- 3. the PRODUCT starts this workspace. Nothing here is a percentage check first:
	// if the pair is not sent together, Start itself fails, which is what users saw. ---
	w := d.watch(name)
	from := time.Now()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("PRE-UPGRADE SERVICE: Start failed — this is the production symptom: %v", err)
	}
	live.waitState(u, "running", 12*time.Minute)
	time.Sleep(90 * time.Second)
	r := w.finish()
	n, b := d.countSince(name, from)
	after := d.describe(name)
	d.requireDeploymentConfig(name, 100, 0, "the pre-upgrade service after the product Started it")
	if after.AvailabilityZoneRebalancing != ecstypes.AvailabilityZoneRebalancingDisabled {
		t.Errorf("availabilityZoneRebalancing is still %q; the product must turn it off in the same call "+
			"that sets maximumPercent 100", after.AvailabilityZoneRebalancing)
	}
	t.Logf("[PRE-UPGRADE SERVICE started by the product] tasks created = %d, 'unable to place' = %d, "+
		"most tasks seen at once = %d, availabilityZoneRebalancing = %s",
		n, b, r.maxTasks, after.AvailabilityZoneRebalancing)
	for _, line := range d.tasksSince(name, from) {
		t.Logf("    task %s", line)
	}
	r.report(t)
	r.requireOrdered(t, "the first Start of a pre-upgrade service")
	r.requireNoDemotedTask(t, "the first Start of a pre-upgrade service")
	if n != 1 || b != 0 {
		t.Errorf("the first Start of a pre-upgrade service created %d tasks and %d placement complaints, want 1/0", n, b)
	}
}

// createLegacyService creates the service the way a build BEFORE ec2SingleTaskDeployment
// did: no deployment configuration of our own (so ECS applies its 200/100 default) and
// Availability Zone rebalancing left at the create-time default, ENABLED.
//
// ⚠️ It has to be CreateService. `availabilityZoneRebalancing` cannot be reached by
// pushing values onto an existing service — that is precisely the mistake that let
// §64.39.11 report green while production was about to break.
func (d *liveDeploy) createLegacyService(name, namespaceArn string) {
	d.t.Helper()
	if s, err := d.ecs.DescribeServices(d.ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(d.cluster), Services: []string{name},
	}); err == nil && len(s.Services) == 1 && aws.ToString(s.Services[0].Status) == "ACTIVE" {
		d.t.Fatalf("%s already exists; this test needs a name the product has never created "+
			"(use AF_ECS_EC2_LIVE_SUFFIX)", name)
	}
	// A placeholder revision, only ever pointed at: desiredCount 0 means it never runs,
	// and the product replaces it on the first Start.
	td, err := d.ecs.RegisterTaskDefinition(d.ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(name),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityEc2},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:              aws.String("agent"),
			Image:             aws.String(os.Getenv("AF_ECS_WORKSPACE_IMAGE")),
			MemoryReservation: aws.Int32(512),
			PortMappings: []ecstypes.PortMapping{
				{ContainerPort: aws.Int32(ecsAgentPort), Name: aws.String("agent")},
			},
		}},
	})
	if err != nil {
		d.t.Fatalf("placeholder task definition for %s: %v", name, err)
	}
	subnets := strings.Split(os.Getenv("AF_ECS_SUBNETS"), ",")
	if len(subnets) < 2 {
		// Rebalancing is about spreading across zones; one subnet is not the shape a real
		// deployment has, and ECS may not even enable it.
		d.t.Fatalf("AF_ECS_SUBNETS has %d subnet(s); this test needs the two-AZ harness", len(subnets))
	}
	if _, err := d.ecs.CreateService(d.ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(d.cluster),
		ServiceName:    aws.String(name),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(0),
		LaunchType:     ecstypes.LaunchTypeEc2,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        subnets,
				SecurityGroups: []string{os.Getenv("AF_ECS_SECURITY_GROUP")},
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
		ServiceConnectConfiguration: &ecstypes.ServiceConnectConfiguration{
			Enabled:   true,
			Namespace: aws.String(namespaceArn),
			Services: []ecstypes.ServiceConnectService{{
				PortName:      aws.String("agent"),
				DiscoveryName: aws.String(name),
				ClientAliases: []ecstypes.ServiceConnectClientAlias{
					{DnsName: aws.String(name), Port: aws.Int32(ecsAgentPort)},
				},
			}},
		},
	}); err != nil {
		d.t.Fatalf("creating the pre-upgrade service %s: %v", name, err)
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
// desiredCount. Measured at ~90s on a stopped service (docs/log/64 §64.39.4).
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

// --- deployment configuration: read it, force it, and watch it move ---

// requireDeploymentConfig fails the test unless the service really carries max/min. Read
// with the deployer's credentials, from the service itself: what the product believes it
// sent is exactly the claim under test (the unit tests already cover that half).
func (d *liveDeploy) requireDeploymentConfig(service string, wantMax, wantMin int32, what string) {
	d.t.Helper()
	dc := d.describe(service).DeploymentConfiguration
	if dc == nil {
		d.t.Fatalf("%s: %s has no deploymentConfiguration at all", what, service)
		return
	}
	max, min := aws.ToInt32(dc.MaximumPercent), aws.ToInt32(dc.MinimumHealthyPercent)
	if max != wantMax || min != wantMin {
		d.t.Errorf("%s: %s is at maximumPercent=%d minimumHealthyPercent=%d, want %d/%d",
			what, service, max, min, wantMax, wantMin)
		return
	}
	d.t.Logf("%s: %s is at %d/%d", what, service, max, min)
}

// setDeploymentConfig puts the service back the way AWS would have left it before this
// change (200/100) — the only honest way to test the upgrade path, since every service in
// the production pool was created by a build that never sent the setting.
func (d *liveDeploy) setDeploymentConfig(service string, max, min int32) {
	d.t.Helper()
	if _, err := d.ecs.UpdateService(d.ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(d.cluster), Service: aws.String(service),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MaximumPercent: aws.Int32(max), MinimumHealthyPercent: aws.Int32(min),
		},
	}); err != nil {
		d.t.Fatalf("forcing %s to %d/%d: %v", service, max, min, err)
	}
}

// logDeployments records the state a round STARTS from. The first run of this test made
// its ★ round start from a service whose deployment was still rolling and had never
// placed a task — a state no production Start is ever in — and the round then measured
// that instead of what it was asked to.
func (d *liveDeploy) logDeployments(service string, what string) {
	d.t.Helper()
	s := d.describe(service)
	d.t.Logf("%s: desired=%d running=%d pending=%d", what, s.DesiredCount, s.RunningCount, s.PendingCount)
	for _, dep := range s.Deployments {
		rev := aws.ToString(dep.TaskDefinition)
		if i := strings.LastIndex(rev, ":"); i >= 0 {
			rev = "rev " + rev[i+1:]
		}
		d.t.Logf("    %s %s %s d=%d r=%d p=%d", aws.ToString(dep.Status), rev, dep.RolloutState,
			dep.DesiredCount, dep.RunningCount, dep.PendingCount)
	}
}

// serviceWatch samples the service while someone else drives it. Two facts are only
// visible here: whether the deployment configuration was already at 100% by the time the
// desiredCount went to 1 (the ORDER is what makes the very first Start after a deploy
// safe), and whether two tasks ever coexisted (the end state says nothing — the extra
// task in production was gone again a couple of minutes later).
type serviceWatch struct {
	d       *liveDeploy
	service string
	begin   time.Time
	stop    chan struct{}
	done    chan struct{}

	mu            sync.Mutex
	first100      time.Time
	firstDesired1 time.Time
	maxTasks      int32
	// Every distinct (deployment, status, revision, counts) the service passed through.
	// ⚠️ This is the part event counting cannot do: "two tasks" does not say WHOSE, and
	// the whole of §64.39 is about a task belonging to the DEMOTED deployment. A
	// deployment carries its own taskDefinition and its own running/pending counts, so
	// attribution is a fact here rather than an inference from timing.
	trail    []string
	seen     map[string]bool
	demoted  []string
	lastLine string
}

type watchResult struct {
	to100, toCount   time.Duration
	saw100, sawCount bool
	maxTasks         int32
	trail            []string
	// demoted lists the moments an ACTIVE (i.e. superseded) deployment held a task —
	// exactly the production symptom, whether or not it overlapped the real one.
	demoted []string
}

func (d *liveDeploy) watch(service string) *serviceWatch {
	w := &serviceWatch{d: d, service: service, begin: time.Now(),
		stop: make(chan struct{}), done: make(chan struct{}), seen: map[string]bool{}}
	go func() {
		defer close(w.done)
		for {
			// A service that does not exist yet is the normal state at the start of the
			// first round; keep sampling rather than deciding anything about it.
			out, err := d.ecs.DescribeServices(d.ctx, &ecs.DescribeServicesInput{
				Cluster: aws.String(d.cluster), Services: []string{service},
			})
			if err == nil && len(out.Services) == 1 {
				s := out.Services[0]
				w.mu.Lock()
				if dc := s.DeploymentConfiguration; dc != nil && aws.ToInt32(dc.MaximumPercent) == 100 && w.first100.IsZero() {
					w.first100 = time.Now()
				}
				if s.DesiredCount >= 1 && w.firstDesired1.IsZero() {
					w.firstDesired1 = time.Now()
				}
				if n := s.RunningCount + s.PendingCount; n > w.maxTasks {
					w.maxTasks = n
				}
				var line string
				for _, dep := range s.Deployments {
					rev := aws.ToString(dep.TaskDefinition)
					if i := strings.LastIndex(rev, ":"); i >= 0 {
						rev = "rev " + rev[i+1:]
					}
					status := aws.ToString(dep.Status)
					entry := fmt.Sprintf("%s %s d=%d r=%d p=%d", status, rev,
						dep.DesiredCount, dep.RunningCount, dep.PendingCount)
					line += entry + " | "
					if status == "ACTIVE" && dep.RunningCount+dep.PendingCount > 0 && !w.seen["demoted:"+entry] {
						w.seen["demoted:"+entry] = true
						w.demoted = append(w.demoted, fmt.Sprintf("+%.0fs  %s", time.Since(w.begin).Seconds(), entry))
					}
				}
				if line != w.lastLine {
					w.lastLine = line
					w.trail = append(w.trail, fmt.Sprintf("+%.0fs  %s", time.Since(w.begin).Seconds(), line))
				}
				w.mu.Unlock()
			}
			select {
			case <-w.stop:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	return w
}

func (w *serviceWatch) finish() watchResult {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	res := watchResult{maxTasks: w.maxTasks, trail: w.trail, demoted: w.demoted}
	if !w.first100.IsZero() {
		res.saw100, res.to100 = true, w.first100.Sub(w.begin)
	}
	if !w.firstDesired1.IsZero() {
		res.sawCount, res.toCount = true, w.firstDesired1.Sub(w.begin)
	}
	return res
}

// report prints the deployment trail the watcher recorded and names, plainly, every
// moment a superseded (ACTIVE) deployment held a task — the production symptom.
func (r watchResult) report(t *testing.T) {
	t.Helper()
	for _, line := range r.trail {
		t.Logf("    deployments  %s", line)
	}
	for _, line := range r.demoted {
		t.Logf("    ⚠ a SUPERSEDED deployment held a task:  %s", line)
	}
}

// requireNoDemotedTask is the §64.39 defect stated as an assertion. Not "two tasks at
// once": a task from the demoted revision costs its start-up and its replacement even
// when maximumPercent 100 keeps it from overlapping the real one, and that is what the
// production pool was paying 40 seconds for.
func (r watchResult) requireNoDemotedTask(t *testing.T, what string) {
	t.Helper()
	if len(r.demoted) == 0 {
		return
	}
	t.Errorf("%s: a superseded (ACTIVE) deployment was handed %d task(s) — the count reached the OLD "+
		"revision, which is docs/log/64 §64.39 still happening: %v", what, len(r.demoted), r.demoted)
}

// requireOrdered is the answer to "does the first Start after the deploy still behave like
// the old build?": the service must be at maximumPercent 100 no later than the moment its
// desiredCount becomes 1, or the count is raised on a service that still has room for a
// second task. Sampling is 2s, so equal timestamps mean "the same call" — which is what
// the `!prepared` path genuinely does.
func (r watchResult) requireOrdered(t *testing.T, what string) {
	t.Helper()
	if !r.sawCount {
		t.Errorf("%s: never observed desiredCount reaching 1 — the watcher missed the Start", what)
		return
	}
	if !r.saw100 {
		t.Errorf("%s: the service never showed maximumPercent 100 while it was being started", what)
		return
	}
	if r.to100 > r.toCount {
		t.Errorf("%s: maximumPercent reached 100 at +%.0fs, AFTER desiredCount reached 1 at +%.0fs — "+
			"the count was raised while the service could still run two tasks",
			what, r.to100.Seconds(), r.toCount.Seconds())
		return
	}
	t.Logf("%s: maximumPercent was 100 at +%.0fs, desiredCount reached 1 at +%.0fs (sampled every 2s) — "+
		"the count is never raised at 200%%", what, r.to100.Seconds(), r.toCount.Seconds())
}

// tasksSince names the REVISION every task the service created after `since` came from —
// what "two tasks" alone does not say, and the detail that identified the production bug:
// the extra task ran the OLD revision.
func (d *liveDeploy) tasksSince(service string, since time.Time) []string {
	d.t.Helper()
	var arns []string
	for _, st := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		out, err := d.ecs.ListTasks(d.ctx, &ecs.ListTasksInput{
			Cluster: aws.String(d.cluster), ServiceName: aws.String(service), DesiredStatus: st,
		})
		if err != nil {
			d.t.Logf("list %s tasks of %s: %v", st, service, err)
			continue
		}
		arns = append(arns, out.TaskArns...)
	}
	if len(arns) == 0 {
		return nil
	}
	out, err := d.ecs.DescribeTasks(d.ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(d.cluster), Tasks: arns,
	})
	if err != nil {
		d.t.Logf("describe tasks of %s: %v", service, err)
		return nil
	}
	type rec struct {
		at   time.Time
		line string
	}
	var recs []rec
	for _, task := range out.Tasks {
		if task.CreatedAt == nil || task.CreatedAt.Before(since) {
			continue
		}
		rev := aws.ToString(task.TaskDefinitionArn)
		if i := strings.LastIndex(rev, "/"); i >= 0 {
			rev = rev[i+1:]
		}
		recs = append(recs, rec{*task.CreatedAt, fmt.Sprintf("%s  %s  %s (%s)",
			task.CreatedAt.Format(time.TimeOnly), rev, aws.ToString(task.LastStatus), aws.ToString(task.StoppedReason))})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].at.Before(recs[j].at) })
	lines := make([]string, 0, len(recs))
	for _, r := range recs {
		lines = append(lines, r.line)
	}
	return lines
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
