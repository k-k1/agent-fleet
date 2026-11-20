package bootinstall

import (
	"context"
	"fmt"
	"os"
)

// Run is `workspace-agent boot-install <cli> [--repin]`: one CLI of the entrypoint's lean
// boot-install. Exit 1 is a failed install, 3 a skip for lack of space (the entrypoint then
// holds back its other downloads), 2 a usage error.
func Run(args []string) {
	repin := false
	var names []string
	for _, a := range args {
		if a == "--repin" {
			repin = true
		} else {
			names = append(names, a)
		}
	}
	if len(names) != 1 {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent boot-install rtk [--repin]")
		os.Exit(2)
	}
	switch names[0] {
	case "rtk":
		os.Exit(RTK(context.Background(), DefaultEnv(repin)))
	default:
		fmt.Fprintf(os.Stderr, "boot-install: unknown CLI %q (have: rtk)\n", names[0])
		os.Exit(2)
	}
}
