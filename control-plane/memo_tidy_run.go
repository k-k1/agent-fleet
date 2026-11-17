package main

// Tidying before a fire, the automatic run (ADR 0137 section 8). A tick that finds an open queue with
// tidy on and a message no run has looked at reserves a run instead of claiming (reserveTidy, in
// fireQueue's transaction). The job wakes the workspace, asks the Agent (POST /memos/tidy) and hands
// the reply to memoTidyApply. The claim is a later tick's, and by then every message of the run's
// input is marked seen, whatever the outcome, so a refusal or a failure lets the messages fire as
// they are.
//
// Nothing here changes a memo or a queue row except the seen marks, the run row and the draining
// flag of a threshold queue (so that a merge that drops the count below the threshold does not
// close the queue); apply is the one write that changes messages.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/memotidy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// memoTidyTakeoverAfter is how long a run may stay running before the next tick that meets it
	// ends it as failed. It must exceed the job's worst case: the wait for a cold Agent (the agent boot
	// budget, 5 minutes), the sessions read (15 s) and the call (150 s), plus a margin.
	memoTidyTakeoverAfter = 10 * time.Minute
	// memoTidyCallWait is the Control Plane's wait for the Agent, above the Agent's own 120 s bound.
	// agentHTTPClient cuts at two minutes, so the call uses agentLongCallClient with this deadline.
	memoTidyCallWait = 150 * time.Second
	// memoTidyRunsPerTick bounds the runs one tick reserves: the tick waits for every job, and each
	// may wait for a cold start and a model.
	memoTidyRunsPerTick = 2
	// memoTidyTextBytes is the text one run sends, the Agent's own bound.
	memoTidyTextBytes = 96 << 10
	// memoTidyReplyMax bounds the reply the Control Plane reads: the Agent parses at most 256 KiB of
	// the model's output and returns it re-marshalled.
	memoTidyReplyMax = 1 << 20
)

// Reasons a run ends without a change that no memotidy refusal and no Agent code names.
const (
	memoTidyReasonTakeover   = "takeover"
	memoTidyReasonNoMarker   = "no_marker"
	memoTidyReasonNoAgent    = "agent_unreachable"
	memoTidyReasonWakeFailed = "wake_failed"
	memoTidyReasonNoMember   = "no_workspace"
	memoTidyReasonTimeout    = "timeout"
	memoTidyReasonCallFailed = "call_failed"
	memoTidyReasonAgentError = "agent_error"
)

// The Agent's codes on a non-200 (workspace/agent internal/memotidy). Two codes share 503 and two
// statuses share tidy_failed, so the code decides and the status never does.
const (
	tidyCodeDenyAll     = "deny_all_unavailable"
	tidyCodeUnconfirmed = "policy_unconfirmed"
	tidyCodeForbidden   = "route_forbidden"
	tidyCodeAgentOff    = "agent_disabled"
	tidyCodeBusy        = "tidy_busy"
	tidyCodeBadRequest  = "bad_tidy_request"
	tidyCodeMalformed   = "tidy_malformed"
	tidyCodeFailed      = "tidy_failed"
	tidyCodeTimeout     = "tidy_timeout"
)

// memoTidyWireItem, memoTidyWireDest and memoTidyRequest are POST /memos/tidy.
type memoTidyWireItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	File  string `json:"file"`
	Group int    `json:"group"`
	Merge bool   `json:"merge"`
	Move  bool   `json:"move"`
}

type memoTidyWireDest struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Queue bool   `json:"queue"`
}

type memoTidyRequest struct {
	Run          string             `json:"run"`
	Trigger      string             `json:"trigger"`
	Queue        memoTidyWireQueue  `json:"queue"`
	Items        []memoTidyWireItem `json:"items"`
	Destinations []memoTidyWireDest `json:"destinations"`
	Limits       memoTidyLimits     `json:"limits"`
}

type memoTidyWireQueue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type memoTidyLimits struct {
	MergeSources int `json:"mergeSources"`
	SplitParts   int `json:"splitParts"`
	TextBytes    int `json:"textBytes"`
	Operations   int `json:"operations"`
}

// memoTidyReply is the Agent's 200. Operations stays raw: memotidy.Parse reads it strictly.
type memoTidyReply struct {
	Run        string          `json:"run"`
	Operations json.RawMessage `json:"operations"`
}

// memoTidyJob is a reserved run. The items hold message text and live in memory only: the run row
// keeps ids, hashes and states.
type memoTidyJob struct {
	runID string
	q     store.MemoQueue
	c     store.MemoCategory
	items []memoTidyWireItem
	input []memoTidyItem
}

// memoTidySince is the start of the daily cap's window.
func memoTidySince(now time.Time) string {
	return now.UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
}

