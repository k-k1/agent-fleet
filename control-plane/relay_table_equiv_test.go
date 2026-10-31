// relay_table_equiv_test.go proves that the relay table (ADR 0124 Phase 2) classifies and signs
// exactly as the hand-kept code it replaced: auditActionTarget's switch in proxy.go and the two
// route maps in member_origin.go. Both are frozen here verbatim and compared, route by route, with
// what the production code derives today.
//
// Every route of testdata/routes.golden is served through a mux registered with the same patterns,
// so the request carries the pattern and path values the real mux gives it. Each wildcard is then
// also filled with every literal the old switch matched a suffix on, because the old switch read
// the path while the table reads the pattern: a value that spelled one of those words is where the
// two could part.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// frozenAuditActionTarget is auditActionTarget as it stood before the relay table, kept verbatim
// as the reference the derived classification is compared with. It classifies a proxied request as an auditable CHANGE operation
// (M1 audit, docs/log/20 §A / §E.1: file, git, session mutations). Read-only and other
// non-mutating calls return ok=false. Target is taken from the URL only (path value
// or query), never the request body — so no secrets are read (docs §A.6).
func frozenAuditActionTarget(r *http.Request) (action, target string, ok bool) {
	p := r.URL.Path
	q := r.URL.Query()
	name := r.PathValue("name") // repo / session name for {name} routes ("" otherwise)
	switch r.Method {
	case http.MethodPut:
		if p == "/api/fs/file" {
			target, ok := r.Context().Value(fsPutAuditTargetContextKey{}).(string)
			return "fs.file.put", target, ok && target != ""
		}
	case http.MethodPost:
		switch {
		case p == "/api/fs/upload":
			return "fs.upload", q.Get("path"), true
		case p == "/api/fs/mkdir":
			return "fs.mkdir", q.Get("path"), true
		case p == "/api/fs/newfile":
			return "fs.newfile", q.Get("path"), true
		case p == "/api/fs/rename":
			return "fs.rename", q.Get("from") + " → " + q.Get("to"), true
		case p == "/api/repos":
			return "repo.clone", "", true
		case p == "/api/repos/svn":
			return "repo.svn.checkout", "", true
		case name != "" && strings.HasSuffix(p, "/svn-update"):
			return "repo.svn.update", name, true
		case name != "" && strings.HasSuffix(p, "/svn-cleanup"):
			return "repo.svn.cleanup", name, true
		case name != "" && strings.HasSuffix(p, "/svn-auth"):
			// Storing a credential for this working copy's server. The target is the
			// working copy; the credential itself is in the body, which is never read.
			return "repo.svn.auth", name, true
		case name != "" && strings.HasSuffix(p, "/commit"):
			return "git.commit", name, true
		case name != "" && strings.HasSuffix(p, "/discard"):
			return "git.discard", name, true
		case name != "" && strings.HasSuffix(p, "/checkout"):
			return "git.checkout", name, true
		case name != "" && strings.HasSuffix(p, "/fetch"):
			return "git.fetch", name, true
		case name != "" && strings.HasSuffix(p, "/ff"):
			return "git.ff", name, true
		case name != "" && strings.HasSuffix(p, "/parent-ff"):
			return "git.parent_ff", name, true
		case p == "/api/agents/memory/snapshots":
			// Manual snapshot of the agent memory (docs/log/39).
			return "memory.snapshot", "", true
		case p == "/api/agents/memory/import":
			// Receipt into refs/imports; live is not touched yet.
			return "memory.import", "", true
		case p == "/api/agents/memory/import/apply":
			// Applying an imported tree to live; which lineage comes from the URL hint.
			return "memory.import.apply", q.Get("importId"), true
		case p == "/api/agents/memory/restore":
			// Rollback (docs/log/39 stage 4). The Console repeats the source rev in the
			// query as an audit hint, because the body is never read (§A.6). The body's
			// rev/at/scope is what actually governs, and what happened is recorded in the
			// repo's restore commit (AF-Restore-Rev / -Scope).
			return "memory.restore", q.Get("rev"), true
		case p == "/api/agents/memory/entries/revert":
			// ADR 0108: undoing or forgetting one AF memory change. Like restore, the Console
			// repeats the commit in the query as the audit hint; the body governs.
			// Only a commit id is copied into the ledger: the hint is free text the Agent never
			// reads, so anything else is recorded as an empty target.
			return "memory.entry.revert", auditCommitHint(q.Get("commit")), true
		case p == "/api/agents/memory-review/run":
			// ADR 0117: the member starts a review of one session now. The Console repeats the
			// session name in the query; only a name-shaped value is recorded, never a body.
			return "memory.review.run", auditSessionNameHint(q.Get("session")), true
		case p == "/api/agents/memory-review/proposals/approve":
			// ADR 0117: the member publishes one proposed memory. The Console repeats the proposal
			// id in the query; only an id-shaped value is recorded, never text.
			return "memory.review.approve", auditReviewIDHint(q.Get("id")), true
		case p == "/api/agents/memory-review/proposals/reject":
			return "memory.review.reject", auditReviewIDHint(q.Get("id")), true
		case p == "/api/agents/memory-review/proposals/reject-all":
			// The hint is the run whose proposals are rejected, empty for all of them.
			return "memory.review.reject_all", auditReviewIDHint(q.Get("run")), true
		case p == "/api/agents/memory-review/runs/revert":
			return "memory.review.revert_run", auditReviewIDHint(q.Get("run")), true
		case p == "/api/agents/memory/entries/pin":
			// #1703: the member pins or unpins a memory. The Console repeats the project id in the query;
			// only an id-shaped value is recorded, never the free-text memory name.
			return "memory.entry.pin", auditProjectHint(q.Get("project")), true
		case p == "/api/agents/memory/entries/member-save":
			// #1997: the member writes a memory by hand. Same id-shaped project hint as the pin;
			// the memory name is free text and never copied into the ledger.
			return "memory.entry.save", auditProjectHint(q.Get("project")), true
		case p == "/api/agents/memory/entries/purge":
			// #1982: the member rewrites the memory history to drop one memory's past revisions.
			// Same id-shaped project hint as the save; the memory name is never copied.
			return "memory.entry.purge", auditProjectHint(q.Get("project")), true
		case p == "/api/agents/memory/entries/project-move":
			// #1984: the member moves an orphaned project's memories into another project. The
			// Console repeats the source project id in the query; only an id-shaped value is
			// recorded, never a memory name.
			return "memory.project.move", auditProjectHint(q.Get("project")), true
		case p == "/api/agents/memory/claude-import":
			// ADR 0108 decision 6: the one-time import of claude's memory writes the store. The
			// Console repeats the project id in the query; only an id-shaped value is recorded.
			return "memory.claude_import", auditProjectHint(q.Get("project")), true
		case p == "/api/agents/memory/af-import":
			// #1983: the member imports AF memory out of a bundle into this workspace's store. The
			// Console repeats the import id in the query; only an id-shaped value is recorded.
			return "memory.af_import", auditImportHint(q.Get("importId")), true
		case p == "/api/agents/memory/auto-sync":
			// #1972: the Agent memory switch imports / writes back by itself, so nothing else
			// passes through here for those runs (the Agent has no channel to this ledger; each
			// memory change is a commit by the member in the 0022 history). What does pass is
			// the member's part: the Console wakes the run once the confirmation flipped the
			// switch, retries a failed write-back, or gives it up. The Console repeats the action
			// (and the direction of a wake) in the query; only those words are recorded.
			return "memory.auto_sync", auditAutoSyncHint(q.Get("action"), q.Get("to")), true
		case p == "/api/agents/memory/muse-import":
			// #1991: the one-time import of muse's project memory writes the store; the project id
			// in the query is the only value recorded, as for the claude import.
			return "memory.muse_import", auditProjectHint(q.Get("project")), true
		case p == "/api/agents/memory/claude-export":
			// #1914: the write-back changes claude's store. The Console repeats the project id in
			// the query; only an id-shaped value is recorded.
			return "memory.claude_export", auditProjectHint(q.Get("project")), true
		case strings.HasPrefix(p, "/api/aws-login/profiles/") && strings.HasSuffix(p, "/logout"):
			return "aws.logout", "profile: " + name, true
		case strings.HasPrefix(p, "/api/aws-login/profiles/") && strings.HasSuffix(p, "/start"):
			// #1028: the Settings row's press names the profile in the path, and there is no
			// request. Matched before the request form, which would take "profiles" for an id.
			return "aws.login.start", "profile: " + name, true
		case strings.HasPrefix(p, "/api/aws-login/") && strings.HasSuffix(p, "/start"):
			// ADR 0102. The request id is from the URL; the profile is only the Console's
			// hint in the query (the Agent decides from the id alone), so it is labelled one.
			// The code never passes through here.
			return "aws.login.start", awsLoginAuditTarget(p, q), true
		case strings.HasPrefix(p, "/api/aws-login/") && strings.HasSuffix(p, "/cancel"):
			return "aws.login.cancel", awsLoginAuditTarget(p, q), true
		case strings.HasPrefix(p, "/api/gcp-login/"):
			// ADR 0107 decision 3: the press, the cancel and the code, never the code itself.
			return gcpLoginAudit(r, p, q)
		case p == "/api/sessions":
			return "session.create", "", true
		case name != "" && strings.HasSuffix(p, "/fork"):
			return "session.fork", name, true
		case name != "" && p == "/api/sessions/"+name+"/move":
			return "session.move", name, true
		case name != "" && strings.HasPrefix(p, "/api/sessions/"+name+"/move/"):
			// retry, cancel, reopen and deliver (ADR 0115 decisions 1, 11.4 and 11.6); the preview
			// is a GET.
			return "session.move." + strings.TrimPrefix(p, "/api/sessions/"+name+"/move/"), name, true
		case name != "" && strings.HasSuffix(p, "/stop"):
			// /stop is the old name of deleting a session (it moves it to the trash, ADR 0101),
			// so it is recorded as the delete it is.
			return "session.delete", name, true
		}
	case http.MethodGet:
		// Reads are not audited, with one exception: memory export is the only path that
		// carries someone's personal memory out of the environment, and without a record
		// of who exported what format when there is nothing to trace afterwards
		// (docs/log/39 stage 4). The target is the format only — no body, no content.
		if p == "/api/agents/memory/export" {
			return "memory.export", q.Get("format"), true
		}
	case http.MethodDelete:
		switch {
		case p == "/api/fs/delete":
			return "fs.delete", q.Get("path"), true
		case strings.HasPrefix(p, "/api/repo-jobs/"):
			// Cancel / dismiss an import job (docs/log/78). The target is the job id,
			// taken from the URL alone.
			return "repo.job.cancel", strings.TrimPrefix(p, "/api/repo-jobs/"), true
		case name != "" && p == "/api/repos/"+name:
			return "repo.delete", name, true
		case name != "" && p == "/api/sessions/"+name:
			// Deleting a session (to the trash, ADR 0101). Before, the Console deleted through
			// /stop, which was audited; moving it to DELETE must not drop it from the log.
			return "session.delete", name, true
		}
	}
	return "", "", false
}

