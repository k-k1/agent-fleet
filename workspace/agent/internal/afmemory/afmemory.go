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
	"strings"
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
  af-memory muse-import-sources              muse memory directories that can be imported
  af-memory muse-import --dir <name> [--scope project|personal_project] --dry-run
                                             preview: what a muse import would do, nothing written
  af-memory muse-import --dir <name> [--scope ...]
                                             import every new and updated muse note
  af-memory muse-import --scope personal --into-user [--dry-run]
                                             muse's personal notes, into your user memory
  af-memory export-sources                   AF projects whose memory can be written back to claude
  af-memory export --project <id|name> --dry-run
                                             preview: what a write-back would do, nothing written
  af-memory export --project <id|name> [--overwrite <name>]...
                                             copy AF memory into claude's own memory (claude only)
  af-memory lint [--user | --project <id|name>] [--json]
                                             read-only report: broken [[links]], paths and symbols
                                             missing from the default branch, retired terms,
                                             bodies over 4,096 bytes (nothing is written)
  af-memory --help

<slug> is a name from import-sources. The import needs "Agent Fleet memory" turned on in
Settings > Agent memory; a file the secret scan flags is listed and skipped, never imported.

muse-import reads one of muse's three stores. project (the default): <working copy>/.agents/memory
into that working copy's AF project; <name> is the working copy's directory name. A file git
tracks there may be shared with teammates; it is marked "shared" in the preview.
personal_project: muse's own directory for a working copy (~/.local/share/muse/memory/projects),
<name> as listed by muse-import-sources, into that working copy's AF project.
personal: muse's notes for all projects, into AF's USER scope, which every project reads; it is
never imported unless you pass --into-user. A note needs no frontmatter: its name is the file
name ("a/b.md" becomes "a--b"), its description the first non-empty line, and it has no type.
Same switch and secret scan as import.

lint reads every memory of the scope (no use is counted) and the project's origin default branch by
ref; it never touches a working copy. A project without git, an orphaned one and the user scope get
the link, term and size checks only. The retired-term list is edited in Settings > Agent memory.

export is the other direction, one-shot: new and changed AF memories become files in claude's
memory directory for the project, MEMORY.md is regenerated, and claude's memory is snapshotted
first (Settings > Agent memory can restore it). A file claude has that AF did not write, or that
changed since, is a conflict and is kept unless you name it with --overwrite. It does not need the
"Agent Fleet memory" switch.
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
	case "muse-import-sources":
		err = cmdMuseSources(c, out)
	case "muse-import":
		err = runImport(c, args[1:], out, museImportSpec)
	case "export-sources":
		err = cmdExportSources(c, out)
	case "export":
		err = cmdExport(c, args[1:], out)
	case "lint":
		err = cmdLint(c, args[1:], out)
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

func cmdMuseSources(c *Client, out io.Writer) error {
	var res struct {
		Sources []struct {
			Scope   string
			Dir     string
			Count   int
			Shared  int
			Target  string
			Project *project
			Reason  string
		}
		Withheld int
	}
	if err := c.do("GET", "/agents/memory/muse-import", nil, &res); err != nil {
		return err
	}
	for _, s := range res.Sources {
		dest := s.Reason
		if s.Project != nil {
			dest = "-> " + s.Project.Display
		}
		if s.Target == "user" {
			dest = "-> your USER memory (every project reads it); only with --into-user"
		}
		shared := ""
		if s.Shared > 0 {
			shared = fmt.Sprintf("  (%d tracked by git: may be shared with teammates)", s.Shared)
		}
		name := s.Dir
		if name == "" {
			name = "(personal)"
		}
		fmt.Fprintf(out, "%-16s %-60s %4d files  %s%s\n", s.Scope, name, s.Count, dest, shared)
	}
	if len(res.Sources) == 0 {
		fmt.Fprintln(out, "no muse memory to import")
	}
	if res.Withheld > 0 {
		fmt.Fprintf(out, "(%d working copy(ies) not listed: the name did not pass the secret scan)\n", res.Withheld)
	}
	return nil
}

// importSpec is what differs between the claude import and the muse import.
type importSpec struct {
	cmd, flag, what string // "import", "project" | "muse-import", "dir"
	base, query     string // REST path and the preview's query parameter
	reqKey          string // the apply body's source key
}

