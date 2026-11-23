package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/autorunfile"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Repository settings of a memo queue (#4881). A repository may carry its auto-run category
// settings in .agent-fleet/autorun.yaml; the workspace Agent reads that file from origin's
// default branch only, and this answers how it differs from the member's own settings. Nothing
// is applied until the member approves a difference (repoApprove), and settings stay per member:
// each member approves for their own queue. The member's settings can also be written as such a
// file (repoExport).
//
// The Control Plane parses the file, not the Agent: the Agent shares its workspace with the runs
// the settings govern, and the refusal of the Console-only switches must not depend on it.
//
// The routes answer 404 when AF_MEMO_QUEUE_REPO_SETTINGS is turned off (it is on by default).

// memoRepoFileWait bounds the Agent call, above the Agent's own 60-second bound on the fetch.
const memoRepoFileWait = 75 * time.Second

// memoRepoDiff is one setting the file holds with a value other than the member's.
type memoRepoDiff struct {
	// Key is the file's name for the setting, Field the settings' own on the wire.
	Key   string `json:"key"`
	Field string `json:"field"`
	Line  int    `json:"line"`
	// Current and File are JSON values of the setting's type.
	Current json.RawMessage `json:"current"`
	File    json.RawMessage `json:"file"`
	// Raise marks a limit the file raises: a higher spend cap or none at all, more active runs,
	// more continuations, more fires a day. The Console shows it prominently.
	Raise bool `json:"raise,omitempty"`
}

// memoRepoSettingsWire is GET /api/memo-queues/{categoryId}/repo-settings. Errors and Warnings
// are about the file and this category's entry; while Errors is not empty Diffs is empty and the
// member's settings stay as they are. A key the file leaves out is not compared.
type memoRepoSettingsWire struct {
	CategoryID string `json:"categoryId"`
	Repo       string `json:"repo"`
	Category   string `json:"category"`
	Path       string `json:"path"`
	// Remote is origin's URL as the workspace's clone names it, credentials removed.
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	// Found is whether the default branch holds the file, Entry whether it names this category.
	Found bool `json:"found"`
	Entry bool `json:"entry"`
	// Queue is whether the category has settings yet; without them the file is compared with the
	// defaults a new queue gets.
	Queue bool `json:"queue"`
	// ApprovalTag names what the difference was taken from: the category (id, repository, name),
	// the member's settings and the difference itself. An approval sends it back. ApprovedCommit
	// and ApprovedAt are the file commit the member last approved.
	ApprovalTag    string              `json:"approvalTag"`
	ApprovedCommit string              `json:"approvedCommit"`
	ApprovedAt     string              `json:"approvedAt"`
	Errors         []autorunfile.Issue `json:"errors"`
	Warnings       []autorunfile.Issue `json:"warnings"`
	Diffs          []memoRepoDiff      `json:"diffs"`
}

// agentAutorunFile is the Agent's GET /repos/{name}/autorun-file
// (workspace/agent/internal/gitx/autorun_file.go).
type agentAutorunFile struct {
	Path    string `json:"path"`
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Found   bool   `json:"found"`
	Content string `json:"content"`
}

// memoRepoField maps one file key onto the queue settings. raise is nil for a setting that is
// no limit.
type memoRepoField struct {
	key, field string
	set        func(in *memoQueueIn, v autorunfile.Value)
	get        func(q store.MemoQueue) any
	raise      func(cur, next store.MemoQueue) bool
}

func intPtr(n int) *int { return &n }

var memoRepoFieldByKey = func() map[string]memoRepoField {
	m := make(map[string]memoRepoField, len(memoRepoFields))
	for _, f := range memoRepoFields {
		m[f.key] = f
	}
	return m
}()

// memoRepoFields covers every autorunfile.Keys entry except instruction_file, which is read into
// instruction before anything is compared (TestMemoRepoFieldsCoverTheFileKeys).
var memoRepoFields = []memoRepoField{
	{key: "agent_kind", field: "agentKind",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.AgentKind = v.Str },
		get: func(q store.MemoQueue) any { return q.AgentKind }},
	{key: "model", field: "model",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.Model = v.Str },
		get: func(q store.MemoQueue) any { return q.Model }},
	{key: "effort", field: "effort",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.Effort = v.Str },
		get: func(q store.MemoQueue) any { return q.Effort }},
	{key: "route", field: "route",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.Route = v.Str },
		get: func(q store.MemoQueue) any { return q.Route }},
	{key: "spend_cap_usd", field: "spendCapUsd",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.SpendCapUSD = v.Num },
		get: func(q store.MemoQueue) any { return q.SpendCapUSD },
		// 0 is no cap: dropping a cap is the largest raise there is.
		raise: func(cur, next store.MemoQueue) bool {
			return cur.SpendCapUSD > 0 && (next.SpendCapUSD == 0 || next.SpendCapUSD > cur.SpendCapUSD)
		}},
	{key: "max_active", field: "maxActive",
		set:   func(in *memoQueueIn, v autorunfile.Value) { in.MaxActive = intPtr(v.Int) },
		get:   func(q store.MemoQueue) any { return q.MaxActive },
		raise: func(cur, next store.MemoQueue) bool { return next.MaxActive > cur.MaxActive }},
	{key: "batch", field: "batch",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.Batch = intPtr(v.Int) },
		get: func(q store.MemoQueue) any { return q.Batch }},
	{key: "open_cron", field: "openCron",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.OpenCron = v.Str },
		get: func(q store.MemoQueue) any { return q.OpenCron }},
	{key: "open_minutes", field: "openMinutes",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.OpenMinutes = v.Int },
		get: func(q store.MemoQueue) any { return q.OpenMinutes }},
	{key: "tz", field: "tz",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.TZ = v.Str },
		get: func(q store.MemoQueue) any { return q.TZ }},
	{key: "open_after", field: "openAfter",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.OpenAfter = v.Int },
		get: func(q store.MemoQueue) any { return q.OpenAfter }},
	{key: "max_fires_per_day", field: "maxFiresPerDay",
		set:   func(in *memoQueueIn, v autorunfile.Value) { in.MaxFiresPerDay = intPtr(v.Int) },
		get:   func(q store.MemoQueue) any { return q.MaxFiresPerDay },
		raise: func(cur, next store.MemoQueue) bool { return next.MaxFiresPerDay > cur.MaxFiresPerDay }},
	{key: "max_task_hours", field: "maxTaskHours",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.MaxTaskHours = intPtr(v.Int) },
		get: func(q store.MemoQueue) any { return q.MaxTaskHours }},
	{key: "worktree", field: "worktree",
		set: func(in *memoQueueIn, v autorunfile.Value) { b := v.Bool; in.Worktree = &b },
		get: func(q store.MemoQueue) any { return q.Worktree }},
	{key: "tidy", field: "tidy",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.Tidy = v.Bool },
		get: func(q store.MemoQueue) any { return q.Tidy }},
	{key: "continuation_max", field: "continuationMax",
		set:   func(in *memoQueueIn, v autorunfile.Value) { in.ContinuationMax = intPtr(v.Int) },
		get:   func(q store.MemoQueue) any { return q.ContinuationMax },
		raise: func(cur, next store.MemoQueue) bool { return next.ContinuationMax > cur.ContinuationMax }},
	{key: "instruction", field: "instruction",
		set: func(in *memoQueueIn, v autorunfile.Value) { in.Instruction = v.Str },
		get: func(q store.MemoQueue) any { return q.Instruction }},
}

