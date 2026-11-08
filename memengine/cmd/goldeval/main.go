// Command goldeval runs both model tiers over the member-labelled gold set and
// writes the scores. The model is a one-shot command given by argv (prompt on
// stdin, JSON on stdout); nothing here knows an SDK or a key.
//
//	go run ./cmd/goldeval -cases cases.jsonl -inputs inputs.jsonl -out run/ \
//	  -config models.json -bar bar.json -tier claude:<model id> -tier codex:<model id>
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
	out := flag.String("out", "run", "directory for outputs and the report; keep it outside the repository (it holds memory text)")
	workers := flag.Int("workers", 3, "model calls in flight per tier")
	limit := flag.Int("limit", 0, "run only the first N cases (a pilot); 0 means all")
	timeout := flag.Duration("timeout", 3*time.Minute, "per-call timeout")
	resume := flag.Bool("resume", true, "reuse answers already in -out instead of calling again")
	conditioned := flag.Bool("gold-conditioned", false, "also make a patch call for every case labelled supersede or merge (as replay does), scored on validity and kept facts only")
	cfgPath := flag.String("config", "", "model config (roles, runners, candidates); built-in runners without it")
	barPath := flag.String("bar", "", "thresholds file; adds pass/fail per candidate and a suggested role mapping")
	flag.Var(&tiers, "tier", "<runner>:<model id> (e.g. claude:<id>), or name=command; repeatable. Default: the config's candidates")
	flag.Parse()
	cfg, err := memengine.LoadConfig(*cfgPath)
	if err != nil {
		fatal(err.Error())
	}
	if len(tiers) == 0 {
		for _, c := range cfg.Candidates {
			r, _, ok := memengine.SplitTier(c.Tier)
			switch {
			case !ok:
				fmt.Fprintf(os.Stderr, "goldeval: skipping candidate %q: not <runner>:<model id>\n", c.Tier)
			case cfg.Runners[r].Session:
				fmt.Fprintf(os.Stderr, "goldeval: candidate %q is session-only; score it with `goldeval score`\n", c.Tier)
			default:
				tiers = append(tiers, c.Tier)
			}
		}
	}
	if len(tiers) == 0 {
		fatal("no tier: give -tier <runner>:<model> or a -config with candidates")
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
	for _, spec := range tiers {
		name, runner, model, cmd, err := resolveTier(cfg, spec)
		if err != nil {
			fatal(err.Error())
		}
		cmd.Timeout = *timeout
		path := runPath(*out, name)
		var prev *goldset.TierRun
		if *resume {
			prev = loadRun(path)
		}
		if prev == nil || (prev.Baseline.TotalInput() == 0 && len(prev.Classify) == 0 && len(prev.Patch) == 0) {
			// The fixed overhead of the command, from a prompt with no content. A
			// retired model fails here, before anything is scored: there is no
			// fallback to another model.
			rep, err := cmd.Call(ctx, `Reply with the JSON object {"ok":true}.`)
			if err != nil {
				fatal("baseline call for " + name + " failed, nothing scored: " + err.Error())
			}
			if prev == nil {
				prev = &goldset.TierRun{Name: name}
			}
			prev.Baseline = rep.Usage
		}
		run := goldset.RunWith(ctx, name, cmd, set, *workers, prev, *conditioned, func(s string) { fmt.Fprintln(os.Stderr, s) })
		run.Model, run.Runner, run.Date, run.GoldHash = model, runner, time.Now().UTC().Format("2006-01-02"), set.GoldHash
		saveRun(path, run)
	}
	writeReport(*out, set, cfg, *barPath)
}

// writeReport scores every tier run found in dir (live and replay alike) and
// writes report.md and report.json, one column per tier name.
func writeReport(dir string, set *goldset.Set, cfg memengine.Config, barPath string) {
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
	if barPath != "" {
		bar, err := goldset.LoadBar(barPath)
		if err != nil {
			fatal(err.Error())
		}
		md += goldset.BarMarkdown(bar, scores, set.GoldHash, cfg.Cost)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		fatal(err.Error())
	}
	js, _ := json.MarshalIndent(map[string]any{"tiers": scores, "reference": ref}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "report.json"), js, 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Print(md)
}

