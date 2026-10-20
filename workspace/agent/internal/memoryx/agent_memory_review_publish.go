package memoryx

// The write half of the background memory review (ADR 0117 decisions 5 and 6): publishing one
// approved proposal, undoing a whole run, and the reads the approval screen needs. The review
// engine (internal/memreview) decides what to publish and when; everything that touches the store,
// its lock, its scan and its history stays here, so a proposal takes exactly the path an agent's
// memory_save takes (revision check, secret scan, one commit) and nothing else.
//
// Who may call this is not decided here. The only callers are the member-origin routes of
// internal/memreview; no af MCP tool reaches them (ADR 0113).

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// ReviewAuthorKind is the author kind of a published proposal. agentMemKindRe admits it, so
	// the change list shows it like any other author.
	ReviewAuthorKind = "review"

	reviewRunTrailer      = "AF-Review-Run"
	reviewProposalTrailer = "AF-Review-Proposal"
)

// reviewIDRe is the form of run and proposal ids (20 hex characters). They go into commit
// messages, so nothing else is accepted.
var reviewIDRe = regexp.MustCompile(`^[0-9a-f]{20}$`)

// ValidReviewID says whether s has the form of a run or a proposal id.
func ValidReviewID(s string) bool { return reviewIDRe.MatchString(s) }

// The reasons a proposal is not published. Each leaves the store as it was.
const (
	// ReviewStale: the memory is not at the revision the proposal was made against, or no longer
	// exists, or now exists although the proposal creates it.
	ReviewStale = "stale"
	// ReviewForgotten: a create would revive a memory that was forgotten (or that existed once).
	ReviewForgotten = "forgotten"
	// ReviewSecret: the store's scan refused the text.
	ReviewSecret = "secret"
	// ReviewInvalid: the store's own limits refused it, or its project cannot be established.
	ReviewInvalid = "invalid"
)

// ReviewRefusal is why a proposal was not published. Msg names a rule, never a value.
type ReviewRefusal struct {
	Kind string
	Msg  string
}

func (e *ReviewRefusal) Error() string {
	return "review proposal not published (" + e.Kind + "): " + e.Msg
}

// errReviewRevival is the refusal of a create over a name that was forgotten. It is returned from
// inside the store lock and turned into a ReviewRefusal on the way out.
var errReviewRevival = errors.New("the memory was forgotten or existed before; a review does not bring it back")

// ReviewProposal is one approved operation as the store needs it.
type ReviewProposal struct {
	ID, RunID string
	// Session is the reviewed session: the author session of the commit, even if it is gone.
	Session   string
	Op        string // create | update | forget
	Scope     string // project | user
	ProjectID string // the project's store id, for scope project
	Name      string
	Type      string
	// Description and Body are the new text (not for a forget).
	Description  string
	Body         string
	BaseRevision int
}

// ReviewPublished is the outcome of PublishReview. Already says the proposal had been published by
// an earlier call (a retry after a stop), so nothing was written now.
type ReviewPublished struct {
	Commit   string
	Revision int
	Deleted  bool
	Already  bool
}

// PublishReview publishes one approved proposal through agentMemSave / agentMemForget as author
// kind "review", the reviewed session as author session, with the run and proposal ids as
// trailers. A create is refused over a forgotten name, checked inside the same lock as the save.
// A retry of a proposal that was published but not yet marked decided finds its commit by the
// proposal trailer and writes nothing: this is what makes approval idempotent across a stop.
func PublishReview(p ReviewProposal, now time.Time) (ReviewPublished, error) {
	invalid := func(msg string) error { return &ReviewRefusal{Kind: ReviewInvalid, Msg: msg} }
	switch {
	case !reviewIDRe.MatchString(p.ID) || !reviewIDRe.MatchString(p.RunID):
		return ReviewPublished{}, invalid("proposal or run id is malformed")
	case !session.ValidName(p.Session):
		return ReviewPublished{}, invalid("session is not a valid session name")
	case p.Op != "create" && p.Op != "update" && p.Op != "forget":
		return ReviewPublished{}, invalid("op must be create, update or forget")
	case p.Scope != agentMemScopeProject && p.Scope != agentMemScopeUser:
		return ReviewPublished{}, invalid("scope must be project or user")
	case !agentMemValidName(p.Name):
		return ReviewPublished{}, invalid("name is not a valid memory name")
	case p.Scope == agentMemScopeProject && !agentMemValidProjectID(p.ProjectID):
		return ReviewPublished{}, invalid("project is not a project id")
	}
	if commit, err := reviewProposalCommit(p.ID); err != nil {
		return ReviewPublished{}, err
	} else if commit != "" {
		return ReviewPublished{Commit: commit, Already: true}, nil
	}
	c, err := reviewCaller(p)
	if err != nil {
		return ReviewPublished{}, err
	}
	var res agentMemWriteResult
	switch p.Op {
	case "create", "update":
		c.noRevival = p.Op == "create"
		c.keepKinds = p.Op == "update"
		res, err = agentMemSave(c, agentMemSaveReq{
			Session: p.Session, Scope: p.Scope, Name: p.Name, Description: p.Description, Type: p.Type,
			Body: p.Body, Revision: p.BaseRevision,
		}, now)
	default:
		res, err = agentMemForget(c, agentMemForgetReq{
			Session: p.Session, Scope: p.Scope, Name: p.Name, Revision: p.BaseRevision,
		}, now)
	}
	if err != nil {
		return ReviewPublished{}, reviewRefusal(err)
	}
	return ReviewPublished{Commit: res.Commit, Revision: res.Revision, Deleted: res.Deleted}, nil
}

