package afmemory

// `af-memory promote-proposal`: the proposed change for one promotion candidate (ADR 0130
// decision 11): the document, the section, the text, the memory it came from, the scans, and the
// patch over the default branch. The Agent writes nothing and pushes nothing; the member applies
// the patch and opens the pull request (`--patch | git apply`), or asks a session to.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// promoteProposalTimeout is this command's HTTP timeout: the Agent checks out the tree and runs
// the repository's pre-commit hook (bounded at 3 minutes there) after the candidate checks.
const promoteProposalTimeout = 10 * time.Minute

type proposal struct {
	Project *project `json:"project,omitempty"`
	Ref     string   `json:"ref"`
	Commit  string   `json:"commit"`
	Target  struct {
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Heading string `json:"heading"`
	} `json:"target"`
	FileExists    bool `json:"fileExists"`
	SectionExists bool `json:"sectionExists"`
	Sources       []struct {
		Name       string `json:"name"`
		Scope      string `json:"scope"`
		Revision   int    `json:"revision"`
		BodySHA256 string `json:"bodySha256"`
	} `json:"sources"`
	Text     string   `json:"text,omitempty"`
	Patch    string   `json:"patch,omitempty"`
	Warnings []string `json:"warnings"`
	Scans    struct {
		Secret []struct {
			Line int    `json:"line"`
			Rule string `json:"rule"`
		} `json:"secret"`
		PreCommit struct {
			Ran     bool   `json:"ran"`
			Hook    string `json:"hook"`
			Passed  bool   `json:"passed"`
			Absent  bool   `json:"absent"`
			Changed bool   `json:"changed"`
			Output  string `json:"output"`
			Note    string `json:"note"`
		} `json:"preCommit"`
	} `json:"scans"`
	Ready bool `json:"ready"`
}

var proposalWarnings = map[string]string{
	"language-differs": "the memory is in another language than the document: rewrite the text in the repository's language before committing",
	"memory-links":     "the text links memories with [[name]], which no reader of the repository can follow",
	"new-file":         "the document does not exist on the default branch; the patch creates it",
}

func cmdPromoteProposal(c *Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("promote-proposal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	proj := fs.String("project", "", "")
	name := fs.String("name", "", "")
	file := fs.String("file", "", "")
	section := fs.String("section", "", "")
	daysArg := fs.String("days", "", "")
	asJSON := fs.Bool("json", false, "")
	patchOnly := fs.Bool("patch", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *proj == "" || *name == "" || *asJSON && *patchOnly {
		return &usageError{"promote-proposal takes --project <id|name> --name <memory>, and optionally --file <path.md>, --section <heading>, --days N, and one of --json or --patch"}
	}
	days := 0
	if *daysArg != "" {
		n, err := strconv.Atoi(*daysArg)
		if err != nil || n < 1 || n > 90 || strings.TrimLeft(*daysArg, "0123456789") != "" {
			return &usageError{"--days is a whole number from 1 to 90"}
		}
		days = n
	}
	ids, err := promoteProjects(c, *proj)
	if err != nil {
		return err
	}
	slow := *c
	if c.HTTP != nil {
		h := *c.HTTP
		h.Timeout = promoteProposalTimeout
		slow.HTTP = &h
	}
	body := map[string]any{"project": ids[0], "name": *name}
	if *file != "" {
		body["file"] = *file
	}
	if *section != "" {
		body["section"] = *section
	}
	if days > 0 {
		body["days"] = days
	}
	var p proposal
	if err := slow.do("POST", "/agents/memory/entries/promotion-proposal", body, &p); err != nil {
		return err
	}
	switch {
	case *patchOnly:
		_, err := io.WriteString(out, p.Patch)
		return err
	case *asJSON:
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	// The command that prints this patch again, with every option that shaped it.
	again := []string{"af-memory", "promote-proposal", "--project", ids[0], "--name", *name}
	for _, f := range []struct{ flag, v string }{{"--file", *file}, {"--section", *section}, {"--days", *daysArg}} {
		if f.v != "" {
			again = append(again, f.flag, f.v)
		}
	}
	printProposal(out, p, again)
	return nil
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func printProposal(out io.Writer, p proposal, again []string) {
	state := "ready to commit"
	if !p.Ready {
		state = "NOT ready: fix what the scans report"
	}
	fmt.Fprintf(out, "== proposal: %s (%s)\n", p.Target.Path, state)
	fmt.Fprintf(out, "   repository: %s  [%s @ %s]\n", p.Target.Repo, p.Ref, shortCommit(p.Commit))
	sec := "new section"
	if p.SectionExists {
		sec = "end of the existing section"
	}
	if p.Target.Heading != "" {
		fmt.Fprintf(out, "   section:    %q (%s)\n", p.Target.Heading, sec)
	}
	for _, s := range p.Sources {
		fmt.Fprintf(out, "   from:       %s r%d (%s)\n", s.Name, s.Revision, s.Scope)
	}
	for _, w := range p.Warnings {
		msg := proposalWarnings[w]
		if msg == "" {
			msg = w
		}
		fmt.Fprintf(out, "   warning:    %s\n", msg)
	}
	if n := len(p.Scans.Secret); n > 0 {
		fmt.Fprintf(out, "   secret scan: %d finding(s) — the text and patch are withheld:\n", n)
		for _, f := range p.Scans.Secret {
			fmt.Fprintf(out, "     line %d: %s\n", f.Line, f.Rule)
		}
	} else {
		fmt.Fprintln(out, "   secret scan: clean")
	}
	pc := p.Scans.PreCommit
	switch {
	case pc.Ran && pc.Passed:
		fmt.Fprintf(out, "   pre-commit:  passed (%s)\n", pc.Hook)
	case pc.Ran:
		fmt.Fprintf(out, "   pre-commit:  FAILED (%s): %s\n", pc.Hook, pc.Note)
	default:
		fmt.Fprintf(out, "   pre-commit:  not run: %s\n", pc.Note)
	}
	if pc.Changed {
		fmt.Fprintln(out, "   pre-commit:  the hook changed the staged change (it reformats?); the patch below is the one given to it, not what it left")
	}
	if pc.Output != "" && !pc.Passed {
		for _, ln := range strings.Split(strings.TrimRight(pc.Output, "\n"), "\n") {
			fmt.Fprintf(out, "     | %s\n", ln)
		}
	}
	if p.Patch != "" {
		fmt.Fprintf(out, "\n%s", p.Patch)
		q := make([]string, len(again))
		for i, a := range again {
			q[i] = shellQuote(a)
		}
		fmt.Fprintf(out, "\nAF pushes nothing: apply it with `%s --patch | git apply`, then open the pull request.\n", strings.Join(q, " "))
	}
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