// memoTidyTakeOver ends the member's run when it has been running past the takeover bound, as
// failed with reason takeover, and marks its input seen so the messages fire as they are. A late
// apply of the first process then finds the run not running and changes nothing. It reports whether
// a run was taken over.
func memoTidyTakeOver(tx store.MemoTxOps, now time.Time) (bool, error) {
	run, ok, err := tx.RunningTidyRun()
	if err != nil || !ok {
		return false, err
	}
	if created, perr := time.Parse(time.RFC3339Nano, run.CreatedAt); perr == nil && now.Sub(created) < memoTidyTakeoverAfter {
		return false, nil
	}
	var in memoTidyInput
	_ = json.Unmarshal([]byte(run.Input), &in)
	for _, it := range in.Items {
		if err := tx.MarkTidySeen(it.ID, it.Hash); err != nil {
			return false, err
		}
	}
	at := memoNow()
	run.State, run.Reason, run.FinishedAt, run.UpdatedAt = store.TidyRunFailed, memoTidyReasonTakeover, at, at
	won, err := tx.FinishTidyRun(run)
	return won, err
}

// takeOverTidy is memoTidyTakeOver for a member nothing else is looking at this tick: a stale run of
// a queue that has nothing left to fire would otherwise stay running, and hold the member's other
// queues back with the reason tidying. The read takes no lock, so a member with no run costs one query.
func (r *memoQueueRunner) takeOverTidy(ctx context.Context, membershipID string, now time.Time) {
	stale := false
	err := r.mgr.store.MemoRead(ctx, membershipID, func(rd store.MemoWaitOps) error {
		run, ok, err := rd.RunningTidyRun()
		if err != nil || !ok {
			return err
		}
		created, perr := time.Parse(time.RFC3339Nano, run.CreatedAt)
		stale = perr != nil || now.Sub(created) >= memoTidyTakeoverAfter
		return nil
	})
	if err != nil || !stale {
		return
	}
	err = r.mgr.store.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		_, err := memoTidyTakeOver(tx, now)
		return err
	})
	if err != nil {
		log.Printf("memo queue: member %s: take over a tidy run: %v", membershipID, err)
	}
}

// reserveTidy is the stage between "the queue may fire" and the claim, inside fireQueue's
// transaction. It answers a job when it reserved a run. hold is set when the queue must neither tidy
// nor fire this tick. Neither set means the messages fire as they are: nothing is unseen, the day's
// cap is used, or the unseen ones lie beyond what one run reads.
func (r *memoQueueRunner) reserveTidy(tx store.MemoTxOps, cur store.MemoQueue, c store.MemoCategory, startDrain bool,
	now time.Time) (job *memoTidyJob, hold bool, err error) {
	rows, err := tx.ListMemosIn(c.Repo, c.Name)
	if err != nil {
		return nil, false, err
	}
	var cands []store.Memo
	for _, m := range rows {
		if m.State == store.MemoStatePending && m.SentAt == "" && m.SendToken == "" && m.DeliveryID == "" {
			cands = append(cands, m)
		}
	}
	// The order the claim reads them in, so that "the oldest" means the same thing in both.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].CreatedAt != cands[j].CreatedAt {
			return cands[i].CreatedAt < cands[j].CreatedAt
		}
		if cands[i].Position != cands[j].Position {
			return cands[i].Position < cands[j].Position
		}
		return cands[i].ID < cands[j].ID
	})
	unseen := false
	for _, m := range cands {
		if m.TidySeenHash != memoContentHash(m) {
			unseen = true
			break
		}
	}
	if !unseen {
		return nil, false, nil
	}
	used, err := tx.CountTidyRunsSince(c.ID, memoTidySince(now))
	if err != nil {
		return nil, false, err
	}
	if used >= memotidy.DailyCap {
		return nil, false, nil
	}
	if len(r.tidyJobs) >= memoTidyRunsPerTick {
		return nil, true, nil
	}

	// What one run reads: the oldest messages, at most MaxInputItems and memoTidyTextBytes of text.
	// The rest are marked seen and fire as they are.
	groups := map[memotidy.Group]int{}
	job = &memoTidyJob{q: cur, c: c}
	bytesUsed, cut, chosenUnseen := 0, len(cands), false
	for i, m := range cands {
		size := len(m.Body) + len(m.RefPath)
		if i >= memotidy.MaxInputItems || bytesUsed+size > memoTidyTextBytes {
			cut = i
			break
		}
		bytesUsed += size
		it := memoTidyItemOf(m)
		g, ok := groups[it.Group]
		if !ok {
			g = len(groups)
			groups[it.Group] = g
		}
		kind := "text"
		if m.Kind == "file" {
			kind = "file"
		}
		job.items = append(job.items, memoTidyWireItem{ID: m.ID, Kind: kind, Text: m.Body, File: m.RefPath, Group: g,
			Merge: it.Mergeable, Move: !it.Moved})
		job.input = append(job.input, memoTidyItem{ID: m.ID, Hash: memoContentHash(m), State: m.State})
		if m.TidySeenHash != memoContentHash(m) {
			chosenUnseen = true
		}
	}
	for _, m := range cands[cut:] {
		if err := tx.MarkTidySeen(m.ID, memoContentHash(m)); err != nil {
			return nil, false, err
		}
	}
	if !chosenUnseen {
		// Nothing a run could read is new to it.
		return nil, false, nil
	}
	input, _ := json.Marshal(memoTidyInput{Items: job.input})
	at := memoNow()
	job.runID = store.NewID()
	if err := tx.InsertTidyRun(store.MemoTidyRun{ID: job.runID, MembershipID: cur.MembershipID, QueueID: c.ID,
		Trigger: store.TidyTriggerAuto, State: store.TidyRunRunning, Input: string(input), CreatedAt: at, UpdatedAt: at}); err != nil {
		return nil, false, err
	}
	if startDrain {
		// A threshold queue opened on this many messages: a merge may leave fewer, and the queue must
		// still fire what is left. The claim that would have set this runs a tick later.
		if err := tx.SetQueueDraining(c.ID, true); err != nil {
			return nil, false, err
		}
	}
	return job, false, nil
}

