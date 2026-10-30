//go:build e2e

// Multi-member L2 harness: one CP with AUTH=proxy, a tenant of its own, one client per
// identity and one workspace container per member, all through the public API.
//
// AUTH=proxy takes the caller's identity from X-Forwarded-Email, so a test acts as any
// member by setting that header; SUPER_ADMIN_EMAILS makes the harness's admin identity a
// super_admin, which creates the tenant and invites the members through the admin API.
// AF_PROVISION=invite keeps an unknown address out instead of dropping it into the
// default tenant. Every request a member sends carries X-AF-Tenant.
//
// Adding a multi-member scenario:
//
//	f := startMemberFleet(t)              // CP + tenant; the image check runs here
//	a, b := f.addMember("alice"), f.addMember("bob")
//	other := f.addTenant()                // a second tenant on the same CP
//	b2 := b.joinTenant(other)             // bob's membership there: same identity, own client
//	c := f.addMemberIn(other, "carol")    // a member of that tenant only
//	a.startWorkspace()                    // each member's container is its own
//	got := a.getJSON("/api/...")          // or a.postJSON / a.do for any status
//	a.shell(session, "git ...")           // run a command in a shell session, read its output
//
// A git remote both workspaces can reach comes from newGitRemote(t, dockerBridgeGateway(t)).
// Teardown (CP, every member's container and network, the data dir) is registered by
// startMemberFleet and addMember; a scenario only has to stop what it wants stopped as part
// of what it tests. Names are unique per test because the tenant slug is random and every
// container and network is scoped by it (af-ws-<slug>-<user key>).
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// emailDomain is reserved (RFC 2606), so no address the harness mints can belong to
// anybody.
const emailDomain = "e2e.test"

// memberFleet is one CP running AUTH=proxy with a tenant created for this test.
type memberFleet struct {
	t       *testing.T
	base    string
	dataDir string
	slug    string
	cp      *cpProcess
	admin   *member
	members []*member
}

// member is one membership's client: every request carries the identity's email header
// and the membership's tenant.
type member struct {
	f      *memberFleet
	t      *testing.T
	name   string
	email  string
	key    string // the user key the CP derived from the email (also the home dir name)
	tenant string // the slug sent as X-AF-Tenant; "" for the admin
	cmds   int    // shell() call counter, for unique output file names
}

// startMemberFleet starts the CP with AUTH=proxy and creates this test's tenant. Docker and
// the workspace image are required (a skip without them, a failure under E2E_REQUIRE=1).
// env adds KEY=VAL entries to the CP environment (a short reaper sweep, or cpReachableEnv).
func startMemberFleet(t *testing.T, env ...string) *memberFleet {
	t.Helper()
	image := envOr("WS_IMAGE", "agent-fleet/workspace:dev")
	requireDockerAndImage(t, image)
	return bootMemberFleet(t, image, env...)
}

// bootMemberFleet is startMemberFleet without the docker prerequisite: the CP and the
// tenant/membership setup run without docker, which is what lets the setup be tested on a
// host that has none (TestMemberFleetSetup). Starting a workspace still needs docker.
func bootMemberFleet(t *testing.T, image string, env ...string) *memberFleet {
	t.Helper()
	f := &memberFleet{t: t, slug: "e2e" + randHex(t, 4)}
	f.dataDir = newDataDir(t, image)
	f.admin = &member{f: f, t: t, name: "admin", email: "admin-" + f.slug + "@" + emailDomain}
	// Each member's Agent port is WS_AGENT_PORT plus the order its workspace was created
	// in, so the whole range has to be free.
	f.cp = startCP(t, image, f.dataDir, f.workspaceNames, append([]string{
		"AUTH=proxy",
		"AF_PROVISION=invite",
		"SUPER_ADMIN_EMAILS=" + f.admin.email,
		fmt.Sprintf("WS_AGENT_PORT=%d", freePortRange(t, 4)),
	}, env...)...)
	f.base = f.cp.base
	f.createTenant(f.slug)
	return f
}

func (f *memberFleet) createTenant(slug string) {
	f.t.Helper()
	created := f.admin.postJSON("/api/admin/tenants", map[string]any{"slug": slug, "name": "e2e " + slug}, 200)
	if created["slug"] != slug {
		f.t.Fatalf("tenant create returned slug %v, want %q", created["slug"], slug)
	}
}

