package memorylint

import (
	"regexp"
	"strings"
)

// VerifyAnchors answers, for each memory, which of its stored anchors the revision does not
// positively hold. Lint's checks are built to report what is missing with few false alarms, so an
// anchor they cannot classify — a root-level file, a command, an ignored path that is not in the
// tree, an ADR number in a repository without ADRs — is simply not checked. Promotion (ADR 0130
// decision 11) needs the converse, "every anchor exists", so here an anchor counts only when one
// rule finds it exactly:
//
//   - a path (a leading "./" and a trailing "/" ignored) that is a file or directory of the tree;
//   - [[name]], when a memory of that name is among mems;
//   - "ADR <n>", when the ADR directory has a record with that number;
//   - a code-shaped symbol (the body's symbol rules), when it occurs as a whole word in the tree.
//
// Anything else is returned as unverified. An error means the revision could not be read, and
// then nothing is verified. Memories without anchors are not in the answer.
func VerifyAnchors(mems []Memory, opts Options, repo Repo) (map[string][]string, error) {
	rc, _, err := loadRepo(repo, opts.ADRDir)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(mems))
	for _, m := range mems {
		names[m.Name] = true
	}
	type pending struct{ mem, anchor, sym string }
	var syms []pending
	symSet := map[string]bool{}
	out := map[string][]string{}
	for _, m := range mems {
		for _, raw := range m.Anchors {
			a := strings.TrimSpace(raw)
			switch ok, sym := rc.verifyLocal(a, names); {
			case ok:
			case sym != "":
				syms = append(syms, pending{m.Name, raw, sym})
				symSet[sym] = true
			default:
				out[m.Name] = append(out[m.Name], raw)
			}
		}
	}
	if len(symSet) > 0 {
		list := make([]string, 0, len(symSet))
		for s := range symSet {
			list = append(list, s)
		}
		found, err := repo.FindSymbols(list)
		if err != nil {
			return nil, err
		}
		for _, p := range syms {
			if !found[p.sym] {
				out[p.mem] = append(out[p.mem], p.anchor)
			}
		}
	}
	return out, nil
}

var (
	wholeLinkRe = regexp.MustCompile(`^\[\[([^\[\]\n]+)\]\]$`)
	wholeADRRe  = regexp.MustCompile(`^ADR[ -]?0*(\d{1,5})$`)
)

// verifyLocal decides an anchor from the file list alone. When it is a code-shaped symbol not
// settled by the list, sym names what to search for.
func (rc *repoCtx) verifyLocal(a string, names map[string]bool) (ok bool, sym string) {
	if a == "" {
		return false, ""
	}
	if m := wholeLinkRe.FindStringSubmatch(a); m != nil {
		t := strings.TrimSpace(m[1])
		return linkTargetRe.MatchString(t) && names[t], ""
	}
	if m := wholeADRRe.FindStringSubmatch(a); m != nil {
		return rc.hasADRs && rc.adrs[trimZeros(m[1])], ""
	}
	p := strings.TrimSuffix(strings.TrimPrefix(a, "./"), "/")
	if p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "..") && rc.hasPath(p) {
		return true, ""
	}
	t := strings.Trim(a, "`")
	if found := extractSymbols("`" + t + "`"); len(found) == 1 {
		return false, found[0].text
	}
	return false, ""
}
