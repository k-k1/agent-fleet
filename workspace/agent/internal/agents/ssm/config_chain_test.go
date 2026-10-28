package sessionx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Role chaining in an SSM session (issue #1109): the isolated config holds the source SSO
// profile and the chained profile, the login runs under the source, and no value that could
// carry a second INI key gets through.

func chainMeta() session.SSMMeta {
	return session.SSMMeta{Profile: "deploy", Target: "i-0123456789abcdef0", Region: "eu-west-1",
		SourceProfile: "sso-main", RoleARN: "arn:aws:iam::210987654321:role/deploy", ExternalID: "ext-1", RoleSessionName: "af", DurationSeconds: 3600,
		StartURL: "https://example.awsapps.com/start", SSORegion: "us-east-1", AccountID: "123456789012", RoleName: "Dev"}
}

func TestRenderSSMConfigForAChain(t *testing.T) {
	got, err := RenderSSMConfig(chainMeta())
	if err != nil {
		t.Fatal(err)
	}
	want := "[sso-session af-sso-main]\nsso_start_url = https://example.awsapps.com/start\nsso_region = us-east-1\nsso_registration_scopes = sso:account:access\n\n" +
		"[profile sso-main]\nsso_session = af-sso-main\nsso_account_id = 123456789012\nsso_role_name = Dev\nregion = us-east-1\n\n" +
		"[profile deploy]\nrole_arn = arn:aws:iam::210987654321:role/deploy\nsource_profile = sso-main\nexternal_id = ext-1\nrole_session_name = af\nduration_seconds = 3600\nregion = eu-west-1\n"
	if got != want {
		t.Fatalf("config =\n%s\nwant\n%s", got, want)
	}
	// An sso profile renders exactly as it did before role chaining existed.
	plain, err := RenderSSMConfig(session.SSMMeta{Profile: "p", StartURL: "https://x.awsapps.com/start", SSORegion: "us-east-1", AccountID: "1", RoleName: "R"})
	if err != nil || strings.Contains(plain, "role_arn") || !strings.HasPrefix(plain, "[sso-session af-p]\n") {
		t.Fatalf("plain = %q %v", plain, err)
	}
}

func TestRenderSSMConfigRefusesChainValuesThatCouldInjectKeys(t *testing.T) {
	for name, mut := range map[string]func(m *session.SSMMeta){
		"role arn":      func(m *session.SSMMeta) { m.RoleARN += "\ncredential_process = /bin/sh" },
		"external id":   func(m *session.SSMMeta) { m.ExternalID = "a\nb" },
		"session name":  func(m *session.SSMMeta) { m.RoleSessionName = "a\nb" },
		"source":        func(m *session.SSMMeta) { m.SourceProfile = "x\ny" },
		"source = self": func(m *session.SSMMeta) { m.SourceProfile = m.Profile },
		"duration":      func(m *session.SSMMeta) { m.DurationSeconds = 1 },
		"no source":     func(m *session.SSMMeta) { m.SourceProfile = "" },
	} {
		t.Run(name, func(t *testing.T) {
			m := chainMeta()
			mut(&m)
			if out, err := RenderSSMConfig(m); err == nil {
				t.Fatalf("rendered:\n%s", out)
			}
		})
	}
	// Chain fields without a role ARN are not silently dropped.
	m := chainMeta()
	m.RoleARN = ""
	if _, err := RenderSSMConfig(m); err == nil {
		t.Fatal("assume-role fields without a role ARN were accepted")
	}
}

func TestBuildSSMProgramLogsInToTheSourceOfAChain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, err := buildSSMProgram("chain1", chainMeta(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export AWS_PROFILE='deploy'; ",
		"aws sts get-caller-identity >/dev/null 2>&1 || { ",
		"export AWS_PROFILE='sso-main'; aws sso login --use-device-code --no-browser; export AWS_PROFILE='deploy'; }; ",
		"exec aws ssm start-session",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("program lacks %q:\n%s", want, p)
		}
	}
	if b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".aws", "af-sessions", "chain1.config")); err != nil || !strings.Contains(string(b), "source_profile = sso-main") {
		t.Fatalf("session config: %q %v", b, err)
	}
	f, err := buildSSMProgram("chain2", chainMeta(), true)
	if err != nil || !strings.Contains(f, "export AWS_PROFILE='sso-main'; "+ssmForgetLogin+"aws sso login") {
		t.Fatalf("forced program resets the wrong profile's login:\n%s %v", f, err)
	}
	// An sso session's program is untouched: no AWS_PROFILE switching around the login.
	plain, err := buildSSMProgram("plain", session.SSMMeta{Target: "i-0123456789abcdef0", Profile: "p1"}, false)
	if err != nil || strings.Count(plain, "export AWS_PROFILE") != 1 {
		t.Fatalf("plain program:\n%s %v", plain, err)
	}
}

// A same-named section in ~/.aws/credentials, or credentials in the environment, would
// override the role and source of the isolated config (measured with aws-cli 2.36.46), so a
// chained SSM session and its discovery resolve from the isolated config alone.
func TestChainIsolationDropsOtherCredentialChannels(t *testing.T) {
	dirty := []string{"PATH=/bin", "AWS_ACCESS_KEY_ID=AKIA", "AWS_SESSION_TOKEN=t", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/x",
		"AWS_ENDPOINT_URL_STS=http://evil", "AWS_SHARED_CREDENTIALS_FILE=/home/dev/.aws/credentials", "AWS_PROFILE=other", "AWS_REGION=eu-west-1"}
	got := map[string]string{}
	for _, kv := range ChainIsolatedEnv(dirty, "/cfg", "deploy") {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_ENDPOINT_URL_STS"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s survived", k)
		}
	}
	if got["AWS_SHARED_CREDENTIALS_FILE"] != os.DevNull || got["AWS_CONFIG_FILE"] != "/cfg" || got["AWS_PROFILE"] != "deploy" || got["AWS_REGION"] != "eu-west-1" {
		t.Errorf("env = %v", got)
	}

	// The pane's program does the same, before the isolated config is named.
	script := chainIsolationShell() + `echo "key=${AWS_ACCESS_KEY_ID-unset} ep=${AWS_ENDPOINT_URL_STS-unset} creds=$AWS_SHARED_CREDENTIALS_FILE"`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = dirty
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "key=unset ep=unset creds=/dev/null" {
		t.Fatalf("shell isolation: %q %v", out, err)
	}
	t.Setenv("HOME", t.TempDir())
	p, err := buildSSMProgram("iso", chainMeta(), false)
	if err != nil || strings.Index(p, chainIsolationShell()) < 0 || strings.Index(p, chainIsolationShell()) > strings.Index(p, "export AWS_CONFIG_FILE=") {
		t.Fatalf("program does not isolate before naming the config:\n%s %v", p, err)
	}
	plain, _ := buildSSMProgram("iso2", session.SSMMeta{Profile: "p", Target: "i-0123456789abcdef0", StartURL: "https://x.awsapps.com/start", SSORegion: "us-east-1"}, false)
	if strings.Contains(plain, "AWS_SHARED_CREDENTIALS_FILE") {
		t.Fatalf("an sso session's program changed:\n%s", plain)
	}
}
