package memconsol

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memreview"
)

// maxNoteRunes bounds the text of one observation that goes into a prompt.
const maxNoteRunes = 600

// ScopeKey names a scope in maps and ids.
func ScopeKey(scope, projectID string) string {
	if scope == "project" {
		return "project:" + projectID
	}
	return "user"
}

// Inbox is the observations of every scope plus the memories they are about.
type Inbox struct {
	Observations []Observation
	Memories     map[string][]memoryx.ConsolMemory // by ScopeKey
	// Skipped counts observations left out because their text failed the secret scan: a string is
	// sent to a model whole or not at all (memoryx.ReviewTextProblem).
	Skipped int
}

// readExport reads the exported 0117 queue. The file is read-only input: this function opens it
// for reading and nothing in this package writes, renames or removes it. A missing or unreadable
// file is no observations, not an error.
func readExport() []memreview.ExportedProposal {
	b, err := os.ReadFile(memreview.ExportPath())
	if err != nil {
		return nil
	}
	var ex memreview.Export
	if json.Unmarshal(b, &ex) != nil || ex.Version != memreview.ExportVersion {
		return nil
	}
	return ex.Proposals
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxNoteRunes {
		s = string([]rune(s)[:maxNoteRunes]) + " …"
	}
	return s
}

// Gather collects the observations of every scope: stale / wrong feedback reasons, lint reasons
// and the exported 0117 queue. now stamps the first sighting of a reason (a reason carries no
// time of its own; the state remembers when it was first seen). It reads the store without
// counting a use and writes nothing.
func Gather(now time.Time) (Inbox, error) {
	in := Inbox{Memories: map[string][]memoryx.ConsolMemory{}}
	scopes, err := store.Scopes()
	if err != nil {
		return in, err
	}
	add := func(o Observation) {
		if memoryx.ReviewTextProblem(o.Note) != "" || (o.Candidate != nil && memoryx.ReviewTextProblem(o.Candidate.Body+"\n"+o.Candidate.Description) != "") {
			in.Skipped++
			return
		}
		in.Observations = append(in.Observations, o)
	}
	for _, sc := range scopes {
		key := ScopeKey(sc.Scope, sc.ProjectID)
		mems, err := store.Memories(sc.Scope, sc.ProjectID)
		if err != nil {
			return in, err
		}
		in.Memories[key] = mems
		for _, m := range mems {
			for _, r := range m.Reasons {
				var src string
				switch {
				case r.By == "feedback" && (r.Rule == "stale" || r.Rule == "wrong"):
					src = SourceFeedback
				case r.By == "lint":
					src = SourceLint
				default:
					continue
				}
				note := oneLine(r.Note)
				add(Observation{
					ID:     obsID(src, key, m.Name, r.Rule, note, strconv.Itoa(r.Revision)),
					Source: src, Scope: sc.Scope, ProjectID: sc.ProjectID, Memory: m.Name,
					Rule: r.Rule, Note: note, Revision: r.Revision, FirstSeen: now,
				})
			}
		}
	}
	for _, p := range readExport() {
		if p.Scope != "project" && p.Scope != "user" {
			continue
		}
		o := Observation{
			ID:     obsID(SourceExport, p.ID),
			Source: SourceExport, Scope: p.Scope, ProjectID: p.ProjectID, Memory: p.Name,
			Rule: "review-" + p.Op, Note: oneLine(p.Reason), Revision: p.BaseRevision, FirstSeen: p.Created,
		}
		if p.Op == "create" {
			o.Candidate = &Candidate{Name: p.Name, Description: p.Description, Type: p.Type, Body: p.Body}
		} else if p.Body != "" {
			// An update or forget is never applied from here; its text is evidence only.
			o.Note = oneLine(o.Note + " | proposed text: " + p.Body)
		}
		add(o)
	}
	sort.SliceStable(in.Observations, func(i, j int) bool { return in.Observations[i].ID < in.Observations[j].ID })
	return in, nil
}

// Reconcile folds a gathered inbox into the state: a new observation is stamped with its first
// sighting (an exported proposal keeps its own creation time), a known one is touched, and an
// entry unseen for a long time and not covered is dropped. It returns each observation with its
// first-seen time as the state knows it.
func Reconcile(s *State, in Inbox, now time.Time) []Observation {
	out := make([]Observation, 0, len(in.Observations))
	live := map[string]bool{}
	for _, o := range in.Observations {
		live[o.ID] = true
		st := s.Observations[o.ID]
		if st == nil {
			st = &ObsState{FirstSeen: o.FirstSeen}
			s.Observations[o.ID] = st
		}
		st.LastSeen = now
		o.FirstSeen = st.FirstSeen
		out = append(out, o)
	}
	for id, st := range s.Observations {
		if !live[id] && st.Covered == nil && now.Sub(st.LastSeen) > 30*24*time.Hour {
			delete(s.Observations, id)
		}
		if !live[id] && now.Sub(st.LastSeen) > 180*24*time.Hour {
			delete(s.Observations, id)
		}
	}
	return out
}
