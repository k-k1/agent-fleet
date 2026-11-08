package memoryx

// memory_feedback (ADR 0130 decision 4, step 2): a reader says whether the memory it relied on
// held. Only a report on the current revision moves anything; one on an older revision is kept as
// an observation, so a report on revision 3 can never mark a fixed revision 4. One report per
// session per revision counts. `confirmed` only counts (and renews); it never clears a reason.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	agentMemVerdictConfirmed = "confirmed"
	agentMemVerdictStale     = "stale"
	agentMemVerdictWrong     = "wrong"

	// agentMemFeedbackMaxRecords bounds the record file of one memory; past it a new report is
	// still applied but no longer recorded, so a tampered or chatty store cannot grow it.
	agentMemFeedbackMaxRecords = 200
	agentMemFeedbackReasonBy   = agentMemReasonByFeed
)

// agentMemFeedbackReq is one report. Revision is the one the reader relied on.
type agentMemFeedbackReq struct {
	Session  string `json:"session"`
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Verdict  string `json:"verdict"`
	Note     string `json:"note,omitempty"`
}

// agentMemFeedbackRecord is one line of the record file.
type agentMemFeedbackRecord struct {
	Session  string `json:"session"`
	Revision int    `json:"revision"`
	Verdict  string `json:"verdict"`
	At       string `json:"at"`
}

// agentMemFeedbackResult says what the report did: counted (current revision), observation
// (older revision, kept only), duplicate (this session already reported this revision).
type agentMemFeedbackResult struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Revision int    `json:"revision"` // the memory's current revision
	Outcome  string `json:"outcome"`
	Commit   string `json:"commit,omitempty"`
	Message  string `json:"message,omitempty"`
}

func agentMemFeedbackBad(format string, args ...any) error {
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, args...)
}

