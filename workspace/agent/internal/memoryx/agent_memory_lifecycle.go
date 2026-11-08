package memoryx

// The optional classification and lifecycle frontmatter of ADR 0130 decision 3, the one schema
// every writer's output is checked against, and the one visibility predicate every agent-facing
// read path applies. A memory with none of these fields behaves exactly as it did before them.
//
// Version skew: the fields are plain frontmatter keys. An Agent that predates them skips the keys
// when it reads, so an old reader shows the memory as it always did; it also drops them when it
// rewrites the file, which is why the Agent and the store are upgraded together.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	agentMemStatusActive   = "active"
	agentMemStatusQuiet    = "quiet"
	agentMemStatusArchived = "archived"

	agentMemMaxAnchors     = 32
	agentMemMaxAnchorRunes = 200
	agentMemMaxReasons     = 16
	agentMemMaxReasonNote  = 300
	agentMemMaxDocField    = 300
	agentMemReasonsTextCut = 160

	agentMemReasonByLint   = "lint"
	agentMemReasonByFeed   = "feedback"
	agentMemReasonByMember = "member"
	agentMemStatusByAgent  = "agent"
	agentMemStatusByMember = "member"
	agentMemStatusByConsol = "consolidator"

	agentMemMemKindLog    = "log"
	agentMemMemKindState  = "state"
	agentMemMemKindLesson = "lesson"
	agentMemMemKindProc   = "procedure"
	agentMemMemKindPrefer = "preference"

	agentMemKeyKind         = "kind"
	agentMemKeyAnchors      = "anchors"
	agentMemKeyStatus       = "status"
	agentMemKeyStatusBy     = "status_by"
	agentMemKeySupersededBy = "superseded_by"
	agentMemKeyInvalidAt    = "invalid_at"
	agentMemKeyReasons      = "reasons"
)

var (
	agentMemMemKinds    = map[string]bool{agentMemMemKindPrefer: true, agentMemMemKindLesson: true, agentMemMemKindProc: true, agentMemMemKindState: true, agentMemMemKindLog: true}
	agentMemStatuses    = map[string]bool{agentMemStatusActive: true, agentMemStatusQuiet: true, agentMemStatusArchived: true}
	agentMemStatusBys   = map[string]bool{agentMemStatusByAgent: true, agentMemStatusByMember: true, agentMemStatusByConsol: true}
	agentMemReasonBys   = map[string]bool{agentMemReasonByLint: true, agentMemReasonByFeed: true, agentMemReasonByMember: true}
	agentMemReasonRule  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	agentMemCommitIDRe  = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	agentMemDocBadChars = regexp.MustCompile(`[\x00-\x1f]`)
)

// agentMemReason is one open doubt about a memory: who raised it, the rule that fired (a lint
// check id, or stale / wrong from feedback), a one-line note and the revision it was found on.
type agentMemReason struct {
	By       string `json:"by"`
	Rule     string `json:"rule"`
	Note     string `json:"note,omitempty"`
	Revision int    `json:"revision"`
}

// agentMemDocTarget names a repository document that took over a memory's content (decision 11):
// the repository's identity, a normalised path, the heading and the merge commit that was
// checked to contain it.
type agentMemDocTarget struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Heading string `json:"heading,omitempty"`
	Commit  string `json:"commit"`
}

// agentMemSuperseded is the union target of superseded_by: exactly one of a memory (by name, in
// the same scope unless Scope says otherwise) or a document.
type agentMemSuperseded struct {
	Memory   string             `json:"memory,omitempty"`
	Scope    string             `json:"scope,omitempty"`
	Document *agentMemDocTarget `json:"document,omitempty"`
}

// agentMemLife is every classification and lifecycle field. It is embedded in agentMemEntry, so a
// writer that copies the stored entry and changes what it names keeps the rest, and a rebuilt
// entry carries them with one assignment (`e.agentMemLife = cur.agentMemLife`).
type agentMemLife struct {
	// MemKind is how the memory goes stale (preference, lesson, procedure, state, log), beside
	// Type (who it is about) and Kinds (which agent kinds it is for). Its frontmatter key is `kind`.
	MemKind string `json:"kind,omitempty"`
	// Anchors are what the memory depends on: paths, symbols, test names, ADR numbers, commands.
	Anchors []string `json:"anchors,omitempty"`
	// Status is active (also when empty), quiet or archived; StatusBy says who set it.
	Status       string              `json:"status,omitempty"`
	StatusBy     string              `json:"statusBy,omitempty"`
	SupersededBy *agentMemSuperseded `json:"supersededBy,omitempty"`
	InvalidAt    string              `json:"invalidAt,omitempty"`
	// Reasons are the open doubts. Suspect is not a status: a memory is suspect while this is not empty.
	Reasons []agentMemReason `json:"reasons,omitempty"`
	// Stub is set on the answer of a read of an archived or superseded memory: only the name,
	// status, superseded_by and reasons are filled in. It is never stored.
	Stub bool `json:"stub,omitempty"`
}