// The old member_origin.go maps, frozen.
var frozenMemberOriginRoutes = map[string]bool{
	// Every settings write is signed, because some settings are the member's alone: turning the
	// memory review on (ADR 0117) sends other sessions' conversations to a model provider, and a
	// session holds the same AGENT_TOKEN the relay uses. The Agent asks for the mark only on a
	// write that arms such a setting (workspace/agent/ui_prefs.go); the rest ignore it.
	"PUT /api/env/ui-prefs": true,
	// "Review now" for one session (ADR 0117): it spends tokens and sends an excerpt to a model
	// provider, so a session's own AGENT_TOKEN must not be able to start it.
	"POST /api/agents/memory-review/run":           true,
	"POST /api/agents/memory/entries/pin":          true,
	"POST /api/agents/memory/entries/revert":       true,
	"POST /api/agents/memory/entries/member-save":  true,
	"POST /api/agents/memory/entries/purge":        true,
	"POST /api/agents/memory/entries/project-move": true,
	// The member's decisions on what the review proposed (ADR 0117 decision 6): approving
	// publishes into every kind's memory, and the list returns text nobody has published, so a
	// session's own AGENT_TOKEN must not be able to do either.
	"POST /api/agents/memory-review/proposals/list":       true,
	"POST /api/agents/memory-review/proposals/approve":    true,
	"POST /api/agents/memory-review/proposals/reject":     true,
	"POST /api/agents/memory-review/proposals/reject-all": true,
	"POST /api/agents/memory-review/runs/revert":          true,
	// Importing AF memory out of a bundle (#1983).
	"POST /api/agents/memory/af-import": true,
	// The member's part in what the Agent memory switch does by itself with claude's native
	// memory (#1972): waking the run after the confirmation, retrying, giving up.
	"POST /api/agents/memory/auto-sync": true,
	// The 0022 history routes only the Console calls (ADR 0113, 2026-10-13 note): a manual
	// snapshot and the apply step of a bundle import.
	"POST /api/agents/memory/snapshots":    true,
	"POST /api/agents/memory/import/apply": true,
	// A multipart upload: signed by body hash (memberOriginHashedRoutes), not buffered.
	"POST /api/agents/memory/import": true,
}

