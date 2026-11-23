package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/autorunfile"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Repository settings of a memo queue (#4881). A repository may carry its auto-run category
// settings in .agent-fleet/autorun.yaml; the workspace Agent reads that file from origin's
// default branch only, and this answers how it differs from the member's own settings. Nothing
// is saved here: the member approves a difference before anything is applied, and settings stay
// per member.
//
// The Control Plane parses the file, not the Agent: the Agent shares its workspace with the runs
// the settings govern, and the refusal of the Console-only switches must not depend on it.
//
// The route answers 404 unless AF_MEMO_QUEUE_REPO_SETTINGS is on, so a release can carry it
// before the Console shows it.

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
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	// Found is whether the default branch holds the file, Entry whether it names this category.
	Found bool `json:"found"`
	Entry bool `json:"entry"`
	// Queue is whether the category has settings yet; without them the file is compared with the
	// defaults a new queue gets.
	Queue    bool                `json:"queue"`
	Errors   []autorunfile.Issue `json:"errors"`
	Warnings []autorunfile.Issue `json:"warnings"`
	Diffs    []memoRepoDiff      `json:"diffs"`
}

// agentAutorunFile is the Agent's GET /repos/{name}/autorun-file
// (workspace/agent/internal/gitx/autorun_file.go).
type agentAutorunFile struct {
	Path    string `json:"path"`
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

// memoRepoFields covers every autorunfile.Keys entry except instruction_file, which is not read
// yet (TestMemoRepoFieldsCoverTheFileKeys).
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

// memoRepoCompare lays the entry's values over the member's settings (cur, or the defaults when
// hasQueue is false), runs the result through the Console's own validation, and lists what
// would change. A refusal comes back as an error on the line of the key it names.
func memoRepoCompare(cur store.MemoQueue, hasQueue bool, e *autorunfile.Entry) ([]memoRepoDiff, []autorunfile.Issue) {
	base := memoQueueIn{}
	if hasQueue {
		base = memoQueueInFrom(cur)
	}
	curN, aerr := base.validate()
	if aerr != nil {
		// Settings saved before a rule tightened: compare with them as stored.
		curN = cur
	}
	in := base
	for _, f := range memoRepoFields {
		if v, ok := e.Values[f.key]; ok {
			f.set(&in, v)
		}
	}
	next, aerr := in.validate()
	if aerr != nil {
		line, key := e.Line, ""
		for _, m := range memoQueueErrKeys {
			if strings.HasPrefix(aerr.message, m.prefix) {
				key = m.key
				if v, ok := e.Values[m.key]; ok {
					line = v.Line
				}
				break
			}
		}
		return nil, []autorunfile.Issue{{Line: line, Key: key, Reason: aerr.message}}
	}
	// validate drops a window's length and zone when there is no window; a file that sets them
	// would otherwise see its values vanish without a word.
	if next.OpenCron == "" {
		var errs []autorunfile.Issue
		for _, k := range []string{"open_minutes", "tz"} {
			if v, ok := e.Values[k]; ok {
				errs = append(errs, autorunfile.Issue{Line: v.Line, Key: k, Reason: k + " needs open_cron: without a window it has no effect"})
			}
		}
		if len(errs) > 0 {
			return nil, errs
		}
	}
	diffs := []memoRepoDiff{}
	for _, f := range memoRepoFields {
		v, ok := e.Values[f.key]
		if !ok {
			continue
		}
		c, _ := json.Marshal(f.get(curN))
		n, _ := json.Marshal(f.get(next))
		if string(c) == string(n) {
			continue
		}
		d := memoRepoDiff{Key: f.key, Field: f.field, Line: v.Line, Current: c, File: n}
		if f.raise != nil {
			d.Raise = f.raise(curN, next)
		}
		diffs = append(diffs, d)
	}
	return diffs, nil
}

// repoSettings answers how the repository's settings file differs from the member's settings for
// one category. It needs the workspace running: the file is read there, from origin.
func (a memoAPI) repoSettings(w http.ResponseWriter, r *http.Request, res *resolved) {
	if !a.mgr.memoRepoSettings {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "repository settings are not turned on"})
		return
	}
	ctx := r.Context()
	c, ok, err := a.store.GetCategory(ctx, r.PathValue("categoryId"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || c.MembershipID != res.mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category not found"})
		return
	}
	if c.Repo == "" {
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeMemoQueueNoRepo, "this category belongs to no repository"})
		return
	}
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
	if res.rt.State(ctx) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "your workspace must be running to read the repository's settings"})
		return
	}
	file, aerr := a.mgr.agentAutorunFile(ctx, res, c.Repo)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	out := memoRepoSettingsWire{CategoryID: c.ID, Repo: c.Repo, Category: c.Name, Path: autorunfile.Path,
		Branch: file.Branch, Commit: file.Commit, Found: file.Found, Queue: hasQueue,
		Errors: []autorunfile.Issue{}, Warnings: []autorunfile.Issue{}, Diffs: []memoRepoDiff{}}
	if !file.Found {
		writeJSON(w, http.StatusOK, out)
		return
	}
	parsed := autorunfile.Parse([]byte(file.Content))
	out.Errors = append(out.Errors, parsed.Errors...)
	e := parsed.Categories[c.Name]
	out.Entry = e != nil
	if e != nil {
		out.Errors = append(out.Errors, e.Errors...)
		out.Warnings = append(out.Warnings, e.Warnings...)
		if v, ok := e.Values["instruction_file"]; ok {
			out.Warnings = append(out.Warnings, autorunfile.Issue{Line: v.Line, Key: "instruction_file",
				Reason: "instruction_file is not read yet; the instruction is left as it is"})
		}
	}
	if len(out.Errors) == 0 && e != nil {
		diffs, errs := memoRepoCompare(cur, hasQueue, e)
		out.Errors = append(out.Errors, errs...)
		if len(errs) == 0 {
			out.Diffs = diffs
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// agentAutorunFile asks the workspace Agent for the settings file. A refusal about the file
// itself (too large, not a regular file) keeps the Agent's message; anything else is reported by
// code only, because a failed fetch's message carries git's own output.
func (m *manager) agentAutorunFile(ctx context.Context, res *resolved, repo string) (agentAutorunFile, *apiError) {
	var out agentAutorunFile
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
		return out, &apiError{http.StatusBadGateway, errCodeMemoQueueRepoUnreadable, "the workspace did not answer"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*autorunfile.MaxBytes+4096))
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(body, &out); err != nil {
			return out, &apiError{http.StatusBadGateway, errCodeMemoQueueRepoUnreadable, "the workspace's answer could not be read"}
		}
		return out, nil
	case resp.StatusCode == http.StatusNotFound && agentErrCode(body) == "not_found":
		return out, &apiError{http.StatusNotFound, errCodeMemoQueueRepoUnreadable, "the workspace has no git working copy named " + repo}
	case resp.StatusCode == http.StatusUnprocessableEntity:
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return out, &apiError{http.StatusUnprocessableEntity, errCodeMemoQueueRepoUnreadable, e.Error.Message}
	}
	code := agentErrCode(body)
	if code == "" {
		code = resp.Status
	}
	return out, &apiError{http.StatusBadGateway, errCodeMemoQueueRepoUnreadable, "the workspace could not read the file from origin's default branch (" + code + ")"}
}
