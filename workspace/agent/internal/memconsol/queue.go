package memconsol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// Proposal statuses.
const (
	StatusPending  = "pending"  // waiting for the member
	StatusApproved = "approved" // published
	StatusRejected = "rejected" // the member said no
	StatusExpired  = "expired"  // undecided for the TTL; the record stays, approval no longer works
	StatusRefused  = "refused"  // approval was attempted and re-validation found it no longer applies
	StatusPartial  = "partial"  // approval published some items and the store refused a later one
)

// Item operations. Patch, create, replace and retire can be published; a note is advice for the member and
// writes nothing. A replace is a supersede or merge: the new memory and the supersede marks of the
// ones it replaces, published as one change (ADR 0130 decision 6). A retire archives one existing
// memory (status archived, by the consolidator) and deletes nothing. A note remains for what the
// engine could not turn into a checked replacement (the model's text failed validation).
const (
	OpPatch   = "patch"
	OpCreate  = "create"
	OpReplace = "replace"
	OpRetire  = "retire"
	OpNote    = "note"
)

// Source is one memory a replace supersedes, with the revision the engine saw. Approval re-validates
// it against the memory as it is then.
type Source struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

// Edit is one exact-text edit of a patch.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Item is one operation of a proposal.
type Item struct {
	Op   string `json:"op"`
	Name string `json:"name"` // the target memory (patch, retire, note) or the new memory (create, replace)
	// Reason is why the engine chose this; for a retire it is also the one line left on the memory.
	Reason string `json:"reason,omitempty"`
	// Revision is the revision the engine saw when it made a patch or a retire.
	Revision int    `json:"revision,omitempty"`
	Edits    []Edit `json:"edits,omitempty"`
	// Create, replace: the new memory.
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Body        string `json:"body,omitempty"`
	// Replace: the memories it supersedes (one for a supersede, several for a merge).
	Sources []Source `json:"sources,omitempty"`
	// Note: the engine's operation (add, supersede, merge) and the memories it named.
	Advice  string   `json:"advice,omitempty"`
	Targets []string `json:"targets,omitempty"`
	// Role is the model role whose answer this is (small or large).
	Role string `json:"role,omitempty"`
	// PublishID and Commit are set when the item was published.
	PublishID string `json:"publishId,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// Evidence is what a proposal rests on, kept with it after the observations are gone.
type Evidence struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Memory string `json:"memory"`
	Rule   string `json:"rule"`
	Note   string `json:"note"`
}

// Proposal is one topic's queued change.
type Proposal struct {
	ID        string    `json:"id"`
	TopicID   string    `json:"topicId"`
	RunID     string    `json:"runId"`
	Scope     string    `json:"scope"`
	ProjectID string    `json:"projectId,omitempty"`
	Memory    string    `json:"memory"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	Decided   time.Time `json:"decided,omitzero"`
	Reason    string    `json:"reason,omitempty"` // why it was refused, or the member's note
	Items     []Item    `json:"items"`
	// ObservationIDs, Evidence and Targets are the record that outlives expiry (decision 7): the
	// observation ids, the target revisions and the evidence the proposal used.
	ObservationIDs  []string          `json:"observationIds"`
	TargetRevisions map[string]int    `json:"targetRevisions,omitempty"`
	Evidence        []Evidence        `json:"evidence"`
	Models          map[string]string `json:"models,omitempty"`
}

var idRe = regexp.MustCompile(`^[0-9a-f]{20}$`)

// ValidID says whether s has the form of a proposal id.
func ValidID(s string) bool { return idRe.MatchString(s) }

func newID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func queueDir() string { return filepath.Join(Dir(), "proposals") }

func proposalPath(id string) string { return filepath.Join(queueDir(), id+".json") }

// ErrNotFound is a proposal id nobody has.
var ErrNotFound = errors.New("memconsol: no such proposal")

