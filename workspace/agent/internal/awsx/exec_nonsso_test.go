package awsx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	deployAccount = "222233334444"
	deployARN     = "arn:aws:sts::222233334444:assumed-role/deployer/botocore-session-1"
)

// deployProfile is [profile prod] as a role assumed from the source profile "src".
var deployProfile = map[string]string{
	"role_arn": "arn:aws:iam::222233334444:role/deploy/deployer", "source_profile": "src", "region": "ap-northeast-1",
}

// fakeDeploy is fakeAWS for a non-SSO profile: config and credentials get the extra
// sections, and STS reports arn.
func fakeDeploy(t *testing.T, cfg map[string]string, config, credentials, arn string) (bin, state string) {
	t.Helper()
	bin, state = fakeAWS(t, cfg)
	aws := filepath.Join(os.Getenv("HOME"), ".aws")
	appendFile(t, filepath.Join(aws, "config"), config)
	appendFile(t, filepath.Join(aws, "credentials"), credentials)
	for name, body := range map[string]string{"loggedIn": "", "arn": arn} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bin, state
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + text); err != nil {
		t.Fatal(err)
	}
}

const srcConfig = "[profile src]\nregion = us-east-1\n"
const srcKeys = "[src]\naws_access_key_id = AKIASOURCE\naws_secret_access_key = source-secret\n"

func cliCalls(state string) int {
	b, _ := os.ReadFile(filepath.Join(state, "calls"))
	return strings.Count(string(b), "x")
}

func TestPlanExecRunsARoleAssumedFromASourceProfile(t *testing.T) {
	bin, state := fakeDeploy(t, deployProfile, srcConfig, srcKeys, deployARN)
	var stderr bytes.Buffer
	env := append(append([]string{}, workloadEnv...), "AWS_ENDPOINT_URL=http://127.0.0.1:9", "AWS_ENDPOINT_URL_STS=http://127.0.0.1:9")
	prog, argv, childEnv, err := PlanExec(bin, env, ExecOptions{Profile: "prod", Account: deployAccount, Settings: map[string]Profile{},
		Login: "never", Argv: []string{"true"}, Stderr: &stderr, CredentialHelper: "/bin/helper aws-env-credentials"})
	if err != nil {
		t.Fatal(err)
	}
	if prog == "" || len(argv) != 1 {
		t.Fatalf("prog %q argv %v", prog, argv)
	}
	m := envMap(childEnv)
	if m["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" || m["AWS_SESSION_TOKEN"] != "tok" || m[execKeyIDVar] != "ASIAFAKE" {
		t.Fatalf("child credentials: %v", m)
	}
	if m["AWS_REGION"] != "ap-northeast-1" || m["AWS_SHARED_CREDENTIALS_FILE"] != os.DevNull {
		t.Fatalf("child env: %v", m)
	}
	// The child gets the one-profile config, never the member's files with the source keys.
	if cfg, _ := os.ReadFile(m["AWS_CONFIG_FILE"]); strings.Contains(string(cfg), "source_profile") || !strings.Contains(string(cfg), "credential_process") {
		t.Fatalf("child config %s:\n%s", m["AWS_CONFIG_FILE"], cfg)
	}
	for k := range m {
		if strings.HasPrefix(k, "AWS_ENDPOINT_URL") {
			t.Fatalf("child kept %s", k)
		}
	}
	// The export resolves the member's own config, with no endpoint override anywhere.
	seen, _ := os.ReadFile(filepath.Join(state, "seenEnv.configure-export-credentials"))
	aws := filepath.Join(os.Getenv("HOME"), ".aws")
	if !strings.Contains(string(seen), "AWS_CONFIG_FILE="+filepath.Join(aws, "config")+"\n") ||
		!strings.Contains(string(seen), "AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(aws, "credentials")+"\n") {
		t.Fatalf("export saw another config:\n%s", seen)
	}
	if !strings.Contains(string(seen), "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true") || strings.Contains(string(seen), "AWS_ENDPOINT_URL") {
		t.Fatalf("export env:\n%s", seen)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "leaked")); len(b) != 0 {
		t.Fatalf("the aws CLI calls saw workload credentials:\n%s", b)
	}
	if !strings.Contains(stderr.String(), "running as "+deployARN) || strings.Contains(stderr.String(), "sekret") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if n := cliCalls(state); n != 2 {
		t.Fatalf("aws was started %d times, want 2", n)
	}
}