func (l agentMemLife) isZero() bool {
	return l.MemKind == "" && len(l.Anchors) == 0 && l.Status == "" && l.StatusBy == "" &&
		l.SupersededBy == nil && l.InvalidAt == "" && len(l.Reasons) == 0
}

// ---- visibility ----------------------------------------------------------------------------

// agentMemExcluded is step 1 of the visibility order: an archived or superseded memory is not
// shown to an agent on any path, whatever its kind or pin. The member's routes do not ask.
func agentMemExcluded(e agentMemEntry) bool {
	return e.Status == agentMemStatusArchived || e.SupersededBy != nil
}

// agentMemSuspect: open reasons are shown on every line, hit, read and injection.
func agentMemSuspect(e agentMemEntry) bool { return len(e.Reasons) > 0 }

// agentMemDescribable says whether a memory may sit in the described part of the index: a quiet
// memory and a log never do, and a pin does not change that.
func agentMemDescribable(e agentMemEntry) bool {
	return !agentMemExcluded(e) && e.Status != agentMemStatusQuiet && e.MemKind != agentMemMemKindLog
}

// agentMemWriteBackEligible is the single predicate of the claude write-back: it decides what AF
// selects, which copies it wrote earlier are withdrawn and what MEMORY.md lists. When in doubt the
// answer is false.
func agentMemWriteBackEligible(e agentMemEntry) bool {
	return agentMemDescribable(e) && !agentMemSuspect(e)
}

// agentMemReasonsText is the prefix a suspect memory carries wherever an agent reads it, empty
// when there are no open reasons. mcpx formats the same text for the index, search hits and reads;
// both pin it with the same literal.
func agentMemReasonsText(rs []agentMemReason) string {
	lines := make([]ReasonLine, len(rs))
	for i, r := range rs {
		lines[i] = ReasonLine{By: r.By, Rule: r.Rule, Note: r.Note}
	}
	return ReasonsPrefix(lines)
}

// ReasonLine is one open doubt as a client prints it.
type ReasonLine struct{ By, Rule, Note string }

// ReasonsPrefix is the one rendering of the suspect prefix; the af MCP tools call it, so the
// index budget (measured on agentMemIndexLine) and the printed text cannot drift apart.
func ReasonsPrefix(rs []ReasonLine) string {
	if len(rs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		p := r.By + " " + r.Rule
		if r.Note != "" {
			p += ": " + r.Note
		}
		parts = append(parts, p)
	}
	return "[suspect: " + agentMemCutRunes(strings.Join(parts, "; "), agentMemReasonsTextCut) + "] "
}

// agentMemStubOf is what memory_read answers for an excluded memory: the way to find out why it
// is gone, never the text. The revision stays so that the memory can still be forgotten.
func agentMemStubOf(e agentMemEntry) agentMemEntry {
	s := agentMemEntry{Name: e.Name, Scope: e.Scope, Revision: e.Revision}
	s.Status, s.SupersededBy, s.Reasons, s.Stub = e.Status, e.SupersededBy, e.Reasons, true
	if s.Status == "" || s.Status == agentMemStatusActive {
		// Superseded with the status still active: say what it is, not what it was.
		s.Status = agentMemStatusArchived
	}
	return s
}

// ---- parse ---------------------------------------------------------------------------------

// agentMemParseLifeField reads one lifecycle key. A value that does not fit the schema is dropped
// (the reader shows the memory without it) and reported in problems, so a hand-edited file loads
// while a writer's output can still be judged strictly (agentMemCheckLife).
func agentMemParseLifeField(l *agentMemLife, key, raw string, problems *[]string) bool {
	v := agentMemUnquote(raw)
	bad := func() { *problems = append(*problems, key) }
	switch key {
	case agentMemKeyKind:
		if agentMemMemKinds[v] {
			l.MemKind = v
		} else if v != "" {
			bad()
		}
	case agentMemKeyStatus:
		if agentMemStatuses[v] {
			l.Status = v
		} else if v != "" {
			bad()
		}
	case agentMemKeyStatusBy:
		if agentMemStatusBys[v] {
			l.StatusBy = v
		} else if v != "" {
			bad()
		}
	case agentMemKeyInvalidAt:
		if _, err := time.Parse(time.RFC3339, v); err == nil {
			l.InvalidAt = v
		} else if v != "" {
			bad()
		}
	case agentMemKeyAnchors:
		var a []string
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &a) == nil {
			l.Anchors = a
		} else if strings.TrimSpace(raw) != "" {
			bad()
		}
	case agentMemKeyReasons:
		var rs []agentMemReason
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &rs) == nil {
			if len(rs) > 0 {
				l.Reasons = rs // an empty list is no reasons, stored the same as absent
			}
		} else if strings.TrimSpace(raw) != "" {
			bad()
		}
	case agentMemKeySupersededBy:
		var s agentMemSuperseded
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &s) == nil && (s.Memory != "" || s.Document != nil) {
			l.SupersededBy = &s
		} else if strings.TrimSpace(raw) != "" {
			bad()
		}
	default:
		return false
	}
	return true
}

