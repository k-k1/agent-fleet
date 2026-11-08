// Command goldeval runs both model tiers over the member-labelled gold set and
// writes the scores. The model is a one-shot command given by argv (prompt on
// stdin, JSON on stdout); nothing here knows an SDK or a key.
//
//	go run ./cmd/goldeval -cases cases.jsonl -inputs inputs.jsonl -out run/ \
//	  -tier small='claude -p --model haiku --output-format json ...' \
//	  -tier large='claude -p --model sonnet --output-format json ...'
//
// A tier command is split on whitespace with single and double quotes; there
// is no shell, so no expansion, pipes or redirects.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/memengine"
	"github.com/k-k1/memengine/internal/goldset"
)

type tierFlags []string

func (t *tierFlags) String() string     { return strings.Join(*t, ",") }
func (t *tierFlags) Set(v string) error { *t = append(*t, v); return nil }

func main() {
	var tiers tierFlags
	cases := flag.String("cases", "cases.jsonl", "gold set (JSONL)")
	inputs := flag.String("inputs", "inputs.jsonl", "memories the cases cite (JSONL)")
	out := flag.String("out", "run", "directory for outputs and the report")
	workers := flag.Int("workers", 3, "model calls in flight per tier")
	limit := flag.Int("limit", 0, "run only the first N cases (a pilot); 0 means all")
	timeout := flag.Duration("timeout", 3*time.Minute, "per-call timeout")
	resume := flag.Bool("resume", true, "reuse answers already in -out instead of calling again")
	flag.Var(&tiers, "tier", "name=command (repeatable), the first is the small tier")
	flag.Parse()
	if len(tiers) == 0 {
		fatal("at least one -tier name=command is required")
	}
	set, err := goldset.Load(*cases, *inputs)
	if err != nil {
		fatal(err.Error())
	}
	if *limit > 0 && *limit < len(set.Cases) {
		set.Cases = set.Cases[:*limit]
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err.Error())
	}
	ctx := context.Background()
	var runs []*goldset.TierRun
	for _, spec := range tiers {
		name, cmdline, ok := strings.Cut(spec, "=")
		argv, err := splitArgs(cmdline)
		if err != nil {
			fatal("bad -tier " + spec + ": " + err.Error())
		}
		if !ok || name == "" || len(argv) == 0 {
			fatal("bad -tier " + spec + ": want name=command")
		}
		model := memengine.Command{Argv: argv, Timeout: *timeout}
		path := filepath.Join(*out, name+".run.json")
		var prev *goldset.TierRun
		if *resume {
			prev = loadRun(path)
		}
		if prev == nil || prev.Baseline.TotalInput() == 0 {
			// The fixed overhead of the command, from a prompt with no content.
			rep, err := model.Call(ctx, `Reply with the JSON object {"ok":true}.`)
			if err != nil {
				fatal("baseline call for " + name + ": " + err.Error())
			}
			if prev == nil {
				prev = &goldset.TierRun{Name: name}
			}
			prev.Baseline = rep.Usage
		}
		run := goldset.Run(ctx, name, model, set, *workers, prev, func(s string) { fmt.Fprintln(os.Stderr, s) })
		saveRun(path, run)
		runs = append(runs, run)
	}
	var scores []goldset.TierScore
	for _, r := range runs {
		scores = append(scores, goldset.ScoreTier(r))
	}
	var agree [][3]int
	for i := 1; i < len(runs); i++ {
		s, b := goldset.Agreement(runs[0], runs[i])
		agree = append(agree, [3]int{s, b, i})
	}
	ref := goldset.Reference(set)
	notes := []string{notesOf(set)}
	md := goldset.Markdown(scores, ref, agree, notes)
	if err := os.WriteFile(filepath.Join(*out, "report.md"), []byte(md), 0o644); err != nil {
		fatal(err.Error())
	}
	js, _ := json.MarshalIndent(map[string]any{"tiers": scores, "reference": ref}, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "report.json"), js, 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Print(md)
}

func notesOf(s *goldset.Set) string {
	mism, trunc, ng := 0, 0, 0
	for _, c := range s.Cases {
		for _, m := range s.Inputs[c.ID].Memories {
			if m.RevisionMismatch {
				mism++
				break
			}
		}
		for _, m := range s.Inputs[c.ID].Memories {
			if m.Truncated {
				trunc++
				break
			}
		}
		if c.Label.OK == "NG" {
			ng++
		}
	}
	return fmt.Sprintf("Cases: %d. Cases citing a memory whose revision differs from the one the case cites: %d. Cases whose memory excerpt is truncated (never a patch target): %d. Cases the member marked NG: %d (scored like the others; the member's note is not machine-checkable).", len(s.Cases), mism, trunc, ng)
}

func loadRun(path string) *goldset.TierRun {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r goldset.TierRun
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func saveRun(path string, r *goldset.TierRun) {
	b, _ := json.MarshalIndent(r, "", " ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "goldeval:", msg)
	os.Exit(2)
}

// splitArgs splits on unquoted whitespace and honours '...' and "..." (an
// empty quoted string is an empty argument).
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	has := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, has = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}