// runTidy is the job of a reserved run: wake, ask, apply. Every way out but a successful apply ends
// the run through finishTidy, which marks the input seen.
func (r *memoQueueRunner) runTidy(ctx context.Context, job *memoTidyJob) {
	res := r.resolveMember(ctx, job.q.MembershipID)
	if res == nil {
		r.finishTidy(ctx, job, store.TidyRunSkipped, memoTidyReasonNoMember)
		return
	}
	if why := r.wakeAndWait(ctx, res); why != "" {
		log.Printf("memo queue: queue %s: tidy run %s: %s", job.c.ID, job.runID, why)
		r.finishTidy(ctx, job, store.TidyRunSkipped, memoTidyReasonWakeFailed)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	env, err := r.mgr.agentSessionsEnv(cctx, res.rt)
	cancel()
	if err != nil {
		r.finishTidy(ctx, job, store.TidyRunSkipped, memoTidyReasonNoAgent)
		return
	}
	r.setNoTidy(ctx, job.q, !env.showsTidy())
	if !env.showsTidy() {
		r.finishTidy(ctx, job, store.TidyRunSkipped, memoTidyReasonNoMarker)
		return
	}
	req, offered := r.tidyRequest(ctx, job)
	status, body, err := r.callTidy(ctx, res, req)
	if err != nil {
		reason := memoTidyReasonCallFailed
		if errors.Is(err, context.DeadlineExceeded) {
			reason = memoTidyReasonTimeout
		}
		log.Printf("memo queue: queue %s: tidy run %s: %s", job.c.ID, job.runID, reason)
		r.finishTidy(ctx, job, store.TidyRunFailed, reason)
		return
	}
	if status != http.StatusOK {
		state, reason := tidyCodeOutcome(agentErrCode(body))
		if reason == tidyCodeDenyAll {
			r.setNoTidy(ctx, job.q, true)
		}
		log.Printf("memo queue: queue %s: tidy run %s: the Agent answered %d %s", job.c.ID, job.runID, status, reason)
		r.finishTidy(ctx, job, state, reason)
		return
	}
	var reply memoTidyReply
	if err := json.Unmarshal(body, &reply); err != nil || reply.Run != job.runID {
		r.finishTidy(ctx, job, store.TidyRunDropped, memotidy.CodeBadShape)
		return
	}
	ops, ref := memotidy.Parse(append(append([]byte(`{"operations":`), reply.Operations...), '}'))
	if ref != nil {
		r.finishTidy(ctx, job, store.TidyRunDropped, ref.Code)
		return
	}
	wctx, wcancel := memoWriteCtx(ctx)
	defer wcancel()
	out, err := memoTidyApply(wctx, r.mgr.store, memoTidyApplyIn{MembershipID: job.q.MembershipID, QueueID: job.c.ID,
		RunID: job.runID, Trigger: store.TidyTriggerAuto, Input: job.input, Destinations: offered, Ops: ops})
	switch {
	case errors.Is(err, errMemoTidyNotRunning):
		log.Printf("memo queue: queue %s: tidy run %s was taken over; its reply is discarded", job.c.ID, job.runID)
	case err != nil:
		// The run stays running and the takeover ends it, marking its input seen.
		log.Printf("memo queue: queue %s: tidy run %s: apply: %v", job.c.ID, job.runID, err)
	default:
		log.Printf("memo queue: queue %s: tidy run %s ended %s %s", job.c.ID, job.runID, out.Run.State, out.Run.Reason)
	}
}

// tidyCodeOutcome says how a run ends for the Agent's code. Skipped: nothing reached a model.
// Dropped: the reply was refused whole. Failed: the call failed or timed out, and any code this
// Control Plane does not know.
func tidyCodeOutcome(code string) (state, reason string) {
	switch code {
	case tidyCodeDenyAll, tidyCodeUnconfirmed, tidyCodeForbidden, tidyCodeAgentOff, tidyCodeBusy, tidyCodeBadRequest:
		return store.TidyRunSkipped, code
	case tidyCodeMalformed:
		return store.TidyRunDropped, code
	case tidyCodeFailed, tidyCodeTimeout:
		return store.TidyRunFailed, code
	}
	return store.TidyRunFailed, memoTidyReasonAgentError
}

// callTidy posts the request. The Agent's own bound is 120 s; this waits memoTidyCallWait (or the
// runner's own, in tests), on the client that has no timeout of its own.
func (r *memoQueueRunner) callTidy(ctx context.Context, res *resolved, req memoTidyRequest) (int, []byte, error) {
	wait := r.tidyCallWait
	if wait <= 0 {
		wait = memoTidyCallWait
	}
	cctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	b, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(cctx, http.MethodPost, res.rt.Endpoint()+"/memos/tidy", bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if res.rt.Token() != "" {
		hr.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentLongCallClient.Do(hr)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, memoTidyReplyMax))
	return resp.StatusCode, out, err
}

// tidyRequest is the call's body and the ids of the destinations in it, which apply takes as the
// snapshot of what the model was offered. The destinations are the categories of the queue's own
// repository, queues or not, and the queue's listed destinations (which may lie in another
// repository). The queue itself is never one.
func (r *memoQueueRunner) tidyRequest(ctx context.Context, job *memoTidyJob) (memoTidyRequest, []string) {
	req := memoTidyRequest{Run: job.runID, Trigger: store.TidyTriggerAuto, Items: job.items, Destinations: []memoTidyWireDest{},
		Limits: memoTidyLimits{MergeSources: memotidy.MaxMergeSources, SplitParts: memotidy.MaxSplitParts,
			TextBytes: memotidy.MaxTextBytes, Operations: memotidy.MaxOperations}}
	label := job.c.Repo + "/" + job.c.Name
	if !tidyLabelOK(label) {
		label = job.c.ID
	}
	req.Queue = memoTidyWireQueue{ID: job.c.ID, Label: label}
	cats, cerr := r.mgr.store.ListCategories(ctx, job.q.MembershipID)
	queues, qerr := r.mgr.store.ListMemoQueues(ctx, job.q.MembershipID)
	if cerr != nil || qerr != nil {
		return req, []string{} // no destination is offered: the model can only merge, split and keep
	}
	isQueue := map[string]bool{}
	for _, q := range queues {
		isQueue[q.CategoryID] = true
	}
	byID := map[string]store.MemoCategory{}
	for _, c := range cats {
		byID[c.ID] = c
	}
	offered := []string{}
	add := func(c store.MemoCategory) {
		name := c.Repo + "/" + c.Name
		if c.ID == job.c.ID || !tidyLabelOK(name) || len(offered) >= memotidy.MaxDestinations {
			return
		}
		for _, id := range offered {
			if id == c.ID {
				return
			}
		}
		offered = append(offered, c.ID)
		req.Destinations = append(req.Destinations, memoTidyWireDest{ID: c.ID, Name: name, Queue: isQueue[c.ID]})
	}
	for _, id := range job.q.TidyDestinations {
		if c, ok := byID[id]; ok {
			add(c)
		}
	}
	for _, c := range cats {
		if c.Repo == job.c.Repo {
			add(c)
		}
	}
	return req, offered
}

// tidyLabelOK is the Agent's rule for a label: short, valid UTF-8, no control character.
func tidyLabelOK(s string) bool {
	return len(s) <= 200 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// finishTidy ends a run that no apply ended and marks its whole input seen, so that the messages
// fire as they are on the next tick. A run that is no longer running (a takeover ended it) is left.
func (r *memoQueueRunner) finishTidy(ctx context.Context, job *memoTidyJob, state, reason string) {
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err := r.mgr.store.MemoTx(wctx, job.q.MembershipID, func(tx store.MemoTxOps) error {
		run, ok, err := tx.GetTidyRun(job.runID)
		if err != nil || !ok || run.State != store.TidyRunRunning {
			return err
		}
		for _, it := range job.input {
			if err := tx.MarkTidySeen(it.ID, it.Hash); err != nil {
				return err
			}
		}
		at := memoNow()
		run.State, run.Reason, run.FinishedAt, run.UpdatedAt = state, reason, at, at
		_, err = tx.FinishTidyRun(run)
		return err
	})
	if err != nil {
		log.Printf("memo queue: queue %s: finish tidy run %s: %v", job.c.ID, job.runID, err)
	}
}
