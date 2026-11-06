package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// credEnv is one start's worth of credential variables, with values that cannot be mistaken
// for anything else so a leak is found by substring.
func credEnv() []string {
	var env []string
	for _, n := range secretEnvNames {
		env = append(env, n+"=value-of-"+strings.ToLower(n))
	}
	return append(env, "AF_CP_BASE_URL=https://cp.example", "AF_PREVIEW_PORTS=3000")
}

func hasSecret(secrets []ecstypes.Secret, name string) (string, bool) {
	for _, s := range secrets {
		if aws.ToString(s.Name) == name {
			return aws.ToString(s.ValueFrom), true
		}
	}
	return "", false
}

// Fargate: no credential value may reach the task definition's environment, and each
// reaches the container as an SSM SecureString referenced by `secrets`.
func TestECSCredentialEnvGoesThroughSecretsNotEnvironment(t *testing.T) {
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	rt.extraEnv = credEnv()
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fe.regCalls) != 1 {
		t.Fatalf("RegisterTaskDefinition calls = %d", len(fe.regCalls))
	}
	c := fe.regCalls[0].ContainerDefinitions[0]
	for _, kv := range c.Environment {
		if IsSecretEnv(aws.ToString(kv.Name)) || strings.Contains(aws.ToString(kv.Value), "value-of-") {
			t.Errorf("credential in the task definition's environment: %s", aws.ToString(kv.Name))
		}
	}
	if ecsEnvValue(c.Environment, "AF_CP_BASE_URL") != "https://cp.example" {
		t.Error("a plain variable must stay in environment")
	}
	for _, n := range secretEnvNames {
		from, ok := hasSecret(c.Secrets, n)
		if !ok {
			t.Errorf("%s missing from secrets", n)
			continue
		}
		if want := "/af-ws/af-ws-acme-alice/" + secretEnvParamSuffix(n); from != want {
			t.Errorf("%s valueFrom = %q, want %q", n, from, want)
		}
		if got := fs.values[from]; got != "value-of-"+strings.ToLower(n) {
			t.Errorf("%s parameter holds %q", n, got)
		}
	}
	for _, p := range fs.puts {
		if p.Type != "SecureString" {
			t.Errorf("param %s is %q, want SecureString", aws.ToString(p.Name), p.Type)
		}
	}
}

// A credential a later start no longer carries has its parameter deleted, not left behind.
func TestECSStaleCredentialParameterIsDropped(t *testing.T) {
	fe, ff, fs := &fakeECS{}, &fakeEFS{}, &fakeSSM{}
	rt := newTestECS(fe, ff, fs)
	rt.extraEnv = credEnv()
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	memo := "/af-ws/af-ws-acme-alice/af-memo-token"
	if _, ok := fs.values[memo]; !ok {
		t.Fatal("memo parameter was not written")
	}
	rt2 := newTestECS(&fakeECS{}, &fakeEFS{}, fs)
	rt2.extraEnv = []string{"AF_SCHEDULE_TOKEN=s"} // public base unset: only one credential
	if _, err := rt2.putSecrets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.values[memo]; ok {
		t.Error("the memo parameter of the earlier start was left behind")
	}
	if fs.values["/af-ws/af-ws-acme-alice/af-schedule-token"] != "s" {
		t.Error("the carried credential must be (re)written")
	}
}

// ECS-EC2 builds its own task definition: the same rule.
func TestECSEC2TaskDefCarriesNoCredentialInEnvironment(t *testing.T) {
	base := newTestECS(&fakeECS{}, &fakeEFS{}, &fakeSSM{})
	base.extraEnv = credEnv()
	e := &ecsEC2Runtime{base: base, pool: ec2PoolConfig{}}
	secrets, err := base.putSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	in := e.buildTaskDef(context.Background(), ec2Placement{instanceID: "i-1"}, ec2Prep{secrets: secrets})
	c := in.ContainerDefinitions[0]
	for _, kv := range c.Environment {
		if IsSecretEnv(aws.ToString(kv.Name)) || strings.Contains(aws.ToString(kv.Value), "value-of-") {
			t.Errorf("credential in the task definition's environment: %s", aws.ToString(kv.Name))
		}
	}
	for _, n := range secretEnvNames {
		if _, ok := hasSecret(c.Secrets, n); !ok {
			t.Errorf("%s missing from secrets", n)
		}
	}
	// The fingerprint hashes the secrets list; sorted by name it must not depend on order.
	env := credEnv()
	plain := env[len(secretEnvNames):] // the two plain entries keep their order
	rev := make([]string, 0, len(env))
	for i := len(secretEnvNames) - 1; i >= 0; i-- {
		rev = append(rev, env[i])
	}
	base.extraEnv = append(rev, plain...)
	secrets2, _ := base.putSecrets(context.Background())
	if taskDefFingerprint(in) != taskDefFingerprint(e.buildTaskDef(context.Background(), ec2Placement{instanceID: "i-1"}, ec2Prep{secrets: secrets2})) {
		t.Error("the task definition fingerprint depends on the order the CP listed its credentials")
	}
}

// docker: nothing on the command line carries a credential value; the 0600 env file does.
func TestDockerCredentialEnvIsInTheEnvFileNotArgv(t *testing.T) {
	d := &dockerRuntime{token: "agent-token-value", extraEnv: credEnv()}
	for _, a := range d.envArgs() {
		if strings.Contains(a, "value-of-") || IsSecretEnv(strings.SplitN(a, "=", 2)[0]) {
			t.Errorf("credential on the docker command line: %s", strings.SplitN(a, "=", 2)[0])
		}
	}
	args := strings.Join(d.envArgs(), " ")
	if !strings.Contains(args, "AF_CP_BASE_URL=https://cp.example") {
		t.Errorf("a plain variable must stay a -e argument: %s", args)
	}
	ef, err := d.writeSecretEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ef)
	if st, err := os.Stat(ef); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("env file mode: %v %v", st, err)
	}
	b, _ := os.ReadFile(ef)
	for _, n := range secretEnvNames {
		if !strings.Contains(string(b), n+"=value-of-"+strings.ToLower(n)+"\n") {
			t.Errorf("%s not in the env file", n)
		}
	}
}

var _ = ecs.RegisterTaskDefinitionInput{}
