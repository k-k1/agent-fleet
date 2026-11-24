package afmemory

// `af-memory promote-candidates`: which project memories have proven themselves enough to be
// proposed to the repository's own documents (ADR 0130 decision 11). Read-only, one project at a
// time; the exit status says whether the command worked, not whether anything qualified.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

type promoteReport struct {
	Project     *project `json:"project,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	Commit      string   `json:"commit,omitempty"`
	MinSessions int      `json:"minSessions"`
	QuietDays   int      `json:"quietDays"`
	Withheld    int      `json:"withheld,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	memorypromote.Result
}

func cmdPromoteCandidates(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("promote-candidates", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	proj := fs.String("project", "", "")
	days := fs.Int("days", 0, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *days < 0 {
		return &usageError{"promote-candidates takes --project <id|name> (without it, every project that holds memory), and optionally --days N and --json"}
	}
	var projects struct {
		Projects []struct {
			Project *project
			Count   int
		}
	}
	if err := c.do("GET", "/agents/memory/entries/projects", nil, &projects); err != nil {
		return err
	}
	var ids []string
	for _, p := range projects.Projects {
		if p.Project == nil {
			continue
		}
		if *proj != "" && (p.Project.ID == *proj || p.Project.Display == *proj) {
			ids = []string{p.Project.ID}
			break
		}
		if *proj == "" && p.Count > 0 {
			ids = append(ids, p.Project.ID)
		}
	}
	if *proj != "" && len(ids) == 0 {
		return fmt.Errorf("no project %q (the ids and names are in `af-memory export-sources`)", *proj)
	}
	var reports []promoteReport
	for _, id := range ids {
		q := "project=" + queryEscape(id)
		if *days > 0 {
			q += "&days=" + strconv.Itoa(*days)
		}
		var r promoteReport
		if err := c.do("GET", "/agents/memory/entries/promotion-candidates?"+q, nil, &r); err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if *asJSON {
		if reports == nil {
			reports = []promoteReport{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	if len(reports) == 0 {
		fmt.Fprintln(out, "no project holds memory")
	}
	for _, r := range reports {
		printPromote(out, r)
	}
	return nil
}

func printPromote(out io.Writer, r promoteReport) {
	name := "?"
	if r.Project != nil {
		name = r.Project.Display
	}
	rev := ""
	if r.Ref != "" {
		rev = fmt.Sprintf("  [%s @ %s]", r.Ref, r.Commit)
	}
	fmt.Fprintf(out, "== %s: %d candidate(s), %d held (confirmed by %d sessions, unchanged for %d days)%s\n",
		name, len(r.Candidates), len(r.Held), r.MinSessions, r.QuietDays, rev)
	if r.Withheld > 0 {
		fmt.Fprintf(out, "   (%d file(s) not checked: they did not pass the secret scan or cannot be parsed)\n", r.Withheld)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(out, "   skipped: %s\n", s)
	}
	for _, c := range r.Candidates {
		fmt.Fprintf(out, "   candidate  %s r%d  %s, %d sessions, since %s\n              anchors: %s\n",
			c.Name, c.Revision, c.Kind, c.Sessions, c.Updated, strings.Join(c.Anchors, ", "))
	}
	for _, h := range r.Held {
		parts := make([]string, 0, len(h.Missing))
		for _, m := range h.Missing {
			p := m.Rule
			if m.Detail != "" {
				p += " (" + m.Detail + ")"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(out, "   held       %s r%d  %s: %s\n", h.Name, h.Revision, h.Kind, strings.Join(parts, "; "))
	}
}