func agentMemFeedbackRecords(scopeRel, name string) []agentMemFeedbackRecord {
	dir, err := agentMemCheckDir(agentMemCounterRel(scopeRel, agentMemCounterFeedback), false)
	if err != nil {
		return nil
	}
	b, ok, err := agentMemReadFile(filepath.Join(dir, name))
	if err != nil || !ok {
		return nil
	}
	var out []agentMemFeedbackRecord
	for _, ln := range bytes.Split(b, []byte("\n")) {
		var r agentMemFeedbackRecord
		if len(ln) > 0 && json.Unmarshal(ln, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func agentMemFeedbackAppend(scopeRel, name string, rec agentMemFeedbackRecord) error {
	dir, err := agentMemCheckDir(agentMemCounterRel(scopeRel, agentMemCounterFeedback), true)
	if err != nil {
		return err
	}
	abs := filepath.Join(dir, name)
	old, _, err := agentMemReadFile(abs)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(rec)
	return agentMemWriteFile(abs, append(old, append(line, '\n')...))
}

// agentMemFeedback applies one report.
func agentMemFeedback(c agentMemCaller, req agentMemFeedbackReq, now time.Time) (agentMemFeedbackResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Note = strings.TrimSpace(req.Note)
	switch {
	case !agentMemValidName(req.Name):
		return agentMemFeedbackResult{}, agentMemBadName()
	case req.Verdict != agentMemVerdictConfirmed && req.Verdict != agentMemVerdictStale && req.Verdict != agentMemVerdictWrong:
		return agentMemFeedbackResult{}, agentMemFeedbackBad("verdict must be one of confirmed, stale, wrong")
	case req.Revision < 1:
		return agentMemFeedbackResult{}, agentMemFeedbackBad("revision is the one you read (1 or more)")
	case c.Session == agentMemUnknown || c.Session == "":
		return agentMemFeedbackResult{}, agentMemFeedbackBad("memory_feedback needs a session: one report per session per revision is counted")
	case strings.ContainsAny(req.Note, "\r\n"):
		return agentMemFeedbackResult{}, agentMemFeedbackBad("note must be one line")
	case utf8.RuneCountInString(req.Note) > agentMemMaxReasonNote:
		return agentMemFeedbackResult{}, agentMemFeedbackBad("note is longer than %d characters", agentMemMaxReasonNote)
	case req.Verdict != agentMemVerdictConfirmed && req.Note == "":
		return agentMemFeedbackResult{}, agentMemFeedbackBad("a stale or wrong report needs a note saying what no longer holds")
	}
	if f := agentMemScanText("note", req.Note); len(f) > 0 {
		return agentMemFeedbackResult{}, &agentMemSecretErr{Findings: f}
	}
	req.Scope = agentMemDefaultScope(req.Scope, c)
	rel, err := agentMemScopeDir(req.Scope, c)
	if err != nil {
		return agentMemFeedbackResult{}, err
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	cur, exists, err := agentMemLoadOne(c, req.Scope, req.Name)
	if err != nil {
		return agentMemFeedbackResult{}, err
	}
	if exists && !cur.Withheld && (c.Kind == agentMemExternalKind && !agentMemAppliesTo(cur.Entry, c.Kind) || agentMemExcluded(cur.Entry)) {
		exists = false
	}
	switch {
	case !exists:
		return agentMemFeedbackResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name in this scope")
	case cur.Withheld:
		return agentMemFeedbackResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this file is withheld until your user fixes it; it cannot take feedback")
	case req.Revision > cur.Entry.Revision:
		return agentMemFeedbackResult{}, agentMemFeedbackBad("the memory is at revision %d; you cannot have relied on revision %d", cur.Entry.Revision, req.Revision)
	}
	e := cur.Entry
	res := agentMemFeedbackResult{Name: e.Name, Scope: e.Scope, Revision: e.Revision}

	records := agentMemFeedbackRecords(rel, req.Name)
	for _, r := range records {
		if r.Session == c.Session && r.Revision == req.Revision {
			res.Outcome, res.Message = "duplicate", "this session already reported this revision; only the first report counts"
			return res, nil
		}
	}
	if len(records) < agentMemFeedbackMaxRecords {
		if err := agentMemFeedbackAppend(rel, req.Name, agentMemFeedbackRecord{
			Session: c.Session, Revision: req.Revision, Verdict: req.Verdict, At: now.UTC().Format(time.RFC3339)}); err != nil {
			return agentMemFeedbackResult{}, err
		}
	}
	if req.Revision < e.Revision {
		res.Outcome = "observation"
		res.Message = "kept as an observation: the memory has changed since revision " + strconv.Itoa(req.Revision) + ", so nothing was marked"
		return res, nil
	}
	res.Outcome = "counted"
	if req.Verdict == agentMemVerdictConfirmed {
		agentMemRecordCounter(rel, agentMemCounterConfirmed, req.Name, true)
		return res, nil
	}
	agentMemRecordCounter(rel, agentMemCounterStale, req.Name, false)
	reason := agentMemReason{By: agentMemFeedbackReasonBy, Rule: req.Verdict, Note: req.Note, Revision: e.Revision}
	for _, r := range e.Reasons {
		if r == reason {
			return res, nil
		}
	}
	if len(e.Reasons) >= agentMemMaxReasons {
		res.Message = "counted, but the memory already carries the most open reasons it can hold"
		return res, nil
	}
	// The revision stays: the mark is about this text, and other sessions that read the same
	// revision must still be able to report on it (one report per session per revision counts).
	// A save keeps the reason, and a body change drops it (agentMemDropFeedbackReasons).
	e.Reasons = append(append([]agentMemReason(nil), e.Reasons...), reason)
	e.AuthorKind, e.AuthorSession = c.Kind, c.Session
	data := agentMemRender(e)
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemFeedbackResult{}, &agentMemSecretErr{Findings: f}
	}
	repoRel := rel + "/" + req.Name + ".md"
	changes, err := agentMemEntryChanges(c, req.Scope, repoRel, data)
	if err != nil {
		return agentMemFeedbackResult{}, err
	}
	commit, err := agentMemApplyLocked(changes, "update", repoRel, c, now)
	if err != nil {
		return agentMemFeedbackResult{}, err
	}
	agentMemPrimeScan(data, req.Scope, req.Name)
	res.Commit = commit
	return res, nil
}
