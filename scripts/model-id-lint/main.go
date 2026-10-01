// Command model-id-lint fails when a model-id-shaped string literal appears in the product
// Go code outside the fallback registry (issue #1084). Model ids move every few weeks; a
// fallback pinned somewhere nobody owns keeps steering sessions onto a retired model long
// after the catalog has moved on, and nothing says so. The registry
// (workspace/agent/internal/modelfallback) records for each pinned id who owns it, where it
// came from and why discovery cannot supply it; this lint keeps new ones from bypassing it.
//
// Usage, from the repository root or anywhere (the root defaults to two levels above this
// directory):
//
//	go run ./scripts/model-id-lint          (from scripts/model-id-lint: go run .)
//	go run . -list                          every finding, allowed ones included
//
// The Console has its own half (console/scripts/model-id-lint.mjs) reading the same
// patterns.txt.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// scanDirs are the product Go modules, relative to the repository root.
var scanDirs = []string{"control-plane", "workspace/agent"}

// registryFiles own the fallbacks; nothing else in scanDirs may spell a model id.
var registryFiles = map[string]bool{
	"workspace/agent/internal/modelfallback/modelfallback.go": true,
}

func main() {
	root := flag.String("root", defaultRoot(), "repository root")
	list := flag.Bool("list", false, "print every finding with its status, not only violations")
	flag.Parse()

	pat, err := LoadPattern(filepath.Join(*root, "scripts/model-id-lint/patterns.txt"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "model-id-lint:", err)
		os.Exit(2)
	}
	l := &Linter{Pattern: pat, Registry: registryFiles, Root: *root}
	findings, errs := l.Scan(scanDirs...)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "model-id-lint:", e)
	}
	bad := 0
	for _, f := range findings {
		if f.Status == StatusViolation {
			bad++
		}
		if *list {
			fmt.Printf("%-12s %s\n", f.Status, f)
		} else if f.Status == StatusViolation {
			fmt.Println(f)
		}
	}
	fmt.Fprintf(os.Stderr, "model-id-lint: %d model-id-shaped literal(s) in %v, %d outside the registry without a %s\n",
		len(findings), scanDirs, bad, allowDirective)
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "model-id-lint: move the id into %s, or mark a literal that is not a fallback with `// %s <reason>`\n",
			"workspace/agent/internal/modelfallback", allowDirective)
	}
	if bad > 0 || len(errs) > 0 {
		os.Exit(1)
	}
}

// defaultRoot is the repository root as seen from this source file, so `go run .` works from
// any working directory.
func defaultRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}