// addTenant creates another tenant on the same CP and returns its slug. The slug extends the
// fleet's, so teardown's per-slug names stay unique to this test.
func (f *memberFleet) addTenant() string {
	f.t.Helper()
	slug := f.slug + "x" + randHex(f.t, 2)
	f.createTenant(slug)
	return slug
}

// workspaceNames lists the af-ws-/af-net- suffixes of every membership, for teardown.
func (f *memberFleet) workspaceNames() []string {
	out := make([]string, 0, len(f.members))
	for _, m := range f.members {
		out = append(out, m.tenant+"-"+m.key)
	}
	return out
}

// addMember invites name@e2e.test into the fleet's tenant as a plain member and prepares
// the directories its workspace will mount. The workspace itself is not started.
func (f *memberFleet) addMember(name string) *member {
	f.t.Helper()
	return f.addMemberIn(f.slug, name)
}

// addMemberIn is addMember for another tenant of the fleet (addTenant).
func (f *memberFleet) addMemberIn(slug, name string) *member {
	f.t.Helper()
	return f.invite(&member{f: f, t: f.t, name: name, email: name + "@" + emailDomain, tenant: slug})
}

// joinTenant invites m's identity into the tenant slug as a plain member too, and returns
// the client for that membership. Its workspace is a separate one, not started.
func (m *member) joinTenant(slug string) *member {
	m.t.Helper()
	return m.f.invite(&member{f: m.f, t: m.t, name: m.name + "@" + slug, email: m.email, tenant: slug})
}

func (f *memberFleet) invite(m *member) *member {
	f.t.Helper()
	m.key = f.inviteKey(m.tenant, m.email)
	// manager.workspaceNames: a non-default tenant's workspace lives at <WS_DATA>/<slug>/<key>.
	prepareWorkspaceDirs(f.t, filepath.Join(f.dataDir, m.tenant, m.key))
	f.members = append(f.members, m)
	return m
}

// inviteKey adds (or restores) the membership of email in slug and returns its user key.
func (f *memberFleet) inviteKey(slug, email string) string {
	f.t.Helper()
	got := f.admin.postJSON("/api/admin/memberships", map[string]any{
		"email": email, "tenant_slug": slug, "role": "member",
	}, 200)
	key, _ := got["user_key"].(string)
	if key == "" {
		f.t.Fatalf("add membership %s to %s: no user_key in %v", email, slug, got)
	}
	return key
}

// --- the admin's member lifecycle --------------------------------------------

// remove is the tenant admin's "remove member" (the logical delete: status inactive).
func (m *member) remove() {
	m.t.Helper()
	m.f.admin.call(http.MethodDelete, "/api/admin/memberships", map[string]any{"user_key": m.key, "tenant_slug": m.tenant}, 200)
}

// reinvite restores a removed membership the way the admin does, by inviting again.
func (m *member) reinvite() {
	m.t.Helper()
	if key := m.f.inviteKey(m.tenant, m.email); key != m.key {
		m.t.Fatalf("%s: re-invite returned user key %q, want %q", m.name, key, m.key)
	}
}

// destroyWorkspace destroys a removed member's workspace (DELETE /api/admin/workspaces).
func (m *member) destroyWorkspace() {
	m.t.Helper()
	code, b := m.f.admin.do(http.MethodDelete, "/api/admin/workspaces", map[string]any{"user_key": m.key, "tenant_slug": m.tenant})
	if code != 200 && code != 202 {
		m.t.Fatalf("%s: destroy workspace: %d %s", m.name, code, truncate(b))
	}
}

// deleteMembership is the hard delete of a removed member's row
// (DELETE /api/admin/tenants/{slug}/members/{key}); it answers with the status for the
// refusals a scenario checks.
func (m *member) deleteMembership() (int, map[string]any) {
	m.t.Helper()
	code, b := m.f.admin.do(http.MethodDelete, "/api/admin/tenants/"+m.tenant+"/members/"+m.key, nil)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return code, out
}

// --- a member's requests ---------------------------------------------------