// memoQueueErrKeys names the file key a settings refusal is about, by the start of its message
// (memoQueueIn.validate). A refusal that matches none is put on the category's line.
var memoQueueErrKeys = []struct{ prefix, key string }{
	{"a queue's consumer", "agent_kind"},
	{"model ", "model"},
	{"effort ", "effort"},
	{"spendCapUsd ", "spend_cap_usd"},
	{"continuationMax ", "continuation_max"},
	{"maxActive ", "max_active"},
	{"batch ", "batch"},
	{"maxTaskHours ", "max_task_hours"},
	{"maxFiresPerDay ", "max_fires_per_day"},
	{"openAfter ", "open_after"},
	{"a queue opens in a window", "open_cron"},
	{"openCron", "open_cron"},
	{"openMinutes ", "open_minutes"},
	{"tz ", "tz"},
	{"instruction ", "instruction"},
	{"a queue without an instruction", "instruction"},
}

// memoQueueInFrom is the PUT body that would save q as it is: the starting point the file's
// values are laid over.
func memoQueueInFrom(q store.MemoQueue) memoQueueIn {
	wt := q.Worktree
	return memoQueueIn{Instruction: q.Instruction, AgentKind: q.AgentKind, Model: q.Model, Effort: q.Effort,
		Route: q.Route, SpendCapUSD: q.SpendCapUSD, Worktree: &wt, AcceptOwnSessions: q.AcceptOwnSessions,
		ContinuationMax: intPtr(memoContinuationMax(q)), MaxActive: intPtr(q.MaxActive), Batch: intPtr(q.Batch),
		MaxTaskHours: intPtr(memoQueueTaskHours(q)), OpenCron: q.OpenCron, OpenMinutes: q.OpenMinutes, TZ: q.TZ,
		OpenAfter: q.OpenAfter, MaxFiresPerDay: intPtr(memoQueueFiresPerDay(q)), Enabled: q.Enabled,
		Tidy: q.Tidy, TidyDestinations: q.TidyDestinations}
}

// memoRepoOpenKeys are the settings that together decide when a queue fires by itself: a window
// (open_cron, open_minutes, tz) or a threshold (open_after). They change as one.
var memoRepoOpenKeys = map[string]bool{"open_cron": true, "open_minutes": true, "tz": true, "open_after": true}

// memoQueueGated says whether a queue waits for a window or a threshold before it fires by itself.
func memoQueueGated(q store.MemoQueue) bool { return q.OpenCron != "" || q.OpenAfter > 0 }

// memoRepoResult is what laying a file entry over the member's settings gives. While Errors is not
// empty the rest is unset. In is the settings body that saves Next, the entry's values that apply
// laid over the member's settings: what an approval writes.
type memoRepoResult struct {
	In       memoQueueIn
	Next     store.MemoQueue
	Diffs    []memoRepoDiff
	Warnings []autorunfile.Issue
	Errors   []autorunfile.Issue
}

// memoRepoCompare lays the entry's values over the member's settings (cur, or the defaults when
// hasQueue is false), runs the result through the Console's own validation, and lists what
// would change. A refusal comes back as an error on the line of the key that causes it.
func memoRepoCompare(cur store.MemoQueue, hasQueue bool, e *autorunfile.Entry) memoRepoResult {
	base := memoQueueIn{}
	if hasQueue {
		base = memoQueueInFrom(cur)
	}
	curN, aerr := base.validate()
	if aerr != nil {
		// Settings saved before a rule tightened: compare with them as stored.
		curN = cur
	}
	vals := make([]autorunfile.Value, 0, len(e.Values))
	for _, v := range e.Values {
		if _, known := memoRepoFieldByKey[v.Key]; known {
			vals = append(vals, v)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i].Line < vals[j].Line })
	build := func(vs []autorunfile.Value, skip int) memoQueueIn {
		in := base
		for i, v := range vs {
			if i != skip {
				memoRepoFieldByKey[v.Key].set(&in, v)
			}
		}
		return in
	}
	overlay := func(vs []autorunfile.Value, skip int) (store.MemoQueue, *apiError) {
		in := build(vs, skip)
		return in.validate()
	}
	next, aerr := overlay(vals, -1)
	if aerr != nil {
		return memoRepoResult{Errors: []autorunfile.Issue{memoRepoBlame(vals, e.Line, aerr, overlay)}}
	}
	var warns []autorunfile.Issue
	// Taking the window and the threshold both away makes the queue fire whenever a message
	// waits: that removes a gate, which only the Console may do (#4881).
	if memoQueueGated(curN) && !memoQueueGated(next) {
		kept := vals[:0:0]
		// The warning points at the key that takes the queue's gate away (open_cron for a window,
		// open_after for a threshold), else at the first firing key the file sets.
		var gate autorunfile.Value
		removes := false
		for _, v := range vals {
			if !memoRepoOpenKeys[v.Key] {
				kept = append(kept, v)
				continue
			}
			r := (v.Key == "open_cron" && curN.OpenCron != "") || (v.Key == "open_after" && curN.OpenAfter > 0)
			if gate.Key == "" || (r && !removes) {
				gate, removes = v, r
			}
		}
		warns = append(warns, autorunfile.Issue{Line: gate.Line, Key: gate.Key,
			Reason: "removing the window and the threshold makes the queue fire whenever a message waits; that is set in the Console only, and the file's open_cron, open_minutes, tz and open_after are not applied"})
		vals = kept
		if next, aerr = overlay(vals, -1); aerr != nil {
			return memoRepoResult{Warnings: warns, Errors: []autorunfile.Issue{memoRepoBlame(vals, e.Line, aerr, overlay)}}
		}
	}
	openLine := 0
	for _, v := range vals {
		if memoRepoOpenKeys[v.Key] {
			openLine = v.Line
			break
		}
	}
	inFile := map[string]autorunfile.Value{}
	for _, v := range vals {
		inFile[v.Key] = v
	}
	// validate drops a window's length and zone when there is no window. A file that sets them to
	// something else would see its values vanish without a word; zero and empty are what the
	// Console itself sends for a threshold queue.
	if next.OpenCron == "" {
		var errs []autorunfile.Issue
		if v, ok := inFile["open_minutes"]; ok && v.Int != 0 {
			errs = append(errs, autorunfile.Issue{Line: v.Line, Key: "open_minutes", Reason: "open_minutes needs open_cron: without a window it has no effect"})
		}
		if v, ok := inFile["tz"]; ok && v.Str != "" {
			errs = append(errs, autorunfile.Issue{Line: v.Line, Key: "tz", Reason: "tz needs open_cron: without a window it has no effect"})
		}
		if len(errs) > 0 {
			return memoRepoResult{Warnings: warns, Errors: errs}
		}
	}
	diffs := []memoRepoDiff{}
	for _, f := range memoRepoFields {
		v, ok := inFile[f.key]
		line := v.Line
		// The settings that decide when the queue fires change together: switching a window for a
		// threshold also clears the window's length and zone, which the file need not mention.
		if !ok && memoRepoOpenKeys[f.key] && openLine != 0 {
			ok, line = true, openLine
		}
		if !ok {
			continue
		}
		c, _ := json.Marshal(f.get(curN))
		n, _ := json.Marshal(f.get(next))
		if string(c) == string(n) {
			continue
		}
		d := memoRepoDiff{Key: f.key, Field: f.field, Line: line, Current: c, File: n}
		if f.raise != nil {
			d.Raise = f.raise(curN, next)
		}
		diffs = append(diffs, d)
	}
	return memoRepoResult{In: build(vals, -1), Next: next, Diffs: diffs, Warnings: warns}
}

