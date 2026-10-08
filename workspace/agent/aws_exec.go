package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const awsExecUsage = `usage: af-aws-exec --profile <name> [--account <id>] [--region <region>] [--login|--no-login]
                   [--keep-aws-config] [-q] -- <command> [args...]
       af-aws-exec --list | --help | --version

Runs <command> with short-lived credentials of one SSO profile (Settings > AWS
profiles/SSM, exported into ~/.aws/config). The credentials are passed to that one child process through its
environment only. The container's workload role is blocked for the child: a missing or
expired SSO login fails instead of silently running as another principal.

A profile of your own that assumes a role from a source_profile, or runs a
credential_process, is run too when --account names its account: the command gets its
temporary credentials only if AWS reports them in that account. Long-lived keys,
credential_source, web identity and mfa_serial are refused.

  --region <region>  the region for the command; without it a region already exported in
                     the shell (AWS_REGION, then AWS_DEFAULT_REGION) wins over the profile's
  --account <id>     refuse unless the profile is this AWS account (required for a profile
                     that is not one of your Settings profiles, and so for every
                     role_arn / credential_process profile)
  --keep-aws-config  give the child your own ~/.aws files and AWS_ENDPOINT_URL* settings.
                     By default its config defines only the chosen profile, so a tool that
                     names another one fails with "could not be found": fix the tool rather
                     than reaching for this flag.
  --login            always run the device-code login in this terminal when the SSO login is
                     not usable
  --no-login         never prompt; exit 3 with the login command instead
                     (default: inside a workspace, ask the Agent Fleet Console to show the
                     login and wait for the member to approve it there, at a terminal too;
                     Ctrl-C stops waiting with exit 3. Outside a workspace: the device-code
                     login in the terminal when stdin and stderr are one)
  --list             pull the profiles from Settings now and list them with account and role
  -q                 do not print the principal the command runs as
  -h, --help         print this help
  --version          print the version (the workspace-agent build it belongs to)

Exit status: the command's own on success; 2 usage error; 3 SSO login required but not
started (--no-login, no Console
answer in time, or Ctrl-C while waiting for it); 1 any other refusal or failure.
`

// awsExec is af-aws-exec's skeleton: its name, usage text and version line.
var awsExec = cloudexec.Wrapper{Name: "af-aws-exec", Usage: awsExecUsage, Version: versionLine}