// reviewCaller is the store caller for a proposal: kind review, the reviewed session as author,
// and the project the proposal names. The session is looked up only to describe a project that has
// no memory yet (a first memory brings the project's record along); an existing project is read
// from the store, so a session that is gone or has moved on does not strand its proposals.
func reviewCaller(p ReviewProposal) (agentMemCaller, error) {
	c := agentMemCaller{Session: p.Session, Kind: ReviewAuthorKind,
		trailers: []string{reviewRunTrailer + ": " + p.RunID, reviewProposalTrailer + ": " + p.ID}}
	if p.Scope != agentMemScopeProject {
		return c, nil
	}
	if rc, err := agentMemResolveCaller(p.Session); err == nil && rc.Project != nil && rc.Project.ID == p.ProjectID {
		c.Project = rc.Project
		return c, nil
	}
	if _, err := agentMemCheckDir("projects/"+p.ProjectID, false); err != nil {
		if os.IsNotExist(err) {
			return c, &ReviewRefusal{Kind: ReviewInvalid, Msg: "the proposal's project is not known to this workspace any more"}
		}
		return c, err
	}
	c.Project = agentMemProjectInfo(p.ProjectID)
	return c, nil
}

// reviewRefusal sorts a store error into the reasons a proposal can be left unpublished. Anything
// else (git, the disk) is returned as it is: it is not the proposal's fault and a retry may work.
func reviewRefusal(err error) error {
	var se *agentMemSecretErr
	var ue *memoryUserErr
	switch {
	case errors.As(err, &se):
		return &ReviewRefusal{Kind: ReviewSecret, Msg: "the store's secret scan refused the text"}
	case errors.Is(err, errReviewRevival):
		return &ReviewRefusal{Kind: ReviewForgotten, Msg: errReviewRevival.Error()}
	case errors.As(err, &ue) && (ue.Code == errCodeMemoryConflict || ue.Code == errCodeMemoryNotFound):
		return &ReviewRefusal{Kind: ReviewStale, Msg: "the memory is not as it was when the review looked at it"}
	case errors.As(err, &ue) && ue.Status < 500:
		return &ReviewRefusal{Kind: ReviewInvalid, Msg: ue.Msg}
	}
	return err
}

// agentMemRefuseRevival is the rule of ADR 0117 decision 5, with agentMemMu held: a memory that has
// a tombstone, or any change in the history, is not created again by a review.
// agentMemSave would continue the tombstone's revision and delete the tombstone.
func agentMemRefuseRevival(rel, name string) error {
	tomb, err := agentMemTombRevision(rel, name)
	if err != nil {
		return err
	}
	if tomb > 0 {
		return errReviewRevival
	}
	if !memoryHasCommits() {
		return nil
	}
	last, err := agentMemLatestChange(rel + "/" + name + ".md")
	if err != nil {
		return err
	}
	if last != "" {
		return errReviewRevival
	}
	return nil
}

