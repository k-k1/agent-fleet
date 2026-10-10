package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// Every adapter carries AF_SECRET_KEY_NEXT through the same secret channel as AF_SECRET_KEY.
// An adapter that dropped it would start the Agent on the derived key alone, and a store
// already re-sealed under the home's key would not open.
var bothKeys = SecretKeys{Key: "dek-derived", Next: "dek-next"}

func TestSecretKeysEnvPairs(t *testing.T) {
	if got := (SecretKeys{}).envPairs(); len(got) != 0 {
		t.Fatalf("zero keys = %v, want nothing", got)
	}
	if got := (SecretKeys{Key: "k"}).envPairs(); len(got) != 1 || got[0] != [2]string{"AF_SECRET_KEY", "k"} {
		t.Fatalf("key only = %v", got)
	}
	got := bothKeys.envPairs()
	if len(got) != 2 || got[1] != [2]string{"AF_SECRET_KEY_NEXT", "dek-next"} {
		t.Fatalf("both = %v", got)
	}
}

func TestDockerSecretEnvFileCarriesNextKey(t *testing.T) {
	d := &dockerRuntime{keys: bothKeys}
	p, err := d.writeSecretEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AF_SECRET_KEY=dek-derived\n", "AF_SECRET_KEY_NEXT=dek-next\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("env file lacks %q", want)
		}
	}
}

func TestKubeSecretEnvCarriesNextKey(t *testing.T) {
	k := &kubeRuntime{cfg: &kubeConfig{}, keys: bothKeys}
	env := k.secretEnv()
	if env["AF_SECRET_KEY"] != "dek-derived" || env["AF_SECRET_KEY_NEXT"] != "dek-next" {
		t.Fatalf("secret env = %v", env)
	}
}

func TestNativeEnvCarriesNextKey(t *testing.T) {
	n := &nativeRuntime{keys: bothKeys}
	env := map[string]string{}
	n.overlayWorkspaceEnv(env)
	if env["AF_SECRET_KEY"] != "dek-derived" || env["AF_SECRET_KEY_NEXT"] != "dek-next" {
		t.Fatalf("env = %v", env)
	}
}

func TestECSSecretsCarryNextKey(t *testing.T) {
	fs := &fakeSSM{}
	rt := newTestECS(&fakeECS{}, &fakeEFS{}, fs)
	rt.keys = bothKeys
	secrets, err := rt.putSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{}
	for _, s := range secrets {
		refs[aws.ToString(s.Name)] = aws.ToString(s.ValueFrom)
	}
	if refs["AF_SECRET_KEY_NEXT"] != "/af-ws/af-ws-acme-alice/secret-key-next" {
		t.Fatalf("container secrets = %v, want AF_SECRET_KEY_NEXT from SSM", refs)
	}
	if fs.values["/af-ws/af-ws-acme-alice/secret-key-next"] != "dek-next" {
		t.Fatal("the next key was not written to its SecureString")
	}
	// Without a next key nothing references the parameter.
	rt.keys = SecretKeys{Key: "dek-derived"}
	if secrets, err = rt.putSecrets(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if aws.ToString(s.Name) == "AF_SECRET_KEY_NEXT" {
			t.Fatal("AF_SECRET_KEY_NEXT injected with no next key")
		}
	}
}
