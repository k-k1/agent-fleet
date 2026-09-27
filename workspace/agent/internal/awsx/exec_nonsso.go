package awsx

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A profile of the member's own that is not SSO — a role assumed from a source profile,
// or a credential_process — is how some deployments are reached at all (a deploy role in
// another organization's account, say). af-aws-exec runs one only when the caller names
// the account with --account, and only with temporary credentials whose STS identity is
// in that account. The property it keeps is the one it exists for: the command runs as
// that named profile or fails, never as the container's workload role.

// nonSSOProfile reports whether keys describe a profile the CLI resolves by assuming a
// role or running a credential_process, with no SSO setting at all. A profile that mixes
// the two stays with checkSSOProfile, which refuses it: the name would mean different
// identities to different tools.
func nonSSOProfile(keys map[string]string) bool {
	for k := range keys {
		if strings.HasPrefix(k, "sso_") {
			return false
		}
	}
	_, role := keys["role_arn"]
	_, process := keys["credential_process"]
	return role || process
}

// maxSourceChain bounds the source_profile walk; botocore itself stops only at a loop.
const maxSourceChain = 16

// checkSourceChain follows source_profile from the named profile the way botocore's
// assume-role provider does, and refuses anything along it that could end at an
// identity the member did not name. It returns the SSO profile the chain ends in, if it
// does, so an expired login there can be reported as a login.
func checkSourceChain(env []string, profile string, keys, origin map[string]string) (ssoRoot string, err error) {
	cur, seen := profile, map[string]bool{profile: true}
	for hop := 0; ; hop++ {
		in := fmt.Sprintf("profile %q", cur)
		if cur != profile {
			in = fmt.Sprintf("profile %q (source_profile of the chain from %q)", cur, profile)
		}
		// credential_source is how a profile asks for the container's own credentials
		// (EcsContainer, Ec2InstanceMetadata, Environment): the workload role this tool
		// keeps away from the member's commands.
		if _, set := keys["credential_source"]; set {
			return "", fmt.Errorf("%s sets credential_source (%s), which takes the workspace's own credentials, not yours; "+
				"af-aws-exec refuses it", in, where(origin["credential_source"]))
		}
		if _, set := keys["web_identity_token_file"]; set {
			return "", fmt.Errorf("%s sets web_identity_token_file (%s); af-aws-exec does not run web-identity profiles",
				in, where(origin["web_identity_token_file"]))
		}
		// The CLI would prompt for the code on a terminal the command does not have when an
		// agent runs it, and wait there.
		if _, set := keys["mfa_serial"]; set {
			return "", fmt.Errorf("%s sets mfa_serial (%s); af-aws-exec cannot answer an MFA prompt, so it refuses the profile",
				in, where(origin["mfa_serial"]))
		}
		role, hasRole := keys["role_arn"]
		if !hasRole {
			if keys["sso_session"] != "" || keys["sso_start_url"] != "" {
				return cur, nil
			}
			return "", nil
		}
		if scalar(role) == "" {
			return "", fmt.Errorf("%s has an empty or multi-line role_arn (%s)", in, where(origin["role_arn"]))
		}
		src := scalar(keys["source_profile"])
		if src == "" {
			return "", fmt.Errorf("%s sets role_arn without a source_profile; af-aws-exec only runs a role assumed from a source profile", in)
		}
		// botocore lets a profile be its own source when it holds static keys: the keys
		// assume the role.
		if src == cur {
			if keys["aws_access_key_id"] != "" {
				return "", nil
			}
			return "", fmt.Errorf("%s names itself as source_profile but has no keys of its own", in)
		}
		if seen[src] || hop >= maxSourceChain {
			return "", fmt.Errorf("the source_profile chain from profile %q loops back to %q", profile, src)
		}
		seen[src] = true
		if keys, origin, err = profileKeysFrom(env, src); err != nil {
			return "", err
		}
		if len(keys) == 0 {
			return "", fmt.Errorf("%s names source_profile %q, which is not defined", in, src)
		}
		cur = src
	}
}

// roleARNParts splits arn:<partition>:iam::<account>:role/<path/>name into the account
// and the role name, which is what an STS session of the role reports.
func roleARNParts(arn string) (account, name string, ok bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || !strings.HasPrefix(parts[1], "aws") || parts[2] != "iam" || parts[4] == "" {
		return "", "", false
	}
	res, found := strings.CutPrefix(parts[5], "role/")
	if !found || res == "" || strings.HasSuffix(res, "/") {
		return "", "", false
	}
	return parts[4], res[strings.LastIndexByte(res, '/')+1:], true
}

// callerAccount is the account of an STS caller ARN (arn:<partition>:sts::<account>:…).
func callerAccount(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sts" {
		return ""
	}
	return parts[4]
}