// reviewTrailerCommit finds the newest commit on main whose message carries trailer key = value
// as an agent-memory change by the review, or "". git's --grep only narrows the candidates.
func reviewTrailerCommit(key, value string) (string, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	if !memoryHasCommits() {
		return "", nil
	}
	log, err := memoryGitRun("log", memoryBranch, "-F", "--grep="+key+": "+value,
		"--format=%H"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return "", err
	}
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 2)
		if len(f) < 2 {
			continue
		}
		tr := agentMemTrailers(f[1])
		if tr["AF-Trigger"] == memoryTriggerAgentMemory && tr[key] == value && tr["AF-Author-Kind"] == ReviewAuthorKind {
			return f[0], nil
		}
	}
	return "", nil
}

func reviewProposalCommit(id string) (string, error) {
	return reviewTrailerCommit(reviewProposalTrailer, id)
}

// ReviewTargetState is what the store holds for the memory a proposal is about.
type ReviewTargetState struct {
	Exists   bool
	Revision int
	Type     string
	// Description and Body are the live text, empty when Withheld.
	Description string
	Body        string
	// Withheld: the file exists but fails the store's scan or cannot be read, so its text is not
	// returned.
	Withheld bool
	// Forgotten: a tombstone, or (for a name that does not exist) any change in the history, says the
	// memory was forgotten; a create is refused over it.
	Forgotten bool
}

