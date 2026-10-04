// Package afmemory is `workspace-agent af-memory`, behind the af-memory PATH shim: the member's
// command-line side of AF-owned agent memory (ADR 0108). It lists the published changes and
// imports claude's own auto-memory, through the local Agent's REST with the shell's
// AGENT_TOKEN. The token is only ever sent as a header, never printed.
//
// Applying an import from here is as safe as from the Console: the Agent runs the secret scan
// on every item and the per-user switch gates the write, so this can import nothing a session
// could not already save through memory_save.
package afmemory

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// importBatch is the Agent's per-request cap on items.
const importBatch = 50

const usage = `af-memory — Agent Fleet memory from the command line

Usage:
  af-memory changes [--limit N]              published changes, newest first
  af-memory import-sources                   claude projects that have memory to import
  af-memory import --project <slug> --dry-run
                                             preview: what an import would do, nothing written
  af-memory import --project <slug>          import every new and updated memory
  af-memory --help

<slug> is a name from import-sources. The import needs "Agent Fleet memory" turned on in
Settings > Agents; a file the secret scan flags is listed and skipped, never imported.
`

// Client is the Agent REST as this command uses it.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewClient reads the Agent's address and token from the environment, like the af MCP server.
func NewClient() *Client {
	port := "7700"
	if _, p, err := net.SplitHostPort(os.Getenv("AGENT_ADDR")); err == nil && p != "" {
		port = p
	}
	return &Client{Base: "http://127.0.0.1:" + port, Token: os.Getenv("AGENT_TOKEN"), HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Run is the entry point for `workspace-agent af-memory <args>`.
func Run(args []string) {
	os.Exit(Main(NewClient(), args, os.Stdout, os.Stderr))
}

// Main runs one invocation and returns the exit code: 0 done, 1 failed, 2 usage.
func Main(c *Client, args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errw, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return 0
	case "changes":
		err = cmdChanges(c, args[1:], out)
	case "import-sources":
		err = cmdSources(c, out)
	case "import":
		err = cmdImport(c, args[1:], out)
	default:
		fmt.Fprint(errw, usage)
		return 2
	}
	var ue *usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintln(errw, "af-memory:", ue.msg)
		fmt.Fprint(errw, usage)
		return 2
	}
	fmt.Fprintln(errw, "af-memory:", err)
	return 1
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("cannot reach the Agent on this workspace")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct{ Code, Message string }
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%s (%s)", e.Error.Message, e.Error.Code)
		}
		return fmt.Errorf("the Agent answered HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

type project struct {
	ID      string `json:"id"`
	Display string `json:"display"`
}

func cmdChanges(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("changes", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 30, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return &usageError{"changes takes only --limit N"}
	}
	var res struct {
		Changes []struct {
			At, Op, Scope, Name, AuthorKind, AuthorSession string
			Project                                        *project
		}
		Withheld int
	}
	if err := c.do("GET", fmt.Sprintf("/agents/memory/entries/changes?limit=%d", *limit), nil, &res); err != nil {
		return err
	}
	for _, ch := range res.Changes {
		where := "user"
		if ch.Project != nil {
			where = ch.Project.Display
		}
		fmt.Fprintf(out, "%s  %-7s %s/%s  by %s (%s)\n", ch.At, ch.Op, where, ch.Name, ch.AuthorKind, ch.AuthorSession)
	}
	if res.Withheld > 0 {
		fmt.Fprintf(out, "(%d change(s) withheld: they did not pass the secret scan)\n", res.Withheld)
	}
	return nil
}

func cmdSources(c *Client, out io.Writer) error {
	var res struct {
		Sources []struct {
			Slug    string
			Count   int
			Project *project
			Reason  string
		}
		Withheld int
	}
	if err := c.do("GET", "/agents/memory/claude-import", nil, &res); err != nil {
		return err
	}
	for _, s := range res.Sources {
		dest := s.Reason
		if s.Project != nil {
			dest = "-> " + s.Project.Display
		}
		fmt.Fprintf(out, "%-60s %4d files  %s\n", s.Slug, s.Count, dest)
	}
	if len(res.Sources) == 0 {
		fmt.Fprintln(out, "no claude memory to import")
	}
	if res.Withheld > 0 {
		fmt.Fprintf(out, "(%d project(s) not listed: the name did not pass the secret scan)\n", res.Withheld)
	}
	return nil
}

type previewItem struct {
	Name           string
	Status, Reason string
	Shortened      bool
	SourceHash     string
	SourceModified string
	AFUpdated      string
	Findings       []struct {
		Path, Rule, Hint string
		Line             int
	}
}

type preview struct {
	Project  *project
	Reason   string
	Items    []previewItem
	Counts   map[string]int
	Withheld int
	Truncate bool `json:"truncated"`
}

func cmdImport(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	slug := fs.String("project", "", "")
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *slug == "" {
		return &usageError{"import needs --project <slug> (see import-sources) and optionally --dry-run"}
	}
	var pv preview
	if err := c.do("GET", "/agents/memory/claude-import/preview?slug="+queryEscape(*slug), nil, &pv); err != nil {
		return err
	}
	if pv.Project == nil {
		return fmt.Errorf("this claude project cannot be imported (%s)", pv.Reason)
	}
	fmt.Fprintf(out, "claude project -> %s\n", pv.Project.Display)
	var todo []previewItem
	for _, st := range []string{"new", "update", "unchanged", "forgotten", "secret", "invalid"} {
		if pv.Counts[st] == 0 {
			continue
		}
		fmt.Fprintf(out, "%-10s %d\n", st, pv.Counts[st])
		for _, it := range pv.Items {
			if it.Status != st {
				continue
			}
			switch st {
			case "new", "update":
				todo = append(todo, it)
				note := ""
				if it.Shortened {
					note = "  (description shortened)"
				}
				if st == "update" {
					note += fmt.Sprintf("  (claude file %s is newer than AF %s)", it.SourceModified, it.AFUpdated)
				}
				fmt.Fprintf(out, "  %s%s\n", it.Name, note)
			case "secret":
				for _, f := range it.Findings {
					fmt.Fprintf(out, "  %s: %s in %s, line %d  %s\n", it.Name, f.Rule, f.Path, f.Line, f.Hint)
				}
			case "invalid":
				fmt.Fprintf(out, "  %s: %s\n", it.Name, it.Reason)
			}
		}
	}
	if pv.Withheld > 0 {
		fmt.Fprintf(out, "withheld   %d (file name did not pass the secret scan)\n", pv.Withheld)
	}
	if pv.Truncate {
		fmt.Fprintln(out, "note: only the first files of a very large directory were examined")
	}
	if *dry {
		fmt.Fprintf(out, "dry run: %d would be imported, nothing was written\n", len(todo))
		return nil
	}
	if len(todo) == 0 {
		fmt.Fprintln(out, "nothing to import")
		return nil
	}
	imported, skipped := 0, 0
	for start := 0; start < len(todo); start += importBatch {
		batch := todo[start:min(start+importBatch, len(todo))]
		req := struct {
			Project string              `json:"project"`
			Slug    string              `json:"slug"`
			Items   []map[string]string `json:"items"`
		}{Project: pv.Project.ID, Slug: *slug}
		for _, it := range batch {
			req.Items = append(req.Items, map[string]string{"name": it.Name, "sourceHash": it.SourceHash})
		}
		var res struct {
			Results []struct{ Name, Result, Reason string }
		}
		if err := c.do("POST", "/agents/memory/claude-import?project="+queryEscape(pv.Project.ID), req, &res); err != nil {
			fmt.Fprintf(out, "imported %d, skipped %d before the failure\n", imported, skipped)
			return err
		}
		for _, r := range res.Results {
			if r.Result == "skipped" {
				skipped++
				fmt.Fprintf(out, "  skipped %s: %s\n", r.Name, r.Reason)
			} else {
				imported++
			}
		}
		fmt.Fprintf(out, "progress: %d/%d\n", min(start+importBatch, len(todo)), len(todo))
	}
	fmt.Fprintf(out, "done: %d imported, %d skipped\n", imported, skipped)
	if skipped > 0 {
		return fmt.Errorf("%d item(s) were skipped", skipped)
	}
	return nil
}

func queryEscape(s string) string { return url.QueryEscape(s) }