var frozenMemberOriginHashedRoutes = map[string]bool{
	"POST /api/agents/memory/import": true,
}

// auditSweepWords are the path literals the frozen switch matched a suffix or a whole path on. A
// wildcard filled with one of them is where reading the path and reading the pattern could part.
var auditSweepWords = []string{
	"x1", "svn-update", "svn-cleanup", "svn-auth", "commit", "discard", "checkout", "fetch",
	"ff", "parent-ff", "fork", "move", "retry", "cancel", "reopen", "deliver", "preview", "stop",
	"start", "logout", "code", "profiles", "sessions", "repos", "export",
}

// auditSweepQuery carries every query hint the frozen switch reads, each in a shape its filter
// keeps, so a target that drops or renames a hint shows up as a difference.
var auditSweepQuery = url.Values{
	"path": {"repos/app/a.txt"}, "from": {"a"}, "to": {"on"}, "rev": {"deadbeef"},
	"commit": {"abc1234"}, "session": {"s_1"}, "id": {"0123456789abcdef0123"},
	"run": {"0123456789abcdef0123"}, "project": {"agent-fleet-0123456789ab"},
	"importId": {"20260727T010203Z"}, "action": {"wake"}, "format": {"bundle"},
	"profile": {"prod"}, "force": {"1"},
}.Encode()