// memoRepoBlame puts a refusal on the key that causes it: the field the message names when the
// file sets it and its removal alone makes the rest valid, else the first key whose removal does.
// With no such key (two faults, or a fault in the stored settings) the message's own field is
// used when the file sets it, and the category's line otherwise.
func memoRepoBlame(vals []autorunfile.Value, entryLine int, aerr *apiError,
	overlay func([]autorunfile.Value, int) (store.MemoQueue, *apiError)) autorunfile.Issue {
	// The field the message names, when the file sets it and taking it away is enough.
	for _, m := range memoQueueErrKeys {
		if strings.HasPrefix(aerr.message, m.prefix) {
			for i, v := range vals {
				if v.Key != m.key {
					continue
				}
				if _, err := overlay(vals, i); err == nil || len(vals) == 1 {
					return autorunfile.Issue{Line: v.Line, Key: v.Key, Reason: aerr.message}
				}
			}
			break
		}
	}
	if len(vals) > 1 {
		for i, v := range vals {
			if _, err := overlay(vals, i); err == nil {
				return autorunfile.Issue{Line: v.Line, Key: v.Key, Reason: aerr.message}
			}
		}
	} else if len(vals) == 1 {
		return autorunfile.Issue{Line: vals[0].Line, Key: vals[0].Key, Reason: aerr.message}
	}
	for _, m := range memoQueueErrKeys {
		if strings.HasPrefix(aerr.message, m.prefix) {
			for _, v := range vals {
				if v.Key == m.key {
					return autorunfile.Issue{Line: v.Line, Key: v.Key, Reason: aerr.message}
				}
			}
			break
		}
	}
	return autorunfile.Issue{Line: entryLine, Reason: aerr.message}
}

// repoSwitch checks the switch before anything else, so that with the switch off the routes
// neither resolve nor create a workspace.
func (a memoAPI) repoSwitch(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.mgr.memoRepoSettings {
			writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "repository settings are not turned on"})
			return
		}
		h(w, r)
	}
}

func (a memoAPI) repoSettingsRoute() http.HandlerFunc {
	return a.repoSwitch(a.withResolved(a.repoSettings))
}

func (a memoAPI) repoApproveRoute() http.HandlerFunc {
	return a.repoSwitch(a.withResolved(a.repoApprove))
}

func (a memoAPI) repoExportRoute() http.HandlerFunc {
	return a.repoSwitch(a.withMembership(a.repoExport))
}

// memoRepoLoaded is a category's settings file as the workspace read it from origin's default
// branch.
type memoRepoLoaded struct {
	c    store.MemoCategory
	file agentAutorunFile
}

// loadRepoFile checks that the category is the member's and belongs to a repository, and reads
// its settings file through the running workspace.
func (a memoAPI) loadRepoFile(ctx context.Context, res *resolved, categoryID string) (memoRepoLoaded, *apiError) {
	c, ok, err := a.store.GetCategory(ctx, categoryID)
	if err != nil {
		return memoRepoLoaded{}, internalErr(err)
	}
	if !ok || c.MembershipID != res.mv.MembershipID {
		return memoRepoLoaded{}, &apiError{http.StatusNotFound, "not_found", "category not found"}
	}
	if c.Repo == "" {
		return memoRepoLoaded{}, &apiError{http.StatusConflict, errCodeMemoQueueNoRepo, "this category belongs to no repository"}
	}
	if res.rt.State(ctx) != "running" {
		return memoRepoLoaded{}, &apiError{http.StatusConflict, "workspace_not_running", "your workspace must be running to read the repository's settings"}
	}
	file, aerr := a.mgr.agentAutorunFile(ctx, res, c.Repo)
	if aerr != nil {
		return memoRepoLoaded{}, aerr
	}
	// The Agent shows host and path only; a token pasted into the path is not passed on either.
	if autorunfile.LooksLikeCredential(file.Remote) {
		file.Remote = memoRepoRemoteHidden
	}
	return memoRepoLoaded{c: c, file: file}, nil
}

// memoRepoEntry parses the file and answers the category's entry with every finding about the
// file and the entry; e is nil when the file does not name the category.
func memoRepoEntry(content, category string) (e *autorunfile.Entry, errs, warns []autorunfile.Issue) {
	parsed := autorunfile.Parse([]byte(content))
	errs = append(errs, parsed.Errors...)
	e = parsed.Categories[category]
	if e != nil {
		errs = append(errs, e.Errors...)
		warns = append(warns, e.Warnings...)
	}
	return e, errs, warns
}

// memoRepoReadEntry is memoRepoEntry with instruction_file read: the file it names comes from the
// commit the settings file was read at, and stands in the entry as instruction. A file that
// cannot be used is an error on the instruction_file line. errs is set when the entry cannot be
// compared; aerr when the workspace could not be asked.
func (m *manager) memoRepoReadEntry(ctx context.Context, res *resolved, repo string, file agentAutorunFile, category string) (
	e *autorunfile.Entry, errs, warns []autorunfile.Issue, aerr *apiError) {
	e, errs, warns = memoRepoEntry(file.Content, category)
	if e == nil || len(errs) > 0 {
		return e, errs, warns, nil
	}
	v, ok := e.Values["instruction_file"]
	if !ok {
		return e, errs, warns, nil
	}
	content, found, why, aerr := m.agentAutorunInstruction(ctx, res, repo, file.Commit, v.Str)
	if aerr != nil {
		return nil, nil, nil, aerr
	}
	bad := func(reason string) (*autorunfile.Entry, []autorunfile.Issue, []autorunfile.Issue, *apiError) {
		return e, append(errs, autorunfile.Issue{Line: v.Line, Key: "instruction_file", Reason: reason}), warns, nil
	}
	switch {
	case why != "":
		return bad(why)
	case !found:
		return bad(fmt.Sprintf("instruction_file names %s, which the commit does not hold", v.Str))
	case strings.ContainsRune(content, 0):
		// Postgres cannot store the byte, so an approval would fail where the difference looked fine.
		return bad("the instruction file holds a NUL byte, which an instruction cannot hold")
	case autorunfile.LooksLikeCredential(content):
		return bad("the instruction file looks like it holds a credential: the repository's settings hold names only, never a secret")
	}
	// The entry may be shared with other categories through an alias: its values are copied, not
	// changed in place.
	out := *e
	out.Values = make(map[string]autorunfile.Value, len(e.Values))
	for k, val := range e.Values {
		if k != "instruction_file" {
			out.Values[k] = val
		}
	}
	out.Values["instruction"] = autorunfile.Value{Key: "instruction", Line: v.Line, Str: content}
	return &out, errs, warns, nil
}

