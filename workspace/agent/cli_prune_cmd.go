package main

import (
	"fmt"
	"os"
)

// runPruneCLIVersions is `workspace-agent prune-cli-versions`: the Agent's boot-time prune of
// old agent CLI versions (cli_version_prune.go), run on demand. The entrypoint calls it before
// the boot-install when home is too full for the installs to fit, because the Agent's own
// pass runs after them, too late to make room (a full home failed every install and the space
// came back only afterwards).
func runPruneCLIVersions(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent prune-cli-versions")
		os.Exit(2)
	}
	pruneOldCLIVersionsAtBoot()
}
