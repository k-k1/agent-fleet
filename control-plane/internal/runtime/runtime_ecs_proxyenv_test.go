package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

const testProxy = "http://proxy.internal:3128"

// testProxyEnv is what main.go generates for AF_EGRESS_PROXY_ADDR; testExtraEnv is the
// deployment env: an operator's WS_ENV entries with that list behind them.
var testProxyEnv = []string{
	"http_proxy=" + testProxy, "https_proxy=" + testProxy, "HTTP_PROXY=" + testProxy, "HTTPS_PROXY=" + testProxy,
	"no_proxy=localhost,127.0.0.1,::1", "NO_PROXY=localhost,127.0.0.1,::1",
}
var testExtraEnv = append([]string{"CLAUDE_INSTALL=0", "SECRET_FROM_WS_ENV=hunter2"}, testProxyEnv...)

// envOf flattens a container definition's environment into a map, failing on a name that
// appears twice: ECS does not define which of two entries wins.
func envOf(t *testing.T, c ecstypes.ContainerDefinition) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range c.Environment {
		k := aws.ToString(kv.Name)
		if _, dup := m[k]; dup {
			t.Errorf("environment names %s twice", k)
		}
		m[k] = aws.ToString(kv.Value)
	}
	return m
}

// ecsFactoryFor builds the Fargate factory the way the CP does (newECSFactory reads no
// network at boot) and swaps the AWS clients for the fakes.
func ecsFactoryFor(t *testing.T, extra, proxy []string, fe *fakeECS) *ecsFactory {
	t.Helper()
	t.Setenv("AWS_REGION", "ap-northeast-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "x")
	t.Setenv("AF_ECS_REGION", "ap-northeast-1")
	f, err := newECSFactory(Config{ExtraEnv: extra, EgressProxyEnv: proxy, Image: "ecr/af-workspace:dev"})
	if err != nil {
		t.Fatalf("newECSFactory: %v", err)
	}
	base := f.(*ecsFactory)
	ref := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
	base.cfg.cluster, base.cfg.subnets, base.cfg.efsFileSystem = ref.cfg.cluster, ref.cfg.subnets, ref.cfg.efsFileSystem
	base.ecs, base.efs, base.ssm, base.tasks = fe, ref.efs, ref.ssm, nil
	return base
}

var testWS = Workspace{ContainerName: "af-ws-acme-alice", MembershipID: "M-1", AgentToken: "tok"}

func TestECSFactoryPassesProxyEnv(t *testing.T) {
	fe := &fakeECS{}
	f := ecsFactoryFor(t, testExtraEnv, testProxyEnv, fe)
	rt := f.New(testWS, SecretKeys{Key: "dek"}, []string{"AF_CP_INTERNAL_URL=http://cp.internal:8081"})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	env := envOf(t, fe.regCalls[0].ContainerDefinitions[0])
	for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
		if env[k] != testProxy {
			t.Errorf("%s = %q, want %q", k, env[k], testProxy)
		}
	}
	if _, leaked := env["SECRET_FROM_WS_ENV"]; leaked {
		t.Error("WS_ENV reached the task definition's plain environment")
	}
	if _, leaked := env["CLAUDE_INSTALL"]; leaked {
		t.Error("WS_ENV entry CLAUDE_INSTALL reached the task definition")
	}
	for _, k := range []string{"no_proxy", "NO_PROXY"} {
		got := splitCSV(env[k])
		for _, want := range []string{"localhost", "127.0.0.1", "cp.internal", "169.254.170.2", "169.254.169.254"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s = %q, missing %s", k, env[k], want)
			}
		}
	}
	if env["AF_CP_INTERNAL_URL"] != "http://cp.internal:8081" {
		t.Errorf("per-start AF_CP_INTERNAL_URL lost: %q", env["AF_CP_INTERNAL_URL"])
	}
}

