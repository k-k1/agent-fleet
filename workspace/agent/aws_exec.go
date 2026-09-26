package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"golang.org/x/sys/unix"
)

const awsExecUsage = `usage: af-aws-exec --profile <name> [--account <id>] [--region <region>] [--login|--no-login]
                   [--keep-aws-config] [-q] -- <command> [args...]
       af-aws-exec --list | --help | --version

Runs <command> with short-lived credentials of one SSO profile (Settings > SSM, exported
into ~/.aws/config). The credentials are passed to that one child process through its
environment only. The container's workload role is blocked for the child: a missing or
expired SSO login fails instead of silently running as another principal.

  --region <region>  the region for the command; without it a region already exported in
                     the shell (AWS_REGION, then AWS_DEFAULT_REGION) wins over the profile's
  --account <id>     refuse unless the profile is this AWS account (required for a profile
                     that is not one of your Settings profiles)
  --keep-aws-config  give the child your own ~/.aws files and AWS_ENDPOINT_URL* settings.
                     By default its config defines only the chosen profile, so a tool that
                     names another one fails with "could not be found": fix the tool rather
                     than reaching for this flag.
  --login            always start the device-code login when the SSO login is not usable
  --no-login         never prompt; exit 3 with the login command instead
                     (default: prompt only when stdin and stderr are a terminal)
  --list             pull the profiles from Settings now and list them with account and role
  -q                 do not print the principal the command runs as
  -h, --help         print this help
  --version          print the version (the workspace-agent build it belongs to)

Exit status: the command's own on success; 2 usage error; 3 SSO login required but not
started (no terminal, or --no-login); 1 any other refusal or failure.
`

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
	case serr != nil:
		if m, c, ok := awsx.CachedSettings(); ok {
			res.Settings, res.Conflicts = m, c
			fmt.Fprintf(os.Stderr, "af-aws-exec: could not refresh profiles from Settings (%v); checking against the last copy\n", serr)
		} else {
			fmt.Fprintf(os.Stderr, "af-aws-exec: could not refresh profiles from Settings (%v) and there is no earlier copy; "+
				"only profiles run with --account are allowed\n", serr)
		}
	}
	o.Settings, o.Conflicts, o.DefaultClash = res.Settings, res.Conflicts, res.DefaultClash
	if exe, err := os.Executable(); err == nil {
		o.CredentialHelper = exe + " aws-env-credentials"
	}

	if list {
		if errors.Is(serr, awsx.ErrBridgeOff) {
			fmt.Fprintln(os.Stderr, "af-aws-exec: this deployment does not export Settings profiles; ~/.aws/config is used as is")
		}
		names := res.Exported
		if !fresh {
			// Could not ask the CP: list what the file holds now rather than nothing,
			// and mark the cached Settings names it does not hold as not exported so a
			// shadowed name still shows both accounts.
			names = awsx.ExportedIn(awsx.ConfigPath())
			if !res.Fetched {
				res.Shadowed = awsx.NotExported(res.Settings, names)
			}
		}
		for _, n := range names {
			acct, role := awsx.DescribeProfile(n)
			label := ""
			if sp, ok := res.Settings[n]; ok {
				label = "\t(" + strconv.Quote(sp.Label) + ")"
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
					"Settings %q is account %s, role %s; rename one)\n", n, orNone(acct), orNone(role), sp.Label, sp.AccountID, sp.RoleName)
				continue
			}
			fmt.Printf("%s\t(not exported: your own definition in ~/.aws is used: account %s, role %s)\n", n, orNone(acct), orNone(role))
		}
		for n, reason := range res.Incomplete {
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
		fmt.Fprint(os.Stderr, awsExecUsage)
		os.Exit(2)
	}
	awsBin, err := ensureAWSCLI()
	if err != nil {
		awsExecFail(1, err.Error())
	}
	o.Interactive = isTerminal(os.Stdin) && isTerminal(os.Stderr)
	prog, argv, env, err := awsx.PlanExec(awsBin, os.Environ(), o)
	if err != nil {
		if errors.Is(err, awsx.ErrLoginRequired) {
			awsExecFail(3, err.Error())
		}
		awsExecFail(1, err.Error())
	}
	if err := syscall.Exec(prog, argv, env); err != nil {
		awsExecFail(1, "exec "+prog+": "+err.Error())
	}
}

// parseAWSExecArgs reads the flags up to "--"; everything after it is the command.
func parseAWSExecArgs(args []string) (awsx.ExecOptions, bool) {
	o := awsx.ExecOptions{Login: "auto", Stderr: os.Stderr}
	list := false
	value := map[string]*string{"--profile": &o.Profile, "--region": &o.Region, "--account": &o.Account}
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		if a == "--" {
			o.Argv = args
			break
		}
		if k, v, ok := strings.Cut(a, "="); ok && value[k] != nil {
			*value[k] = v
			continue
		}
		if dst := value[a]; dst != nil {
			if len(args) == 0 {
				awsExecFail(2, a+" needs a value")
			}
			*dst, args = args[0], args[1:]
			continue
		}
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
		case "-h", "--help":
			fmt.Print(awsExecUsage)
			os.Exit(0)
		case "--version":
			fmt.Println("af-aws-exec, part of " + versionLine())
			os.Exit(0)
		default:
			awsExecFail(2, "unknown argument "+a+" (put the command after --)")
		}
	}
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

func awsExecFail(code int, msg string) {
	fmt.Fprintln(os.Stderr, "af-aws-exec: "+msg)
	os.Exit(code)
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

// isTerminal reports whether f is a terminal. A character-device check is not enough:
// /dev/null is one too, and treating a redirected, unattended run as interactive would
// start a device-code login that waits for nobody.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