// exportPrompts writes the two prompt files a replay session answers. They are
// separate on purpose: a patch line exists only for cases whose gold label is
// patch, supersede or merge, so a session that saw it next to its classify
// line could read the label off it. Answer the classify file completely, in a
// fresh session, before handing over the patch file.
func exportPrompts(args []string) {
	fs := flag.NewFlagSet("export-prompts", flag.ExitOnError)
	cases := fs.String("cases", "cases.jsonl", "gold set (JSONL)")
	inputs := fs.String("inputs", "inputs.jsonl", "memories the cases cite (JSONL)")
	out := fs.String("out", "run", "directory for prompts-classify.jsonl and prompts-patch.jsonl")
	fs.Parse(args)
	set, err := goldset.Load(*cases, *inputs)
	if err != nil {
		fatal(err.Error())
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err.Error())
	}
	for name, lines := range map[string][]goldset.PromptLine{"prompts-classify.jsonl": goldset.ClassifyPrompts(set), "prompts-patch.jsonl": goldset.PatchPrompts(set)} {
		var b strings.Builder
		for _, p := range lines {
			j, _ := json.Marshal(p)
			b.Write(j)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(*out, name), []byte(b.String()), 0o644); err != nil {
			fatal(err.Error())
		}
		fmt.Fprintf(os.Stderr, "wrote %d prompts to %s\n", len(lines), filepath.Join(*out, name))
	}
}

// scoreAnswers scores a recorded answers file with the same validation and
// scoring as a live run, then refreshes the combined report.
func scoreAnswers(args []string) {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	cases := fs.String("cases", "cases.jsonl", "gold set (JSONL)")
	inputs := fs.String("inputs", "inputs.jsonl", "memories the cases cite (JSONL)")
	out := fs.String("out", "run", "directory holding the tier runs and the report")
	var answers tierFlags
	fs.Var(&answers, "answers", "JSONL of {case_id, call, output[, usage]}; repeatable (the classify answers and the patch answers)")
	tier := fs.String("tier", "", "<runner>:<model id> (a report column), e.g. agy-session:some-model")
	cfgPath := fs.String("config", "", "model config (for candidate costs)")
	barPath := fs.String("bar", "", "thresholds file")
	fs.Parse(args)
	cfg, err := memengine.LoadConfig(*cfgPath)
	if err != nil {
		fatal(err.Error())
	}
	if len(answers) == 0 || *tier == "" {
		fatal("score needs --answers and --tier")
	}
	set, err := goldset.Load(*cases, *inputs)
	if err != nil {
		fatal(err.Error())
	}
	as, srcs, err := goldset.LoadAnswerFiles(answers)
	if err != nil {
		fatal(err.Error())
	}
	run := goldset.RunWith(context.Background(), *tier, goldset.NewReplay(set, as), set, 1, nil, true, nil)
	run.Mode = "replay"
	run.AnswerFiles = srcs
	run.Runner, run.Model, _ = memengine.SplitTier(*tier)
	run.Date, run.GoldHash = time.Now().UTC().Format("2006-01-02"), set.GoldHash
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err.Error())
	}
	saveRun(runPath(*out, *tier), run)
	writeReport(*out, set, cfg, *barPath)
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

// runPath is the file a tier's run is kept in.
func runPath(dir, name string) string {
	return filepath.Join(dir, strings.NewReplacer(":", "_", "/", "_").Replace(name)+".run.json")
}

// resolveTier turns a -tier value into a command. "<runner>:<model id>" goes
// through the config's runner templates (so no model ID lives in code);
// "name=command" is any one-shot CLI, taken as written. A session-only runner
// or an unknown one is an error, never a different model.
func resolveTier(cfg memengine.Config, spec string) (name, runner, model string, cmd memengine.Command, err error) {
	if n, c, found := strings.Cut(spec, "="); found {
		argv, err := splitArgs(c)
		if err != nil || n == "" || len(argv) == 0 {
			return "", "", "", cmd, fmt.Errorf("bad -tier %q: want name=command", spec)
		}
		return n, "custom", n, memengine.Command{Argv: argv}, nil
	}
	cmd, err = cfg.Command(spec)
	if err != nil {
		return "", "", "", cmd, err
	}
	runner, model, _ = memengine.SplitTier(spec)
	return spec, runner, model, cmd, nil
}