func TestECSFactoryPerStartEnvWinsOverTemplate(t *testing.T) {
	fe := &fakeECS{}
	f := ecsFactoryFor(t, testExtraEnv, testProxyEnv, fe)
	rt := f.New(testWS, SecretKeys{Key: "dek"}, []string{"https_proxy=http://other:1", "FOO=bar"})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	env := envOf(t, fe.regCalls[0].ContainerDefinitions[0])
	if env["https_proxy"] != "http://other:1" || env["HTTPS_PROXY"] != testProxy || env["FOO"] != "bar" {
		t.Errorf("https_proxy=%q HTTPS_PROXY=%q FOO=%q", env["https_proxy"], env["HTTPS_PROXY"], env["FOO"])
	}
}

func TestECSFactoryWithoutProxyChangesNothing(t *testing.T) {
	fe := &fakeECS{}
	f := ecsFactoryFor(t, []string{"CLAUDE_INSTALL=0"}, nil, fe)
	rt := f.New(testWS, SecretKeys{Key: "dek"}, []string{"FOO=bar"})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	env := envOf(t, fe.regCalls[0].ContainerDefinitions[0])
	for k := range env {
		if strings.Contains(strings.ToLower(k), "proxy") || k == "CLAUDE_INSTALL" {
			t.Errorf("unexpected %s in the environment", k)
		}
	}
	if env["FOO"] != "bar" {
		t.Errorf("per-start env lost")
	}
}

// ecs-ec2 builds its own task definition (buildTaskDef) from the base runtime's env, so it
// is checked separately, and twice: the fingerprint that decides whether a start may reuse
// a revision must not move between two builds of the same inputs.
func TestECSEC2FactoryPassesProxyEnv(t *testing.T) {
	h := newEC2Harness(t)
	fe := &fakeECS{}
	base := ecsFactoryFor(t, testExtraEnv, testProxyEnv, fe)
	f := &ecsEC2Factory{base: base, ec2: h.ec2, ssmc: h.ssmc, ci: h.ci, pool: h.rt.pool, bg: h.rt.bg, sleep: h.rt.sleep}
	rt := f.New(testWS, SecretKeys{Key: "dek"}, []string{"AF_CP_INTERNAL_URL=http://cp.internal:8081", "http_proxy=http://other:1"}).(*ecsEC2Runtime)
	build := func() *ecs.RegisterTaskDefinitionInput {
		return rt.buildTaskDef(context.Background(), ec2Placement{instanceID: "i-1", az: "ap-northeast-1a"}, ec2Prep{})
	}
	in := build()
	env := envOf(t, in.ContainerDefinitions[0])
	if env["http_proxy"] != "http://other:1" || env["HTTPS_PROXY"] != testProxy {
		t.Errorf("http_proxy=%q HTTPS_PROXY=%q", env["http_proxy"], env["HTTPS_PROXY"])
	}
	if _, leaked := env["SECRET_FROM_WS_ENV"]; leaked {
		t.Error("WS_ENV reached the task definition")
	}
	for _, k := range []string{"no_proxy", "NO_PROXY"} {
		got := splitCSV(env[k])
		for _, want := range []string{"localhost", "cp.internal", "169.254.170.2", "169.254.169.254"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s = %q, missing %s", k, env[k], want)
			}
		}
	}
	if a, b := taskDefFingerprint(in), taskDefFingerprint(build()); a == "" || a != b {
		t.Errorf("fingerprint moved between identical builds: %q vs %q", a, b)
	}
}

// A proxy variable that comes from WS_ENV, with AF_EGRESS_PROXY_ADDR unset, is the
// operator's own credential-bearing value and must stay out of the task definition.
func TestECSFactoryIgnoresProxyVarsFromWSEnv(t *testing.T) {
	fe := &fakeECS{}
	f := ecsFactoryFor(t, []string{"https_proxy=http://user:password@proxy.internal:3128", "NO_PROXY=x"}, nil, fe)
	rt := f.New(testWS, SecretKeys{Key: "dek"}, nil)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, kv := range fe.regCalls[0].ContainerDefinitions[0].Environment {
		if strings.Contains(strings.ToLower(aws.ToString(kv.Name)), "proxy") || strings.Contains(aws.ToString(kv.Value), "password") {
			t.Errorf("WS_ENV value reached the task definition: %s", aws.ToString(kv.Name))
		}
	}
}