var (
	claudeImportSpec = importSpec{"import", "project", "claude project", "/agents/memory/claude-import", "slug", "slug"}
	museImportSpec   = importSpec{"muse-import", "dir", "muse working copy", "/agents/memory/muse-import", "dir", "dir"}
)

type previewItem struct {
	Name           string
	Status, Reason string
	SourceHash     string
	SourceModified string
	AFUpdated      string
	Shared         bool
	Path           string
	Derived        bool
	Findings       []struct {
		Path, Rule, Hint string
		Line             int
	}
}

type preview struct {
	Target   string
	Project  *project
	Reason   string
	Items    []previewItem
	Counts   map[string]int
	Withheld int
	Truncate bool `json:"truncated"`
}

func cmdImport(c *Client, args []string, out io.Writer) error {
	return runImport(c, args, out, claudeImportSpec)
}

func runImport(c *Client, args []string, out io.Writer, sp importSpec) error {
	fs := flag.NewFlagSet(sp.cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	slug := fs.String(sp.flag, "", "")
	dry := fs.Bool("dry-run", false, "")
	var scope string
	var intoUser bool
	if sp.cmd == "muse-import" {
		fs.StringVar(&scope, "scope", "project", "")
		fs.BoolVar(&intoUser, "into-user", false, "")
	}
	err := fs.Parse(args)
	personal := scope == "personal"
	if err != nil || fs.NArg() != 0 || (*slug == "") == !personal {
		return &usageError{fmt.Sprintf("%s needs --%s <name> (see %s-sources) and optionally --dry-run; the muse personal scope takes --scope personal --into-user and no --%s", sp.cmd, sp.flag, sp.cmd, sp.flag)}
	}
	if sp.cmd == "muse-import" && scope != "project" && scope != "personal_project" && !personal {
		return &usageError{"--scope must be project, personal_project or personal"}
	}
	if intoUser != personal && sp.cmd == "muse-import" {
		if personal {
			return &usageError{"the personal scope lands in your user memory, which every project reads: pass --into-user to say so"}
		}
		return &usageError{"--into-user belongs to --scope personal only"}
	}
	query := sp.query + "=" + queryEscape(*slug)
	if sp.cmd == "muse-import" {
		query += "&scope=" + queryEscape(scope)
		if intoUser {
			query += "&into=user"
		}
	}
	var pv preview
	if err := c.do("GET", sp.base+"/preview?"+query, nil, &pv); err != nil {
		return err
	}
	dest := ""
	switch {
	case pv.Target == "user" && pv.Reason == "":
		dest = "your USER memory (every project reads it)"
	case pv.Project != nil:
		dest = pv.Project.Display
	default:
		return fmt.Errorf("this %s cannot be imported (%s)", sp.what, pv.Reason)
	}
	fmt.Fprintf(out, "%s -> %s\n", sp.what, dest)
	var todo []previewItem
	for _, st := range []string{"new", "update", "unchanged", "diverged", "forgotten", "secret", "invalid"} {
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
				if it.Shared {
					note = "  (shared: git tracks this file, teammates may have it)"
				}
				if st == "update" && it.Reason == "refresh_shortened" {
					note = "  (AF copy was imported with a cut description; refreshed from claude's file)"
				} else if st == "update" {
					note += fmt.Sprintf("  (%s file %s is newer than AF %s)", strings.Fields(sp.what)[0], it.SourceModified, it.AFUpdated)
				}
				if it.Derived {
					note += "  (no frontmatter: name from the file name, description from its first line, no type)"
				}
				fmt.Fprintf(out, "  %s%s\n", it.Name, note)
			case "secret":
				for _, f := range it.Findings {
					fmt.Fprintf(out, "  %s: %s in %s, line %d  %s\n", it.Name, f.Rule, f.Path, f.Line, f.Hint)
				}
			case "invalid", "diverged":
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
		req := map[string]any{"project": "", sp.reqKey: *slug}
		if pv.Project != nil {
			req["project"] = pv.Project.ID
		}
		if sp.cmd == "muse-import" {
			req["scope"] = scope
			if intoUser {
				req["into"] = "user"
			}
		}
		items := []map[string]string{}
		for _, it := range batch {
			items = append(items, map[string]string{"name": it.Name, "sourceHash": it.SourceHash})
		}
		req["items"] = items
		var res struct {
			Results []struct{ Name, Result, Reason string }
		}
		if err := c.do("POST", sp.base+"?project="+queryEscape(req["project"].(string)), req, &res); err != nil {
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