// ---- render --------------------------------------------------------------------------------

// agentMemRenderLife writes the lifecycle keys of l that are set, in a fixed order. Structured
// values are one-line JSON, which is a valid YAML flow value.
func agentMemRenderLife(b *strings.Builder, l agentMemLife) {
	q := func(s string) string { v, _ := json.Marshal(s); return string(v) }
	if l.MemKind != "" {
		b.WriteString(agentMemKeyKind + ": " + q(l.MemKind) + "\n")
	}
	if len(l.Anchors) > 0 {
		v, _ := json.Marshal(l.Anchors)
		b.WriteString(agentMemKeyAnchors + ": " + string(v) + "\n")
	}
	if l.Status != "" {
		b.WriteString(agentMemKeyStatus + ": " + q(l.Status) + "\n")
	}
	if l.StatusBy != "" {
		b.WriteString(agentMemKeyStatusBy + ": " + q(l.StatusBy) + "\n")
	}
	if l.SupersededBy != nil {
		v, _ := json.Marshal(l.SupersededBy)
		b.WriteString(agentMemKeySupersededBy + ": " + string(v) + "\n")
	}
	if l.InvalidAt != "" {
		b.WriteString(agentMemKeyInvalidAt + ": " + q(l.InvalidAt) + "\n")
	}
	if len(l.Reasons) > 0 {
		v, _ := json.Marshal(l.Reasons)
		b.WriteString(agentMemKeyReasons + ": " + string(v) + "\n")
	}
}

// ---- the schema ----------------------------------------------------------------------------

// agentMemLifeProblems lists the fields of l that break the schema. The messages name the field
// and the rule, never the value: a value refused here has not been scanned yet.
func agentMemLifeProblems(l agentMemLife) []string {
	var p []string
	oneLine := func(field, s string, max int) {
		switch {
		case agentMemDocBadChars.MatchString(s):
			p = append(p, field+" must be one line")
		case utf8.RuneCountInString(s) > max:
			p = append(p, fmt.Sprintf("%s is longer than %d characters", field, max))
		}
	}
	if l.MemKind != "" && !agentMemMemKinds[l.MemKind] {
		p = append(p, "kind must be one of preference, lesson, procedure, state, log")
	}
	if len(l.Anchors) > agentMemMaxAnchors {
		p = append(p, fmt.Sprintf("at most %d anchors", agentMemMaxAnchors))
	}
	for _, a := range l.Anchors {
		if strings.TrimSpace(a) == "" {
			p = append(p, "an anchor must not be empty")
			continue
		}
		oneLine("an anchor", a, agentMemMaxAnchorRunes)
	}
	if l.Status != "" && !agentMemStatuses[l.Status] {
		p = append(p, "status must be one of active, quiet, archived")
	}
	if l.StatusBy != "" {
		switch {
		case !agentMemStatusBys[l.StatusBy]:
			p = append(p, "status_by must be one of agent, member, consolidator")
		case l.Status == "":
			p = append(p, "status_by needs a status")
		}
	}
	if l.InvalidAt != "" {
		if _, err := time.Parse(time.RFC3339, l.InvalidAt); err != nil {
			p = append(p, "invalid_at must be an RFC 3339 time")
		}
	}
	if s := l.SupersededBy; s != nil {
		p = append(p, agentMemSupersededProblems(*s)...)
	}
	if len(l.Reasons) > agentMemMaxReasons {
		p = append(p, fmt.Sprintf("at most %d reasons", agentMemMaxReasons))
	}
	for _, r := range l.Reasons {
		switch {
		case !agentMemReasonBys[r.By]:
			p = append(p, "a reason's by must be one of lint, feedback, member")
		case !agentMemReasonRule.MatchString(r.Rule):
			p = append(p, "a reason's rule must be a short lower-case id")
		case r.Revision < 0:
			p = append(p, "a reason's revision must not be negative")
		}
		oneLine("a reason's note", r.Note, agentMemMaxReasonNote)
	}
	return p
}

