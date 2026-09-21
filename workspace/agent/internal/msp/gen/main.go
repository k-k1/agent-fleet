// Command gen regenerates internal/msp/types_gen.go from the schema bundle beside it.
//
// Usage: go generate ./internal/msp/... (or `go run ./internal/msp/gen <pkgDir>`).
package main

import (
	"fmt"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/schemagen"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := schemagen.Write(dir); err != nil {
		fmt.Fprintln(os.Stderr, "msp/gen:", err)
		os.Exit(1)
	}
}