// goldenPattern turns a routes.golden line back into the pattern it was registered with.
func goldenPattern(line string) string {
	if p, ok := strings.CutPrefix(line, "ANY "); ok {
		return p
	}
	return line
}

// sweepPaths fills pattern's wildcards with x1 (a {...} wildcard with a/b), then once more per
// wildcard with each sweep word.
func sweepPaths(pattern string) []string {
	_, path, _ := strings.Cut(pattern, " ")
	if strings.HasPrefix(pattern, "/") {
		path = pattern
	}
	fill := func(only int, word string) string {
		i := -1
		return wildcardRE.ReplaceAllStringFunc(path, func(w string) string {
			i++
			name := strings.Trim(w, "{}")
			switch {
			case name == "$":
				return ""
			case i == only:
				return word
			case strings.HasSuffix(name, "..."):
				return "a/b"
			}
			return "x1"
		})
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(fill(-1, ""))
	for i := range wildcardRE.FindAllString(path, -1) {
		for _, w := range auditSweepWords {
			add(fill(i, w))
		}
	}
	return out
}

// classified is one request's outcome under one classifier.
type classified struct {
	action, target string
	ok             bool
}

// TestRelayAuditMatchesFrozenSwitch — for every route of the golden, every method it answers
// (HEAD too, which a GET pattern serves), with and without the query hints and with and without
// the PUT /api/fs/file target, auditActionTarget gives what the frozen switch gives. It runs both
// on the request the mux served, and on a copy that carries no pattern, the shape the unit tests
// hand in.
func TestRelayAuditMatchesFrozenSwitch(t *testing.T) {
	type outcome struct{ got, want classified }
	results := map[string]outcome{}
	var key, sweeping string
	mux := http.NewServeMux()
	lines := readGoldenLines(t, routesGoldenPath)
	for _, line := range lines {
		mux.HandleFunc(goldenPattern(line), func(_ http.ResponseWriter, r *http.Request) {
			// A filled path another pattern serves (the static catch-all, mostly) is not this
			// route's request, and never reaches the relay through it.
			if r.Pattern != sweeping {
				return
			}
			for i, req := range []*http.Request{r, withoutPattern(r)} {
				var o outcome
				o.got.action, o.got.target, o.got.ok = auditActionTarget(req)
				o.want.action, o.want.target, o.want.ok = frozenAuditActionTarget(req)
				results[key+[]string{"", " (no pattern)"}[i]] = o
			}
		})
	}

	audited := map[string]bool{}
	for _, line := range lines {
		method, _, _ := strings.Cut(line, " ")
		methods := []string{method}
		switch method {
		case "ANY":
			methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}
		case http.MethodGet:
			methods = append(methods, http.MethodHead)
		}
		sweeping = goldenPattern(line)
		for _, path := range sweepPaths(goldenPattern(line)) {
			for _, m := range methods {
				for _, q := range []string{"", "?" + auditSweepQuery} {
					for _, fsPut := range []string{"", "repos/app/a.txt"} {
						req := httptest.NewRequest(m, path+q, nil)
						if fsPut != "" {
							req = req.WithContext(context.WithValue(req.Context(), fsPutAuditTargetContextKey{}, fsPut))
						}
						key = line + " <- " + m + " " + path + q + " fsput=" + fsPut
						mux.ServeHTTP(httptest.NewRecorder(), req)
					}
				}
			}
		}
	}
	var diffs []string
	for k, o := range results {
		if route, _, _ := strings.Cut(k, " <- "); auditedAfterTheFreeze[route] {
			continue
		}
		if o.got != o.want {
			diffs = append(diffs, k+": derived "+fmtClassified(o.got)+", frozen "+fmtClassified(o.want))
		}
		if o.want.ok {
			route, _, _ := strings.Cut(k, " <- ")
			audited[route] = true
		}
	}
	sort.Strings(diffs)
	for _, d := range diffs {
		t.Error(d)
	}
	// The frozen switch audits these many routes today. Fewer means the sweep stopped reaching
	// them, and the comparison above would then pass over nothing.
	if len(audited) < minFrozenAuditedRoutes {
		t.Errorf("the frozen switch audited %d golden routes, want at least %d: the sweep is not reaching them", len(audited), minFrozenAuditedRoutes)
	}
	t.Logf("%d requests compared, %d golden routes audited", len(results), len(audited))
}