// do sends one request as this member and returns the status and the whole body. It never
// fails the test, so it is the one to use inside waitFor conditions.
func (m *member) do(method, path string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, []byte(err.Error())
		}
		rd = bytes.NewReader(b)
	}
	req, err := m.request(method, path, rd)
	if err != nil {
		return 0, []byte(err.Error())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// request builds a request carrying this member's identity and tenant.
func (m *member) request(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, m.f.base+path, body)
	if err != nil {
		return nil, err
	}
	if m.email != "" { // a member without one is the signed-out caller
		req.Header.Set("X-Forwarded-Email", m.email)
	}
	if m.tenant != "" {
		req.Header.Set("X-AF-Tenant", m.tenant)
	}
	return req, nil
}

// call sends a request, fails unless the status is want, and decodes a JSON object body.
func (m *member) call(method, path string, body any, want int) map[string]any {
	m.t.Helper()
	code, b := m.do(method, path, body)
	if code != want {
		m.t.Fatalf("%s: %s %s: %d (want %d) %s", m.name, method, path, code, want, truncate(b))
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			m.t.Fatalf("%s: %s %s: bad JSON: %v: %s", m.name, method, path, err, truncate(b))
		}
	}
	return out
}

func (m *member) getJSON(path string) map[string]any {
	m.t.Helper()
	return m.call(http.MethodGet, path, nil, http.StatusOK)
}

func (m *member) postJSON(path string, body any, want int) map[string]any {
	m.t.Helper()
	return m.call(http.MethodPost, path, body, want)
}

func (m *member) putJSON(path string, body any, want int) map[string]any {
	m.t.Helper()
	return m.call(http.MethodPut, path, body, want)
}

// apiErr returns the error code of an {"error":{"code":…}} body, or "".
func apiErr(v map[string]any) (code, reason string) {
	e, _ := v["error"].(map[string]any)
	code, _ = e["code"].(string)
	reason, _ = e["reason"].(string)
	return code, reason
}

// --- a member's workspace --------------------------------------------------

// workspaceState is GET /api/workspace's state ("running", "stopped", …).
func (m *member) workspaceState() string {
	m.t.Helper()
	return fmt.Sprint(m.getJSON("/api/workspace")["state"])
}

// startWorkspace starts the member's workspace and waits until it is running. Start
// answers 500 whenever the Agent-healthz wait is missed once, but it is idempotent, so it
// is retried until it takes.
func (m *member) startWorkspace() {
	m.t.Helper()
	waitFor(m.t, 150*time.Second, m.name+" workspace/start accepted", func() (bool, string) {
		code, b := m.do(http.MethodPost, "/api/workspace/start", nil)
		return code == 200, fmt.Sprintf("%d %s", code, truncate(b))
	})
	waitFor(m.t, 60*time.Second, m.name+" workspace running", func() (bool, string) {
		code, b := m.do(http.MethodGet, "/api/workspace", nil)
		var ws map[string]any
		_ = json.Unmarshal(b, &ws)
		return code == 200 && ws["state"] == "running", fmt.Sprintf("%d %v", code, ws["state"])
	})
}

// stopWorkspace stops the member's workspace and checks it reads as stopped.
func (m *member) stopWorkspace() {
	m.t.Helper()
	got := m.postJSON("/api/workspace/stop", nil, 200)
	if got["state"] != "stopped" {
		m.t.Fatalf("%s: workspace/stop state = %v, want stopped", m.name, got["state"])
	}
}

// requireStopped fails unless the workspace has no running container — used after reads
// that must not wake it.
func (m *member) requireStopped(why string) {
	m.t.Helper()
	if err := checkStopped(m.workspaceState()); err != nil {
		m.t.Fatalf("%s: %v %s", m.name, err, why)
	}
}

// checkStopped accepts the two states with no container serving: "none" (the docker
// runtime removes the container on stop, so a stopped workspace reads none) and "stopped"
// (a container that exists but is not running). Rejecting only "running" is not enough:
// a read that woke the workspace reads "starting" while the Agent is not ready yet
// (runtime_docker.go State), which is exactly the regression being looked for.
func checkStopped(state string) error {
	if state != "none" && state != "stopped" {
		return fmt.Errorf("workspace state is %q, want none or stopped", state)
	}
	return nil
}

// createSession creates a session (POST /api/sessions) and returns its name.
func (m *member) createSession(body map[string]any) string {
	m.t.Helper()
	got := m.postJSON("/api/sessions", body, 201)
	name, _ := got["name"].(string)
	if name == "" {
		m.t.Fatalf("%s: session create returned no name: %v", m.name, got)
	}
	m.t.Logf("%s: session %s created (kind=%v dir=%v)", m.name, name, got["kind"], got["dir"])
	return name
}