// ReviewTarget reads the live state of one memory for the approval screen. It counts no use and
// writes nothing; a project that has no memory yet reads as an absent memory.
func ReviewTarget(scope, projectID, name string) (ReviewTargetState, error) {
	if err := agentMemCheckName(name); err != nil {
		return ReviewTargetState{}, err
	}
	var c agentMemCaller
	rel := "user"
	switch scope {
	case agentMemScopeUser:
		c = agentMemMember
	case agentMemScopeProject:
		if !agentMemValidProjectID(projectID) {
			return ReviewTargetState{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "project is not a project id")
		}
		rel = "projects/" + projectID
		var err error
		if c, err = agentMemMemberCaller(scope, projectID); err != nil {
			var ue *memoryUserErr
			if errors.As(err, &ue) && ue.Code == errCodeMemoryNotFound {
				// No directory for the project: the memory is absent, but the history may still
				// know the name (a lost directory takes the tombstone with it).
				agentMemMu.RLock()
				defer agentMemMu.RUnlock()
				out := ReviewTargetState{}
				out.Forgotten = errors.Is(agentMemRefuseRevival(rel, name), errReviewRevival)
				return out, nil
			}
			return ReviewTargetState{}, err
		}
	default:
		return ReviewTargetState{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be project or user")
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := ReviewTargetState{}
	if tomb, err := agentMemTombRevision(rel, name); err == nil && tomb > 0 {
		out.Forgotten = true
	}
	l, ok, err := agentMemLoadOne(c, scope, name)
	if err != nil {
		// A file that cannot be read is withheld, not an error of the screen.
		return ReviewTargetState{Exists: true, Withheld: true, Forgotten: out.Forgotten}, nil
	}
	if !ok {
		// The rule a create is held to at approval, so the screen agrees with it: a lost tombstone
		// does not hide a name the history knows.
		if !out.Forgotten && errors.Is(agentMemRefuseRevival(rel, name), errReviewRevival) {
			out.Forgotten = true
		}
		return out, nil
	}
	out.Exists = true
	if l.Withheld {
		out.Withheld = true
		return out, nil
	}
	out.Revision, out.Type, out.Description, out.Body = l.Entry.Revision, l.Entry.Type, l.Entry.Description, l.Entry.Body
	return out, nil
}

// ReviewProjectDisplay is the display name of a project for the approval screen, or its id when
// the store's record is missing or fails the scan.
func ReviewProjectDisplay(id string) string {
	if !agentMemValidProjectID(id) {
		return ""
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	return agentMemProjectInfo(id).Display
}

// Results of one commit of a run when it is reverted.
const (
	ReviewRevertDone     = "reverted"
	ReviewRevertAlready  = "already"  // an earlier revert of this run undid it
	ReviewRevertConflict = "conflict" // left alone, see the reason
	ReviewRevertFailed   = "failed"   // the store could not do it now; running the revert again retries
)

// The reasons a commit of a run is a conflict.
const (
	// ReviewConflictEdited: the commit is no longer the memory's newest change.
	ReviewConflictEdited = "edited"
	// ReviewConflictBlocked: it is the newest, and the revert still refused (history purged, the
	// text to restore fails the scan, the file differs from the commit).
	ReviewConflictBlocked = "blocked"
)

// ReviewRevertItem is one commit of a run.
type ReviewRevertItem struct {
	Commit    string `json:"commit"`
	Op        string `json:"op"`
	Scope     string `json:"scope"`
	ProjectID string `json:"projectId,omitempty"`
	Name      string `json:"name"`
	Result    string `json:"result"`
	Reason    string `json:"reason,omitempty"`
}

// ReviewRevertReport is what reverting a run did, commit by commit.
type ReviewRevertReport struct {
	Run   string             `json:"run"`
	Items []ReviewRevertItem `json:"items"`
}

// RevertReviewRun undoes the commits a review run published, each as a new revert commit by the
// member (ADR 0117 decision 5). A commit is reverted only while it is still its memory's newest
// change and the memory is exactly as it left it; one edited since is left and reported, never
// overwritten. Nothing rewrites history, and running it again retries only what is left.
func RevertReviewRun(runID string, now time.Time) (ReviewRevertReport, error) {
	if !reviewIDRe.MatchString(runID) {
		return ReviewRevertReport{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "run is not a run id")
	}
	cands, err := reviewRunCommits(runID)
	if err != nil {
		return ReviewRevertReport{}, err
	}
	out := ReviewRevertReport{Run: runID, Items: []ReviewRevertItem{}}
	for _, cd := range cands {
		it := ReviewRevertItem{Commit: cd.commit, Op: cd.op, Scope: cd.scope, ProjectID: cd.projectID, Name: cd.name}
		switch state, err := reviewRevertState(cd); {
		case err != nil:
			it.Result = ReviewRevertFailed
		case state == ReviewRevertAlready:
			it.Result = ReviewRevertAlready
		case state == ReviewConflictEdited:
			it.Result, it.Reason = ReviewRevertConflict, ReviewConflictEdited
		default:
			_, err := agentMemRevert(agentMemRevertReq{Commit: cd.commit}, now)
			var ue *memoryUserErr
			var se *agentMemSecretErr
			switch {
			case err == nil:
				it.Result = ReviewRevertDone
			case errors.As(err, &ue) && ue.Status < 500, errors.As(err, &se):
				it.Result, it.Reason = ReviewRevertConflict, ReviewConflictBlocked
			default:
				it.Result = ReviewRevertFailed
			}
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

type reviewRunCommit struct {
	commit, op, scope, projectID, name, rel string
}

// reviewRunCommits lists the commits carrying the run id, newest first. A commit counts only when
// it is an agent-memory change authored by the review whose path is well-formed and whose name and
// project pass the scan, the conditions the change list applies.
func reviewRunCommits(runID string) ([]reviewRunCommit, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	if !memoryHasCommits() {
		return nil, nil
	}
	log, err := memoryGitRun("log", memoryBranch, "-F", "--grep="+reviewRunTrailer+": "+runID,
		"--format=%H"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return nil, err
	}
	var out []reviewRunCommit
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 2)
		if len(f) < 2 || !agentMemCommitRe.MatchString(f[0]) {
			continue
		}
		tr := agentMemTrailers(f[1])
		if tr["AF-Trigger"] != memoryTriggerAgentMemory || tr[reviewRunTrailer] != runID || tr["AF-Author-Kind"] != ReviewAuthorKind {
			continue
		}
		rel, scope, pid, name, ok := agentMemParseRepoPath(tr["AF-Memory"])
		if !ok || !agentMemCleanText(name+"\n"+pid) {
			continue
		}
		out = append(out, reviewRunCommit{commit: f[0], op: tr["AF-Op"], scope: scope, projectID: pid, name: name, rel: rel})
	}
	return out, nil
}

// reviewRevertState says ReviewRevertAlready when a revert commit of cd exists, ReviewConflictEdited
// when cd is no longer its memory's newest change, and "" when it may be reverted.
func reviewRevertState(cd reviewRunCommit) (string, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	log, err := memoryGitRun("log", memoryBranch, "-F", "--grep=AF-Revert-Of: "+cd.commit, "--format=%B"+memoryRecSep)
	if err != nil {
		return "", err
	}
	for _, rec := range strings.Split(log, memoryRecSep) {
		tr := agentMemTrailers(rec)
		if tr["AF-Trigger"] == memoryTriggerAgentMemory && tr["AF-Revert-Of"] == cd.commit && tr["AF-Op"] == "revert" {
			return ReviewRevertAlready, nil
		}
	}
	latest, err := agentMemLatestChange(cd.rel)
	if err != nil {
		return "", err
	}
	if latest != cd.commit {
		return ReviewConflictEdited, nil
	}
	return "", nil
}

// ForgetHooks tell the review that a memory is going away or has gone, so it can release the
// rejected proposals it remembered about it. Intent is called before the forget is committed and
// may refuse it: the review has to write down, durably, that a release is owed before the memory
// goes, or a stop after the commit would leave nothing to recover from; an error from Intent stops
// the forget with nothing changed. Done is called after the commit. base is the memory's newest
// change before the forget ("" if it had none): it identifies which forget the intent is about, so a
// later re-creation or a second forget is not mistaken for it (ReviewForgetCommitted). Both are
// called with the store lock held and must not call back into this package.
type ForgetHooks struct {
	Intent func(scope, projectID, name, base string) error
	Done   func(scope, projectID, name, base string)
}

var forgetHooks atomic.Pointer[ForgetHooks]

// SetForgetHooks installs the hooks; nil removes them.
func SetForgetHooks(h *ForgetHooks) { forgetHooks.Store(h) }

// forgetIntent runs the Intent hook for a forget of the memory in the scope directory rel ("user"
// or "projects/<id>"), with agentMemMu held, and returns the base the Done call must carry.
func forgetIntent(rel, name string) (string, error) {
	h := forgetHooks.Load()
	if h == nil {
		return "", nil
	}
	base := ""
	if memoryHasCommits() {
		var err error
		if base, err = agentMemLatestChange(rel + "/" + name + ".md"); err != nil {
			return "", err
		}
	}
	if h.Intent != nil {
		scope, pid := reviewScopeOf(rel)
		if err := h.Intent(scope, pid, name, base); err != nil {
			return "", memoryErrf(http.StatusInternalServerError, errCodeMemorySnapshotFailed,
				"the memory review could not record the release of its rejected proposals for this memory, so it was not forgotten; try again")
		}
	}
	return base, nil
}

// forgetDone runs the Done hook after the forget committed.
func forgetDone(rel, name, base string) {
	if h := forgetHooks.Load(); h != nil && h.Done != nil {
		scope, pid := reviewScopeOf(rel)
		h.Done(scope, pid, name, base)
	}
}

// ReviewForgetCommitted says whether the memory was deleted by a change newer than base, the newest
// change it had when a forget began (ForgetHooks). It is the evidence a pending release works from:
// the memory existing again says nothing, since an explicit save may have re-created it after the
// forget. An error means it cannot tell (base is not in the history any more, git failed).
func ReviewForgetCommitted(scope, projectID, name, base string) (bool, error) {
	rel := "user"
	switch {
	case scope == agentMemScopeProject && agentMemValidProjectID(projectID):
		rel = "projects/" + projectID
	case scope != agentMemScopeUser:
		return false, errors.New("not a memory")
	}
	if !agentMemValidName(name) {
		return false, errors.New("not a memory")
	}
	path := rel + "/" + name + ".md"
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	errNoBase := errors.New("the change a forget started from is no longer in the history")
	if !memoryHasCommits() {
		if base != "" {
			return false, errNoBase
		}
		return false, nil
	}
	log, err := memoryGitRun("log", memoryBranch, "-F", "--grep=AF-Memory: "+agentMemRepoPrefix+"/"+path,
		"--format=%H"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return false, err
	}
	// A deletion counts only if it is newer than base. A history rewrite (a purge) renames every
	// later commit, so a base that is gone says nothing about which forget is this one's: an older
	// forget of an earlier incarnation must not stand in for it. The scan therefore runs to base
	// before it answers.
	deleted := false
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 2)
		if len(f) < 2 {
			continue
		}
		tr := agentMemTrailers(f[1])
		if tr["AF-Trigger"] != memoryTriggerAgentMemory || tr["AF-Memory"] != agentMemRepoPrefix+"/"+path {
			continue
		}
		if f[0] == base {
			return deleted, nil // reached the change the forget started from
		}
		switch tr["AF-Op"] {
		case "forget":
			deleted = true
		case "revert":
			if _, exists, err := agentMemBlob(f[0], path); err != nil {
				return false, err
			} else if !exists {
				deleted = true
			}
		}
	}
	if base != "" {
		return false, errNoBase
	}
	return deleted, nil
}

func reviewScopeOf(rel string) (scope, projectID string) {
	if id, ok := strings.CutPrefix(filepath.ToSlash(rel), "projects/"); ok {
		return agentMemScopeProject, id
	}
	return agentMemScopeUser, ""
}