// auditedAfterTheFreeze are golden routes the relay table audits that did not exist when the
// switch above was frozen, so it cannot classify them. TestAuditActionTarget pins what each is
// audited as instead. Only a new route belongs here, never one the frozen switch knows.
var auditedAfterTheFreeze = map[string]bool{
	"POST /api/aws-confirm/{id}/approve": true, // ADR 0126 decision 5
	"POST /api/aws-confirm/{id}/deny":    true,
	// #2883
	"POST /api/sessions/{name}/archive":       true,
	"POST /api/sessions/{name}/restore":       true,
	"POST /api/sessions/{name}/recreate":      true,
	"POST /api/sessions/{name}/start":         true,
	"POST /api/sessions/{name}/rename-branch": true,
	"DELETE /api/cleanup/archives":            true,
	"DELETE /api/cleanup/archives/{id}":       true,
	"POST /api/cleanup/archives/{id}/restore": true,
	"POST /api/repos/init":                    true,
	"POST /api/repos/{name}/recreate":         true,
	"POST /api/repos/{name}/stage":            true,
	"POST /api/repos/{name}/unstage":          true,
	"DELETE /api/repos/{name}/branch":         true,
	"POST /api/repos/{name}/gitflow/init":     true,
	"POST /api/repos/{name}/mcp/apply":        true,
}

// minFrozenAuditedRoutes is how many golden routes the frozen switch audits (57 when it was frozen).
const minFrozenAuditedRoutes = 57

// withoutPattern is r as a test builds it by hand: the path values set, no pattern.
func withoutPattern(r *http.Request) *http.Request {
	c := r.Clone(r.Context())
	c.Pattern = ""
	return c
}

func fmtClassified(c classified) string {
	if !c.ok {
		return "(not audited " + c.action + "|" + c.target + ")"
	}
	return c.action + "|" + c.target
}

// TestMemberOriginSigningMatchesFrozenMaps — the signed and hashed routes are the frozen maps',
// and so is the answer for every golden route.
func TestMemberOriginSigningMatchesFrozenMaps(t *testing.T) {
	if !equalRouteSets(memberOriginRoutes, frozenMemberOriginRoutes) {
		t.Errorf("signed routes differ:\nderived %v\nfrozen  %v", sortedTrue(memberOriginRoutes), sortedTrue(frozenMemberOriginRoutes))
	}
	if !equalRouteSets(memberOriginHashedRoutes, frozenMemberOriginHashedRoutes) {
		t.Errorf("hashed routes differ:\nderived %v\nfrozen  %v", sortedTrue(memberOriginHashedRoutes), sortedTrue(frozenMemberOriginHashedRoutes))
	}
	signed := 0
	for _, line := range readGoldenLines(t, routesGoldenPath) {
		method, _, _ := strings.Cut(line, " ")
		if method == "ANY" {
			continue
		}
		for _, path := range sweepPaths(line) {
			r := httptest.NewRequest(method, path, nil)
			key := r.Method + " " + r.URL.Path
			if got, want := memberOriginRoute(r), frozenMemberOriginRoutes[key]; got != want {
				t.Errorf("%s: signed %v, frozen %v", key, got, want)
			}
			if got, want := memberOriginHashedRoute(r), frozenMemberOriginHashedRoutes[key]; got != want {
				t.Errorf("%s: hashed %v, frozen %v", key, got, want)
			}
			if frozenMemberOriginRoutes[key] {
				signed++
			}
		}
	}
	if signed != len(frozenMemberOriginRoutes) {
		t.Errorf("%d golden routes are signed by the frozen map, which has %d: a signed route is missing from the golden", signed, len(frozenMemberOriginRoutes))
	}
}

func equalRouteSets(a, b map[string]bool) bool {
	return strings.Join(sortedTrue(a), "\n") == strings.Join(sortedTrue(b), "\n")
}

func sortedTrue(m map[string]bool) []string {
	var out []string
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
