package memorylint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Proposal kinds.
const (
	ProposalSupersededADR = "superseded-adr" // a fully superseded ADR's name, as a phrase
	ProposalRemovedKey    = "removed-key"    // a configuration key removed from the default branch
)

// Proposal is a retired-term candidate read from the repository. It never enters the member's
// list by itself: the member accepts or rejects it, and a rejected term is passed back in
// Options.RejectedTerms so it is not proposed again.
type Proposal struct {
	Term string `json:"term"`
	Kind string `json:"kind"`
	// Evidence says why: the ADR with its status line, or the commit that removed the key.
	Evidence string `json:"evidence"`
	// Memories names the memories that use the term (sorted). A candidate no memory uses is not
	// proposed: it could only be noise.
	Memories []string `json:"memories"`
}

// RemovedKey is a key some commit of the default branch removed, with the commit as evidence.
type RemovedKey struct {
	Key, Commit, Subject string
}

// KeyHistory is implemented by a Repo that can name keys removed by recent commits. The engine
// itself checks that a key is gone from the revision's current files before proposing it.
type KeyHistory interface {
	RemovedKeys() ([]RemovedKey, error)
}

var (
	// adrSlugRe picks the name of an ADR file; a translation (.ja.md) repeats it.
	adrSlugRe    = regexp.MustCompile(`^(\d{3,5})-([a-z0-9]+(?:-[a-z0-9]+)+)\.md$`)
	adrStatusRe  = regexp.MustCompile(`(?m)^- Status:\s*(.+)$`)
	supersededRe = regexp.MustCompile(`(?i)^\**\s*superseded\b`)
)

const maxEvidence = 200

// candidate is a term before it is matched against the memories.
type candidate struct {
	term, kind, evidence string
}

// supersededADRCandidates reads the ADR directory of the revision. Only a status that begins
// with "superseded" counts: "accepted, mostly superseded by ..." is partly alive. The term is the
// file's slug as a phrase ("operator-interaction-graph" -> "operator interaction graph"): a
// multi-word name is a phrase memories repeat, while a title is a sentence and never matches.
func supersededADRCandidates(rc *repoCtx, fr FileReader, adrDir string) ([]candidate, []string) {
	var out []candidate
	var notes []string
	var files []string
	for p := range rc.files {
		if rest, ok := strings.CutPrefix(p, adrDir); ok && adrSlugRe.MatchString(rest) {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	for _, p := range files {
		m := adrSlugRe.FindStringSubmatch(p[len(adrDir):])
		b, err := fr.ReadFile(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("retired-term proposals: %s unreadable: %v", p, err))
			return out, notes
		}
		sm := adrStatusRe.FindStringSubmatch(string(b))
		if sm == nil || !supersededRe.MatchString(strings.TrimSpace(sm[1])) {
			continue
		}
		status := strings.TrimSpace(sm[1])
		status = clip(status, maxEvidence)
		out = append(out, candidate{
			term:     strings.ReplaceAll(m[2], "-", " "),
			kind:     ProposalSupersededADR,
			evidence: fmt.Sprintf("ADR %s: Status: %s", trimZeros(m[1]), status),
		})
	}
	return out, notes
}

// removedKeyCandidates asks the history for removed keys and keeps those that no file of the
// revision mentions any more.
func removedKeyCandidates(repo Repo, kh KeyHistory) ([]candidate, []string) {
	keys, err := kh.RemovedKeys()
	if err != nil {
		return nil, []string{fmt.Sprintf("retired-term proposals (removed keys): %v", err)}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, k.Key)
	}
	sort.Strings(names)
	still, err := repo.FindSymbols(names)
	if err != nil {
		return nil, []string{fmt.Sprintf("retired-term proposals (removed keys): %v", err)}
	}
	var out []candidate
	for _, k := range keys {
		if still[k.Key] {
			continue
		}
		subj := k.Subject
		subj = clip(subj, 100)
		out = append(out, candidate{k.Key, ProposalRemovedKey, fmt.Sprintf("removed in %s: %s", k.Commit, subj)})
	}
	return out, nil
}

// buildProposals keeps the candidates that are neither in the member's list nor rejected, and
// that at least one memory uses (matched like a retired term, so "muse" never hits "museum").
func buildProposals(cands []candidate, mems []Memory, listed, rejected []string) []Proposal {
	skip := map[string]bool{}
	for _, l := range [][]string{listed, rejected} {
		for _, t := range l {
			skip[strings.ToLower(strings.TrimSpace(t))] = true
		}
	}
	out := []Proposal{}
	seen := map[string]bool{}
	for _, c := range cands {
		k := strings.ToLower(c.term)
		if skip[k] || seen[k] {
			continue
		}
		seen[k] = true
		tm := newTermMatcher([]string{c.term})
		var used []string
		for _, m := range mems {
			if len(tm.hits(m.Body)) > 0 {
				used = append(used, m.Name)
			}
		}
		if len(used) == 0 {
			continue
		}
		sort.Strings(used)
		out = append(out, Proposal{Term: c.term, Kind: c.kind, Evidence: c.evidence, Memories: used})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Term < out[j].Term })
	return out
}

// clip shortens s to n runes, never cutting inside a character.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