func loadProposal(id string) (Proposal, error) {
	if !ValidID(id) {
		return Proposal{}, ErrNotFound
	}
	b, err := os.ReadFile(proposalPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	var p Proposal
	if err := json.Unmarshal(b, &p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

func saveProposal(p Proposal) error { return writeJSONAtomic(proposalPath(p.ID), p) }

// ListProposals returns every proposal, newest first, optionally one status only. Expiry is
// applied to what it returns (an expired one reads as expired even before a sweep stored it).
func ListProposals(status string, now time.Time) ([]Proposal, error) {
	ents, err := os.ReadDir(queueDir())
	if errors.Is(err, os.ErrNotExist) {
		return []Proposal{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Proposal{}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidID(id) {
			continue
		}
		p, err := loadProposal(id)
		if err != nil {
			continue
		}
		if p.Status == StatusPending && !now.Before(p.Expires) {
			p.Status = StatusExpired
		}
		if status == "" || p.Status == status {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// SweepExpired stores the expiry of pending proposals past their time. It changes nothing else:
// the record stays, and the observations keep their coverage, so expiry alone never ripens the
// topic again.
func SweepExpired(now time.Time) (int, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	ps, err := ListProposals(StatusPending, time.Time{}) // as stored: nothing is "expired" at the zero time
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		if now.Before(p.Expires) {
			continue
		}
		p.Status, p.Decided = StatusExpired, now
		if err := saveProposal(p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// pendingTopics is the set of topic ids that have a proposal waiting.
func pendingTopics(now time.Time) (map[string]bool, error) {
	ps, err := ListProposals(StatusPending, now)
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, p := range ps {
		m[p.TopicID] = true
	}
	return m, nil
}

// ---- deterministic validation --------------------------------------------------------------

const (
	maxPatchedBody   = 64 << 10
	maxProposalBytes = 256 << 10
)

var linkRe = regexp.MustCompile(`\[\[([^\[\]\n]{1,80})\]\]`)

// brokenLinks lists the [[links]] of text that name no memory of the scope or of the user scope.
func brokenLinks(text string, scope, projectID string) []string {
	names := map[string]bool{}
	for _, key := range []string{ScopeKey("user", ""), ScopeKey(scope, projectID)} {
		sc, pid := "user", ""
		if key != "user" {
			sc, pid = "project", strings.TrimPrefix(key, "project:")
		}
		ms, _ := store.Memories(sc, pid)
		for _, m := range ms {
			names[m.Name] = true
		}
	}
	var bad []string
	seen := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
		n := strings.TrimSpace(m[1])
		if !names[n] && !seen[n] {
			seen[n] = true
			bad = append(bad, n)
		}
	}
	return bad
}

// Problem is why an item cannot be queued or applied. Kind is a short code, Msg names the rule,
// never a value.
type Problem struct{ Kind, Msg string }

func (p *Problem) Error() string { return p.Kind + ": " + p.Msg }

// Problem kinds.
const (
	ProbStale     = "stale"     // the memory is not as it was when the engine looked
	ProbForgotten = "forgotten" // a create over a name that was forgotten or ever existed
	ProbExists    = "exists"    // a create over a name that exists now
	ProbInvalid   = "invalid"   // schema, size, links, the edits
	ProbSecret    = "secret"
)

// validateItem is the deterministic gate every publishable item passes twice: when it is queued
// and when it is approved. It reads the memory as it is now and writes nothing. The model's answer
// is untrusted input here: nothing about the item is believed before this returns nil.
func validateItem(scope, projectID string, it Item) *Problem {
	st, err := store.Target(scope, projectID, it.Name)
	if err != nil {
		return &Problem{ProbInvalid, "the memory cannot be read"}
	}
	switch it.Op {
	case OpPatch:
		switch {
		case !st.Exists || st.Withheld:
			return &Problem{ProbStale, "the memory is gone or withheld"}
		case st.Revision != it.Revision:
			return &Problem{ProbStale, "the memory has changed since the engine read it"}
		case len(it.Edits) == 0:
			return &Problem{ProbInvalid, "a patch has no edits"}
		}
		edits := make([]memengine.Edit, len(it.Edits))
		for i := range it.Edits {
			edits[i] = memengine.Edit{Old: &it.Edits[i].Old, New: &it.Edits[i].New}
		}
		body, _, err := memengine.ApplyEdits(st.Body, "", edits)
		if err != nil {
			return &Problem{ProbStale, "the edits do not apply to the memory as it is"}
		}
		if len(body) > maxPatchedBody || strings.TrimSpace(body) == "" {
			return &Problem{ProbInvalid, "the patched body is empty or too large"}
		}
		for _, e := range it.Edits {
			if memoryx.ReviewTextProblem(e.New) != "" {
				return &Problem{ProbSecret, "the replacement text failed the secret scan"}
			}
		}
		// A link that the patch adds must resolve; one that was already broken is lint's.
		for _, l := range brokenLinks(strings.Join(editNews(it.Edits), "\n"), scope, projectID) {
			return &Problem{ProbInvalid, "the patch adds a link to a memory that does not exist: " + l}
		}
	case OpRetire:
		switch {
		case !st.Exists || st.Withheld:
			return &Problem{ProbStale, "the memory is gone or withheld"}
		case st.Excluded:
			return &Problem{ProbStale, "the memory is archived or superseded already"}
		case st.Pinned:
			return &Problem{ProbStale, "the memory is pinned; only the member retires it"}
		case st.Revision != it.Revision:
			return &Problem{ProbStale, "the memory has changed since the engine read it"}
		case !memoryx.ReviewRetireReasonOK(it.Reason) || len([]rune(it.Reason)) > maxRetireReason:
			return &Problem{ProbInvalid, "a retire needs a one-line reason"}
		case memoryx.ReviewTextProblem(it.Reason) != "":
			return &Problem{ProbSecret, "the reason failed the secret scan"}
		}
	case OpCreate:
		return validateNewMemory(scope, projectID, it, st)
	case OpReplace:
		if pr := validateNewMemory(scope, projectID, it, st); pr != nil {
			return pr
		}
		return validateSources(scope, projectID, it)
	default:
		return &Problem{ProbInvalid, "only a patch, a create, a replace or a retire can be applied"}
	}
	return nil
}

// validateNewMemory is the gate of a memory that does not exist yet (a create, and the new side of
// a replace): the name is free and was never used, and the text passes the store's schema, the
// secret scan and the link check. A superseded memory still has its file, so its name is taken.
func validateNewMemory(scope, projectID string, it Item, st memoryx.ReviewTargetState) *Problem {
	switch {
	case st.Forgotten:
		return &Problem{ProbForgotten, "the name was forgotten or existed before; a consolidation never brings it back"}
	case st.Exists || st.Withheld:
		return &Problem{ProbExists, "a memory by that name exists"}
	case !memoryx.ValidReviewName(it.Name):
		return &Problem{ProbInvalid, "the name is not a valid memory name"}
	}
	if err := store.Validate(scope, it.Name, it.Description, it.Type, it.Body); err != nil {
		return &Problem{ProbInvalid, err.Error()}
	}
	if memoryx.ReviewTextProblem(it.Body+"\n"+it.Description) != "" {
		return &Problem{ProbSecret, "the text failed the secret scan"}
	}
	if bl := brokenLinks(it.Body, scope, projectID); len(bl) > 0 {
		return &Problem{ProbInvalid, "the body links to a memory that does not exist: " + bl[0]}
	}
	return nil
}

// maxSources bounds how many memories one replace supersedes; the store refuses more.
const maxSources = 8

// validateSources checks every memory a replace supersedes against its current revision. All sides
// are read before anything is written, so one that moved refuses the whole replacement.
func validateSources(scope, projectID string, it Item) *Problem {
	if len(it.Sources) == 0 || len(it.Sources) > maxSources {
		return &Problem{ProbInvalid, "a replacement supersedes between one and eight memories"}
	}
	seen := map[string]bool{}
	for _, src := range it.Sources {
		if src.Name == it.Name || seen[src.Name] || src.Revision < 1 {
			return &Problem{ProbInvalid, "the memories a replacement supersedes are not distinct, or one is the replacement itself"}
		}
		seen[src.Name] = true
		st, err := store.Target(scope, projectID, src.Name)
		switch {
		case err != nil:
			return &Problem{ProbInvalid, "a memory to supersede cannot be read"}
		case !st.Exists || st.Withheld:
			return &Problem{ProbStale, "a memory to supersede is gone or withheld"}
		case st.Excluded:
			return &Problem{ProbStale, "a memory to supersede is archived or superseded already"}
		case st.Revision != src.Revision:
			return &Problem{ProbStale, "a memory to supersede has changed since the engine read it"}
		}
	}
	return nil
}

func editNews(es []Edit) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.New
	}
	return out
}

// ---- decisions -------------------------------------------------------------------------------

// ErrNotPending: the proposal was decided, or has expired, and approval no longer works.
var ErrNotPending = errors.New("memconsol: the proposal is not pending")

// Reject marks a pending proposal rejected. The observations keep their coverage: the same
// proposal does not come back; new evidence, a change to the memory or Retry does.
func Reject(id string, now time.Time) (Proposal, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	p, err := loadProposal(id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != StatusPending || !now.Before(p.Expires) {
		return p, ErrNotPending
	}
	p.Status, p.Decided = StatusRejected, now
	return p, saveProposal(p)
}

// Retry reopens the observations of a decided proposal that was not approved (rejected, expired,
// refused): the member's explicit request to look at the topic again. The proposal's record stays.
func Retry(id string, now time.Time) (Proposal, int, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	p, err := loadProposal(id)
	if err != nil {
		return Proposal{}, 0, err
	}
	if p.Status == StatusPending && !now.Before(p.Expires) {
		p.Status, p.Decided = StatusExpired, now
		if err := saveProposal(p); err != nil {
			return p, 0, err
		}
	}
	switch p.Status {
	case StatusRejected, StatusExpired, StatusRefused, StatusPartial:
	default:
		return p, 0, ErrNotPending
	}
	s, err := loadState()
	if err != nil {
		return p, 0, err
	}
	n := 0
	for _, oid := range p.ObservationIDs {
		if st := s.Observations[oid]; st != nil && st.Covered != nil && st.Covered.Proposal == p.ID {
			st.Covered, st.Dropped = nil, 0
			n++
		}
	}
	return p, n, s.save()
}

func isPublishable(op string) bool {
	return op == OpPatch || op == OpCreate || op == OpReplace || op == OpRetire
}

func publishID(proposalID string, i int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", proposalID, i)))
	return hex.EncodeToString(h[:])[:20]
}

// Approve publishes a pending proposal. It is the only function in the package that writes the
// memory store, and the only caller is the member-origin approve route. Every publishable item is
// re-validated against the memory as it is now before the first one is written; a refusal leaves
// the store untouched and marks the proposal refused, with the reason. Items publish through the
// store's patch / save / replace path authored as the consolidator (memoryx.PublishConsolidation),
// so the change list shows them and each can be reverted; a replace is one change over all its
// memories, and the store re-checks each side's revision under its lock (the CAS). A note item is
// advice and writes nothing.
func Approve(id string, now time.Time) (Proposal, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	p, err := loadProposal(id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != StatusPending || !now.Before(p.Expires) {
		return p, ErrNotPending
	}
	// An item an earlier, cut-off approval already published is found by its trailer and skipped:
	// its memory has moved on (that was the publish), so re-validating it would call the proposal
	// stale against its own write.
	publishable := 0
	for i := range p.Items {
		it := &p.Items[i]
		if !isPublishable(it.Op) {
			continue
		}
		publishable++
		it.PublishID = publishID(p.ID, i)
		if it.Commit == "" {
			c, err := store.Published(it.PublishID)
			if err != nil {
				return p, err
			}
			it.Commit = c
		}
		if it.Commit != "" {
			continue
		}
		if pr := validateItem(p.Scope, p.ProjectID, *it); pr != nil {
			p.Status, p.Decided, p.Reason = StatusRefused, now, pr.Error()
			for j := range p.Items {
				if p.Items[j].Commit != "" {
					p.Status = StatusPartial
				}
			}
			if err := saveProposal(p); err != nil {
				return p, err
			}
			return p, pr
		}
	}
	if publishable == 0 {
		return p, &Problem{ProbInvalid, "the proposal is advice only: there is nothing to apply"}
	}
	done := 0
	for i := range p.Items {
		it := &p.Items[i]
		if !isPublishable(it.Op) {
			continue
		}
		if it.Commit != "" {
			done++
			continue
		}
		ci := memoryx.ConsolItem{Op: it.Op, Scope: p.Scope, ProjectID: p.ProjectID, Name: it.Name, Revision: it.Revision,
			Description: it.Description, Type: it.Type, Body: it.Body}
		if it.Op == OpRetire {
			ci.Reason = it.Reason
		}
		for _, src := range it.Sources {
			ci.Sources = append(ci.Sources, memoryx.ConsolSource{Name: src.Name, Revision: src.Revision})
		}
		for _, e := range it.Edits {
			ci.Edits = append(ci.Edits, memoryx.ConsolEdit{Old: e.Old, New: e.New})
		}
		res, err := store.Publish(it.PublishID, ci, now)
		if err != nil {
			var rf *memoryx.ReviewRefusal
			if !errors.As(err, &rf) {
				// The store could not be reached now (git, the disk): nothing says the proposal is
				// wrong, so it stays pending and the member can approve again; the items that were
				// published are found again by their trailers and not written twice.
				_ = saveProposal(p)
				return p, err
			}
			p.Decided, p.Reason = now, rf.Error()
			p.Status = StatusRefused
			if done > 0 {
				p.Status = StatusPartial
			}
			if serr := saveProposal(p); serr != nil {
				return p, serr
			}
			return p, &Problem{rf.Kind, rf.Msg}
		}
		it.Commit = res.Commit
		done++
		// Written down at once: a stop before the next item must not lose that this one is out.
		if err := saveProposal(p); err != nil {
			return p, err
		}
	}
	p.Status, p.Decided = StatusApproved, now
	return p, saveProposal(p)
}

// ---- the read side ------------------------------------------------------------------------

// ItemView is an item with what the member needs to judge it: for a patch, the memory's body now
// and after the edits (a diff), or why it no longer applies.
type ItemView struct {
	Item
	// Diff is a unified-style line diff of the memory's current body against the patched one (a
	// patch), or the new body as additions (a create).
	Diff string `json:"diff,omitempty"`
	// Stale says the item no longer applies to the memory as it is now, with the reason.
	Stale string `json:"stale,omitempty"`
	// Retire is set on a retire: what agents stop seeing once it is approved.
	Retire *RetirePreview `json:"retire,omitempty"`
}

// RetirePreview is what a retire takes away from agents. The Diff of the item shows the text that
// will no longer be read; this adds the line memory_index prints now (it disappears) and the
// reason a later memory_read stub will carry in place of the text. Search hits for the memory
// disappear too; they have no text of their own to show.
type RetirePreview struct {
	// IndexLine is the memory's line in memory_index now, "" when it is not visible to agents.
	IndexLine string `json:"indexLine"`
	// StubReason is the one line memory_read will answer instead of the text.
	StubReason string `json:"stubReason"`
}

// ProposalView is a proposal with its items' diffs, as the get route returns it.
type ProposalView struct {
	Proposal
	Views []ItemView `json:"itemViews"`
}

// View builds the read-only view of one proposal. It reads the memories and writes nothing.
func View(id string, now time.Time) (ProposalView, error) {
	p, err := loadProposal(id)
	if err != nil {
		return ProposalView{}, err
	}
	if p.Status == StatusPending && !now.Before(p.Expires) {
		p.Status = StatusExpired
	}
	v := ProposalView{Proposal: p, Views: make([]ItemView, 0, len(p.Items))}
	for _, it := range p.Items {
		iv := ItemView{Item: it}
		switch it.Op {
		case OpPatch:
			st, err := store.Target(p.Scope, p.ProjectID, it.Name)
			if err != nil || !st.Exists || st.Withheld {
				iv.Stale = "the memory is gone or withheld"
				break
			}
			edits := make([]memengine.Edit, len(it.Edits))
			for i := range it.Edits {
				edits[i] = memengine.Edit{Old: &it.Edits[i].Old, New: &it.Edits[i].New}
			}
			body, _, aerr := memengine.ApplyEdits(st.Body, "", edits)
			if aerr != nil {
				iv.Stale = "the edits do not apply to the memory as it is"
				break
			}
			if st.Revision != it.Revision {
				iv.Stale = "the memory has changed since the engine read it"
			}
			iv.Diff = lineDiff(st.Body, body)
		case OpRetire:
			st, err := store.Target(p.Scope, p.ProjectID, it.Name)
			if err != nil || !st.Exists || st.Withheld {
				iv.Stale = "the memory is gone or withheld"
				break
			}
			iv.Diff = lineDiff(st.Body, "")
			iv.Retire = &RetirePreview{IndexLine: st.IndexLine, StubReason: it.Reason}
			if it.Commit == "" {
				if pr := validateItem(p.Scope, p.ProjectID, it); pr != nil {
					iv.Stale = pr.Msg
				}
			}
		case OpCreate, OpReplace:
			iv.Diff = lineDiff("", it.Body)
			if it.Commit == "" {
				if pr := validateItem(p.Scope, p.ProjectID, it); pr != nil {
					iv.Stale = pr.Msg
				}
			}
		}
		v.Views = append(v.Views, iv)
	}
	return v, nil
}

// lineDiff is a small LCS line diff ("-" removed, "+" added, " " kept), with unchanged runs
// folded to two lines of context. Past a size bound it falls back to all-removed / all-added.
func lineDiff(a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	if len(al)*len(bl) > 4_000_000 {
		return prefixLines("-", al) + prefixLines("+", bl)
	}
	lcs := make([][]int, len(al)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(bl)+1)
	}
	for i := len(al) - 1; i >= 0; i-- {
		for j := len(bl) - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type row struct{ tag, text string }
	var rows []row
	i, j := 0, 0
	for i < len(al) || j < len(bl) {
		switch {
		case i < len(al) && j < len(bl) && al[i] == bl[j]:
			rows = append(rows, row{" ", al[i]})
			i++
			j++
		case i < len(al) && (j == len(bl) || lcs[i+1][j] >= lcs[i][j+1]):
			rows = append(rows, row{"-", al[i]})
			i++
		default:
			rows = append(rows, row{"+", bl[j]})
			j++
		}
	}
	const ctx = 2
	keep := make([]bool, len(rows))
	for k, r := range rows {
		if r.tag != " " {
			for d := -ctx; d <= ctx; d++ {
				if k+d >= 0 && k+d < len(rows) {
					keep[k+d] = true
				}
			}
		}
	}
	var out strings.Builder
	gap := false
	for k, r := range rows {
		if !keep[k] {
			gap = true
			continue
		}
		if gap && out.Len() > 0 {
			out.WriteString("…\n")
		}
		gap = false
		out.WriteString(r.tag + r.text + "\n")
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func prefixLines(p string, ls []string) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(p + l + "\n")
	}
	return b.String()
}
