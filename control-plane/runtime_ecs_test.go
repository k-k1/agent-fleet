package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// --- fakes for the narrow AWS ports; they record calls and return canned data so
// the ECS adapter's create-or-get / desired-count logic is testable off-AWS. ---

type fakeECS struct {
	services    map[string]ecstypes.Service // by service name
	createCalls []*ecs.CreateServiceInput
	updateCalls []*ecs.UpdateServiceInput
	regCalls    []*ecs.RegisterTaskDefinitionInput
	deleteCalls []*ecs.DeleteServiceInput
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	out := &ecs.DescribeServicesOutput{}
	for _, n := range in.Services {
		if s, ok := f.services[n]; ok {
			out.Services = append(out.Services, s)
		}
	}
	return out, nil
}
func (f *fakeECS) CreateService(_ context.Context, in *ecs.CreateServiceInput, _ ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error) {
	f.createCalls = append(f.createCalls, in)
	if f.services == nil {
		f.services = map[string]ecstypes.Service{}
	}
	f.services[aws.ToString(in.ServiceName)] = ecstypes.Service{
		Status: aws.String("ACTIVE"), DesiredCount: aws.ToInt32(in.DesiredCount), RunningCount: 1,
	}
	return &ecs.CreateServiceOutput{}, nil
}
func (f *fakeECS) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.updateCalls = append(f.updateCalls, in)
	s := f.services[aws.ToString(in.Service)]
	s.DesiredCount = aws.ToInt32(in.DesiredCount)
	f.services[aws.ToString(in.Service)] = s
	return &ecs.UpdateServiceOutput{}, nil
}
func (f *fakeECS) RegisterTaskDefinition(_ context.Context, in *ecs.RegisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	f.regCalls = append(f.regCalls, in)
	return &ecs.RegisterTaskDefinitionOutput{
		TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("arn:task/" + aws.ToString(in.Family) + ":1")},
	}, nil
}

func (f *fakeECS) DeleteService(_ context.Context, in *ecs.DeleteServiceInput, _ ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error) {
	f.deleteCalls = append(f.deleteCalls, in)
	delete(f.services, aws.ToString(in.Service))
	return &ecs.DeleteServiceOutput{}, nil
}

type fakeEFS struct {
	aps         []efstypes.AccessPointDescription
	createCalls []*efs.CreateAccessPointInput
	deleteCalls []*efs.DeleteAccessPointInput
	n           int
}

func (f *fakeEFS) DescribeAccessPoints(_ context.Context, _ *efs.DescribeAccessPointsInput, _ ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error) {
	// A COPY, like the real API: a caller that deletes while iterating its own listing
	// (Destroy does exactly that) must not have the slice change under it.
	return &efs.DescribeAccessPointsOutput{AccessPoints: append([]efstypes.AccessPointDescription(nil), f.aps...)}, nil
}
func (f *fakeEFS) CreateAccessPoint(_ context.Context, in *efs.CreateAccessPointInput, _ ...func(*efs.Options)) (*efs.CreateAccessPointOutput, error) {
	f.createCalls = append(f.createCalls, in)
	f.n++
	id := aws.String("fsap-" + string(rune('0'+f.n)))
	// Reflect the new AP into the listing so a second ensure reuses it.
	f.aps = append(f.aps, efstypes.AccessPointDescription{AccessPointId: id, Tags: in.Tags})
	return &efs.CreateAccessPointOutput{AccessPointId: id}, nil
}

func (f *fakeEFS) DeleteAccessPoint(_ context.Context, in *efs.DeleteAccessPointInput, _ ...func(*efs.Options)) (*efs.DeleteAccessPointOutput, error) {
	f.deleteCalls = append(f.deleteCalls, in)
	var kept []efstypes.AccessPointDescription
	for _, ap := range f.aps {
		if aws.ToString(ap.AccessPointId) != aws.ToString(in.AccessPointId) {
			kept = append(kept, ap)
		}
	}
	f.aps = kept
	return &efs.DeleteAccessPointOutput{}, nil
}

type fakeSSM struct {
	puts    []*ssm.PutParameterInput
	deletes []*ssm.DeleteParameterInput
}

func (f *fakeSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	f.puts = append(f.puts, in)
	return &ssm.PutParameterOutput{}, nil
}

func (f *fakeSSM) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	f.deletes = append(f.deletes, in)
	return &ssm.DeleteParameterOutput{}, nil
}