// memoRepoEvaluate fills what the file says against the member's settings: the findings and the
// difference. The caller has filled the file's own fields.
func (m *manager) memoRepoEvaluate(ctx context.Context, res *resolved, c store.MemoCategory, file agentAutorunFile,
	cur store.MemoQueue, hasQueue bool, out *memoRepoSettingsWire) *apiError {
	if !file.Found {
		return nil
	}
	e, errs, warns, aerr := m.memoRepoReadEntry(ctx, res, c.Repo, file, c.Name)
	if aerr != nil {
		return aerr
	}
	out.Entry = e != nil
	out.Errors = append(out.Errors, errs...)
	out.Warnings = append(out.Warnings, warns...)
	if len(out.Errors) == 0 && e != nil {
		r := memoRepoCompare(cur, hasQueue, e)
		out.Warnings = append(out.Warnings, r.Warnings...)
		out.Errors = append(out.Errors, r.Errors...)
		if len(r.Errors) == 0 {
			out.Diffs = r.Diffs
		}
	}
	return nil
}

// repoSettings answers how the repository's settings file differs from the member's settings for
// one category. It needs the workspace running: the file is read there, from origin.
func (a memoAPI) repoSettings(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	// A read is ordered by when it began: one that began earlier never replaces it (memoRepoCheckFrom).
	started := time.Now()
	ld, aerr := a.loadRepoFile(ctx, res, r.PathValue("categoryId"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	c, file := ld.c, ld.file
	qs, err := a.store.ListMemoQueues(ctx, res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var cur store.MemoQueue
	hasQueue := false
	for _, q := range qs {
		if q.CategoryID == c.ID {
			cur, hasQueue = q, true
			break
		}
	}
	out := memoRepoSettingsWire{CategoryID: c.ID, Repo: c.Repo, Category: c.Name, Path: autorunfile.Path,
		Remote: file.Remote, Branch: file.Branch, Commit: file.Commit, Found: file.Found, Queue: hasQueue,
		ApprovedCommit: cur.RepoCommit, ApprovedAt: cur.RepoApprovedAt,
		Errors: []autorunfile.Issue{}, Warnings: []autorunfile.Issue{}, Diffs: []memoRepoDiff{}}
	if aerr := a.mgr.memoRepoEvaluate(ctx, res, c, file, cur, hasQueue, &out); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	out.ApprovalTag = memoRepoApprovalTag(c, cur, hasQueue, out.Diffs)
	if hasQueue {
		// What the member was shown is the queue's latest read; a failed write only leaves an older one.
		_ = a.mgr.memoRepoRecord(ctx, res.mv.MembershipID, memoRepoCheckFrom(c, cur, &out, started), c.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

// memoRepoApproveIn is POST /api/memo-queues/{categoryId}/repo-settings/approve.
type memoRepoApproveIn struct {
	// Commit is the file's commit and ApprovalTag what the difference was taken from, both as
	// GET .../repo-settings answered. Either having moved since answers 409 memo_queue_repo_stale:
	// the member approves the values they saw, nothing else.
	Commit      string `json:"commit"`
	ApprovalTag string `json:"approvalTag"`
	// Keys are the file keys approved, each one of the differences shown. The settings that
	// decide when the queue fires (open_cron, open_minutes, tz, open_after) change as one: their
	// differences are approved all together or not at all.
	Keys []string `json:"keys"`
}

// memoRepoApprovalTag names what a difference was taken from: the category as it is named now
// (a rename makes the file's entry for another name apply), the member's settings by their
// content (updatedAt has one-second steps), and the difference with its values. The approval
// recomputes it in its own transaction, so it applies exactly what the member was shown.
func memoRepoApprovalTag(c store.MemoCategory, q store.MemoQueue, hasQueue bool, diffs []memoRepoDiff) string {
	var settings *memoQueueIn
	if hasQueue {
		in := memoQueueInFrom(q)
		settings = &in
	}
	if diffs == nil {
		diffs = []memoRepoDiff{}
	}
	b, _ := json.Marshal(struct {
		ID, Repo, Name string
		Settings       *memoQueueIn
		Diffs          []memoRepoDiff
	}{c.ID, c.Repo, c.Name, settings, diffs})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func memoRepoStale(msg string) *apiError {
	return &apiError{http.StatusConflict, errCodeMemoQueueRepoStale, msg}
}

// repoApprove saves the file's values for the approved keys through the same validation and the
// same write as the Console's settings PUT, and records the file's commit on the member's queue.
// The values come from the file as the workspace reads it now, never from the request: the
// request only names keys, so a client cannot approve a value the file does not hold.
func (a memoAPI) repoApprove(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	var in memoRepoApproveIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	if !memoRepoCommitRe.MatchString(in.Commit) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "commit must be the file's commit id"})
		return
	}
	if len(in.Keys) == 0 || len(in.Keys) > len(memoRepoFields) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "keys must name the settings to apply"})
		return
	}
	picked := map[string]bool{}
	for _, k := range in.Keys {
		if _, known := memoRepoFieldByKey[k]; !known || picked[k] {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "keys must name each setting once"})
			return
		}
		picked[k] = true
	}
	if on, err := a.mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID); err != nil || !on {
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeMemoQueuesDisabled, "your tenant has not turned memo queues on"})
		return
	}
	ld, aerr := a.loadRepoFile(ctx, res, r.PathValue("categoryId"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	const changed = "the repository's settings file, the category or your settings changed since you looked at the difference; check it again"
	if !ld.file.Found || ld.file.Commit != in.Commit {
		writeAPIErr(w, memoRepoStale(changed))
		return
	}
	e, errs, _, aerr := a.mgr.memoRepoReadEntry(ctx, res, ld.c.Repo, ld.file, ld.c.Name)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if e == nil || len(errs) > 0 {
		writeAPIErr(w, memoRepoStale(changed))
		return
	}
	var out memoQueueDTO
	err := a.store.MemoTx(ctx, res.mv.MembershipID, func(tx store.MemoTxOps) error {
		// Read again behind the tenant row's lock: the switch may have been turned off while the
		// workspace read the file, and turning it off waits for this transaction.
		if on, err := tx.LockTenantSwitch(); err != nil {
			return err
		} else if !on {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoQueuesDisabled, "your tenant has not turned memo queues on"})
		}
		c, ok, err := tx.GetCategory(ld.c.ID)
		if err != nil {
			return err
		}
		if !ok {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "category not found"})
		}
		cur, existed, err := tx.GetQueue(c.ID)
		if err != nil {
			return err
		}
		full := memoRepoCompare(cur, existed, e)
		var shownDiffs []memoRepoDiff
		if len(full.Errors) == 0 {
			shownDiffs = full.Diffs
		}
		// The entry was read under the name the category had before this transaction: a rename
		// since then changes the tag as well.
		if c.Name != ld.c.Name || len(full.Errors) > 0 || memoRepoApprovalTag(c, cur, existed, shownDiffs) != in.ApprovalTag {
			return memoTxFail(memoRepoStale(changed))
		}
		shown := map[string]bool{}
		openShown, openPicked := 0, 0
		for _, d := range full.Diffs {
			shown[d.Key] = true
			if memoRepoOpenKeys[d.Key] {
				openShown++
				if picked[d.Key] {
					openPicked++
				}
			}
		}
		for k := range picked {
			if !shown[k] {
				return memoTxFail(memoRepoStale("a setting you approved is no longer different; check it again"))
			}
		}
		if openPicked != 0 && openPicked != openShown {
			return memoTxFail(&apiError{http.StatusBadRequest, "bad_request",
				"the settings that decide when the queue runs by itself are approved together"})
		}
		// Only the approved keys are laid over the member's settings; the window and threshold keys
		// come as the whole group the file sets, which is what their differences showed.
		sub := *e
		sub.Values = map[string]autorunfile.Value{}
		for k, v := range e.Values {
			if picked[k] || (openPicked > 0 && memoRepoOpenKeys[k]) {
				sub.Values[k] = v
			}
		}
		part := memoRepoCompare(cur, existed, &sub)
		if len(part.Errors) > 0 {
			return memoTxFail(badQueueSettings(part.Errors[0].Reason))
		}
		saved, err := memoPutQueueTx(tx, c, res.mv.MembershipID, part.Next, part.In, cur.TidyDestinations)
		if err != nil {
			return err
		}
		if err := tx.SetQueueRepoApproval(c.ID, ld.file.Commit, saved.UpdatedAt); err != nil {
			return err
		}
		out = memoQueueToDTO(saved, c)
		return nil
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// memoRepoExportWire is GET /api/memo-queues/{categoryId}/repo-settings/export. Omitted names the
// settings left out because their value looks like a credential: a file is committed, and a
// secret never goes into one.
type memoRepoExportWire struct {
	Path     string              `json:"path"`
	Category string              `json:"category"`
	YAML     string              `json:"yaml"`
	Omitted  []autorunfile.Issue `json:"omitted"`
}

