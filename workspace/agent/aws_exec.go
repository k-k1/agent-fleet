package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
)

const awsExecUsage = `usage: af-aws-exec --profile <name> [--region <region>] [--login|--no-login] [-q] -- <command> [args...]
       af-aws-exec --list

Runs <command> with short-lived credentials of one SSO profile (Settings > SSM, exported
into ~/.aws/config). The credentials are passed to that one child process through its
environment only. The container's workload role is blocked for the child: a missing or
expired SSO login fails instead of silently running as another principal.

  --login     always start the device-code login when the SSO login is not usable
  --no-login  never prompt; exit 3 with the login command instead
              (default: prompt only when stdin and stderr are a terminal)
  --list      pull the profiles from Settings now and list them
  -q          do not print the principal the command runs as
`

// runAWSExec is `workspace-agent aws-exec`, reached through the af-aws-exec PATH shim.
// Exit codes: 2 usage, 3 SSO login required but not attempted, 1 anything else.
func runAWSExec(args []string) {
	o := awsx.ExecOptions{Login: "auto", Stderr: os.Stderr}
	list := false
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		switch {
		case a == "--":
			o.Argv = args
			args = nil
		case a == "--profile" || a == "--region":
			if len(args) == 0 {
				awsExecFail(2, a+" needs a value")
			}
			if a == "--profile" {
				o.Profile = args[0]
			} else {
				o.Region = args[0]
			}
			args = args[1:]
		case strings.HasPrefix(a, "--profile="):
			o.Profile = strings.TrimPrefix(a, "--profile=")
		case strings.HasPrefix(a, "--region="):
			o.Region = strings.TrimPrefix(a, "--region=")
		case a == "--login":
			o.Login = "always"
		case a == "--no-login":
			o.Login = "never"
		case a == "-q" || a == "--quiet":
			o.Quiet = true
		case a == "--list":
			list = true
		case a == "-h" || a == "--help":
			fmt.Print(awsExecUsage)
			os.Exit(0)
		default:
			awsExecFail(2, "unknown argument "+a+" (put the command after --)")
		}
	}

	res, serr := awsx.Sync()
	if serr != nil && !errors.Is(serr, awsx.ErrBridgeOff) {
		fmt.Fprintf(os.Stderr, "af-aws-exec: could not refresh profiles from Settings (%v); using ~/.aws/config as is\n", serr)
	}
	if list {
		if errors.Is(serr, awsx.ErrBridgeOff) {
			fmt.Fprintln(os.Stderr, "af-aws-exec: this deployment does not export Settings profiles; ~/.aws/config is used as is")
		}
		names := res.Exported
		if serr != nil {
			// Could not ask the CP: list what the file holds now rather than nothing.
			names = awsx.ExportedIn(awsx.ConfigPath())
		}
		for _, n := range names {
			fmt.Println(n)
		}
		for _, n := range res.Shadowed {
			if n == "default" {
				fmt.Printf("%s\t(not exported: a Settings profile never becomes the default profile)\n", n)
				continue
			}
			fmt.Printf("%s\t(not exported: your own definition in ~/.aws is used)\n", n)
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

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
