package main

// relays.go — the CP's relays to the Workspace Agent as one table (ADR 0124 Phase 2).
//
// A relay is one row in a relays_<domain>.go file: the route, whether a success is audited and
// with what target, and whether the CP signs it as the member's (member_origin.go). The mux
// registration, auditActionTarget and the signed-route maps are all derived from the rows, so a
// new relay is one line in one place.
//
// Relays that need a wrapper (restLoginFlow, the held stream proxy, a CP handler that forwards
// after its own checks) are registered by hand in routes.go; their row is marked wrapped and only
// declares the audit and the signing.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// relaySigning is whether the CP marks a relayed request as the member's (ADR 0113).
type relaySigning uint8

const (
	unsigned relaySigning = iota
	// signBody buffers the body and signs it.
	signBody
	// signHash spools a multipart upload too large to buffer and signs its hash.
	signHash
)

// relayAudit classifies a relayed request as an auditable change (M1 audit, docs/log/20 §A /
// §E.1). ok=false records nothing. The target is taken from the URL only (path value or query),
// never the request body, so no secret is read (docs §A.6).
type relayAudit func(r *http.Request) (action, target string, ok bool)

// relayOutcome reports from a 2xx answer's body whether the call did the change its row audits.
// An answer it cannot read, or one larger than the proxy buffers, counts as no.
type relayOutcome func(body []byte) bool

// agentRelay is one CP route forwarded to the Workspace Agent with /api stripped.
type agentRelay struct {
	route string     // "METHOD /api/path", exactly as registered and as in testdata/routes.golden
	audit relayAudit // nil: a success is not audited
	// auditIf, when set, holds the audit until the Agent's 2xx answer says the call changed
	// something: a sign-in step is polled until it is done, and a pending answer is a 200 too.
	auditIf relayOutcome
	sign    relaySigning // the method and path must be literal: signing looks the request path up
	wrapped bool         // registered by hand in routes.go; the row only declares audit and signing
}

// relayTables is every relay table. A table missing here is neither registered nor consulted.
var relayTables = [][]agentRelay{
	sessionRelays, chatRelays, assistantRelays, imagegenRelays, memoRelays, repoRelays,
	settingsRelays, memoryRelays, connectionRelays,
}

// registerAgentRelays registers every row that is not wrapped on the plain relay.
func registerAgentRelays(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	for _, table := range relayTables {
		for _, rl := range table {
			if !rl.wrapped {
				mux.HandleFunc(rl.route, rest)
			}
		}
	}
}

// relayIndex is the rows by route. relayMatcher holds the same patterns, to find the row of a
// request that no mux has served (a unit test's hand-built request carries no Pattern).
var relayIndex, relayMatcher = indexRelays()

func indexRelays() (map[string]agentRelay, *http.ServeMux) {
	index := map[string]agentRelay{}
	matcher := http.NewServeMux()
	for _, table := range relayTables {
		for _, rl := range table {
			if _, dup := index[rl.route]; dup {
				panic("relay listed twice: " + rl.route)
			}
			index[rl.route] = rl
			matcher.HandleFunc(rl.route, func(http.ResponseWriter, *http.Request) {})
		}
	}
	return index, matcher
}

// relayFor is the row r was routed to. The method must match the row's own: a GET pattern also
// serves HEAD, which no row declares.
func relayFor(r *http.Request) (agentRelay, bool) {
	pattern := r.Pattern
	if pattern == "" {
		_, pattern = relayMatcher.Handler(r)
	}
	rl, ok := relayIndex[pattern]
	if !ok {
		return agentRelay{}, false
	}
	method, _, _ := strings.Cut(rl.route, " ")
	return rl, method == r.Method
}

// auditActionTarget classifies a proxied request as an auditable CHANGE operation by its relay
// row. Read-only and other non-mutating calls return ok=false.
func auditActionTarget(r *http.Request) (action, target string, ok bool) {
	rl, found := relayFor(r)
	if !found || rl.audit == nil {
		return "", "", false
	}
	return rl.audit(r)
}

// auditGated reports whether r's row holds its audit until the answer says it stored something.
func auditGated(r *http.Request) relayOutcome {
	rl, found := relayFor(r)
	if !found {
		return nil
	}
	return rl.auditIf
}