// memoRepoExportValues are the member's settings as file values: every key a file may set, except
// the ones only the Console may set to the value q holds, and the window or threshold keys q does
// not use. Comparing the result with q finds no difference (TestMemoRepoExportRoundTrips).
func memoRepoExportValues(q store.MemoQueue) (vals []autorunfile.Value, omitted []autorunfile.Issue) {
	n, aerr := memoQueueInFrom(q).validate()
	if aerr != nil {
		n = q
	}
	for _, f := range memoRepoFields {
		switch f.key {
		case "open_cron", "open_minutes", "tz":
			if n.OpenCron == "" {
				continue
			}
		case "open_after":
			if n.OpenAfter == 0 {
				continue
			}
		case "worktree":
			if !n.Worktree {
				continue
			}
		case "tidy":
			if n.Tidy {
				continue
			}
		}
		v := autorunfile.Value{Key: f.key}
		switch x := f.get(n).(type) {
		case string:
			if autorunfile.LooksLikeCredential(x) {
				omitted = append(omitted, autorunfile.Issue{Key: f.key, Reason: f.key + " looks like a credential and is left out"})
				continue
			}
			v.Str = x
		case int:
			v.Int = x
		case float64:
			v.Num = x
		case bool:
			v.Bool = x
		}
		vals = append(vals, v)
	}
	return vals, omitted
}

// repoExport writes the member's settings of one category as a settings file to start the
// repository's from. Nothing is read from the workspace.
func (a memoAPI) repoExport(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	ctx := r.Context()
	c, ok, err := a.store.GetCategory(ctx, r.PathValue("categoryId"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || c.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category not found"})
		return
	}
	qs, err := a.store.ListMemoQueues(ctx, mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The name becomes a key of the file: a token pasted into it must not be written, and the
	// answer does not repeat it.
	if autorunfile.LooksLikeCredential(c.Name) {
		writeAPIErr(w, &apiError{http.StatusUnprocessableEntity, errCodeMemoQueueRepoExportName,
			"the category's name looks like a credential; rename the category before exporting"})
		return
	}
	for _, q := range qs {
		if q.CategoryID != c.ID {
			continue
		}
		vals, omitted := memoRepoExportValues(q)
		b, err := autorunfile.Format(c.Name, vals)
		if errors.Is(err, autorunfile.ErrTooLarge) {
			writeAPIErr(w, &apiError{http.StatusUnprocessableEntity, errCodeMemoQueueRepoExportSize,
				fmt.Sprintf("these settings make a file larger than %d bytes, which a repository's settings file cannot be", autorunfile.MaxBytes)})
			return
		}
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if omitted == nil {
			omitted = []autorunfile.Issue{}
		}
		writeJSON(w, http.StatusOK, memoRepoExportWire{Path: autorunfile.Path, Category: c.Name, YAML: string(b), Omitted: omitted})
		return
	}
	writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "this category has no auto-run settings to export"})
}

// memoRepoRemoteHidden is the Agent's own text for a remote it does not show.
const memoRepoRemoteHidden = "(remote not shown)"

// memoRepoWireMax bounds the Agent's answer: the file at its largest, every byte escaped to six
// in JSON (\u003c), and the envelope.
const memoRepoWireMax = 6*autorunfile.MaxBytes + 8<<10

