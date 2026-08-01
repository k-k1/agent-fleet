// Command scan is the forbidden-token gate for release artifacts.
//
//	scan --ledger deploy/release/forbidden.sha256 deploy/release/dist
//
// It expands every artifact (tar / gzip / zstd / xz / bzip2 / zip, recursively,
// including the layers inside a `docker save` tarball) and folds every leaf byte
// through the ledger's terms. Exit 0 = clean, 1 = a forbidden token is in the
// shipping set, 2 = the check could not run (which also fails a release: a gate
// that cannot run has not passed).
//
// The terms themselves live nowhere in this repository — see ledger.go. Adding
// one:
//
//	printf '%s' 'the term' | scan --ledger <file> --add --id corp-2 >> <file>
//
// (`printf` rather than `echo` so no trailing newline, piped rather than passed
// as an argument so the term never lands in a process listing.)
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	var (
		ledgerPath = flag.String("ledger", "", "path to the hashed term ledger (required)")
		allowPath  = flag.String("allow", "", "path to the allowlist of exempted paths (optional)")
		add        = flag.Bool("add", false, "read one term from stdin and print its ledger line")
		id         = flag.String("id", "", "term id, with --add")
		verbose    = flag.Bool("v", false, "list every file scanned")
	)
	flag.Parse()

	if err := run(*ledgerPath, *allowPath, *add, *id, *verbose, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(exitCode(err))
	}
}

type hitError struct{ n int }

func (e *hitError) Error() string {
	return fmt.Sprintf("%d forbidden token(s) in the shipping set — see the list above", e.n)
}

func exitCode(err error) int {
	if _, ok := err.(*hitError); ok {
		return 1
	}
	return 2
}

func run(ledgerPath, allowPath string, add bool, id string, verbose bool, paths []string) error {
	if add {
		if id == "" {
			return fmt.Errorf("--add needs --id")
		}
		term, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		e, err := NewEntry(string(term), id)
		if err != nil {
			return err
		}
		fmt.Println(e.String())
		return nil
	}

	if ledgerPath == "" {
		return fmt.Errorf("--ledger is required")
	}
	lf, err := os.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer lf.Close()
	entries, err := ParseLedger(lf)
	if err != nil {
		return fmt.Errorf("%s: %w", ledgerPath, err)
	}

	allow := &AllowList{}
	if allowPath != "" {
		af, err := os.Open(allowPath)
		if err != nil {
			return err
		}
		defer af.Close()
		if allow, err = ParseAllow(af); err != nil {
			return fmt.Errorf("%s: %w", allowPath, err)
		}
	}

	if len(paths) == 0 {
		return fmt.Errorf("no paths to scan")
	}

	w := NewWalker(entries, allow, verbose)
	start := time.Now()
	for _, p := range paths {
		if err := w.WalkPath(p); err != nil {
			return err
		}
	}
	el := time.Since(start)

	fmt.Printf("scan: %d terms, %d files, %s in %s (%s/s)\n",
		len(entries), w.Files, humanBytes(w.Bytes), el.Round(time.Millisecond),
		humanBytes(int64(float64(w.Bytes)/el.Seconds())))
	for _, s := range w.Skipped {
		fmt.Printf("scan: NOT EXPANDED: %s\n", s)
	}
	if len(w.Hits) > 0 {
		for _, h := range w.Hits {
			// The term is never printed — only where it is and which ledger
			// entry fired, which is all a human needs to go look.
			fmt.Printf("::error::forbidden token %s at offset %d in %s\n", h.ID, h.Off, h.Path)
		}
		return &hitError{n: len(w.Hits)}
	}
	if w.Files == 0 {
		return fmt.Errorf("scanned 0 files — the paths given hold nothing to check")
	}
	fmt.Println("scan: clean")
	return nil
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for v := n / u; v >= u; v /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
