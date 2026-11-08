package afmemory

// `af-memory lint`: the read-only lint report over AF-owned memory (ADR 0130 decision 5, step 1).
// It asks the Agent for the report of one scope at a time and prints it; nothing is written, and
// the exit status says whether the command worked, not whether anything was found.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
)

type lintScopeReport struct {
	Scope    string            `json:"scope"`
	Project  *project          `json:"project,omitempty"`
	Ref      string            `json:"ref,omitempty"`
	Commit   string            `json:"commit,omitempty"`
	Withheld int               `json:"withheld,omitempty"`
	Terms    int               `json:"terms"`
	Report   memorylint.Report `json:"report"`
}

func cmdLint(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	user := fs.Bool("user", false, "")
	proj := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (*user && *proj != "") {
		return &usageError{"lint takes --user or --project <id|name>, not both (neither lints every scope), and optionally --json"}
	}
	var projects struct {
		Projects []struct {
			Project *project
			Count   int
		}
	}
	if *proj != "" || !*user {
		if err := c.do("GET", "/agents/memory/entries/projects", nil, &projects); err != nil {
			return err
		}
	}
	var queries []string
	switch {
	case *user:
		queries = []string{"scope=user"}
	case *proj != "":
		id := ""
		for _, p := range projects.Projects {
			if p.Project != nil && (p.Project.ID == *proj || p.Project.Display == *proj) {
				id = p.Project.ID
				break
			}
		}
		if id == "" {
			return fmt.Errorf("no project %q (the ids and names are in `af-memory export-sources`)", *proj)
		}
		queries = []string{"scope=project&project=" + queryEscape(id)}
	default:
		queries = []string{"scope=user"}
		for _, p := range projects.Projects {
			if p.Project != nil && p.Count > 0 {
				queries = append(queries, "scope=project&project="+queryEscape(p.Project.ID))
			}
		}
	}
	var reports []lintScopeReport
	for _, q := range queries {
		var r lintScopeReport
		if err := c.do("GET", "/agents/memory/entries/lint?"+q, nil, &r); err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	for _, r := range reports {
		printLint(out, r)
	}
	return nil
}

func printLint(out io.Writer, r lintScopeReport) {
	name := "user (every project reads it)"
	if r.Project != nil {
		name = r.Project.Display
	}
	rev := ""
	if r.Ref != "" {
		rev = fmt.Sprintf("  [%s @ %s]", r.Ref, r.Commit)
	}
	fmt.Fprintf(out, "== %s: %d memories, %d finding(s)%s\n", name, r.Report.Checked, len(r.Report.Findings), rev)
	if r.Withheld > 0 {
		fmt.Fprintf(out, "   (%d file(s) not checked: they did not pass the secret scan or cannot be parsed)\n", r.Withheld)
	}
	for _, s := range r.Report.Skipped {
		fmt.Fprintf(out, "   skipped: %s\n", s)
	}
	for _, f := range r.Report.Findings {
		pos := ""
		if f.Line > 0 {
			pos = fmt.Sprintf(":%d", f.Line)
		}
		subj := f.Subject
		if f.Check == memorylint.CheckOversizeBody {
			subj += " bytes"
		}
		fmt.Fprintf(out, "   %-14s %s%s  %s\n", f.Check, f.Memory, pos, strings.TrimSpace(subj))
	}
	if len(r.Report.Proposals) > 0 {
		fmt.Fprintf(out, "   retired-term proposals (nothing is applied; accept or reject them in the Console, Import & export > Memory lint report):\n")
		for _, p := range r.Report.Proposals {
			fmt.Fprintf(out, "   propose        %q  %s\n                  evidence: %s\n                  used by: %s\n",
				p.Term, p.Kind, p.Evidence, strings.Join(p.Memories, ", "))
		}
	}
}