func TestPlanExecRunsACredentialProcessProfile(t *testing.T) {
	bin, _ := fakeDeploy(t, map[string]string{"credential_process": "/usr/local/bin/get-creds"}, "", "",
		"arn:aws:sts::222233334444:assumed-role/whatever/x")
	_, _, env, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Login: "never", Argv: []string{"true"}, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(env)["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" {
		t.Fatal("no credentials")
	}
}

// Everything here is refused from the files alone, before the CLI is started: a
// credential fetched for a profile that is then refused has already run its
// credential_process or assume-role for nothing.
func TestPlanExecRefusesANonSSOProfileBeforeFetching(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg                    map[string]string
		config, creds, account string
		want                   string
	}{
		"no --account":                 {deployProfile, srcConfig, srcKeys, "", "--account <id>"},
		"other --account":              {deployProfile, srcConfig, srcKeys, "999999999999", "not the 999999999999"},
		"credential_source on top":     {map[string]string{"role_arn": deployProfile["role_arn"], "credential_source": "EcsContainer"}, "", "", deployAccount, "credential_source"},
		"credential_source in chain":   {deployProfile, "[profile src]\nrole_arn = arn:aws:iam::1:role/r\ncredential_source = Ec2InstanceMetadata\n", "", deployAccount, "source_profile of the chain"},
		"credential_source in DEFAULT": {deployProfile, srcConfig + "\n[DEFAULT]\ncredential_source = Environment\n", srcKeys, deployAccount, "[DEFAULT]"},
		"web identity in chain":        {deployProfile, "[profile src]\nrole_arn = arn:aws:iam::1:role/r\nweb_identity_token_file = /t\n", "", deployAccount, "web_identity_token_file"},
		"mfa_serial":                   {map[string]string{"role_arn": deployProfile["role_arn"], "source_profile": "src", "mfa_serial": "arn:aws:iam::1:mfa/me"}, srcConfig, srcKeys, deployAccount, "mfa_serial"},
		"loop":                         {deployProfile, "[profile src]\nrole_arn = arn:aws:iam::1:role/r\nsource_profile = prod\n", "", deployAccount, "loops"},
		"undefined source":             {deployProfile, "", "", deployAccount, "not defined"},
		"no source_profile":            {map[string]string{"role_arn": deployProfile["role_arn"]}, "", "", deployAccount, "without a source_profile"},
		"self source without keys":     {map[string]string{"role_arn": deployProfile["role_arn"], "source_profile": "prod"}, "", "", deployAccount, "names itself"},
		"not a role ARN":               {map[string]string{"role_arn": "arn:aws:iam::222233334444:user/me", "source_profile": "src"}, srcConfig, srcKeys, deployAccount, "not an IAM role ARN"},
		"empty role_arn":               {map[string]string{"raw:role_arn =": "", "source_profile": "src"}, srcConfig, srcKeys, deployAccount, "not an IAM role ARN"},
	} {
		bin, state := fakeDeploy(t, tc.cfg, tc.config, tc.creds, deployARN)
		_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: tc.account, Login: "always", Argv: []string{"true"}, Quiet: true})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
		if n := cliCalls(state); n != 0 {
			t.Errorf("%s: aws was started %d times", name, n)
		}
	}
}

func TestPlanExecRefusesANonSSOIdentityOutsideTheAccount(t *testing.T) {
	for name, arn := range map[string]string{
		"other account": "arn:aws:sts::999999999999:assumed-role/deployer/s",
		"other role":    "arn:aws:sts::222233334444:assumed-role/admin/s",
		"not an ARN":    "garbage",
	} {
		bin, _ := fakeDeploy(t, deployProfile, srcConfig, srcKeys, arn)
		_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Login: "never", Argv: []string{"true"}, Quiet: true})
		if err == nil || !strings.Contains(err.Error(), "refuses it") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestPlanExecRefusesLongLivedKeys(t *testing.T) {
	bin, state := fakeDeploy(t, map[string]string{"credential_process": "/usr/local/bin/get-creds"}, "", "", deployARN)
	onExport := `echo '{"Version":1,"AccessKeyId":"AKIALONG","SecretAccessKey":"long-secret"}'; exit 0` + "\n"
	if err := os.WriteFile(filepath.Join(state, "onExport"), []byte(onExport), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Login: "never", Argv: []string{"true"}, Quiet: true})
	if err == nil || !strings.Contains(err.Error(), "long-lived keys") || strings.Contains(err.Error(), "long-secret") {
		t.Fatalf("err = %v", err)
	}
}

// A role assumed from an SSO profile needs that profile's login; exit 3 names it.
func TestPlanExecNamesTheSSOProfileAChainEndsIn(t *testing.T) {
	src := "[profile src]\nsso_session = af-prod\nsso_account_id = 123456789012\nsso_role_name = Dev\n"
	bin, state := fakeDeploy(t, deployProfile, src, "", deployARN)
	if err := os.Remove(filepath.Join(state, "loggedIn")); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Login: "auto", Argv: []string{"true"}, Quiet: true})
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "aws sso login --profile 'src'") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(state, "loginArgs")); serr == nil {
		t.Fatal("a login was started without a terminal")
	}

	_, _, env, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Login: "always", Argv: []string{"true"}, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if args, _ := os.ReadFile(filepath.Join(state, "loginArgs")); !strings.Contains(string(args), "--profile src") {
		t.Fatalf("login args = %q", args)
	}
	if envMap(env)["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" {
		t.Fatal("no credentials after login")
	}
}

// A Settings name stays the Settings profile: the member's own non-SSO definition of it
// is refused as before, --account or not.
func TestPlanExecRefusesANonSSODefinitionOfASettingsName(t *testing.T) {
	bin, state := fakeDeploy(t, deployProfile, srcConfig, srcKeys, deployARN)
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Settings: prodSettings,
		Login: "never", Argv: []string{"true"}, Quiet: true})
	if err == nil || !strings.Contains(err.Error(), "rename one of them") {
		t.Fatalf("err = %v", err)
	}
	if n := cliCalls(state); n != 0 {
		t.Fatalf("aws was started %d times", n)
	}
}

func TestRoleARNParts(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:iam::222233334444:role/deployer":           "222233334444 deployer",
		"arn:aws:iam::222233334444:role/deploy/x/deployer":  "222233334444 deployer",
		"arn:aws-cn:iam::222233334444:role/deployer":        "222233334444 deployer",
		"arn:aws:iam::222233334444:user/me":                 "",
		"arn:aws:sts::222233334444:assumed-role/deployer/s": "",
		"arn:aws:iam:::role/deployer":                       "",
		"arn:aws:iam::222233334444:role/":                   "",
		"arn:aws:iam::222233334444:role/x/":                 "",
	} {
		acct, name, ok := roleARNParts(arn)
		got := ""
		if ok {
			got = acct + " " + name
		}
		if got != want {
			t.Errorf("%s: %q, want %q", arn, got, want)
		}
	}
}