func newTestECS(fe *fakeECS, ff *fakeEFS, fs *fakeSSM) *ecsRuntime {
	if fe.services == nil {
		fe.services = map[string]ecstypes.Service{}
	}
	return &ecsRuntime{
		cfg: ecsConfig{
			region: "ap-northeast-1", cluster: "clu", subnets: []string{"sub-1"},
			securityGroup: "sg-ws", efsFileSystem: "fs-1", namespaceArn: "arn:ns",
			execRole: "arn:exec", taskRole: "arn:task", logGroup: "/af/ws",
			workspaceImage: "ecr/af-workspace:dev", cpu: "1024", memory: "2048",
			posixUID: 1000, posixGID: 1000, startTimeout: time.Second,
		},
		ecs: fe, efs: ff, ssm: fs,
		name: "af-ws-acme-alice", membershipID: "M-1", token: "tok", secretKey: "dek",
		waitReady: func(context.Context, string, time.Duration) error { return nil },
	}
}

func TestECSEndpointNameToken(t *testing.T) {
	rt := newTestECS(&fakeECS{}, &fakeEFS{}, &fakeSSM{})
	if got := rt.Endpoint(); got != "http://af-ws-acme-alice:7700" {
		t.Errorf("Endpoint = %q", got)
	}
	if rt.Name() != "af-ws-acme-alice" || rt.Token() != "tok" {
		t.Errorf("Name/Token = %q/%q", rt.Name(), rt.Token())
	}
}

func TestECSStartCreatesEverything(t *testing.T) {
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Two EFS access points (home + claude), tagged per membership.
	if len(ff.createCalls) != 2 {
		t.Fatalf("CreateAccessPoint calls = %d, want 2", len(ff.createCalls))
	}
	roles := map[string]bool{}
	for _, c := range ff.createCalls {
		roles[tagValue(c.Tags, "af-role")] = true
		if tagValue(c.Tags, "af-membership") != "M-1" {
			t.Errorf("access point not tagged with membership: %v", c.Tags)
		}
	}
	if !roles["home"] || !roles["claude"] {
		t.Errorf("access-point roles = %v, want home+claude", roles)
	}
	// Two SSM SecureString params (token + DEK).
	if len(fs.puts) != 2 {
		t.Fatalf("PutParameter calls = %d, want 2", len(fs.puts))
	}
	for _, p := range fs.puts {
		if p.Type != "SecureString" {
			t.Errorf("param %s type = %q, want SecureString", aws.ToString(p.Name), p.Type)
		}
	}
	// Task def registered with EFS volumes + secrets + the workspace image.
	if len(fe.regCalls) != 1 {
		t.Fatalf("RegisterTaskDefinition calls = %d, want 1", len(fe.regCalls))
	}
	td := fe.regCalls[0]
	if len(td.Volumes) != 2 {
		t.Errorf("task def volumes = %d, want 2", len(td.Volumes))
	}
	c0 := td.ContainerDefinitions[0]
	if aws.ToString(c0.Image) != "ecr/af-workspace:dev" || len(c0.Secrets) != 2 {
		t.Errorf("container image/secrets = %q/%d", aws.ToString(c0.Image), len(c0.Secrets))
	}
	// Graceful two-stage stop wiring (§20b.7.8 停止改訂): SIGTERM → stopTimeout →
	// SIGKILL, with docker --init parity so the signal actually reaches the Agent,
	// and the Agent's own budget under the runtime grace.
	if aws.ToInt32(c0.StopTimeout) != 30 {
		t.Errorf("StopTimeout = %d, want 30 (AF_STOP_GRACE_SEC default)", aws.ToInt32(c0.StopTimeout))
	}
	if c0.LinuxParameters == nil || !aws.ToBool(c0.LinuxParameters.InitProcessEnabled) {
		t.Errorf("InitProcessEnabled not set — SIGTERM would be suppressed for a PID-1 agent")
	}
	agentGrace := ""
	for _, kv := range c0.Environment {
		if aws.ToString(kv.Name) == "AGENT_STOP_GRACE_SEC" {
			agentGrace = aws.ToString(kv.Value)
		}
	}
	if agentGrace != "25" {
		t.Errorf("AGENT_STOP_GRACE_SEC = %q, want 25 (grace - safety margin)", agentGrace)
	}
	// Service created (first use), desired 1, with Service Connect advertising the name.
	if len(fe.createCalls) != 1 || len(fe.updateCalls) != 0 {
		t.Fatalf("create/update = %d/%d, want 1/0", len(fe.createCalls), len(fe.updateCalls))
	}
	sc := fe.createCalls[0].ServiceConnectConfiguration
	if !sc.Enabled || aws.ToString(sc.Services[0].ClientAliases[0].DnsName) != "af-ws-acme-alice" {
		t.Errorf("service connect misconfigured: %+v", sc)
	}
	if aws.ToInt32(fe.createCalls[0].DesiredCount) != 1 {
		t.Errorf("desiredCount = %d, want 1", aws.ToInt32(fe.createCalls[0].DesiredCount))
	}
}

