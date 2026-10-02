package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gcpx"
)

const gcloudExecUsage = `usage: af-gcloud-exec --profile <name> --project <id> [--login|--no-login] [-q] -- <command> [args...]
       af-gcloud-exec --list | --help | --version

Runs <command> with a short-lived access token of one Google Cloud profile (Settings >
Google Cloud). The token is minted from the Agent's own gcloud store, never from the
workspace's VM or node identity, and reaches only that one command: through
CLOUDSDK_AUTH_ACCESS_TOKEN_FILE (gcloud, the GKE auth plugin) and GOOGLE_OAUTH_ACCESS_TOKEN
(Terraform). GOOGLE_APPLICATION_CREDENTIALS points at a path with no credentials, so a
client library that ignores those variables fails instead of finding another identity.
Your own ~/.config/gcloud is neither read nor changed.

  --project <id>  must be the profile's project. A user token is not bound to a project,
                  so this only checks that you and the profile agree on where the command
                  points by default; a command's own --project still wins.
  --login         always start the gcloud login when the profile has no usable login
  --no-login      never prompt; exit 3 with the command to run in a terminal instead
                  (default: prompt only when stdin and stderr are a terminal)
  --list          pull the profiles from Settings now and list them
  -q              do not print the account and the token's remaining minutes
  -h, --help      print this help
  --version       print the version (the workspace-agent build it belongs to)

The token lasts what remained when it was minted (at least 10 minutes); it is not
refreshed during the command, so a command that outlives it fails.

Exit status: the command's own on success; 2 usage error; 3 login required but not
started (no terminal, or --no-login); 1 any other refusal or failure.
`

// gcloudExec is af-gcloud-exec's skeleton: its name, usage text and version line.
var gcloudExec = cloudexec.Wrapper{Name: "af-gcloud-exec", Usage: gcloudExecUsage, Version: versionLine}

// runGCloudExec is `workspace-agent gcloud-exec`, reached through the af-gcloud-exec PATH
// shim (ADR 0107 decision 2).
func runGCloudExec(args []string) {
	o, list := parseGCloudExecArgs(args)

	res, serr := gcpx.Sync()
	settings, conflicts := res.Exported, res.Conflicts
	switch {
	case errors.Is(serr, gcpx.ErrBridgeOff):
		gcloudExec.Fail(cloudexec.ExitRefused, "this deployment does not export Google Cloud profiles from Settings")
	case serr != nil && res.Fetched:
		fmt.Fprintf(os.Stderr, "af-gcloud-exec: could not update the gcloud configurations (%v)\n", serr)
	case serr != nil && res.FromCache:
		fmt.Fprintf(os.Stderr, "af-gcloud-exec: could not refresh profiles from Settings (%v); using the last copy\n", serr)
	case serr != nil:
		if m, c, ok := gcpx.CachedSettings(); ok {
			settings, conflicts = m, c
			fmt.Fprintf(os.Stderr, "af-gcloud-exec: could not refresh profiles from Settings (%v); checking against the last copy\n", serr)
		} else {
			gcloudExec.Fail(cloudexec.ExitRefused, fmt.Sprintf("could not read the profiles from Settings (%v), and there is no earlier copy", serr))
		}
	}
	o.Settings, o.Conflicts, o.Invalid = settings, conflicts, res.Invalid

	if list {
		listGCPProfiles(settings, conflicts, res.Invalid)
		os.Exit(0)
	}
	if o.Profile == "" || o.Project == "" || len(o.Argv) == 0 {
		gcloudExec.FailUsage()
	}
	gcloudBin, err := ensureGCloud()
	if err != nil {
		gcloudExec.Fail(cloudexec.ExitRefused, err.Error())
	}
	o.Interactive = cloudexec.IsTerminal(os.Stdin) && cloudexec.IsTerminal(os.Stderr)
	prog, argv, env, err := gcpx.PlanExec(gcloudBin, os.Environ(), o)
	if err != nil {
		gcloudExec.FailPlan(err, gcpx.ErrLoginRequired)
	}
	gcloudExec.Exec(prog, argv, env)
}

func listGCPProfiles(settings map[string]gcpx.Profile, conflicts []gcpx.Conflict, invalid map[string]string) {
	names := make([]string, 0, len(settings))
	for n := range settings {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := settings[n]
		account := p.Account
		if account == "" {
			if account = gcpx.ConfiguredAccount(n); account == "" {
				account = "(chosen at the first login)"
			}
		}
		line := fmt.Sprintf("%s\t%s\t%s", n, p.Project, account)
		if p.ImpersonateServiceAccount != "" {
			line += "\timpersonates " + p.ImpersonateServiceAccount
		}
		fmt.Printf("%s\t(%s)\n", line, strconv.Quote(p.Label))
	}
	bad := make([]string, 0, len(invalid))
	for n := range invalid {
		bad = append(bad, n)
	}
	sort.Strings(bad)
	for _, n := range bad {
		fmt.Printf("%s\t(not exported: %s)\n", n, invalid[n])
	}
	for _, c := range conflicts {
		fmt.Printf("%s\t(not exported: Settings labels %s all map to this name; rename all but one)\n", c.Name, strings.Join(c.Labels, " / "))
	}
}

// parseGCloudExecArgs reads the flags up to "--"; everything after it is the command.
func parseGCloudExecArgs(args []string) (gcpx.ExecOptions, bool) {
	o := gcpx.ExecOptions{Login: "auto", Stderr: os.Stderr}
	list := false
	value := map[string]*string{"--profile": &o.Profile, "--project": &o.Project}
	o.Argv = gcloudExec.Parse(args, value, func(a string) bool {
		switch a {
		case "--login":
			o.Login = "always"
		case "--no-login":
			o.Login = "never"
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

// ensureGCloud returns the pinned SDK's gcloud, running `workspace-agent install-gcloud`
// first when it is missing or another version than the pin (decision 4: installed on first
// use, never baked). A gcloud elsewhere on PATH is not used: the wrapper mints with the
// one it installed.
func ensureGCloud() (string, error) {
	root := gcloudSDKRoot()
	bin := filepath.Join(root, "bin", "gcloud")
	if want := readBuildPins()["gcloud"]; want != "" && gcloudSDKVersion(root) == want {
		return bin, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(self, "install-gcloud")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("the Google Cloud SDK is not installed and `workspace-agent install-gcloud` failed: %v", err)
	}
	if gcloudSDKVersion(root) == "" {
		return "", errors.New("`workspace-agent install-gcloud` left no gcloud at " + bin)
	}
	return bin, nil
}
