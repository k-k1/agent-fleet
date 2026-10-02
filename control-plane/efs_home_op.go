package main

import (
	"context"
	"log"
	"os"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// efsHomeOpRoot is where HomeOpsTaskDef mounts the file system's root.
const efsHomeOpRoot = "/mnt/efs"

// runEFSHomeOp is `af-cp efs-home-op`: one operation on one member's EFS home, from the
// task's environment, ending in the exit code the CP reads (runtime.HomeOpExit*).
func runEFSHomeOp() {
	op, membership := os.Getenv("AF_HOME_OP"), os.Getenv("AF_HOME_MEMBERSHIP")
	log.Printf("efs-home-op: %s for membership %q under %s", op, membership, efsHomeOpRoot)
	err := runtime.RunEFSHomeOp(context.Background(), efsHomeOpRoot, op, membership)
	if err != nil {
		log.Printf("efs-home-op: %v", err)
	} else {
		log.Printf("efs-home-op: done")
	}
	os.Exit(runtime.HomeOpExitCode(err))
}