// signedRelayRoutes are the routes signed as the member's, and the hashed subset of them, keyed
// by "METHOD path" as memberOriginRoute looks a request up.
func signedRelayRoutes() (signed, hashed map[string]bool) {
	signed, hashed = map[string]bool{}, map[string]bool{}
	for _, table := range relayTables {
		for _, rl := range table {
			switch rl.sign {
			case signHash:
				hashed[rl.route] = true
				signed[rl.route] = true
			case signBody:
				signed[rl.route] = true
			}
		}
	}
	return signed, hashed
}

// --- audit targets ---------------------------------------------------------------

// auditAs records action with the target target gives (none when nil).
func auditAs(action string, target func(*http.Request) string) relayAudit {
	return func(r *http.Request) (string, string, bool) {
		if target == nil {
			return action, "", true
		}
		return action, target(r), true
	}
}

// auditByName records action with the {name} path value (a repository or session) as the
// target, and nothing when the request carries no name.
func auditByName(action string) relayAudit {
	return func(r *http.Request) (string, string, bool) {
		name := r.PathValue("name")
		return action, name, name != ""
	}
}

// query is the target taken verbatim from one query parameter.
func query(key string) func(*http.Request) string {
	return func(r *http.Request) string { return r.URL.Query().Get(key) }
}

// queryShaped is a query hint the Console repeats for the audit, kept only when keep accepts its
// shape: the Agent never reads it, so anything else is recorded as an empty target.
func queryShaped(key string, keep func(string) string) func(*http.Request) string {
	return func(r *http.Request) string { return keep(r.URL.Query().Get(key)) }
}

// auditConnection records a credential write under the connection's name; target (nil: none)
// names what the write is about, from the URL only.
func auditConnection(kind, verb string, target func(*http.Request) string) relayAudit {
	return auditAs("connection."+kind+"."+verb, target)
}

