package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
)

// runAdvanceBridgeEpochs is `af-cp advance-bridge-epochs [--yes]`: the one-shot that advances
// the bridge and git token epoch of EVERY membership (issue #3452), which kills every token
// minted so far, the ones left in old task-definition revisions, logs and CloudTrail included.
// Without --yes it only counts. Run it after the release that moves the tokens out of the task
// definition's environment, once every workspace is on that release; then restart the running
// workspaces (the Console shows "restart required" on each). It reads the CP's own environment
// for the store, like rewrap-keys, and is safe against a live CP: epochs are read on every
// request. Exit 2 on usage or a store that cannot be read.
func runAdvanceBridgeEpochs(args []string) {
	fs := flag.NewFlagSet("advance-bridge-epochs", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "advance the epochs (without it, only count the memberships)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: control-plane advance-bridge-epochs [--yes]")
		os.Exit(2)
	}
	st, where, err := openRewrapStore()
	if err != nil {
		log.Printf("advance-bridge-epochs: open the metadata store: %v", err)
		os.Exit(2)
	}
	defer st.Close()
	log.Printf("advance-bridge-epochs: %s", where)
	if !*yes {
		n, err := st.CountMemberships(context.Background())
		if err != nil {
			log.Printf("advance-bridge-epochs: %v", err)
			os.Exit(2)
		}
		fmt.Printf("dry run: %d memberships would move; pass --yes to advance their bridge and git token epoch once\n", n)
		return
	}
	n, err := st.BumpAllBridgeEpochs(context.Background())
	if err != nil {
		log.Printf("advance-bridge-epochs: %v", err)
		os.Exit(2)
	}
	fmt.Printf("advanced the bridge and git token epoch of %d memberships; restart running workspaces\n", n)
}
