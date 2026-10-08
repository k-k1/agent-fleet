package afmemory

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

type exportProject struct {
	ID      string
	Display string
	Root    string
}

type exportItem struct {
	Name, Status, Reason string
	Findings             []struct {
		Path, Rule, Hint string
		Line             int
	}
}

type exportPreview struct {
	Project      *exportProject
	Slug         string
	Items        []exportItem
	Counts       map[string]int
	Token        string
	UserScope    int
	NotForClaude int
	Withheld     int
	Index        string
	IndexListed  int
	IndexMore    int
	SwitchOn     bool
}

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func cmdExportSources(c *Client, out io.Writer) error {
	var res struct {
		Projects []struct {
			Project *exportProject
			Count   int
			Reason  string
		}
	}
	if err := c.do("GET", "/agents/memory/claude-export", nil, &res); err != nil {
		return err
	}
	for _, p := range res.Projects {
		note := p.Reason
		if note == "no_root" {
			note = "no main working copy recorded"
		}
		fmt.Fprintf(out, "%-50s %4d memories  %s\n", p.Project.ID, p.Count, strings.TrimSpace(p.Project.Display+" "+note))
	}
	if len(res.Projects) == 0 {
		fmt.Fprintln(out, "no AF project memory to write back")
	}
	return nil
}

// resolveExportProject accepts a project id or its display name from export-sources.
func resolveExportProject(c *Client, want string) (string, error) {
	var res struct {
		Projects []struct{ Project *exportProject }
	}
	if err := c.do("GET", "/agents/memory/claude-export", nil, &res); err != nil {
		return "", err
	}
	var hits []string
	for _, p := range res.Projects {
		if p.Project.ID == want {
			return want, nil
		}
		if p.Project.Display == want {
			hits = append(hits, p.Project.ID)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no AF project %q with memory (see export-sources)", want)
	}
	return "", fmt.Errorf("%q names several projects; use the id from export-sources", want)
}

func cmdExport(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	proj := fs.String("project", "", "")
	dry := fs.Bool("dry-run", false, "")
	var over stringList
	fs.Var(&over, "overwrite", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *proj == "" {
		return &usageError{"export needs --project <id|name> (see export-sources), optionally --dry-run and --overwrite <name>"}
	}
	id, err := resolveExportProject(c, *proj)
	if err != nil {
		return err
	}
	var pv exportPreview
	if err := c.do("GET", "/agents/memory/claude-export/preview?project="+queryEscape(id), nil, &pv); err != nil {
		return err
	}
	fmt.Fprintf(out, "AF project %s -> claude directory %s\n", pv.Project.Display, pv.Slug)
	if pv.SwitchOn {
		fmt.Fprintln(out, "note: Agent Fleet memory is on; a claude session started before it was switched on may still be running and writing its own memory")
	}
	todo := 0
	for _, st := range []string{"new", "update", "remove", "conflict", "secret", "unchanged", "native_only"} {
		if pv.Counts[st] == 0 {
			continue
		}
		fmt.Fprintf(out, "%-11s %d\n", st, pv.Counts[st])
		for _, it := range pv.Items {
			if it.Status != st || st == "unchanged" {
				continue
			}
			switch st {
			case "new", "update", "remove":
				todo++
				fmt.Fprintf(out, "  %s\n", it.Name)
			case "conflict":
				fmt.Fprintf(out, "  %s: %s\n", it.Name, it.Reason)
			case "secret":
				for _, f := range it.Findings {
					fmt.Fprintf(out, "  %s: %s in %s, line %d  %s\n", it.Name, f.Rule, f.Path, f.Line, f.Hint)
				}
			}
		}
	}
	if pv.UserScope > 0 {
		fmt.Fprintf(out, "user scope  %d (not written: claude has no user-wide memory directory)\n", pv.UserScope)
	}
	if pv.NotForClaude > 0 {
		fmt.Fprintf(out, "other kinds %d (limited to other agent kinds, not written)\n", pv.NotForClaude)
	}
	if pv.Withheld > 0 {
		fmt.Fprintf(out, "withheld    %d (did not pass the secret scan or a file-name check)\n", pv.Withheld)
	}
	fmt.Fprintf(out, "MEMORY.md   %s (%d listed, %d in the \"more\" line)\n", pv.Index, pv.IndexListed, pv.IndexMore)
	if *dry {
		fmt.Fprintf(out, "dry run: %d file(s) would change, nothing was written\n", todo)
		return nil
	}
	var res struct {
		Results  []struct{ Name, Result, Reason string }
		Snapshot string
		Index    string
	}
	req := map[string]any{"project": id, "token": pv.Token, "overwrite": []string(over)}
	if err := c.do("POST", "/agents/memory/claude-export?project="+queryEscape(id), req, &res); err != nil {
		return err
	}
	done, skipped, failed := 0, 0, 0
	for _, r := range res.Results {
		switch {
		case r.Result != "skipped":
			done++
		case r.Reason == "write_failed":
			failed++
			fmt.Fprintf(out, "  failed %s\n", r.Name)
		default:
			skipped++
			fmt.Fprintf(out, "  skipped %s: %s\n", r.Name, r.Reason)
		}
	}
	if res.Snapshot != "" {
		fmt.Fprintf(out, "snapshot of claude's memory taken first (%s)\n", res.Snapshot[:min(len(res.Snapshot), 12)])
	}
	fmt.Fprintf(out, "done: %d written or removed, %d skipped, MEMORY.md %s\n", done, skipped, res.Index)
	if failed > 0 {
		return fmt.Errorf("%d file(s) could not be written", failed)
	}
	if res.Index == "failed" {
		return fmt.Errorf("MEMORY.md could not be updated (it changed during the write-back or could not be written); preview again")
	}
	return nil
}