// runAWSExec is `workspace-agent aws-exec`, reached through the af-aws-exec PATH shim.
// Exit codes: 2 usage, 3 SSO login required but not attempted, 1 anything else.
func runAWSExec(args []string) {
	o, list := parseAWSExecArgs(args)

	res, serr := awsx.Sync()
	fresh := serr == nil
	switch {
	case errors.Is(serr, awsx.ErrBridgeOff):
	case serr != nil && res.Fetched:
		// The CP answered but ~/.aws/config could not be written: its answer still
		// decides what is ambiguous or shadowed.
		fmt.Fprintf(os.Stderr, "af-aws-exec: could not update ~/.aws/config (%v)\n", serr)
	case serr != nil && res.FromCache:
		fmt.Fprintf(os.Stderr, "af-aws-exec: could not refresh profiles from Settings (%v); using the last copy\n", serr)
	case serr != nil:
		if m, c, ok := awsx.CachedSettings(); ok {
			res.Settings, res.Conflicts = m, c
			fmt.Fprintf(os.Stderr, "af-aws-exec: could not refresh profiles from Settings (%v); checking against the last copy\n", serr)
		} else {
			fmt.Fprintf(os.Stderr, "af-aws-exec: could not refresh profiles from Settings (%v) and there is no earlier copy; "+
				"only profiles run with --account are allowed\n", serr)
		}
	}
	o.Settings, o.Conflicts, o.DefaultClash, o.ChainBroken = res.Settings, res.Conflicts, res.DefaultClash, res.ChainBroken
	if exe, err := os.Executable(); err == nil {
		o.CredentialHelper = exe + " aws-env-credentials"
	}

	if list {
		if errors.Is(serr, awsx.ErrBridgeOff) {
			fmt.Fprintln(os.Stderr, "af-aws-exec: this deployment does not export Settings profiles; ~/.aws/config is used as is")
		}
		names := res.Exported
		unverified := ""
		if !fresh && !res.FromCache && !errors.Is(serr, awsx.ErrBridgeOff) {
			// Neither the CP nor a cached copy of this member's list: the block is from an
			// earlier sync (possibly another membership's, in a shared home) and nothing
			// here can say whether it still matches Settings or the files.
			names = awsx.ExportedIn(awsx.ConfigPath())
			unverified = "\t(in ~/.aws/config from an earlier sync; not checked against Settings now)"
		}
		for _, n := range names {
			acct, role := awsx.DescribeProfile(n)
			label := unverified
			if sp, ok := res.Settings[n]; ok && unverified == "" {
				label = "\t(" + strconv.Quote(sp.Label) + ")"
				if sp.Chained() {
					label = "\t(" + strconv.Quote(sp.Label) + ", role assumed from " + sp.SourceProfile + ")"
				}
			}
			fmt.Printf("%s\t%s\t%s%s\n", n, acct, role, label)
		}
		for _, n := range res.Shadowed {
			if n == "default" {
				fmt.Printf("%s\t(not exported: a Settings profile never becomes the default profile)\n", n)
				continue
			}
			// Show both sides: a shadowing definition with another account is exactly
			// the mix-up to see before a run, not after af-aws-exec refuses it.
			acct, role := awsx.DescribeProfile(n)
			if sp, ok := res.Settings[n]; ok && (sp.AccountID != acct || sp.RoleName != role) {
				fmt.Printf("%s\t(not exported: your own definition in ~/.aws is used: account %s, role %s; "+
					"Settings %q is account %s, role %s; rename one)\n", n, orNone(acct), orNone(role), sp.Label, orNone(sp.AccountID), orNone(sp.RoleName))
				continue
			}
			fmt.Printf("%s\t(not exported: your own definition in ~/.aws is used: account %s, role %s)\n", n, orNone(acct), orNone(role))
		}
		for n, reason := range res.Invalid {
			fmt.Printf("%s\t(not exported: %s)\n", n, reason)
		}
		for n, reason := range res.Incomplete {
			fmt.Printf("%s\t(not exported: %s)\n", n, reason)
		}
		for n, reason := range res.ChainBroken {
			fmt.Printf("%s\t(not exported: %s)\n", n, reason)
		}
		for _, n := range res.SessionShadowed {
			fmt.Printf("%s\t(not exported: your [sso-session af-%s] in ~/.aws/config uses the name this profile's sso-session "+
				"needs; rename that section)\n", n, n)
		}
		for n, reason := range res.DefaultClash {
			fmt.Printf("%s\t(not exported: [DEFAULT] %s (in ~/.aws/config); remove that line from [DEFAULT])\n", n, reason)
		}
		for _, c := range res.Conflicts {
			fmt.Printf("%s\t(not exported: Settings labels %s all map to this name; rename all but one)\n", c.Name, strings.Join(c.Labels, " / "))
		}
		os.Exit(0)
	}

	if o.Profile == "" || len(o.Argv) == 0 {
		awsExec.FailUsage()
	}
	awsBin, err := ensureAWSCLI()
	if err != nil {
		awsExec.Fail(cloudexec.ExitRefused, err.Error())
	}
	o.Interactive = cloudexec.IsTerminal(os.Stdin) && cloudexec.IsTerminal(os.Stderr)
	// Inside a workspace the Agent can show the login in the Console (ADR 0102).
	if os.Getenv("AF_CP_BASE_URL") != "" {
		name := os.Getenv("AF_SESSION_NAME")
		o.ConsoleLogin = true
		o.ConsoleWait, o.TerminalConsole = consoleLoginWaitFor(name, o.Interactive)
		o.Waiter = awsx.LoginWaiter{Session: name, Command: filepath.Base(o.Argv[0])}
	}
	prog, argv, env, err := awsx.PlanExec(awsBin, os.Environ(), o)
	if err != nil {
		awsExec.FailPlan(err, awsx.ErrLoginRequired)
	}
	awsExec.Exec(prog, argv, env)
}

// consoleLoginWaits is how long af-aws-exec waits for a Console login, per agent kind,
// from measurements of that kind's shell tool (ADR 0102 decision 5): the wait has to end
// before the tool gives up on the command, unless the tool keeps the output of a command
// it stops waiting for. A kind not listed has not been measured. The measurements are in
// docs/log/120-aws-console-login.md §1.1 (all 2026-09-27).
var consoleLoginWaits = map[string]time.Duration{
	// 120 s default, then the command moves to the background with its output kept.
	session.KindClaude: 90 * time.Second,
	// Returns the output so far after 10 s and keeps the command running for the model to poll.
	session.KindCodex: 90 * time.Second,
	// 120 s default, then SIGTERM; the tool result keeps the output printed before it.
	session.KindOpencode: 90 * time.Second,
	// Both return the output so far after 30 s and keep the command running in the background.
	session.KindCopilot: 90 * time.Second,
	session.KindCursor:  90 * time.Second,
	// Managed (muse serve): moves the command to the background with the output so far, and
	// delivers the rest when it ends.
	session.KindMuse: 90 * time.Second,
	// No timeout within 400 s (agy measured with the RDRAND mask of agents/agy/fips.go).
	session.KindKiro: 90 * time.Second,
	session.KindAgy:  90 * time.Second,
	// Our own bash tool (harness/tools_bash.go): 300 s default, output kept on timeout.
	session.KindLcpp: 90 * time.Second,
}

