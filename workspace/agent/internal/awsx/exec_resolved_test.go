package awsx

import (
	"os"
	"path/filepath"
	"testing"
)

// resolvedOf runs PlanExec with a Resolved hook and returns what it was told.
func resolvedOf(t *testing.T, bin string, o ExecOptions) (account, role, region string, calls int) {
	t.Helper()
	o.Resolved = func(a, r, g string) { account, role, region = a, r, g; calls++ }
	if _, _, _, err := PlanExec(bin, workloadEnv, o); err != nil {
		t.Fatal(err)
	}
	return
}

// The run's row (ADR 0126) names the identity STS confirmed: the permission set of an SSO
// profile, not the AWSReservedSSO_ role name; the target role of a chain; the role an STS
// ARN shows for a credential_process.
func TestPlanExecReportsTheResolvedIdentity(t *testing.T) {
	t.Run("sso", func(t *testing.T) {
		bin, state := fakeAWS(t, ssoProfile)
		if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		a, r, g, n := resolvedOf(t, bin, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true})
		if a != "123456789012" || r != "Dev" || g != "us-west-2" || n != 1 {
			t.Fatalf("resolved %q %q %q (%d calls)", a, r, g, n)
		}
	})
	t.Run("role chain", func(t *testing.T) {
		bin, _ := fakeDeploy(t, deployProfile, srcConfig, srcKeys, deployARN)
		a, r, g, n := resolvedOf(t, bin, ExecOptions{Profile: "prod", Account: deployAccount, Settings: map[string]Profile{},
			Login: "never", Argv: []string{"true"}, Quiet: true})
		if a != deployAccount || r != "deployer" || g != "ap-northeast-1" || n != 1 {
			t.Fatalf("resolved %q %q %q (%d calls)", a, r, g, n)
		}
	})
	t.Run("credential process", func(t *testing.T) {
		bin, _ := fakeDeploy(t, map[string]string{"credential_process": "/usr/local/bin/get-creds"}, "", "",
			"arn:aws:sts::222233334444:assumed-role/whatever/x")
		a, r, _, n := resolvedOf(t, bin, ExecOptions{Profile: "prod", Account: deployAccount, Login: "never", Argv: []string{"true"}, Quiet: true})
		if a != deployAccount || r != "whatever" || n != 1 {
			t.Fatalf("resolved %q %q (%d calls)", a, r, n)
		}
	})
}

// A refused identity is never reported: the hook runs only after the check passed.
func TestPlanExecReportsNothingForARefusedIdentity(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "arn"), []byte("arn:aws:sts::999999999999:assumed-role/Other/me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never",
		Argv: []string{"true"}, Quiet: true, Resolved: func(string, string, string) { called = true }})
	if err == nil || called {
		t.Fatalf("err %v, reported %v", err, called)
	}
}

func TestAssumedRoleName(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:sts::1:assumed-role/R/s":   "R",
		"arn:aws:iam::1:role/R":             "",
		"arn:aws:sts::1:federated-user/bob": "",
		"garbage":                           "",
	} {
		if got := assumedRoleName(arn); got != want {
			t.Errorf("assumedRoleName(%q) = %q, want %q", arn, got, want)
		}
	}
}
