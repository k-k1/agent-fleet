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

type fakeEFS struct {
	aps         []efstypes.AccessPointDescription
	createCalls []*efs.CreateAccessPointInput
	n           int
}

func (f *fakeEFS) DescribeAccessPoints(_ context.Context, _ *efs.DescribeAccessPointsInput, _ ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error) {
	return &efs.DescribeAccessPointsOutput{AccessPoints: f.aps}, nil
}
func (f *fakeEFS) CreateAccessPoint(_ context.Context, in *efs.CreateAccessPointInput, _ ...func(*efs.Options)) (*efs.CreateAccessPointOutput, error) {
	f.createCalls = append(f.createCalls, in)
	f.n++
	id := aws.String("fsap-" + string(rune('0'+f.n)))
	// Reflect the new AP into the listing so a second ensure reuses it.
	f.aps = append(f.aps, efstypes.AccessPointDescription{AccessPointId: id, Tags: in.Tags})
	return &efs.CreateAccessPointOutput{AccessPointId: id}, nil
}

type fakeSSM struct{ puts []*ssm.PutParameterInput }

func (f *fakeSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	f.puts = append(f.puts, in)
	return &ssm.PutParameterOutput{}, nil
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
	// starting workspace to "failed" (P3-7 段5 finding A).
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	rt.waitReady = func(context.Context, string, time.Duration) error {
		return fmt.Errorf("agent did not become healthy within 90s")
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail on readiness timeout, got %v", err)
	}
	if len(fe.createCalls) != 1 {
		t.Errorf("service should still be created (desired 1), createCalls=%d", len(fe.createCalls))
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
		{"starting", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0}, "stopped"},
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