var (
	memoRepoAgentCodeRe = regexp.MustCompile(`^[a-z_]{1,40}$`)
	memoRepoCommitRe    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// decodeAgentAutorunFile reads a 200 answer strictly: every field present with its type, and
// found agreeing with content, so a cut or half-written answer never reads as "no file".
func decodeAgentAutorunFile(body []byte) (agentAutorunFile, bool) {
	var raw struct {
		Path    *string `json:"path"`
		Remote  *string `json:"remote"`
		Branch  *string `json:"branch"`
		Commit  *string `json:"commit"`
		Found   *bool   `json:"found"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(body, &raw); err != nil ||
		raw.Path == nil || raw.Remote == nil || raw.Branch == nil || raw.Commit == nil || raw.Found == nil || raw.Content == nil ||
		*raw.Path != autorunfile.Path || *raw.Branch == "" || !memoRepoCommitRe.MatchString(*raw.Commit) ||
		(!*raw.Found && *raw.Content != "") {
		return agentAutorunFile{}, false
	}
	return agentAutorunFile{Path: *raw.Path, Remote: *raw.Remote, Branch: *raw.Branch, Commit: *raw.Commit,
		Found: *raw.Found, Content: *raw.Content}, true
}

// agentAutorunFile asks the workspace Agent for the settings file. Every message is the Control
// Plane's own: what the Agent says is never repeated, because a failed fetch's text carries
// git's output, and only a well-formed code is named.
func (m *manager) agentAutorunFile(ctx context.Context, res *resolved, repo string) (agentAutorunFile, *apiError) {
	var out agentAutorunFile
	unreadable := func(status int, msg string) (agentAutorunFile, *apiError) {
		return agentAutorunFile{}, &apiError{status, errCodeMemoQueueRepoUnreadable, msg}
	}
	cctx, cancel := context.WithTimeout(ctx, memoRepoFileWait)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet,
		res.rt.Endpoint()+"/repos/"+url.PathEscape(repo)+"/autorun-file", nil)
	if err != nil {
		return out, internalErr(err)
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return unreadable(http.StatusBadGateway, "the workspace did not answer")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, memoRepoWireMax+1))
	if err != nil {
		return unreadable(http.StatusBadGateway, "the workspace's answer was cut off")
	}
	if len(body) > memoRepoWireMax {
		return unreadable(http.StatusBadGateway, "the workspace's answer is larger than any settings file can be")
	}
	code := agentErrCode(body)
	switch {
	case resp.StatusCode == http.StatusOK:
		var ok bool
		if out, ok = decodeAgentAutorunFile(body); !ok {
			return unreadable(http.StatusBadGateway, "the workspace's answer could not be read")
		}
		if len(out.Content) > autorunfile.MaxBytes {
			return unreadable(http.StatusUnprocessableEntity, fmt.Sprintf("%s is larger than %d bytes", autorunfile.Path, autorunfile.MaxBytes))
		}
		return out, nil
	case resp.StatusCode == http.StatusNotFound && code == "not_found":
		return unreadable(http.StatusNotFound, "the workspace has no git working copy named "+repo)
	case code == "file_too_large":
		return unreadable(http.StatusUnprocessableEntity, fmt.Sprintf("%s is larger than %d bytes", autorunfile.Path, autorunfile.MaxBytes))
	case code == "file_not_regular":
		return unreadable(http.StatusUnprocessableEntity, autorunfile.Path+" on the default branch is not a regular file")
	case code == "file_not_utf8":
		return unreadable(http.StatusUnprocessableEntity, autorunfile.Path+" on the default branch is not UTF-8 text")
	}
	why := "HTTP " + strconv.Itoa(resp.StatusCode)
	if memoRepoAgentCodeRe.MatchString(code) {
		why = code
	}
	return unreadable(http.StatusBadGateway, "the workspace could not read the file from origin's default branch ("+why+")")
}

// agentAutorunInstruction is the Agent's GET /repos/{name}/autorun-file/instruction
// (workspace/agent/internal/gitx/autorun_file.go).
type agentAutorunInstruction struct {
	Path    string `json:"path"`
	Commit  string `json:"commit"`
	Found   bool   `json:"found"`
	Content string `json:"content"`
}

// memoRepoInstructionWireMax bounds the Agent's answer about an instruction file, as
// memoRepoWireMax does the settings file's.
const memoRepoInstructionWireMax = 6*memoQueueInstructionMax + 8<<10

// agentAutorunInstruction asks the workspace Agent for the file at p in commit, the commit the
// settings file was read at. why is set when the file is there but cannot be an instruction; aerr
// when the workspace could not answer. As for the settings file, the Agent's text is never
// repeated.
func (m *manager) agentAutorunInstruction(ctx context.Context, res *resolved, repo, commit, p string) (
	content string, found bool, why string, aerr *apiError) {
	unreadable := func(msg string) (string, bool, string, *apiError) {
		return "", false, "", &apiError{http.StatusBadGateway, errCodeMemoQueueRepoUnreadable, msg}
	}
	cctx, cancel := context.WithTimeout(ctx, memoRepoFileWait)
	defer cancel()
	q := url.Values{"commit": {commit}, "path": {p}}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet,
		res.rt.Endpoint()+"/repos/"+url.PathEscape(repo)+"/autorun-file/instruction?"+q.Encode(), nil)
	if err != nil {
		return "", false, "", internalErr(err)
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return unreadable("the workspace did not answer")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, memoRepoInstructionWireMax+1))
	if err != nil {
		return unreadable("the workspace's answer was cut off")
	}
	if len(body) > memoRepoInstructionWireMax {
		return unreadable("the workspace's answer is larger than any instruction can be")
	}
	code := agentErrCode(body)
	switch {
	case resp.StatusCode == http.StatusOK:
		var raw struct {
			Path    *string `json:"path"`
			Commit  *string `json:"commit"`
			Found   *bool   `json:"found"`
			Content *string `json:"content"`
		}
		// Strict, as for the settings file: an answer about another file or commit, or a cut one,
		// never reads as the instruction.
		if err := json.Unmarshal(body, &raw); err != nil || raw.Path == nil || raw.Commit == nil || raw.Found == nil ||
			raw.Content == nil || *raw.Path != p || *raw.Commit != commit || (!*raw.Found && *raw.Content != "") {
			return unreadable("the workspace's answer could not be read")
		}
		if len(*raw.Content) > memoQueueInstructionMax {
			return "", false, fmt.Sprintf("the instruction file is larger than %d bytes", memoQueueInstructionMax), nil
		}
		return *raw.Content, *raw.Found, "", nil
	case code == "file_too_large":
		return "", false, fmt.Sprintf("the instruction file is larger than %d bytes", memoQueueInstructionMax), nil
	case code == "file_not_regular":
		return "", false, "instruction_file must name a regular file, not a link, a directory or a submodule", nil
	case code == "file_not_utf8":
		return "", false, "the instruction file is not UTF-8 text", nil
	case resp.StatusCode == http.StatusNotFound && code == "not_found":
		// The settings file was just read from it: the working copy went away in between.
		return unreadable("the workspace no longer has a git working copy named " + repo)
	case resp.StatusCode == http.StatusNotFound && code == "":
		// The route's own 404: an Agent from before instruction files.
		return unreadable("the workspace's Agent is older than this Control Plane and cannot read instruction_file")
	}
	why = "HTTP " + strconv.Itoa(resp.StatusCode)
	if memoRepoAgentCodeRe.MatchString(code) {
		why = code
	}
	return unreadable("the workspace could not read the instruction file from the settings' commit (" + why + ")")
}

// The last read of a repository's settings file, kept on the member's queue (#4881). A read is
// taken when the member checks the file in the Console, and, with AF_MEMO_QUEUE_REPO_RECHECK set,
// again on that interval for every enabled queue whose workspace is running. The Console shows
// "Repository settings differ" from it. It only ever informs: nothing is applied without the
// member's approval, which reads the file again.

// Repo check states. The same words go to the Console.
const (
	memoRepoCheckDiffers = "differs"
	memoRepoCheckSame    = "same"
	memoRepoCheckInvalid = "invalid"
	memoRepoCheckAbsent  = "absent"
)

// memoRepoCheckWire is a queue's last read of the file, on the queue's wire.
type memoRepoCheckWire struct {
	State     string `json:"state"`
	Commit    string `json:"commit"`
	Diffs     int    `json:"diffs"`
	CheckedAt string `json:"checkedAt"`
}

// memoRepoBasis names what a read was compared with: the category as named now (a rename makes
// another entry of the file apply) and the member's settings by their content. A read whose basis
// is not the queue's current one says nothing about it.
func memoRepoBasis(c store.MemoCategory, q store.MemoQueue) string {
	b, _ := json.Marshal(struct {
		Repo, Name string
		Settings   memoQueueIn
	}{c.Repo, c.Name, memoQueueInFrom(q)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// memoRepoCheckTime is a read's time on the queue: fixed width, so that the store's comparison of
// two as text orders them (SetMemoQueueRepoCheck keeps the read that began last).
const memoRepoCheckTime = "2006-01-02T15:04:05.000000Z07:00"

// memoRepoCheckFrom is the record of what a read that began at started found for the queue cur.
// The time is the start, not the end: a slow read that began before another must not replace it.
func memoRepoCheckFrom(c store.MemoCategory, cur store.MemoQueue, out *memoRepoSettingsWire, started time.Time) store.MemoRepoCheck {
	state := memoRepoCheckSame
	switch {
	case len(out.Errors) > 0:
		state = memoRepoCheckInvalid
	case !out.Found || !out.Entry:
		state = memoRepoCheckAbsent
	case len(out.Diffs) > 0:
		state = memoRepoCheckDiffers
	}
	return store.MemoRepoCheck{State: state, Commit: out.Commit, Diffs: len(out.Diffs), Basis: memoRepoBasis(c, cur),
		At: started.UTC().Format(memoRepoCheckTime)}
}

// memoRepoRecord keeps a read on the queue, in one transaction behind the tenant row's lock: not
// once memo queues are turned off, and not when the category or the settings changed since the
// read compared them (its basis is not theirs any more), so a read taken against old settings
// never replaces one taken against the current ones. Between two reads of the same basis the one
// that began later stays.
func (m *manager) memoRepoRecord(ctx context.Context, membershipID string, rc store.MemoRepoCheck, categoryID string) error {
	return m.store.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		if on, err := tx.LockTenantSwitch(); err != nil || !on {
			return err
		}
		c, ok, err := tx.GetCategory(categoryID)
		if err != nil || !ok {
			return err
		}
		q, ok, err := tx.GetQueue(categoryID)
		if err != nil || !ok || memoRepoBasis(c, q) != rc.Basis {
			return err
		}
		return tx.SetQueueRepoCheck(categoryID, rc)
	})
}

// memoRepoCheckOf is the queue's last read as the Console sees it: nil when there is none, or it
// was taken against another name or other settings.
func memoRepoCheckOf(q store.MemoQueue, c store.MemoCategory) *memoRepoCheckWire {
	rc := q.RepoCheck
	if rc.State == "" || c.Repo == "" || rc.Basis != memoRepoBasis(c, q) {
		return nil
	}
	return &memoRepoCheckWire{State: rc.State, Commit: rc.Commit, Diffs: rc.Diffs, CheckedAt: rc.At}
}

// The re-read's bounds. A read can wait up to memoRepoFileWait on a Git host that does not answer,
// so the members are read side by side, and each bound keeps one slow part from holding the rest:
// a host that hangs holds at most memoRepoRecheckPerHost of the memoRepoRecheckReads reads, and a
// member still being read when the next tick comes is left out of that tick, not waited for.
const (
	// memoRepoRecheckReads bounds the reads in flight across every member and host.
	memoRepoRecheckReads = 6
	// memoRepoRecheckPerHost bounds the reads in flight against one Git host. Most members share
	// one or two hosts, so this is also what one host's traffic from the re-read is.
	memoRepoRecheckPerHost = 2
	// memoRepoRecheckPrepare bounds the members finding their workspace at once: database reads,
	// the runtime's state and the Agent's list of working copies.
	memoRepoRecheckPrepare = 4
	// memoRepoRecheckListWait bounds the Agent's list of working copies, which reads no remote.
	memoRepoRecheckListWait = 15 * time.Second
)

// memoRepoRechecker reads the settings file again for every enabled queue on an interval.
type memoRepoRechecker struct {
	mgr   *manager
	every time.Duration
	now   func() time.Time
	// reads, perHost and prepare are the bounds; zero takes the constants above.
	reads, perHost, prepare int

	init sync.Once
	// readSlots and prepSlots are counting semaphores, sized by init.
	readSlots, prepSlots chan struct{}
	wg                   sync.WaitGroup
	mu                   sync.Mutex
	// busy holds the members whose pass has not finished.
	busy map[string]bool
	// hosts holds a semaphore per Git host while anyone holds or waits for it, so the map never
	// outgrows the hosts in use.
	hosts map[string]*memoRepoHostSlots
}

type memoRepoHostSlots struct {
	slots chan struct{}
	users int
}

func (rc *memoRepoRechecker) setup() {
	rc.init.Do(func() {
		if rc.reads <= 0 {
			rc.reads = memoRepoRecheckReads
		}
		if rc.perHost <= 0 {
			rc.perHost = memoRepoRecheckPerHost
		}
		if rc.prepare <= 0 {
			rc.prepare = memoRepoRecheckPrepare
		}
		rc.readSlots = make(chan struct{}, rc.reads)
		rc.prepSlots = make(chan struct{}, rc.prepare)
		rc.busy = map[string]bool{}
		rc.hosts = map[string]*memoRepoHostSlots{}
	})
}

func (rc *memoRepoRechecker) run(ctx context.Context) {
	t := time.NewTicker(rc.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rc.start(ctx)
		}
	}
}

// tick starts a pass and waits for every member's read, the earlier passes' included.
func (rc *memoRepoRechecker) tick(ctx context.Context) {
	rc.start(ctx)
	rc.wg.Wait()
}

// start reads each member's files once per repository and records the result on each enabled
// queue of that repository, each member on its own goroutine; it does not wait for them. A
// member whose earlier pass is still running is left out. A workspace that is not running is left
// alone: a read never starts one. A queue read less than half an interval ago (by the member, or
// by an earlier pass) is skipped.
func (rc *memoRepoRechecker) start(ctx context.Context) {
	if !rc.mgr.memoRepoSettings {
		return
	}
	rc.setup()
	qs, err := rc.mgr.store.ListEnabledMemoQueues(ctx)
	if err != nil {
		log.Printf("memo repo recheck: list queues: %v", err)
		return
	}
	byMember := map[string][]store.MemoQueue{}
	var order []string
	for _, q := range qs {
		if !q.Enabled {
			continue
		}
		if _, ok := byMember[q.MembershipID]; !ok {
			order = append(order, q.MembershipID)
		}
		byMember[q.MembershipID] = append(byMember[q.MembershipID], q)
	}
	for _, mid := range order {
		if ctx.Err() != nil {
			return
		}
		rc.mu.Lock()
		busy := rc.busy[mid]
		rc.busy[mid] = true
		rc.mu.Unlock()
		if busy {
			continue
		}
		rc.wg.Add(1)
		go func(mid string, qs []store.MemoQueue) {
			defer rc.wg.Done()
			defer func() {
				rc.mu.Lock()
				delete(rc.busy, mid)
				rc.mu.Unlock()
			}()
			rc.member(ctx, mid, qs)
		}(mid, byMember[mid])
	}
}

// acquireRead takes one of host's slots, then one of the shared ones, in that order: a member
// waiting for a busy host holds nothing other hosts' reads need. False when ctx ends first.
func (rc *memoRepoRechecker) acquireRead(ctx context.Context, host string) (release func(), ok bool) {
	rc.mu.Lock()
	h := rc.hosts[host]
	if h == nil {
		h = &memoRepoHostSlots{slots: make(chan struct{}, rc.perHost)}
		rc.hosts[host] = h
	}
	h.users++
	rc.mu.Unlock()
	leave := func() {
		rc.mu.Lock()
		if h.users--; h.users == 0 {
			delete(rc.hosts, host)
		}
		rc.mu.Unlock()
	}
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		leave()
		return nil, false
	}
	select {
	case rc.readSlots <- struct{}{}:
	case <-ctx.Done():
		<-h.slots
		leave()
		return nil, false
	}
	return func() {
		<-rc.readSlots
		<-h.slots
		leave()
	}, true
}

// memoRepoRemoteHosts maps each working copy's name to its origin's host, as the Agent's
// GET /repos names it (no remote is asked). Nil when the Agent did not answer.
func memoRepoRemoteHosts(ctx context.Context, res *resolved) map[string]string {
	cctx, cancel := context.WithTimeout(ctx, memoRepoRecheckListWait)
	defer cancel()
	body, err := agentText(cctx, res.rt, http.MethodGet, "/repos", nil)
	if err != nil {
		return nil
	}
	var inventory struct {
		Repos []struct {
			Name   string `json:"name"`
			Remote string `json:"remote"`
		} `json:"repos"`
	}
	if json.Unmarshal([]byte(body), &inventory) != nil {
		return nil
	}
	out := map[string]string{}
	for _, r := range inventory.Repos {
		out[r.Name] = strings.ToLower(r.Remote)
	}
	return out
}

func (rc *memoRepoRechecker) member(ctx context.Context, membershipID string, qs []store.MemoQueue) {
	select {
	case rc.prepSlots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	res, work, hosts := rc.prepareMember(ctx, membershipID, qs)
	<-rc.prepSlots
	if res == nil {
		return
	}
	// The tenant's switch is asked again before each read and each write: turned off while a
	// read waits on the workspace, nothing more is fetched and nothing is recorded.
	on := func() bool {
		v, err := rc.mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID)
		return err == nil && v
	}
	for _, repo := range work.order {
		if !on() {
			return
		}
		// A repository whose host is unknown (not in the Agent's list, no origin, or no list at
		// all) shares the bucket "" with every other such repository.
		release, ok := rc.acquireRead(ctx, hosts[repo])
		if !ok {
			return
		}
		rc.readRepo(ctx, res, membershipID, repo, work.byRepo[repo], on)
		release()
	}
}

// memoRepoDue is a queue due for a read, with its category.
type memoRepoDue struct {
	q store.MemoQueue
	c store.MemoCategory
}

// memoRepoWork is a member's due queues by repository, in the order the queues came.
type memoRepoWork struct {
	order  []string
	byRepo map[string][]memoRepoDue
}

// prepareMember finds the member's due queues and running workspace, and its working copies'
// hosts. A nil resolved means there is nothing to read.
func (rc *memoRepoRechecker) prepareMember(ctx context.Context, membershipID string, qs []store.MemoQueue) (*resolved, memoRepoWork, map[string]string) {
	now := rc.now()
	work := memoRepoWork{byRepo: map[string][]memoRepoDue{}}
	for _, q := range qs {
		c, ok, err := rc.mgr.store.GetCategory(ctx, q.CategoryID)
		if err != nil || !ok || c.Repo == "" || c.MembershipID != membershipID {
			continue
		}
		if at, err := time.Parse(time.RFC3339, q.RepoCheck.At); err == nil && q.RepoCheck.Basis == memoRepoBasis(c, q) &&
			now.Sub(at) < rc.every/2 {
			continue
		}
		if _, ok := work.byRepo[c.Repo]; !ok {
			work.order = append(work.order, c.Repo)
		}
		work.byRepo[c.Repo] = append(work.byRepo[c.Repo], memoRepoDue{q, c})
	}
	if len(work.order) == 0 {
		return nil, work, nil
	}
	// The workspace row is read, never built: resolving it for a start would create a missing one,
	// make a stopped one's keys and publish a runtime in the cache. A read needs the Agent's
	// address and token only. Whether it runs is the runtime's answer below, not the row's state
	// column, which a failed state write or an adopted container can leave at stopped.
	ws, ok, err := rc.mgr.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil, work, nil
	}
	identityID, ok, err := rc.mgr.store.IdentityIDForMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil, work, nil
	}
	ident, mv, aerr := rc.mgr.activeMembership(ctx, identityID, membershipID)
	if aerr != nil {
		return nil, work, nil
	}
	var rt runtime.Runtime
	if c, ok := rc.mgr.cachedRTFor(membershipID); ok && c.ws.ID == ws.ID {
		rt = c.rt
	} else {
		rt = rc.mgr.runtimeFor(ws, noSecretKeys)
	}
	if rt == nil || rt.Endpoint() == "" || rt.State(ctx) != "running" {
		return nil, work, nil
	}
	res := &resolved{rt: rt, ws: ws, ident: ident, mv: mv}
	return res, work, memoRepoRemoteHosts(ctx, res)
}

// readRepo reads one repository's file once and records it on each of its due queues.
func (rc *memoRepoRechecker) readRepo(ctx context.Context, res *resolved, membershipID, repo string, due []memoRepoDue, on func() bool) {
	started := rc.now()
	f, aerr := rc.mgr.agentAutorunFile(ctx, res, repo)
	if aerr != nil {
		// The last read stays as it was: an unreachable remote says nothing about the file.
		log.Printf("memo repo recheck: %s: %s", membershipID, aerr.code)
		return
	}
	for _, d := range due {
		if !on() {
			return
		}
		out := memoRepoSettingsWire{Found: f.Found, Commit: f.Commit}
		if aerr := rc.mgr.memoRepoEvaluate(ctx, res, d.c, f, d.q, true, &out); aerr != nil {
			log.Printf("memo repo recheck: %s: %s", membershipID, aerr.code)
			continue
		}
		if err := rc.mgr.memoRepoRecord(ctx, membershipID, memoRepoCheckFrom(d.c, d.q, &out, started), d.c.ID); err != nil {
			log.Printf("memo repo recheck: %s: record: %v", membershipID, err)
		}
	}
}
