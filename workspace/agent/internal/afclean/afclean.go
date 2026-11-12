// Package afclean is `workspace-agent af-clean`, behind the af-clean PATH shim: the command-line
// side of the low-disk cleanup the Console offers from its notice (ReclaimModal, #3880). It
// holds no classifier of its own. What it proposes is what the Agent's own endpoints report for
// that modal, filtered by the same rules, and what --apply deletes is that list (see applyAll for what the
// guarantee does and does not cover), one
// request per item, through the same two delete calls the Console makes:
//
//	build output in dormant working copies   GET/POST /cleanup/worktree-output
//	package-manager caches                   GET/DELETE /cleanup/tool-caches
//
// Everything else the cleanup screens show (trash, archives, leftovers, ~/.af-work, the home
// breakdown) is informational here and never deleted from this command: the Console puts a
// per-item confirm in front of those, which a flag cannot stand in for.
//
// The Agent is reached on the loopback with the shell's AGENT_TOKEN, as af-memory does, so it
// works inside a running Workspace only. The token is only ever sent as a header.
package afclean

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const usage = `af-clean — free regenerable disk space, the way the Console's low-disk cleanup does

Usage:
  af-clean [--dry-run] [--json]     list what would be removed; nothing is deleted (the default)
  af-clean --apply [--json]         list again, then remove those items
  af-clean --help

It proposes two kinds of item, the same ones the Console's "Clean regenerable items…" offers:
  worktree  node_modules, .next, target and .gradle of a working copy nobody has touched for 30
            days, with no uncommitted change and no session using it (the working copy stays)
  cache     a package-manager cache (go-build, npm, uv, pip, Maven, Gradle versions nothing pins)
            that no build or install is using

Removal is not reversible and does not go to the trash. --apply builds its own fresh list; it does
not reuse the output of an earlier --dry-run, so what it removes can differ from that earlier run.
Just before each working copy is cleaned its list is read again; if it differs from the one
printed, that item is skipped. The Agent's delete names only the working copy, so a folder such as
.next that appears after that last check can still be removed with it. Whatever the
Agent still refuses (a live session, a cache in use) is reported as "skipped".

Exit status: 0 done, 1 something failed or the Agent could not be asked, 2 usage.
`

// Client is the Agent REST as this command uses it.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewClient reads the Agent's address and token from the environment, like af-memory.
func NewClient() *Client {
	port := "7700"
	if _, p, err := net.SplitHostPort(os.Getenv("AGENT_ADDR")); err == nil && p != "" {
		port = p
	}
	// A survey of a big home and a delete of a big cache each take minutes.
	return &Client{Base: "http://127.0.0.1:" + port, Token: os.Getenv("AGENT_TOKEN"), HTTP: &http.Client{Timeout: 30 * time.Minute}}
}

// Run is the entry point for `workspace-agent af-clean <args>`.
func Run(args []string) { os.Exit(Main(NewClient(), args, os.Stdout, os.Stderr)) }

// Dir is one directory a worktree item would drop.
type Dir struct {
	Rel   string `json:"rel"`
	Bytes int64  `json:"bytes"`
	// Link marks a symlink: only the link is removed.
	Link bool `json:"link,omitempty"`
}

// Item is one line of the proposal. Outcome is set by --apply only.
type Item struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // "worktree" | "cache"
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	Dirs      []Dir  `json:"dirs,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Outcome is "removed", "skipped" (the Agent refused, or the item changed since it was
	// listed) or "failed".
	Outcome string `json:"outcome,omitempty"`
	Freed   int64  `json:"freed,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Result is the --json document.
type Result struct {
	Mode  string `json:"mode"` // "dry-run" | "apply"
	Items []Item `json:"items"`
	// Total is what the listed items hold; Freed is what --apply took.
	Total int64 `json:"total_bytes"`
	Freed int64 `json:"freed_bytes,omitempty"`
	// Partial: one of the two sources could not be read, so the list may be incomplete.
	Partial bool `json:"partial,omitempty"`
	Skipped int  `json:"skipped,omitempty"`
	Failed  int  `json:"failed,omitempty"`
}