func agentMemSupersededProblems(s agentMemSuperseded) []string {
	var p []string
	switch d := s.Document; {
	case s.Memory != "" && d != nil, s.Memory == "" && d == nil:
		p = append(p, "superseded_by names exactly one of a memory or a document")
	case s.Memory != "":
		if !agentMemValidName(s.Memory) {
			p = append(p, "superseded_by's memory is not a memory name")
		}
		if s.Scope != "" && s.Scope != agentMemScopeUser && s.Scope != agentMemScopeProject {
			p = append(p, "superseded_by's scope must be user or project")
		}
	default:
		if s.Scope != "" {
			p = append(p, "superseded_by's scope applies to a memory target only")
		}
		path := d.Path
		switch {
		case strings.TrimSpace(d.Repo) == "":
			p = append(p, "superseded_by's document needs a repo")
		case path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") ||
			strings.HasPrefix(path, "./") || strings.Contains(path, "//") || strings.HasSuffix(path, "/"):
			p = append(p, "superseded_by's document path must be a normalised relative path")
		case !agentMemCommitIDRe.MatchString(d.Commit):
			p = append(p, "superseded_by's document commit must be a hex commit id")
		}
		for _, seg := range strings.Split(path, "/") {
			if seg == ".." || seg == "." {
				p = append(p, "superseded_by's document path must be a normalised relative path")
				break
			}
		}
		for _, f := range []string{d.Repo, d.Path, d.Heading} {
			if agentMemDocBadChars.MatchString(f) || utf8.RuneCountInString(f) > agentMemMaxDocField {
				p = append(p, "superseded_by's document fields must be one line of at most "+strconv.Itoa(agentMemMaxDocField)+" characters")
				break
			}
		}
	}
	return p
}

// agentMemCheckLife is the schema applied to a writer's output: the stored bytes are read back
// strictly, so a field the reader would silently drop is refused instead. Every file a write
// publishes passes it (agentMemApplyLocked), whichever writer made it.
func agentMemCheckLife(data []byte) error {
	e, problems, ok := agentMemParseStrict(data)
	if !ok {
		return nil
	}
	var msgs []string
	for _, k := range problems {
		msgs = append(msgs, k+" does not fit the schema")
	}
	msgs = append(msgs, agentMemLifeProblems(e.agentMemLife)...)
	if len(msgs) == 0 {
		return nil
	}
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "%s", msgs[0])
}

// agentMemValidateLifeReq checks the two fields an agent may set on a save.
func agentMemValidateLifeReq(req *agentMemSaveReq) error {
	var l agentMemLife
	if req.MemKind != nil {
		*req.MemKind = strings.TrimSpace(*req.MemKind)
		l.MemKind = *req.MemKind
	}
	if req.Anchors != nil {
		anchors := make([]string, 0, len(*req.Anchors))
		seen := map[string]bool{}
		for _, a := range *req.Anchors {
			if a = strings.TrimSpace(a); !seen[a] {
				seen[a] = true
				anchors = append(anchors, a)
			}
		}
		*req.Anchors = anchors
		l.Anchors = anchors
	}
	if p := agentMemLifeProblems(l); len(p) > 0 {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "%s", p[0])
	}
	return nil
}

// agentMemDropFeedbackReasons removes the feedback reasons of a memory whose body changed: a
// feedback reason is cleared by any body change, a lint or member reason is not.
func agentMemDropFeedbackReasons(l *agentMemLife) {
	var keep []agentMemReason
	for _, r := range l.Reasons {
		if r.By != agentMemReasonByFeed {
			keep = append(keep, r)
		}
	}
	l.Reasons = keep
}

// agentMemLifeStrings is every string value of l, decoded, for the secret scan of what a reader
// would be shown (agentMemPublished): the raw file can hide a value behind a JSON escape.
func agentMemLifeStrings(l agentMemLife) []string {
	out := []string{l.MemKind, l.Status, l.StatusBy, l.InvalidAt}
	out = append(out, l.Anchors...)
	if s := l.SupersededBy; s != nil {
		out = append(out, s.Memory, s.Scope)
		if d := s.Document; d != nil {
			out = append(out, d.Repo, d.Path, d.Heading, d.Commit)
		}
	}
	for _, r := range l.Reasons {
		out = append(out, r.By, r.Rule, r.Note)
	}
	return out
}

// agentMemLifeSame compares the stored lifecycle fields of two memories, for the comparisons that
// decide whether a copy may stand in for its source.
func agentMemLifeSame(a, b agentMemLife) bool {
	a.Stub, b.Stub = false, false
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