func TestECSStartNonFatalWhenAgentNotReady(t *testing.T) {
	// A large image cold-pull can outlast the readiness budget. Start must still
	// succeed (service is at desired 1; the workspace converges) rather than flip a
	// starting workspace to "failed" (P3-7 段5 finding A). The watch is asynchronous
	// now, so wait for it to actually run before asserting.
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	watched := make(chan struct{})
	rt.waitReady = func(context.Context, string, time.Duration) error {
		close(watched)
		return fmt.Errorf("agent did not become healthy within 90s")
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail on readiness timeout, got %v", err)
	}
	select {
	case <-watched:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness watch never ran")
	}
	if len(fe.createCalls) != 1 {
		t.Errorf("service should still be created (desired 1), createCalls=%d", len(fe.createCalls))
	}
}

func TestECSStartDoesNotBlockOnReadiness(t *testing.T) {
	// The 504 fix (docs/62 §62.5): Start runs inside the HTTP request, and the ALB in
	// front of the CP has a 60s idle timeout, so Start must return as soon as the
	// service is at desiredCount 1 — never sit on the readiness poll, whose budget is
	// deliberately longer than any single request may take.
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	rt.cfg.startTimeout = 5 * time.Minute
	budgets := make(chan time.Duration, 1)
	release := make(chan struct{})
	rt.waitReady = func(_ context.Context, _ string, d time.Duration) error {
		budgets <- d
		<-release // a cold pull the poll never sees finish
		return nil
	}
	defer close(release)

	done := make(chan error, 1)
	go func() { done <- rt.Start(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start blocked on the readiness wait — this is what returns 504 through the ALB")
	}
	select {
	case d := <-budgets:
		if d != 5*time.Minute {
			t.Errorf("readiness watch budget = %s, want the full startTimeout (5m)", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readiness watch never ran")
	}
	// Start returning early must not have skipped the launch itself.
	if len(fe.createCalls) != 1 || aws.ToInt32(fe.createCalls[0].DesiredCount) != 1 {
		t.Errorf("service not created at desired 1: %+v", fe.createCalls)
	}
}

func TestECSStartReadinessWatchOutlivesCallerContext(t *testing.T) {
	// The watch must survive the caller's context: Start's ctx is the HTTP request's
	// (and the lifecycle lease's), both of which end the moment the response is
	// written — a watch attached to it would be canceled before the pull finishes.
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	seen := make(chan context.Context, 1)
	rt.waitReady = func(c context.Context, _ string, _ time.Duration) error {
		seen <- c
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var watchCtx context.Context
	select {
	case watchCtx = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness watch never ran")
	}
	cancel() // the request is answered; the workspace is still pulling
	if err := watchCtx.Err(); err != nil {
		t.Errorf("readiness watch context died with the caller: %v", err)
	}
}

func TestECSStartReusesAccessPointsAndUpdatesService(t *testing.T) {
	// Pre-existing APs (both roles) and an ACTIVE service = a warm restart.
	ff := &fakeEFS{aps: []efstypes.AccessPointDescription{
		{AccessPointId: aws.String("fsap-home"), Tags: tags("M-1", "home")},
		{AccessPointId: aws.String("fsap-claude"), Tags: tags("M-1", "claude")},
	}}
	fe := &fakeECS{services: map[string]ecstypes.Service{
		"af-ws-acme-alice": {Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 0},
	}}
	rt := newTestECS(fe, ff, &fakeSSM{})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(ff.createCalls) != 0 {
		t.Errorf("CreateAccessPoint calls = %d, want 0 (reuse by tag)", len(ff.createCalls))
	}
	if len(fe.createCalls) != 0 || len(fe.updateCalls) != 1 {
		t.Fatalf("create/update = %d/%d, want 0/1 (update existing)", len(fe.createCalls), len(fe.updateCalls))
	}
	if aws.ToInt32(fe.updateCalls[0].DesiredCount) != 1 {
		t.Errorf("update desiredCount = %d, want 1", aws.ToInt32(fe.updateCalls[0].DesiredCount))
	}
}

func TestECSStartWhileStartingIsNoop(t *testing.T) {
	// A second Start while the service is converging (desired 1, task still
	// pulling) must not double-drive it: re-registering the task def and forcing a
	// new deployment would restart the cold pull from zero.
	fe := &fakeECS{services: map[string]ecstypes.Service{
		"af-ws-acme-alice": {Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0},
	}}
	ff, fs := &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start while starting: %v", err)
	}
	if len(fe.createCalls) != 0 || len(fe.updateCalls) != 0 || len(fe.regCalls) != 0 {
		t.Errorf("Start while starting must be a no-op, got create/update/register = %d/%d/%d",
			len(fe.createCalls), len(fe.updateCalls), len(fe.regCalls))
	}
	if len(ff.createCalls) != 0 || len(fs.puts) != 0 {
		t.Errorf("no EFS/SSM side effects expected, got %d/%d", len(ff.createCalls), len(fs.puts))
	}
}

func TestECSStopScalesToZero(t *testing.T) {
	fe := &fakeECS{services: map[string]ecstypes.Service{
		"af-ws-acme-alice": {Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1},
	}}
	rt := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(fe.updateCalls) != 1 || aws.ToInt32(fe.updateCalls[0].DesiredCount) != 0 {
		t.Errorf("Stop should UpdateService desired 0, got %+v", fe.updateCalls)
	}
}

func TestECSStopMissingServiceIsNoop(t *testing.T) {
	fe := &fakeECS{}
	rt := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on missing service: %v", err)
	}
	if len(fe.updateCalls) != 0 {
		t.Errorf("Stop on missing service should not UpdateService")
	}
}

func TestECSState(t *testing.T) {
	cases := []struct {
		name string
		svc  *ecstypes.Service
		want string
	}{
		{"missing", nil, "none"},
		{"inactive", &ecstypes.Service{Status: aws.String("INACTIVE"), DesiredCount: 1, RunningCount: 1}, "none"},
		{"stopped", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}, "stopped"},
		// desired 1 with no RUNNING task = a converging launch (e.g. the multi-minute
		// Fargate cold image pull). Reported as its own state — not "stopped" — so the
		// Console shows 起動中 and the reaper/autostart keep their hands off.
		{"starting", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0}, "starting"},
		{"running", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}, "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeECS{services: map[string]ecstypes.Service{}}
			if tc.svc != nil {
				fe.services["af-ws-acme-alice"] = *tc.svc
			}
			rt := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
			if got := rt.State(context.Background()); got != tc.want {
				t.Errorf("State = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestECSSecretsSkippedWhenEmpty(t *testing.T) {
	fs := &fakeSSM{}
	rt := newTestECS(&fakeECS{}, &fakeEFS{}, fs)
	rt.token, rt.secretKey = "", "" // dev: no token / no DEK
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fs.puts) != 0 {
		t.Errorf("PutParameter calls = %d, want 0 (no secrets in dev)", len(fs.puts))
	}
}

func tags(membership, role string) []efstypes.Tag {
	return []efstypes.Tag{
		{Key: aws.String("af-membership"), Value: aws.String(membership)},
		{Key: aws.String("af-role"), Value: aws.String(role)},
	}
}

// The Fargate adapter has the same leak the EC2 one had: an ECS service, two EFS access
// points and two SSM SecureStrings that outlive the membership (docs/64 §64.18.1). What
// it CANNOT do is delete the home itself — the EFS directories survive their access
// points — so those come back as reported leftovers rather than as a silent success.
func TestECSDestroyRemovesServiceAccessPointsAndSecrets(t *testing.T) {
	ctx := context.Background()
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	fe.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	ff.aps = []efstypes.AccessPointDescription{
		{AccessPointId: aws.String("fsap-home"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}}},
		{AccessPointId: aws.String("fsap-claude"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/claude-config/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}}},
		{AccessPointId: aws.String("fsap-other"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home/M-9")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-9")}}},
	}

	leftovers, err := rt.Destroy(ctx)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := fe.services["af-ws-acme-alice"]; ok {
		t.Error("service survived Destroy")
	}
	// Scaled to zero before the delete: a forced delete of a service with a running task
	// kills the container without the Agent's graceful shutdown.
	if len(fe.updateCalls) != 1 || aws.ToInt32(fe.updateCalls[0].DesiredCount) != 0 {
		t.Errorf("Destroy must scale to zero first, updates = %#v", fe.updateCalls)
	}
	if len(ff.aps) != 1 || aws.ToString(ff.aps[0].AccessPointId) != "fsap-other" {
		t.Errorf("another membership's access point was touched, left = %d", len(ff.aps))
	}
	if len(fs.deletes) != 2 {
		t.Errorf("both SSM secrets must go, got %d", len(fs.deletes))
	}
	for _, want := range []string{"efs:fs-1/home/M-1", "efs:fs-1/claude-config/M-1"} {
		found := false
		for _, l := range leftovers {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("leftover %q not reported (the operator would think the home is gone), got %v", want, leftovers)
		}
	}
}