// Main runs one invocation and returns the exit code: 0 done, 1 failed, 2 usage.
func Main(c *Client, args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("af-clean", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apply := fs.Bool("apply", false, "")
	dry := fs.Bool("dry-run", false, "")
	asJSON := fs.Bool("json", false, "")
	help := fs.Bool("help", false, "")
	h := fs.Bool("h", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (*apply && *dry) {
		msg := "unknown or conflicting arguments"
		if *apply && *dry {
			msg = "--apply and --dry-run cannot be combined"
		}
		fmt.Fprintln(errw, "af-clean:", msg)
		fmt.Fprint(errw, usage)
		return 2
	}
	if *help || *h {
		fmt.Fprint(out, usage)
		return 0
	}
	res, err := propose(c)
	if err != nil {
		fmt.Fprintln(errw, "af-clean:", err)
		return 1
	}
	res.Mode = "dry-run"
	if *apply {
		res.Mode = "apply"
		applyAll(c, res)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(out, string(b))
	} else {
		render(out, res)
	}
	if res.Failed > 0 {
		return 1
	}
	return 0
}

// --- Agent REST ---

type reply struct {
	status int
	body   []byte
}

func (c *Client) do(method, path string) (*reply, error) {
	req, err := http.NewRequest(method, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("cannot reach the Agent on this workspace (af-clean works inside a running Workspace only)")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return &reply{status: resp.StatusCode, body: b}, nil
}

func (r *reply) errCode() string {
	var e struct {
		Error struct{ Code, Message string }
	}
	if json.Unmarshal(r.body, &e) == nil {
		return e.Error.Code
	}
	return ""
}

func (r *reply) errText() string {
	var e struct {
		Error struct{ Code, Message string }
	}
	if json.Unmarshal(r.body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return fmt.Sprintf("the Agent answered HTTP %d", r.status)
}

// --- worktree output (console/src/features/sessions/worktreeOutput.ts) ---

type wtoReport struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Dirs      []Dir  `json:"dirs"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

func validReport(r *wtoReport) bool {
	return r.Name != "" && (r.Status == "candidate" || r.Status == "empty" || r.Status == "refused")
}

// wtoPost is one POST for one working copy. A 409 carries the report of a refusal, so a report
// body is a result whatever the status; anything else is an error.
func (c *Client) wtoPost(name string, dryRun bool) (*wtoReport, error) {
	q := "worktree=" + url.QueryEscape(name)
	if dryRun {
		q += "&dry_run=1"
	}
	r, err := c.do("POST", "/cleanup/worktree-output?"+q)
	if err != nil {
		return nil, err
	}
	var rep wtoReport
	if json.Unmarshal(r.body, &rep) == nil && validReport(&rep) {
		return &rep, nil
	}
	return nil, errors.New(r.errText())
}

func dirKey(ds []Dir) string {
	rels := make([]string, len(ds))
	for i, d := range ds {
		rels[i] = d.Rel
	}
	sort.Strings(rels)
	return strings.Join(rels, "\n")
}

// --- proposal (ReclaimModal's loading effect) ---

// propose builds the list the Console's modal builds. A side that fails does not hide the
// other; only both failing (or the list being empty because one failed) is an error.
func propose(c *Client) (*Result, error) {
	res := &Result{Items: []Item{}}
	missed := false

	sr, err := c.do("GET", "/cleanup/worktree-output")
	if err != nil {
		return nil, err
	}
	var survey struct {
		Worktrees []wtoReport `json:"worktrees"`
	}
	switch {
	case sr.status == 404:
		// An Agent older than the endpoint: the Console shows nothing for it and says nothing.
	case sr.status >= 400 || json.Unmarshal(sr.body, &survey) != nil || survey.Worktrees == nil:
		missed = true
	default:
		// Candidates first, largest first, then by name (sortReports).
		sort.SliceStable(survey.Worktrees, func(i, j int) bool {
			a, b := survey.Worktrees[i], survey.Worktrees[j]
			if (a.Status == "candidate") != (b.Status == "candidate") {
				return a.Status == "candidate"
			}
			if a.Bytes != b.Bytes {
				return a.Bytes > b.Bytes
			}
			return a.Name < b.Name
		})
		for _, w := range survey.Worktrees {
			if w.Status != "candidate" {
				continue
			}
			// The dry run is what the delete re-derives, so list ITS directories.
			dry, err := c.wtoPost(w.Name, true)
			if err != nil || dry.Status != "candidate" || dry.Bytes <= 0 {
				continue
			}
			res.Items = append(res.Items, Item{ID: "wt:" + w.Name, Kind: "worktree", Name: w.Name, Bytes: dry.Bytes, Dirs: dry.Dirs, Truncated: dry.Truncated})
		}
	}

	tr, err := c.do("GET", "/cleanup/tool-caches")
	if err != nil {
		return nil, err
	}
	var caches struct {
		Caches []struct {
			Name    string `json:"name"`
			Bytes   int64  `json:"bytes"`
			Busy    []int  `json:"busy"`
			Skipped string `json:"skipped"`
		} `json:"caches"`
	}
	if tr.status >= 400 || json.Unmarshal(tr.body, &caches) != nil || caches.Caches == nil {
		missed = true
	} else {
		for _, k := range caches.Caches {
			// A row in use, or one the Agent offers nothing for, is not something this can take.
			if k.Bytes > 0 && len(k.Busy) == 0 && k.Skipped == "" {
				res.Items = append(res.Items, Item{ID: "cache:" + k.Name, Kind: "cache", Name: k.Name, Bytes: k.Bytes})
			}
		}
	}
	res.Partial = missed
	if len(res.Items) == 0 && missed {
		return nil, errors.New("could not read what can be cleaned from the Agent")
	}
	for _, it := range res.Items {
		res.Total += it.Bytes
	}
	return res, nil
}

// --- apply (ReclaimModal's run) ---

// applyAll deletes the listed items and no other item. A worktree is only cleaned when a fresh
// dry run lists the same directories as the proposal did: the real POST names the working copy
// alone and removes whatever it finds, so a directory that appeared since would otherwise go
// unseen. A directory created between that check and the POST is still removed with it: the API
// takes no expected set, and the Console's ReclaimModal has the same window.
func applyAll(c *Client, res *Result) {
	for i := range res.Items {
		it := &res.Items[i]
		if it.Kind == "worktree" {
			dry, err := c.wtoPost(it.Name, true)
			if err != nil {
				// The re-check could not be done (Agent gone, HTTP 5xx, unreadable answer): nothing
				// was deleted, but that is a failure to report, not a "changed" item.
				it.Outcome, it.Detail = "failed", err.Error()
				res.Failed++
				continue
			}
			if dry.Status != "candidate" || dirKey(dry.Dirs) != dirKey(it.Dirs) {
				it.Outcome, it.Detail = "skipped", "changed since it was listed; run af-clean again"
				res.Skipped++
				continue
			}
			rep, err := c.wtoPost(it.Name, false)
			switch {
			case err != nil:
				it.Outcome, it.Detail = "failed", err.Error()
				res.Failed++
			case rep.Status == "refused":
				it.Outcome = "skipped"
				res.Skipped++
			default:
				it.Outcome, it.Freed = "removed", rep.Bytes
			}
		} else {
			r, err := c.do("DELETE", "/cleanup/tool-caches/"+url.PathEscape(it.Name))
			switch {
			case err != nil:
				it.Outcome, it.Detail = "failed", err.Error()
				res.Failed++
			case r.status == 409 && r.errCode() == "cache_in_use":
				it.Outcome, it.Detail = "skipped", r.errText()
				res.Skipped++
			case r.status >= 400:
				it.Outcome, it.Detail = "failed", r.errText()
				res.Failed++
			default:
				var d struct{ Bytes *int64 }
				_ = json.Unmarshal(r.body, &d)
				it.Outcome, it.Freed = "removed", it.Bytes
				if d.Bytes != nil {
					it.Freed = *d.Bytes
				}
			}
		}
		res.Freed += it.Freed
	}
}

// --- output ---

func human(n int64) string {
	const k = 1024.0
	f := float64(n)
	for _, u := range []string{"B", "KiB", "MiB", "GiB"} {
		if f < k || u == "GiB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= k
	}
	return ""
}

func render(out io.Writer, res *Result) {
	if len(res.Items) == 0 {
		fmt.Fprintln(out, "nothing to clean")
	}
	for _, it := range res.Items {
		plus := ""
		if it.Truncated {
			plus = "+"
		}
		fmt.Fprintf(out, "%-9s %-32s %10s%s", it.Kind, it.Name, human(it.Bytes), plus)
		if it.Outcome != "" {
			fmt.Fprintf(out, "  %s", it.Outcome)
			if it.Outcome == "removed" {
				fmt.Fprintf(out, " (%s)", human(it.Freed))
			}
			if it.Detail != "" {
				fmt.Fprintf(out, ": %s", it.Detail)
			}
		}
		fmt.Fprintln(out)
		for _, d := range it.Dirs {
			fmt.Fprintf(out, "            %-30s %10s\n", d.Rel, human(d.Bytes))
		}
	}
	if res.Partial {
		fmt.Fprintln(out, "(one source could not be read, so this list may be incomplete)")
	}
	if res.Mode == "dry-run" {
		if len(res.Items) > 0 {
			fmt.Fprintf(out, "dry run: %s can be freed; nothing was deleted. Run af-clean --apply to list again and remove these items.\n", human(res.Total))
		}
		return
	}
	fmt.Fprintf(out, "freed %s, %d skipped, %d failed\n", human(res.Freed), res.Skipped, res.Failed)
}