// ownFilesEnv is env for resolving the member's own profile: the config and credentials
// files checkSourceChain read, named explicitly so a HOME that differs from this
// process's cannot point the CLI at others, and no endpoint override from the
// environment or any config, so the STS calls of the assume-role (and the credentials
// they carry) go to AWS and nowhere else.
func ownFilesEnv(env []string) []string {
	cfg, creds := profileFiles(env)
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "AWS_CONFIG_FILE" || k == "AWS_SHARED_CREDENTIALS_FILE" || k == "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS" ||
			strings.HasPrefix(k, "AWS_ENDPOINT_URL") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "AWS_CONFIG_FILE="+expandHome(cfg), "AWS_SHARED_CREDENTIALS_FILE="+expandHome(creds),
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true")
}

// planNonSSO is PlanExec for a profile nonSSOProfile admits. There is no SSO-only
// config to rebuild it in (the source keys would have to be written out to a file), so
// the CLI resolves the member's own files; what stands in for that config is the
// environment baseEnv already emptied of every workload-role channel, the refusals of
// checkSourceChain, and the account STS reports afterwards.
func planNonSSO(awsBin string, env []string, keys, origin map[string]string, steered bool, o ExecOptions) (string, []string, []string, error) {
	roleAcct, roleName, isRole := "", "", false
	if r, set := keys["role_arn"]; set {
		if roleAcct, roleName, isRole = roleARNParts(scalar(r)); !isRole {
			return "", nil, nil, fmt.Errorf("profile %q has a role_arn that is not an IAM role ARN (%s)", o.Profile, where(origin["role_arn"]))
		}
	}
	if o.Account == "" {
		hint := ""
		if isRole {
			hint = fmt.Sprintf(" (its role_arn is in account %s)", roleAcct)
		}
		return "", nil, nil, fmt.Errorf("profile %q is not an SSO profile; af-aws-exec runs a role or credential_process profile "+
			"only with --account <id> naming the account it must be%s", o.Profile, hint)
	}
	if isRole && roleAcct != o.Account {
		return "", nil, nil, fmt.Errorf("profile %q assumes a role in account %s, not the %s given with --account", o.Profile, roleAcct, o.Account)
	}
	ssoRoot, err := checkSourceChain(env, o.Profile, keys, origin)
	if err != nil {
		return "", nil, nil, err
	}
	if err := checkRegion(env, keys, o); err != nil {
		return "", nil, nil, err
	}

	aws := awsRunner{bin: awsBin, env: ownFilesEnv(env)}
	creds, err := exportCreds(aws, o.Profile)
	if err != nil && ssoRoot != "" && loginNeeded(err.Error()) {
		// The Console login (ADR 0102) is for Settings profiles run by name; here the
		// member runs the login for the chain's SSO profile themselves.
		prefix := ""
		if steered {
			prefix = "AWS_CONFIG_FILE=~/.aws/config "
		}
		hint := fmt.Sprintf("%saws sso login --profile %s --use-device-code --no-browser", prefix, session.ShellQuote(ssoRoot))
		if o.Login != "always" && (o.Login == "never" || !o.Interactive) {
			return "", nil, nil, fmt.Errorf("%w for profile %q (the SSO profile %q its source_profile chain ends in): %v\nlog in with: %s",
				ErrLoginRequired, o.Profile, ssoRoot, err, hint)
		}
		if lerr := deviceLogin(awsBin, aws.env, ssoRoot, o.Stderr); lerr != nil {
			return "", nil, nil, fmt.Errorf("aws sso login for profile %s: %w", ssoRoot, lerr)
		}
		creds, err = exportCreds(aws, o.Profile)
	}
	if errors.Is(err, errNoSessionToken) {
		return "", nil, nil, fmt.Errorf("profile %q resolves to long-lived keys, and af-aws-exec passes only temporary credentials "+
			"(a role assumed from a source profile, or a credential_process that returns a session token)", o.Profile)
	}
	if err != nil {
		return "", nil, nil, fmt.Errorf("could not get credentials for profile %q: %v", o.Profile, err)
	}

	return planChild(awsBin, env, keys, creds, os.DevNull, o, func(arn string) error {
		if acct := callerAccount(arn); acct != o.Account {
			return fmt.Errorf("profile %q resolved to %s, which is account %s, not the %s given with --account; af-aws-exec refuses it",
				o.Profile, arn, orNone(acct), o.Account)
		}
		if isRole && !strings.Contains(arn, ":assumed-role/"+roleName+"/") {
			return fmt.Errorf("profile %q resolved to %s, not a session of its role %s; af-aws-exec refuses it", o.Profile, arn, roleName)
		}
		return nil
	})
}