// consoleLoginUnmeasuredWait is the wait of a kind nobody has measured, and so the
// shortest of all kinds: short enough that no tool's timeout plausibly cuts it, so the
// run still files the request, says so, and ends with exit 3 for a rerun.
const consoleLoginUnmeasuredWait = 5 * time.Second

// consoleLoginWait picks the wait for the session that runs this command. An unknown
// caller gets the shortest wait of all kinds, which is the unmeasured one while any kind
// is unmeasured (ADR 0102 decision 5).
func consoleLoginWait(sessionName string) time.Duration {
	if session.ValidName(sessionName) {
		if m, ok := session.ReadMeta(sessionName); ok {
			if d, ok := consoleLoginWaits[m.Kind]; ok {
				return d
			}
		}
	}
	return consoleLoginUnmeasuredWait
}

// consoleLoginTerminalWait is how long a run at a member's own terminal waits for the
// Console login: about the device-code lifetime (ten minutes), because a person is at the
// keyboard and no agent tool is timing the command out; Ctrl-C ends it earlier.
const consoleLoginTerminalWait = 10 * time.Minute

// consoleLoginWaitFor is consoleLoginWait for a run that may be at a terminal. A run with
// a terminal that is not an agent's (a Shell or SSM pane, an ssh login: no session, or a
// session of a kind that is no agent) asks the Console and waits the long time. An
// agent's own run keeps its per-kind wait and, if it somehow has a terminal, its
// in-terminal login, so what an agent does does not depend on this path.
func consoleLoginWaitFor(sessionName string, interactive bool) (time.Duration, bool) {
	if interactive && !agentSession(sessionName) {
		return consoleLoginTerminalWait, true
	}
	return consoleLoginWait(sessionName), false
}

// agentSession reports whether the session is of a kind with a measured wait, an agent.
func agentSession(sessionName string) bool {
	if !session.ValidName(sessionName) {
		return false
	}
	m, ok := session.ReadMeta(sessionName)
	if !ok {
		return false
	}
	_, agent := consoleLoginWaits[m.Kind]
	return agent
}

// parseAWSExecArgs reads the flags up to "--"; everything after it is the command.
func parseAWSExecArgs(args []string) (awsx.ExecOptions, bool) {
	o := awsx.ExecOptions{Login: "auto", Stderr: os.Stderr}
	list := false
	value := map[string]*string{"--profile": &o.Profile, "--region": &o.Region, "--account": &o.Account}
	o.Argv = awsExec.Parse(args, value, func(a string) bool {
		switch a {
		case "--login":
			o.Login = "always"
		case "--no-login":
			o.Login = "never"
		case "--keep-aws-config":
			o.KeepConfig = true
		case "-q", "--quiet":
			o.Quiet = true
		case "--list":
			list = true
		default:
			return false
		}
		return true
	})
	return o, list
}

// runAWSEnvCredentials is `workspace-agent aws-env-credentials`: the credential_process
// of the one-profile config af-aws-exec gives its child. It prints the credentials in its
// own environment, and only while they are still the ones af-aws-exec handed over
// (AF_AWS_EXEC_KEY_ID); after a script swaps them it refuses rather than let the
// profile name stand for another account.
func runAWSEnvCredentials(args []string) {
	b, err := awsx.EnvCredentials(os.Environ())
	if err != nil {
		fmt.Fprintln(os.Stderr, "aws-env-credentials: "+err.Error())
		os.Exit(1)
	}
	os.Stdout.Write(append(b, '\n'))
}

// ensureAWSCLI finds aws, installing the pinned CLI into the home first on a lean
// rootfs (the same on-demand path an SSM session takes).
func ensureAWSCLI() (string, error) {
	if p, err := exec.LookPath("aws"); err == nil {
		return p, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(self, "install-awscli")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("the AWS CLI is not installed and `workspace-agent install-awscli` failed: %v", err)
	}
	return exec.LookPath("aws")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