var (
	// auditHostRe is a git host with an optional port: no userinfo, path or query can pass.
	auditHostRe = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`)
	// auditEnvNameRe is the Agent's own shape for a provider key's environment variable name.
	auditEnvNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

// pathShaped is one path value, kept only when it matches re: anything else is recorded as an
// empty target, never as given.
func pathShaped(key string, re *regexp.Regexp) func(*http.Request) string {
	return func(r *http.Request) string {
		if v := r.PathValue(key); re.MatchString(v) {
			return v
		}
		return ""
	}
}

// auditMCPServer records a change to an MCP server registered under the id in the URL (the
// body carries env and header values, which are never read).
func auditMCPServer(verb string) relayAudit {
	return auditAs("mcp.server."+verb, pathShaped("id", mcpServerIDRe))
}

// storedConnected reads a sign-in step's answer: the credential is stored once it says
// {"connected": true}. A pending, slow-down or failed poll says false, and writes no row.
func storedConnected(body []byte) bool {
	var out struct {
		Connected bool `json:"connected"`
	}
	return json.Unmarshal(body, &out) == nil && out.Connected
}

// auditFSFilePut records a file save under the path fsFilePut checked and put in the context;
// without one nothing is recorded.
func auditFSFilePut(r *http.Request) (string, string, bool) {
	target, ok := r.Context().Value(fsPutAuditTargetContextKey{}).(string)
	return "fs.file.put", target, ok && target != ""
}

func auditFSRename(r *http.Request) (string, string, bool) {
	q := r.URL.Query()
	return "fs.rename", q.Get("from") + " → " + q.Get("to"), true
}

// auditRepoJobCancel names the import job by the id in the URL alone.
func auditRepoJobCancel(r *http.Request) (string, string, bool) {
	return "repo.job.cancel", strings.TrimPrefix(r.URL.Path, "/api/repo-jobs/"), true
}

// auditAutoSync records the member's part in what the Agent memory switch does by itself: the
// action, and the direction of a wake.
func auditAutoSync(r *http.Request) (string, string, bool) {
	q := r.URL.Query()
	return "memory.auto_sync", auditAutoSyncHint(q.Get("action"), q.Get("to")), true
}

// auditAWSLogin classifies the af-aws-exec Console login by its path, as gcpLoginAudit does for
// Google Cloud: a profile row's press or log-out names the profile, a request's press or cancel
// names the request id with the Console's profile hint. The code never passes through here.
func auditAWSLogin(r *http.Request) (string, string, bool) {
	p := r.URL.Path
	name := r.PathValue("name")
	switch {
	case strings.HasPrefix(p, "/api/aws-login/profiles/") && strings.HasSuffix(p, "/logout"):
		return "aws.logout", "profile: " + name, true
	case strings.HasPrefix(p, "/api/aws-login/profiles/") && strings.HasSuffix(p, "/start"):
		// Matched before the request form, which would take "profiles" for an id.
		return "aws.login.start", "profile: " + name, true
	case strings.HasPrefix(p, "/api/aws-login/") && strings.HasSuffix(p, "/start"):
		return "aws.login.start", awsLoginAuditTarget(p, r.URL.Query()), true
	case strings.HasPrefix(p, "/api/aws-login/") && strings.HasSuffix(p, "/cancel"):
		return "aws.login.cancel", awsLoginAuditTarget(p, r.URL.Query()), true
	}
	return "", "", false
}

// auditAWSConfirm records the member's answer to a "confirm before use" request (ADR 0126
// decision 5). The id is from the path; the profile and the session are only the Console's
// hints (the Agent decides from the id alone), so they are labelled as such.
func auditAWSConfirm(r *http.Request) (string, string, bool) {
	p, q := r.URL.Path, r.URL.Query()
	id, verb, _ := strings.Cut(strings.TrimPrefix(p, "/api/aws-confirm/"), "/")
	switch verb {
	case "approve", "deny":
		return "aws.use." + verb, id + " (profile hint: " + q.Get("profile") + ", session hint: " + q.Get("session") + ")", true
	}
	return "", "", false
}

// awsLoginAuditTarget names an AWS login request by its id, with the profile hint the
// Console sends.
func awsLoginAuditTarget(p string, q url.Values) string {
	id, _, _ := strings.Cut(strings.TrimPrefix(p, "/api/aws-login/"), "/")
	return id + " (profile hint: " + q.Get("profile") + ")"
}

// auditGCPLogin is gcpLoginAudit (ADR 0107 decision 3) on the request's own path and query.
func auditGCPLogin(r *http.Request) (string, string, bool) {
	return gcpLoginAudit(r, r.URL.Path, r.URL.Query())
}

var (
	auditArchiveIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)
	auditBranchRe    = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,200}$`)
	auditDaysRe      = regexp.MustCompile(`^[0-9]{1,5}$`)
)

// auditArchiveByID records a cleanup-archive operation under the archive id in the URL; an id of
// an unexpected shape is recorded as an empty target, never as given.
func auditArchiveByID(action string) relayAudit {
	return func(r *http.Request) (string, string, bool) {
		id := r.PathValue("id")
		if !auditArchiveIDRe.MatchString(id) {
			id = ""
		}
		return action, id, true
	}
}

// auditArchivePurgeOld records the bulk purge by the age threshold alone (digits only).
func auditArchivePurgeOld(r *http.Request) (string, string, bool) {
	days := r.URL.Query().Get("older_than_days")
	if !auditDaysRe.MatchString(days) {
		return "cleanup.archive.purge_old", "", true
	}
	return "cleanup.archive.purge_old", "older_than_days: " + days, true
}

// auditRepoBranchDelete records a branch removal as "<repo>: <branch>"; the branch comes from the
// query and is kept only when it is ref-shaped.
func auditRepoBranchDelete(r *http.Request) (string, string, bool) {
	name := r.PathValue("name")
	if name == "" {
		return "", "", false
	}
	q := r.URL.Query()
	target := name
	if b := q.Get("branch"); auditBranchRe.MatchString(b) {
		target = name + ": " + b
	}
	// The Agent also deletes the branch on origin for these two spellings, a far wider reach.
	if rm := q.Get("remote"); rm == "1" || rm == "true" {
		target += " (remote)"
	}
	return "repo.branch.delete", target, true
}

// auditSessionStart records a relaunch; fresh=1 starts a new conversation and sets the old one aside.
func auditSessionStart(r *http.Request) (string, string, bool) {
	name := r.PathValue("name")
	if r.URL.Query().Get("fresh") == "1" {
		name += " (fresh)"
	}
	return "session.start", name, r.PathValue("name") != ""
}
