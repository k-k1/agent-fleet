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
	"sort"
	"strings"
	"time"

	"github.com/k-k1/memengine"
	"github.com/k-k1/memengine/internal/goldset"
)

type tierFlags []string

func (t *tierFlags) String() string     { return strings.Join(*t, ",") }
func (t *tierFlags) Set(v string) error { *t = append(*t, v); return nil }

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "export-prompts":
			exportPrompts(os.Args[2:])
			return
		case "score":
			scoreAnswers(os.Args[2:])
			return
		case "run":
			os.Args = append(os.Args[:1], os.Args[2:]...)
		}
	}
	runLive()
}

func runLive() {
	var tiers tierFlags
	cases := flag.String("cases", "cases.jsonl", "gold set (JSONL)")
	inputs := flag.String("inputs", "inputs.jsonl", "memories the cases cite (JSONL)")
	out := flag.String("out", "run", "directory for outputs and the report")
	workers := flag.Int("workers", 3, "model calls in flight per tier")
	limit := flag.Int("limit", 0, "run only the first N cases (a pilot); 0 means all")
	timeout := flag.Duration("timeout", 3*time.Minute, "per-call timeout")
	resume := flag.Bool("resume", true, "reuse answers already in -out instead of calling again")
	flag.Var(&tiers, "tier", "name=command, or a preset claude:<model> / codex:<model> (repeatable); the first is the small tier")
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
		name, cmdline, ok := resolveTier(spec)
		argv, err := splitArgs(cmdline)
		if err != nil {
			fatal("bad -tier " + spec + ": " + err.Error())
		}
		if !ok || name == "" || len(argv) == 0 {
			fatal("bad -tier " + spec + ": want name=command")
		}
		model := memengine.Command{Argv: argv, Timeout: *timeout}
		path := filepath.Join(*out, strings.NewReplacer(":", "_", "/", "_").Replace(name)+".run.json")
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
	writeReport(*out, set)
}

// writeReport scores every tier run found in dir (live and replay alike) and
// writes report.md and report.json, one column per tier name.
func writeReport(dir string, set *goldset.Set) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.run.json"))
	sort.Strings(files)
	var runs []*goldset.TierRun
	for _, f := range files {
		if r := loadRun(f); r != nil {
			runs = append(runs, r)
		}
	}
	// Live tiers first, so "tier 1" is the small live tier.
	sort.SliceStable(runs, func(i, j int) bool { return (runs[i].Mode != "replay") && (runs[j].Mode == "replay") })
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
	md := goldset.Markdown(scores, ref, agree, []string{notesOf(set)})
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		fatal(err.Error())
	}
	js, _ := json.MarshalIndent(map[string]any{"tiers": scores, "reference": ref}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "report.json"), js, 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Print(md)
}

// exportPrompts writes prompts.jsonl: every call as the one-shot runner would
// send it, for models that can only answer as sessions.
func exportPrompts(args []string) {
	fs := flag.NewFlagSet("export-prompts", flag.ExitOnError)
	cases := fs.String("cases", "cases.jsonl", "gold set (JSONL)")
	inputs := fs.String("inputs", "inputs.jsonl", "memories the cases cite (JSONL)")
	out := fs.String("out", "run/prompts.jsonl", "file to write")
	fs.Parse(args)
	set, err := goldset.Load(*cases, *inputs)
	if err != nil {
		fatal(err.Error())
	}
	var b strings.Builder
	n := 0
	for _, p := range goldset.Prompts(set) {
		j, _ := json.Marshal(p)
		b.Write(j)
		b.WriteByte('\n')
		n++
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Fprintf(os.Stderr, "wrote %d prompts to %s\n", n, *out)
}

// scoreAnswers scores a recorded answers file with the same validation and
// scoring as a live run, then refreshes the combined report.
func scoreAnswers(args []string) {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	cases := fs.String("cases", "cases.jsonl", "gold set (JSONL)")
	inputs := fs.String("inputs", "inputs.jsonl", "memories the cases cite (JSONL)")
	out := fs.String("out", "run", "directory holding the tier runs and the report")
	answers := fs.String("answers", "", "JSONL of {case_id, call, output[, usage]}")
	tier := fs.String("tier", "", "tier name (a report column)")
	fs.Parse(args)
	if *answers == "" || *tier == "" {
		fatal("score needs --answers and --tier")
	}
	set, err := goldset.Load(*cases, *inputs)
	if err != nil {
		fatal(err.Error())
	}
	as, err := goldset.LoadAnswers(*answers)
	if err != nil {
		fatal(err.Error())
	}
	run := goldset.RunWith(context.Background(), *tier, goldset.NewReplay(set, as), set, 1, nil, true, nil)
	run.Mode = "replay"
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err.Error())
	}
	saveRun(filepath.Join(*out, strings.NewReplacer(":", "_", "/", "_").Replace(*tier)+".run.json"), run)
	writeReport(*out, set)
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

// resolveTier turns a -tier value into a name and a command line. A tier is
// "name=command" (any one-shot CLI that reads the prompt on stdin), or a preset
// "claude:<model>" / "codex:<model>" whose name is the preset itself, so a
// later run adds a column to the report by adding a -tier.
func resolveTier(spec string) (name, cmdline string, ok bool) {
	if n, c, found := strings.Cut(spec, "="); found {
		return n, c, true
	}
	cli, model, found := strings.Cut(spec, ":")
	if !found || model == "" {
		return "", "", false
	}
	switch cli {
	case "claude":
		// CLAUDE_CODE_DISABLE_CLAUDE_MDS keeps the host's instruction files out of the call.
		return spec, `env CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 claude -p --output-format json --tools "" --system-prompt "You output only the JSON object asked for." --no-session-persistence --disable-slash-commands --strict-mcp-config --setting-sources "" --model ` + model, true
	case "codex":
		return spec, "codex exec --model " + model + " --json --skip-git-repo-check --sandbox read-only", true
	}
	return "", "", false
}