// shell runs cmd in a shell session (in the session's own cwd) and returns its combined
// output, failing on a non-zero exit. The output goes to a file in home and is read back
// through the fs API, the same route TestFleet uses; the rename makes the file appear only
// once the command has finished.
func (m *member) shell(session, cmd string) string {
	m.t.Helper()
	m.cmds++
	out := fmt.Sprintf("e2e-%s-cmd-%d.out", m.name, m.cmds)
	line := fmt.Sprintf(`{ %s ; } > ~/%s.tmp 2>&1; echo "e2e-exit=$?" >> ~/%s.tmp; mv ~/%s.tmp ~/%s`,
		cmd, out, out, out, out)
	waitFor(m.t, 20*time.Second, m.name+" session input accepted", func() (bool, string) {
		code, b := m.do(http.MethodPost, "/api/sessions/"+session+"/input", map[string]any{"prompt": line})
		return code == 200, fmt.Sprintf("%d %s", code, truncate(b))
	})
	var content string
	waitFor(m.t, 90*time.Second, m.name+" $ "+cmd, func() (bool, string) {
		code, b := m.do(http.MethodGet, "/api/fs/file?path="+url.QueryEscape(out), nil)
		if code != 200 {
			return false, fmt.Sprintf("%d %s", code, truncate(b))
		}
		var f struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(b, &f)
		content = f.Content
		return strings.Contains(content, "e2e-exit="), string(truncate(b))
	})
	body, status, _ := strings.Cut(content, "e2e-exit=")
	if strings.TrimSpace(status) != "0" {
		m.t.Fatalf("%s: `%s` exited %s:\n%s", m.name, cmd, strings.TrimSpace(status), body)
	}
	return strings.TrimSpace(body)
}

// notifications returns the member's notification items (GET /api/notifications).
func (m *member) notifications() []map[string]any {
	m.t.Helper()
	items, _ := m.getJSON("/api/notifications")["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if n, ok := it.(map[string]any); ok {
			out = append(out, n)
		}
	}
	return out
}

// --- a member's event stream ------------------------------------------------

// eventStream is a member's open server-sent-events connection. Each data: payload arrives
// on frames; closed is closed when the stream ends, and err then says how: nil for a clean
// end of the response, the read error for a dropped connection.
type eventStream struct {
	frames chan string
	closed chan struct{}
	err    error
}

// openStream opens path as an SSE stream and fails unless it answers 200. The connection is
// closed with the test.
func (m *member) openStream(path string) *eventStream {
	m.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m.t.Cleanup(cancel)
	req, err := m.request(http.MethodGet, path, nil)
	if err != nil {
		m.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		m.t.Fatalf("%s: open stream %s: %v", m.name, path, err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		m.t.Fatalf("%s: open stream %s: %d %s", m.name, path, resp.StatusCode, truncate(b))
	}
	s := &eventStream{frames: make(chan string, 256), closed: make(chan struct{})}
	go func() {
		defer close(s.closed)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				select {
				case s.frames <- data:
				default: // a scenario that stopped reading must not stall the connection
				}
			}
		}
		s.err = sc.Err() // written before closed is closed, read only after it
	}()
	return s
}

// next returns the next frame, or false when none arrives within d.
func (s *eventStream) next(d time.Duration) (string, bool) {
	select {
	case f := <-s.frames:
		return f, true
	case <-time.After(d):
		return "", false
	}
}

// isOpen reports whether the stream has not ended. A silence is only evidence while the
// stream is still open: a dropped connection is silent too.
func (s *eventStream) isOpen() bool {
	select {
	case <-s.closed:
		return false
	default:
		return true
	}
}

// endsWithin reports whether the stream is closed within d.
func (s *eventStream) endsWithin(d time.Duration) bool {
	select {
	case <-s.closed:
		return true
	case <-time.After(d):
		return false
	}
}

// --- what the CP stored -----------------------------------------------------

// sqlQuery is the reader behind memberFleet.sql. It goes through python3's sqlite3 module so
// the e2e module keeps no dependencies, and opens the database read-only (mode=ro), so a
// query cannot change what the CP sees.
const sqlQuery = `import json, sqlite3, sys, urllib.parse
db = sqlite3.connect("file:" + urllib.parse.quote(sys.argv[1]) + "?mode=" + sys.argv[4], uri=True, timeout=10)
for row in db.execute(sys.argv[2], json.loads(sys.argv[3])):
    print("|".join("" if v is None else str(v) for v in row))
db.commit()
`

// sql runs a query against the CP's SQLite database and returns each row as its columns
// joined by "|". It is the checklist's "look in the table" observation point.
func (f *memberFleet) sql(query string, args ...any) []string {
	f.t.Helper()
	return f.runSQL("ro", query, args...)
}

// sqlWhileStopped changes the CP's database, for state only an older CP or the passing of
// time would leave behind. It refuses while the CP runs: the CP must find the change at its
// next start, not race it.
func (f *memberFleet) sqlWhileStopped(query string, args ...any) {
	f.t.Helper()
	if f.cp.running() {
		f.t.Fatalf("sqlWhileStopped with the CP running: %s", query)
	}
	f.runSQL("rw", query, args...)
}

func (f *memberFleet) runSQL(mode, query string, args ...any) []string {
	f.t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		prereqMissing(f.t, "python3 not on PATH (the harness reads the CP database with it)")
	}
	if args == nil {
		args = []any{}
	}
	a, _ := json.Marshal(args)
	var stderr bytes.Buffer
	cmd := exec.Command(py, "-I", "-c", sqlQuery, filepath.Join(f.dataDir, "e2e-cp.db"), query, string(a), mode)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() > 0 { // a warning is a query to fix, not noise to parse past
		f.t.Fatalf("sql %q: %v\n%s%s", query, err, out, stderr.Bytes())
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// count runs a SELECT count(*) … and returns the number.
func (f *memberFleet) count(query string, args ...any) int {
	f.t.Helper()
	rows := f.sql(query, args...)
	n := -1
	if len(rows) == 1 {
		_, _ = fmt.Sscan(rows[0], &n)
	}
	if n < 0 {
		f.t.Fatalf("sql %q: want one count, got %q", query, rows)
	}
	return n
}

// membershipID is the id of the member's membership row.
func (m *member) membershipID() string {
	m.t.Helper()
	rows := m.f.sql(`SELECT m.id FROM membership m JOIN identity i ON i.id=m.identity_id
		JOIN tenant t ON t.id=m.tenant_id WHERE i.user_key=? AND t.slug=?`, m.key, m.tenant)
	if len(rows) != 1 {
		m.t.Fatalf("%s: membership rows %q", m.name, rows)
	}
	return rows[0]
}

// --- workspaces that call the CP --------------------------------------------

// cpReachableEnv is the CP environment that lets workspaces call the CP, which the memo
// tools (list_memos, add_memo, …) need: the workspace-only listener (ADR 0106 decision 8)
// bound to the docker bridge gateway, where every member's container reaches it and nothing
// outside this machine does, and PUBLIC_BASE_URL, without which the CP injects no memo token
// into a workspace. The listener serves the /internal/ workspace routes only.
func cpReachableEnv(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		prereqMissing(t, "docker not on PATH")
	}
	l, err := net.Listen("tcp", net.JoinHostPort(dockerBridgeGateway(t), "0"))
	if err != nil {
		t.Fatalf("workspace listener address: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	u := "http://" + addr
	return []string{"AF_CP_INTERNAL_LISTEN=" + addr, "AF_CP_INTERNAL_URL=" + u, "PUBLIC_BASE_URL=" + u}
}

// writeHome writes a file into the member's home from the host side (the directory is
// bind-mounted into the container), for input too long or too quoted to type.
func (m *member) writeHome(name, content string) {
	m.t.Helper()
	p := filepath.Join(m.f.dataDir, m.tenant, m.key, "home", name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

// mcpResponse is one JSON-RPC answer of the session's af MCP server.
type mcpResponse struct {
	ID     int `json:"id"`
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// mcp runs the af MCP server inside the workspace the way a session's agent CLI launches it
// (workspace-agent mcp-stdio with flags), from a shell session so it has that session's
// environment, and returns the answers by request id. requests are JSON-RPC requests after
// initialize, numbered from 2.
func (m *member) mcp(session string, flags []string, requests ...map[string]any) map[int]mcpResponse {
	m.t.Helper()
	lines := []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`}
	for i, r := range requests {
		r["jsonrpc"], r["id"] = "2.0", i+2
		b, _ := json.Marshal(r)
		lines = append(lines, string(b))
	}
	m.cmds++
	in := fmt.Sprintf("e2e-%s-mcp-%d.jsonl", m.name, m.cmds)
	m.writeHome(in, strings.Join(lines, "\n")+"\n")
	out := m.shell(session, "workspace-agent mcp-stdio "+strings.Join(flags, " ")+" < ~/"+in)
	got := map[int]mcpResponse{}
	for _, line := range strings.Split(out, "\n") {
		var r mcpResponse
		if json.Unmarshal([]byte(line), &r) == nil && r.ID >= 2 {
			got[r.ID] = r
		}
	}
	if len(got) != len(requests) {
		m.t.Fatalf("%s: mcp-stdio answered %d of %d requests:\n%s", m.name, len(got), len(requests), out)
	}
	return got
}

// mcpTool is a tools/call request for member.mcp.
func mcpTool(name string, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	return map[string]any{"method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
}

// --- a git remote both workspaces can reach ---------------------------------

// gitRemote serves bare repositories over git's smart HTTP protocol (`git http-backend`
// as CGI) from the test process, without credentials, so every member's workspace can
// clone, fetch and push the same repository.
type gitRemote struct {
	root string // GIT_PROJECT_ROOT: one bare repository per <name>.git
	base string // http://host:port
}

// newGitRemote serves an empty remote on host (an address of this machine the workspace
// containers can reach — dockerBridgeGateway — or 127.0.0.1 for host-only use). It needs
// git on the host; the server stops with the test.
func newGitRemote(t *testing.T, host string) *gitRemote {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		prereqMissing(t, "git not on PATH")
	}
	r := &gitRemote{root: t.TempDir()}
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("git remote: listen on %s: %v", host, err)
	}
	r.base = "http://" + l.Addr().String()
	srv := &http.Server{Handler: gitHTTPBackend(gitBin, r.root), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return r
}

// gitHTTPBackend is `git http-backend` behind net/http/cgi, the same way the CP serves its
// internal git provider. The host's git config is kept out, so a fixture behaves the same
// on a laptop and on a runner.
func gitHTTPBackend(gitBin, root string) http.Handler {
	return &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
		},
		InheritEnv: []string{"PATH"},
	}
}

// URL is the clone URL of the repository name.
func (r *gitRemote) URL(name string) string { return r.base + "/" + name + ".git" }

// newRepo creates the bare repository name with one commit on main and returns its URL.
// receivepack is switched on because http-backend refuses an unauthenticated push
// otherwise.
func (r *gitRemote) newRepo(t *testing.T, name string) string {
	t.Helper()
	bare := filepath.Join(r.root, name+".git")
	work := filepath.Join(t.TempDir(), name)
	runGit(t, "", "init", "-q", "--bare", "-b", "main", bare)
	runGit(t, bare, "config", "http.receivepack", "true")
	runGit(t, "", "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("e2e remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "commit", "-q", "-m", "initial")
	runGit(t, work, "push", "-q", bare, "main")
	return r.URL(name)
}

// runGit runs git with a fixed identity and no user or system config, so the host's own
// settings (signing, hooks, templates) cannot leak into the fixture.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=e2e", "-c", "user.email=e2e@" + emailDomain}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// dockerBridgeGateway is the host's address on docker's default bridge. Every member's
// container sits on its own network (af-net-…), but traffic to an address the host owns is
// delivered locally rather than forwarded between bridges, so all of them reach a server
// bound here, and nothing outside this machine does.
func dockerBridgeGateway(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "network", "inspect", "bridge",
		"-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output()
	if err != nil {
		t.Fatalf("docker network inspect bridge: %v", err)
	}
	for _, f := range strings.Fields(string(out)) {
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			return f
		}
	}
	t.Fatalf("docker bridge has no IPv4 gateway: %q", out)
	return ""
}

// --- helpers ---------------------------------------------------------------

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// decode sends a request as m, fails unless the status is want, and unmarshals the body
// into out.
func decode(t *testing.T, m *member, method, path string, body any, want int, out any) {
	t.Helper()
	code, b := m.do(method, path, body)
	if code != want {
		t.Fatalf("%s: %s %s: %d (want %d) %s", m.name, method, path, code, want, truncate(b))
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %s %s: bad JSON: %v: %s", m.name, method, path, err, truncate(b))
	}
}
