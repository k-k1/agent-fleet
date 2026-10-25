package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sharedDaemon is a v2 serve as far as the Terminal launch uses it: sessions with metadata,
// fork, model/agent switches, the environment, interrupt and /api/info.
type sharedDaemon struct {
	srv *httptest.Server
	mu  sync.Mutex
	// calls are "METHOD path body", in arrival order.
	calls      []string
	sessions   map[string]map[string]string // id -> metadata
	envs       map[string]map[string]string // id -> PUT environment
	next       int
	pid        int
	version    string
	failGet    map[string]bool     // GET of these session ids answers 503
	deleteGate func(id string)     // runs on a DELETE before it is applied (the lock is released)
	failSwitch bool                // model/agent switches answer 500
	messageIDs map[string][]string // session id → message ids GET .../message answers with
	failEnv    int                 // fail this many environment PUTs with 503 before accepting
	failPatch  int                 // fail this many metadata PATCHes with 503 before accepting
}

func newSharedDaemon(t *testing.T) *sharedDaemon {
	t.Helper()
	useMajor(t, 2)
	// The fake hands out the same ids in every test; real ids are unique, claim files are not cleaned.
	claims := filepath.Join(paths.AgentStateDir(), "opencode-claim")
	_ = os.RemoveAll(claims)
	t.Cleanup(func() { _ = os.RemoveAll(claims) })
	d := &sharedDaemon{sessions: map[string]map[string]string{}, envs: map[string]map[string]string{}, pid: 4242, version: "2.0.28"}
	d.srv = httptest.NewServer(http.HandlerFunc(d.handle))
	t.Cleanup(d.srv.Close)
	t.Setenv(serveAddrEnv, d.srv.URL)
	t.Setenv(standaloneEnv, "")
	resetServeAuth("pw-shared")
	detectedMajor.Store(int32(apiV2))
	old := ensureServe
	ensureServe = func() (string, int, error) { return d.srv.URL, 1, nil }
	t.Cleanup(func() { ensureServe = old; detectedMajor.Store(int32(apiV1)); resetServeAuth("") })
	return d
}

func (d *sharedDaemon) handle(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "opencode" || p != "pw-shared" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	b, _ := io.ReadAll(r.Body)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, r.Method+" "+r.URL.Path+" "+string(b))
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	md := func(m map[string]any) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
		return out
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api session <id> <verb>
	switch {
	case r.URL.Path == "/api/info":
		reply(map[string]any{"version": d.version, "pid": d.pid})
	case r.Method == "POST" && r.URL.Path == "/api/session":
		d.next++
		id := fmt.Sprintf("ses_new%d", d.next)
		d.sessions[id] = map[string]string{}
		if m, ok := body["metadata"].(map[string]any); ok {
			d.sessions[id] = md(m)
		}
		reply(map[string]any{"data": map[string]any{"id": id}})
	case len(parts) >= 3 && parts[0] == "api" && parts[1] == "session":
		id := parts[2]
		cur, ok := d.sessions[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if d.failGet[id] && r.Method == "GET" && len(parts) == 3 {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if d.failSwitch && len(parts) == 4 && (parts[3] == "model" || parts[3] == "agent") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		switch {
		case len(parts) == 3 && r.Method == "DELETE":
			if g := d.deleteGate; g != nil {
				d.mu.Unlock()
				g(id)
				d.mu.Lock()
			}
			delete(d.sessions, id)
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 3 && r.Method == "GET":
			reply(map[string]any{"data": map[string]any{"id": id, "metadata": cur}})
		case len(parts) == 3 && r.Method == "PATCH":
			if d.failPatch > 0 {
				d.failPatch--
				http.Error(w, "starting", http.StatusServiceUnavailable)
				return
			}
			m, _ := body["metadata"].(map[string]any)
			d.sessions[id] = md(m) // replaces, as the real server does
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 4 && parts[3] == "message" && r.Method == "GET":
			var data []map[string]any
			for _, mid := range d.messageIDs[id] {
				data = append(data, map[string]any{"id": mid, "type": "user"})
			}
			reply(map[string]any{"data": data})
		case len(parts) == 4 && parts[3] == "fork":
			d.next++
			nid := fmt.Sprintf("ses_fork%d", d.next)
			d.sessions[nid] = map[string]string{}
			for k, v := range cur { // a fork copies the source's metadata
				d.sessions[nid][k] = v
			}
			reply(map[string]any{"data": map[string]any{"id": nid}})
		case len(parts) == 4 && parts[3] == "environment" && r.Method == "PUT":
			if d.failEnv > 0 {
				d.failEnv--
				http.Error(w, "starting", http.StatusServiceUnavailable)
				return
			}
			vars, _ := body["variables"].(map[string]any)
			d.envs[id] = md(vars)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent) // model, agent, interrupt
		}
	default:
		http.NotFound(w, r)
	}
}

func (d *sharedDaemon) callsMatching(prefix string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func terminalMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	dir := t.TempDir()
	return session.Meta{Name: name, Dir: dir, Kind: session.KindOpencode, Driver: session.DriverTUI}
}

func launchEnv(plan agents.LaunchPlan) map[string]string { return shellEnvironment(plan.Env) }

// A fresh Terminal slot: the session is created through the API with its metadata, recorded at
// once, and the pane program is the attach wrapper — with the password in the pane environment
// and never on the command line.
func TestSharedLaunchCreatesTheSessionTaggedAndRecorded(t *testing.T) {
	d := newSharedDaemon(t)
	m := terminalMeta(t, "fresh")
	m.Model, m.Mode = "stub/m#high", "plan"
	plan, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sid := session.UUID(m.Dir, m.Name)
	creates := d.callsMatching("POST /api/session {")
	if len(creates) != 1 {
		t.Fatalf("creates = %v", d.calls)
	}
	var body struct {
		Metadata map[string]string `json:"metadata"`
		Model    map[string]string `json:"model"`
		Agent    string            `json:"agent"`
		Location map[string]string `json:"location"`
	}
	_ = json.Unmarshal([]byte(strings.TrimPrefix(creates[0], "POST /api/session ")), &body)
	if body.Metadata[metaSID] != sid || body.Metadata[metaName] != "fresh" {
		t.Errorf("metadata = %v", body.Metadata)
	}
	if body.Model["providerID"] != "stub" || body.Model["id"] != "m" || body.Model["variant"] != "high" || body.Agent != "plan" {
		t.Errorf("model/agent = %v %q", body.Model, body.Agent)
	}
	if body.Location["directory"] != m.Dir {
		t.Errorf("location = %v", body.Location)
	}
	if got := sids.Read(sid); got != "ses_new1" {
		t.Errorf("slot = %q, want the created session recorded", got)
	}
	if strings.Contains(plan.Program, "pw-shared") {
		t.Errorf("the password is on the command line: %s", plan.Program)
	}
	for _, want := range []string{"workspace-agent opencode-attach", "--server '" + d.srv.URL + "'", "--session 'ses_new1'", "'--auto'"} {
		if !strings.Contains(plan.Program, want) {
			t.Errorf("program %q lacks %q", plan.Program, want)
		}
	}
	got := map[string]bool{}
	for _, kv := range plan.Env {
		got[kv] = true
	}
	for _, want := range []string{"OPENCODE_SERVER_PASSWORD=pw-shared", "OPENCODE_SERVER_USERNAME=opencode", "AF_OPENCODE_SHARED=1", "AF_SESSION_SID=" + sid} {
		if !got[want] {
			t.Errorf("pane env lacks %s", want)
		}
	}
	if strings.Contains(plan.Program, "--standalone") {
		t.Errorf("a shared launch must not be standalone: %s", plan.Program)
	}
}

// A slot with a recorded conversation resumes THAT session, switches its model and agent through
// the API (an existing session ignores config) and keeps the other metadata keys when it tags it.
func TestSharedLaunchResumeSwitchesAndKeepsOtherMetadata(t *testing.T) {
	d := newSharedDaemon(t)
	m := terminalMeta(t, "resume")
	m.Model, m.Mode = "stub/m", "normal"
	sid := session.UUID(m.Dir, m.Name)
	d.sessions["ses_old"] = map[string]string{"user.key": "keep"}
	sids.Write(sid, "ses_old")
	t.Cleanup(func() { sids.Remove(sid) })

	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(d.callsMatching("POST /api/session {")) != 0 || len(d.callsMatching("POST /api/session/ses_old/fork")) != 0 {
		t.Fatalf("a resume must not create or fork: %v", d.calls)
	}
	if len(d.callsMatching("POST /api/session/ses_old/model")) != 1 || len(d.callsMatching("POST /api/session/ses_old/agent {\"agent\":\"build\"}")) != 1 {
		t.Errorf("model/agent not switched: %v", d.calls)
	}
	want := map[string]string{"user.key": "keep", metaSID: sid, metaName: "resume"}
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, v := range want {
		if d.sessions["ses_old"][k] != v {
			t.Errorf("metadata[%s] = %q, want %q (PATCH replaces, so the merge must carry the rest): %v", k, d.sessions["ses_old"][k], v, d.sessions["ses_old"])
		}
	}
}

// A recorded session the server no longer has is not attached to: the slot starts a new one.
func TestSharedLaunchVanishedSessionStartsOver(t *testing.T) {
	d := newSharedDaemon(t)
	m := terminalMeta(t, "gone")
	sid := session.UUID(m.Dir, m.Name)
	sids.Write(sid, "ses_missing")
	t.Cleanup(func() { sids.Remove(sid) })
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := sids.Read(sid); got != "ses_new1" || len(d.callsMatching("POST /api/session {")) != 1 {
		t.Errorf("slot = %q, calls %v", got, d.calls)
	}
}

// A fork: the copy is made with `before` when the fork is at a message, re-tagged for the NEW
// slot (the copy carries the source's af.sid), and recorded before anything else can fail.
func TestSharedLaunchForkRetagsTheCopy(t *testing.T) {
	d := newSharedDaemon(t)
	d.sessions["ses_src"] = sharedMetadata("source-sid", "source")
	d.messageIDs = map[string][]string{"ses_src": {"msg_1", "msg_5"}}
	m := terminalMeta(t, "copy")
	m.ForkFrom, m.ForkAt = "ses_src", "msg_5"
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if f := d.callsMatching("POST /api/session/ses_src/fork"); len(f) != 1 || !strings.Contains(f[0], `"before":"msg_5"`) {
		t.Fatalf("fork call = %v", f)
	}
	sid := session.UUID(m.Dir, m.Name)
	t.Cleanup(func() { sids.Remove(sid) })
	if got := sids.Read(sid); got != "ses_fork1" {
		t.Fatalf("slot = %q", got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if md := d.sessions["ses_fork1"]; md[metaSID] != sid || md[metaName] != "copy" {
		t.Errorf("the fork keeps the source's identity: %v", md)
	}
	if md := d.sessions["ses_src"]; md[metaSID] != "source-sid" {
		t.Errorf("the source was retagged: %v", md)
	}
}

// Without a serve to attach to (not connected, disabled, or the switch back) the launch is the
// standalone one, byte for byte.
func TestSharedLaunchFallsBackToStandalone(t *testing.T) {
	newSharedDaemon(t)
	ensureServe = func() (string, int, error) { return "", 0, ErrNotConnected }
	m := terminalMeta(t, "fallback")
	plan, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Program != "opencode --standalone --auto" {
		t.Errorf("fallback program = %q", plan.Program)
	}

	t.Setenv(standaloneEnv, "1")
	ensureServe = func() (string, int, error) { t.Fatal("the daemon must not even be asked for"); return "", 0, nil }
	if plan, err = (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil || plan.Program != "opencode --standalone --auto" {
		t.Errorf("switched back: %q, %v", plan.Program, err)
	}
}

// A daemon older than the installed opencode is replaced before a TUI of the new version attaches.
func TestSharedLaunchReplacesAnOutdatedDaemon(t *testing.T) {
	d := newSharedDaemon(t)
	d.version = "2.0.20"
	var reasons []string
	restartServe = func(reason string) bool {
		reasons = append(reasons, reason)
		d.mu.Lock()
		d.version = "2.0.28" // what the replacement reports
		d.mu.Unlock()
		return true
	}
	t.Cleanup(func() { restartServe = func(reason string) bool { return Serve().Restart(reason) } })
	if _, err := (agentImpl{}).BuildLaunch(terminalMeta(t, "upgraded"), agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "2.0.20") || !strings.Contains(reasons[0], "2.0.28") {
		t.Errorf("restarts = %v", reasons)
	}
	// Matching versions: no restart.
	reasons = nil
	if _, err := (agentImpl{}).BuildLaunch(terminalMeta(t, "current"), agents.LaunchOpts{}); err != nil || len(reasons) != 0 {
		t.Errorf("a current daemon was restarted: %v %v", reasons, err)
	}
}

// opencode 1 never touches the API for a Terminal launch.
func TestSharedLaunchIsV2Only(t *testing.T) {
	useMajor(t, 1)
	ensureServe = func() (string, int, error) { t.Fatal("v1 must not use the shared serve"); return "", 0, nil }
	t.Cleanup(func() { ensureServe = func() (string, int, error) { return Serve().Ensure() } })
	plan, err := (agentImpl{}).BuildLaunch(terminalMeta(t, "v1"), agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Program, "opencode --auto") || strings.Contains(plan.Program, "attach") {
		t.Errorf("v1 program = %q", plan.Program)
	}
}

// The attach wrapper puts the pane's environment — without the server credentials — and only
// then starts the TUI; a server that will not take it stops the launch.
func TestAttachPutsTheEnvironmentBeforeTheTUI(t *testing.T) {
	d := newSharedDaemon(t)
	d.sessions["ses_a"] = map[string]string{metaSID: "sid-1", metaName: "slot"}
	d.failEnv = 2
	oldRetries := attachRetries
	attachRetries = 5
	t.Cleanup(func() { attachRetries = oldRetries })
	environ := []string{"AF_SESSION_SID=sid-1", "AF_SESSION_NAME=slot", "PATH=/x", "OPENCODE_SERVER_PASSWORD=pw-shared", "OPENCODE_SERVER_USERNAME=opencode", "junk"}
	var started []string
	err := runAttach([]string{"--server", d.srv.URL, "--session", "ses_a", "--", "--auto"}, environ, func(argv, _ []string) error {
		if len(d.envs["ses_a"]) == 0 {
			t.Error("the TUI started before the environment was put")
		}
		started = argv
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "--server " + d.srv.URL + " --session ses_a --auto"; strings.Join(started, " ") != want {
		t.Errorf("tui argv = %v, want %q", started, want)
	}
	got := d.envs["ses_a"]
	if got["AF_SESSION_SID"] != "sid-1" || got["PATH"] != "/x" {
		t.Errorf("environment = %v", got)
	}
	for k := range got {
		if strings.HasPrefix(k, "OPENCODE_SERVER") || k == "junk" {
			t.Errorf("%s must not reach the shell", k)
		}
	}

	// Another slot's (or no slot's) session is not attached to: its environment would be this
	// pane's while its metadata, bill and status stayed someone else's.
	d.sessions["ses_foreign"] = map[string]string{metaSID: "foreign-sid", metaName: "other"}
	d.sessions["ses_untagged"] = map[string]string{}
	for _, ses := range []string{"ses_foreign", "ses_untagged", "ses_missing"} {
		err = runAttach([]string{"--server", d.srv.URL, "--session", ses, "--"}, environ, func(_, _ []string) error {
			t.Errorf("the TUI must not start on %s", ses)
			return nil
		})
		if err == nil {
			t.Errorf("%s must be refused", ses)
		}
		if _, has := d.envs[ses]; has {
			t.Errorf("%s got this pane's environment", ses)
		}
	}
	// Flags cannot re-point the TUI at a session or server the environment was not put on.
	for _, flag := range []string{"--session", "--session=ses_foreign", "-s", "--server", "--continue", "--standalone", "--fork"} {
		err = runAttach([]string{"--server", d.srv.URL, "--session", "ses_a", "--", "--auto", flag}, environ, func(_, _ []string) error {
			t.Errorf("flag %s reached the TUI", flag)
			return nil
		})
		if err == nil {
			t.Errorf("flag %s must be refused", flag)
		}
	}

	d.failEnv = 100
	err = runAttach([]string{"--server", d.srv.URL, "--session", "ses_a", "--"}, environ, func(_, _ []string) error {
		t.Error("the TUI must not start without its environment")
		return nil
	})
	if err == nil {
		t.Error("an environment the server never took must stop the launch")
	}
}

// The pane's environment is read back from the process tree for a server restart.
func TestPaneEnvironmentFromTheProcessTree(t *testing.T) {
	proc := t.TempDir()
	write := func(pid, ppid int, environ ...string) {
		dir := filepath.Join(proc, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// The command name may hold spaces and parentheses.
		stat := fmt.Sprintf("%d (sh -c (x)) S %d 1 1 0 -1", pid, ppid)
		_ = os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(environ, "\x00")+"\x00"), 0o644)
	}
	pane := []string{"AF_SESSION_NAME=slot", "OPENCODE_SERVER_PASSWORD=pw", "PATH=/a"}
	write(100, 1, pane...)                                                  // the pane's shell: tmux -e values only
	write(101, 100, append([]string{"JAVA_HOME=/jdk"}, pane...)...)         // the TUI: plus the shell's exports
	write(102, 101, "AF_SESSION_NAME=slot")                                 // a child that is not the TUI
	write(200, 1, "AF_SESSION_NAME=slot", "OPENCODE_SERVER_PASSWORD=other") // not under this pane
	got, ok := envOfPaneTree(proc, 100, "slot")
	if !ok || got["JAVA_HOME"] != "/jdk" || got["PATH"] != "/a" {
		t.Fatalf("env = %v ok=%v, want the TUI's (with the toolchain export)", got, ok)
	}
	if _, has := got["OPENCODE_SERVER_PASSWORD"]; has {
		t.Error("the server password must not be in the shell environment")
	}
	if _, ok := envOfPaneTree(proc, 100, "another"); ok {
		t.Error("another slot's name must not match")
	}
}

// After a restart every live shared Terminal slot gets its pane's environment back (retrying a
// server that is still settling); a slot with no pane is skipped. This is the warm-up: the guarantee
// is putEnvironmentFor, asked by the server plugin before a command.
func TestRestoreTerminalEnvsPutsEveryLiveSlot(t *testing.T) {
	d := newSharedDaemon(t)
	a, b := terminalMeta(t, "a"), terminalMeta(t, "b")
	d.sessions["ses_a"], d.sessions["ses_b"] = map[string]string{}, map[string]string{}
	d.failEnv = 2
	sids.Write(session.UUID(a.Dir, a.Name), "ses_a")
	sids.Write(session.UUID(b.Dir, b.Name), "ses_b")
	t.Cleanup(func() { sids.Remove(session.UUID(a.Dir, a.Name)); sids.Remove(session.UUID(b.Dir, b.Name)) })
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{a, b} })
	oldEnv := paneEnv
	paneEnv = func(name string) (map[string]string, bool) {
		if name == "b" {
			return nil, false // its pane is gone: skipped, not fatal
		}
		return map[string]string{"AF_SESSION_NAME": name}, true
	}
	t.Cleanup(func() { paneEnv = oldEnv })

	restoreTerminalEnvs(d.srv.URL)
	if d.envs["ses_a"]["AF_SESSION_NAME"] != "a" {
		t.Errorf("slot a's environment not restored after the retries: %v", d.envs)
	}
	if _, has := d.envs["ses_b"]; has {
		t.Errorf("slot b has no pane environment to restore: %v", d.envs)
	}
}

// The command the server plugin runs before a shell: it puts the slot's environment on a session of
// that slot (a subagent's or a fork's carries the slot's tags) and on no other, and a failure is an
// error so the plugin can refuse the command.
func TestPutEnvironmentForChecksTheSessionIsTheSlots(t *testing.T) {
	d := newSharedDaemon(t)
	m := terminalMeta(t, "owner")
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	d.sessions["ses_child"] = sharedMetadata(sid, "owner")
	d.sessions["ses_foreign"] = sharedMetadata("other-sid", "other")
	d.sessions["ses_untagged"] = map[string]string{}
	oldEnv := paneEnv
	paneEnv = func(string) (map[string]string, bool) { return map[string]string{"AF_SESSION_SID": sid}, true }
	t.Cleanup(func() { paneEnv = oldEnv })

	if err := putEnvironmentFor(d.srv.URL, "owner", "ses_child"); err != nil || d.envs["ses_child"]["AF_SESSION_SID"] != sid {
		t.Fatalf("own session: %v %v", err, d.envs)
	}
	for _, ses := range []string{"ses_foreign", "ses_untagged", "ses_missing"} {
		if err := putEnvironmentFor(d.srv.URL, "owner", ses); err == nil {
			t.Errorf("%s must be refused", ses)
		}
		if _, has := d.envs[ses]; has {
			t.Errorf("%s got an environment", ses)
		}
	}
	if err := putEnvironmentFor(d.srv.URL, "nobody", "ses_child"); err == nil {
		t.Error("an unknown slot must be refused")
	}
	d.failEnv = 1
	if err := putEnvironmentFor(d.srv.URL, "owner", "ses_child"); err == nil {
		t.Error("a PUT the server refused must be an error")
	}
	paneEnv = func(string) (map[string]string, bool) { return nil, false }
	if err := putEnvironmentFor(d.srv.URL, "owner", "ses_child"); err == nil {
		t.Error("no pane environment must be an error, not an empty PUT")
	}
}

// A pane that fell back to --standalone is not attached to the serve, whatever the workspace
// switch says: its state, dependents, restore, drain and stop must not look there.
func TestAFallbackPaneStaysStandaloneWhileTheServeRecovers(t *testing.T) {
	d := newSharedDaemon(t)
	m := terminalMeta(t, "fell")
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	t.Cleanup(func() { slotModes.Remove(sid); sids.Remove(sid) })
	sids.Write(sid, "ses_p")
	d.sessions["ses_p"] = map[string]string{}

	ensureServe = func() (string, int, error) { return "", 0, ErrNotConnected }
	if plan, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil || !strings.HasPrefix(plan.Program, "opencode --standalone --auto") {
		t.Fatalf("fallback launch: %q %v", plan.Program, err)
	}
	// The serve is back, the switch still says shared: this pane is still a private server.
	ensureServe = func() (string, int, error) { return d.srv.URL, 1, nil }
	if sharedSlot(m) {
		t.Error("a fallen-back pane is not a shared slot")
	}
	if _, ok := v2TargetFor(m); ok {
		t.Error("its state is not on the shared serve")
	}
	supervisor.mu.Lock()
	supervisor.up = true
	supervisor.mu.Unlock()
	t.Cleanup(func() { supervisor.mu.Lock(); supervisor.up = false; supervisor.mu.Unlock() })
	interruptTerminal(m)
	if got := d.callsMatching("POST /api/session/ses_p/interrupt"); len(got) != 0 {
		t.Errorf("a standalone pane was interrupted on the shared serve: %v", got)
	}

	// The next launch attaches, and from then on the slot is shared.
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if !sharedSlot(m) {
		t.Error("a shared launch must mark the slot")
	}
	// A slot launched before any record existed ran --standalone.
	slotModes.Remove(sid)
	if sharedSlot(m) {
		t.Error("no record means a pre-existing standalone pane")
	}
}

// Terminal panes are dependents of the daemon (the idle stop must not pull it from under a TUI),
// and drain covers their running turns.
func TestTerminalSlotsAreDependentsAndDrained(t *testing.T) {
	d := newSharedDaemon(t)
	a := terminalMeta(t, "busy")
	sids.Write(session.UUID(a.Dir, a.Name), "ses_busy")
	t.Cleanup(func() { sids.Remove(session.UUID(a.Dir, a.Name)) })
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{a} })
	markSlotMode(session.UUID(a.Dir, a.Name), modeShared)
	t.Cleanup(func() { slotModes.Remove(session.UUID(a.Dir, a.Name)) })

	base := dependents()
	if base < 1 {
		t.Fatalf("dependents() = %d with a live Terminal slot", base)
	}
	// The switch changes the NEXT launch only: a pane still attached to the serve stays a dependent
	// (the idle stop must not pull the server from under its TUI).
	t.Setenv(standaloneEnv, "1")
	if got := dependents(); got != base {
		t.Errorf("flipping the switch dropped a running shared pane from the dependents: %d vs %d", got, base)
	}
	t.Setenv(standaloneEnv, "")

	// busyTerminals reads the global active list for the slot's session.
	active := ""
	d.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/active" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":{%s}}`, active)
			return
		}
		d.handle(w, r)
	})
	if got := busyTerminals(d.srv.URL); len(got) != 0 {
		t.Errorf("idle slot reported busy: %v", got)
	}
	active = `"ses_busy":{"type":"running"},"ses_other":{"type":"running"}`
	if got := busyTerminals(d.srv.URL); len(got) != 1 || got[0] != "ses_busy" {
		t.Errorf("busy terminals = %v, want only the slot's own session", got)
	}
}

// Stopping a Terminal slot interrupts its turn on the server: closing the pane does not.
func TestStopInterruptsTheTerminalTurn(t *testing.T) {
	d := newSharedDaemon(t)
	d.sessions["ses_x"] = map[string]string{}
	m := terminalMeta(t, "stopme")
	sids.Write(session.UUID(m.Dir, m.Name), "ses_x")
	markSlotMode(session.UUID(m.Dir, m.Name), modeShared)
	t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)); slotModes.Remove(session.UUID(m.Dir, m.Name)) })
	// Running() needs a daemon this supervisor believes in.
	supervisor.mu.Lock()
	supervisor.up = true
	supervisor.mu.Unlock()
	t.Cleanup(func() { supervisor.mu.Lock(); supervisor.up = false; supervisor.mu.Unlock() })

	if (agentImpl{}).GracefulStop(m) {
		t.Error("the pane still has to be killed by the caller")
	}
	if got := d.callsMatching("POST /api/session/ses_x/interrupt"); len(got) != 1 {
		t.Errorf("interrupt calls = %v", d.calls)
	}
	DropHandle(m.Name) // archive and trash go through here; the meta is what names the slot
	session.WriteMeta(m)
	DropHandle(m.Name)
	if got := d.callsMatching("POST /api/session/ses_x/interrupt"); len(got) != 2 {
		t.Errorf("DropHandle of a Terminal slot did not interrupt: %v", d.calls)
	}
}

// State reads for a shared Terminal slot go through the API (its store is the server's), while a
// standalone launch keeps the file reader.
func TestV2TargetForTerminalFollowsTheLaunchMode(t *testing.T) {
	newSharedDaemon(t)
	m := terminalMeta(t, "reader")
	markSlotMode(session.UUID(m.Dir, m.Name), modeShared)
	t.Cleanup(func() { slotModes.Remove(session.UUID(m.Dir, m.Name)) })
	if _, ok := v2TargetFor(m); !ok {
		t.Error("a shared Terminal slot must be read through the API")
	}
	// The switch is for the next launch: this pane is still on the serve, so its state still comes
	// from there, and the daemon keeps its shared password and plugin mode while it has such panes.
	t.Setenv(standaloneEnv, "1")
	if _, ok := v2TargetFor(m); !ok {
		t.Error("flipping the switch must not move a running shared pane's reads to SQLite")
	}
	if !sharedSlot(m) {
		t.Error("a pane keeps its launch mode")
	}
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{m} })
	if !sharedDaemonWanted() {
		t.Error("a daemon with a shared pane must keep starting the shared way after the switch is flipped")
	}
	terminalSlots = func() []session.Meta { return nil }
	if sharedDaemonWanted() {
		t.Error("with the switch flipped and no shared pane left, the daemon needs no shared mode")
	}
	// An unrecorded (pre-existing standalone) slot is read from SQLite.
	slotModes.Remove(session.UUID(m.Dir, m.Name))
	if _, ok := v2TargetFor(m); ok {
		t.Error("standalone slot keeps the SQLite reader")
	}
}

// A daemon the Terminal TUIs attach to keeps its password; any other gets a fresh one.
func TestServePasswordIsStableOnlyForTheSharedDaemon(t *testing.T) {
	if p, err := nextServePassword("kept", true); err != nil || p != "kept" {
		t.Errorf("shared: %q, %v — a restarted server must keep the password the TUIs hold", p, err)
	}
	p1, _ := nextServePassword("kept", false)
	p2, _ := nextServePassword("kept", false)
	if p1 == "kept" || p1 == p2 || len(p1) < 32 {
		t.Errorf("a non-shared daemon gets a fresh random password: %q %q", p1, p2)
	}
	if p, err := nextServePassword("", true); err != nil || p == "" {
		t.Errorf("first start: %q, %v", p, err)
	}
}

func TestSharedErrorsAreTypedStatus(t *testing.T) {
	d := newSharedDaemon(t)
	if _, err := getSharedSession(d.srv.URL, "ses_nope"); !errors.Is(err, statusError(http.StatusNotFound)) {
		t.Errorf("err = %v", err)
	}
}

// Two panes that both show the same untagged session: one wins, the other changes nothing. The
// tag cannot be compare-and-set (PATCH replaces), so the decision is an exclusive claim taken
// before anything is read or written.
func TestClaimSessionHasOneWinnerWhenPanesRace(t *testing.T) {
	d := newSharedDaemon(t)
	a, b := terminalMeta(t, "paneA"), terminalMeta(t, "paneB")
	session.WriteMeta(a)
	session.WriteMeta(b)
	sidA, sidB := session.UUID(a.Dir, a.Name), session.UUID(b.Dir, b.Name)
	oldEnv := paneEnv
	paneEnv = func(name string) (map[string]string, bool) { return map[string]string{"AF_SESSION_NAME": name}, true }
	t.Cleanup(func() { paneEnv = oldEnv })

	for round := 0; round < 25; round++ {
		ses := fmt.Sprintf("ses_race%d", round)
		d.mu.Lock()
		d.sessions[ses] = map[string]string{}
		d.mu.Unlock()
		var wg sync.WaitGroup
		results := make([]error, 2)
		start := make(chan struct{})
		for i, name := range []string{"paneA", "paneB"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i] = claimSession(d.srv.URL, name, ses, claimOpts{putEnv: true})
			}()
		}
		close(start)
		wg.Wait()
		wins := 0
		var winner string
		for i, err := range results {
			switch {
			case err == nil:
				wins++
				winner = []string{"paneA", "paneB"}[i]
			case !errors.Is(err, errClaimTaken):
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d winners (%v)", round, wins, results)
		}
		d.mu.Lock()
		wantSID := map[string]string{"paneA": sidA, "paneB": sidB}[winner]
		if got := d.sessions[ses][metaSID]; got != wantSID {
			t.Errorf("round %d: tag %q, winner %s", round, got, winner)
		}
		if env := d.envs[ses]; env["AF_SESSION_NAME"] != winner {
			t.Errorf("round %d: environment %v, winner %s", round, env, winner)
		}
		d.mu.Unlock()
		loser := map[string]string{"paneA": "paneB", "paneB": "paneA"}[winner]
		if err := claimSession(d.srv.URL, winner, ses, claimOpts{putEnv: true}); err != nil {
			t.Errorf("the winner's retry must be idempotent: %v", err)
		}
		if err := claimSession(d.srv.URL, loser, ses, claimOpts{putEnv: true}); !errors.Is(err, errClaimTaken) {
			t.Errorf("the loser later: %v", err)
		}
	}
	// Exactly one environment PUT and one PATCH per winning round, plus the winner's retry's PUT.
	if n := len(d.callsMatching("PATCH /api/session/ses_race3")); n != 1 {
		t.Errorf("session ses_race3 was tagged %d times", n)
	}
}

// A claim that fails before the tag is set is released, so the pane (or another) can try again;
// managed and foreign-tagged sessions are never claimed.
func TestClaimSessionReleasesAFailedClaimAndRefusesOthers(t *testing.T) {
	d := newSharedDaemon(t)
	a, b := terminalMeta(t, "ca"), terminalMeta(t, "cb")
	session.WriteMeta(a)
	session.WriteMeta(b)
	oldEnv := paneEnv
	paneEnv = func(name string) (map[string]string, bool) { return map[string]string{"N": name}, true }
	t.Cleanup(func() { paneEnv = oldEnv })
	d.sessions["ses_f"] = map[string]string{}
	d.failEnv = 1
	if err := claimSession(d.srv.URL, "ca", "ses_f", claimOpts{putEnv: true}); err == nil || errors.Is(err, errClaimTaken) {
		t.Fatalf("a refused PUT is a failure, not a loss: %v", err)
	}
	if err := claimSession(d.srv.URL, "cb", "ses_f", claimOpts{putEnv: true}); err != nil {
		t.Errorf("the failed claim must have been released: %v", err)
	}
	d.sessions["ses_m"] = map[string]string{metaManaged: "1"}
	d.sessions["ses_o"] = map[string]string{metaSID: "someone-else"}
	for _, ses := range []string{"ses_m", "ses_o"} {
		if err := claimSession(d.srv.URL, "ca", ses, claimOpts{putEnv: true}); !errors.Is(err, errClaimTaken) {
			t.Errorf("%s: %v", ses, err)
		}
		if _, has := d.envs[ses]; has {
			t.Errorf("%s got an environment", ses)
		}
	}
}

// The Go side's fork goes through the same exclusive claim as every other: the copy carries the
// source's tag from the moment it exists, so the source's pane can claim it first — and then the
// fork's slot must NOT overwrite that. It gets a new copy instead, and the first stays with the pane
// that claimed it (metadata, claim file and environment all agree).
func TestForkNeverOverwritesACopyTheSourcePaneAlreadyClaimed(t *testing.T) {
	d := newSharedDaemon(t)
	src := terminalMeta(t, "paneA")
	session.WriteMeta(src)
	sidA := session.UUID(src.Dir, src.Name)
	d.sessions["ses_src"] = sharedMetadata(sidA, "paneA")
	oldEnv := paneEnv
	paneEnv = func(name string) (map[string]string, bool) { return map[string]string{"AF_SESSION_NAME": name}, true }
	t.Cleanup(func() { paneEnv = oldEnv })

	var raced []string
	afterFork = func(addr, ses string) {
		if len(raced) > 0 {
			return // only the first copy is claimed by the source's pane
		}
		raced = append(raced, ses)
		// Pane A shows the new copy (it still carries A's tag) and claims it before B's launch does.
		if err := claimSession(addr, "paneA", ses, claimOpts{putEnv: true}); err != nil {
			t.Errorf("the source pane's claim: %v", err)
		}
	}
	t.Cleanup(func() { afterFork = func(string, string) {} })

	b := terminalMeta(t, "paneB")
	b.ForkFrom = "ses_src"
	sidB := session.UUID(b.Dir, b.Name)
	t.Cleanup(func() { sids.Remove(sidB); slotModes.Remove(sidB) })
	if _, err := (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	first, second := raced[0], sids.Read(sidB)
	if second == "" || second == first {
		t.Fatalf("B must end up on a copy of its own, got %q (first copy %q)", second, first)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sessions[first][metaSID] != sidA || d.envs[first]["AF_SESSION_NAME"] != "paneA" {
		t.Errorf("the copy A claimed was overwritten: %v %v", d.sessions[first], d.envs[first])
	}
	if d.sessions[second][metaSID] != sidB || d.sessions[second][metaName] != "paneB" {
		t.Errorf("B's copy is not tagged for B: %v", d.sessions[second])
	}
	// A's later claims of B's copy are refused, and so is B's of A's.
	d.mu.Unlock()
	if err := claimSession(d.srv.URL, "paneA", second, claimOpts{putEnv: true}); !errors.Is(err, errClaimTaken) {
		t.Errorf("A claiming B's copy: %v", err)
	}
	d.mu.Lock()
}

// The other order: B's claim wins; the source pane's later claim of that copy is refused although
// the copy still carried A's tag when A looked.
func TestForkClaimedFirstKeepsTheSourcePaneOut(t *testing.T) {
	d := newSharedDaemon(t)
	src := terminalMeta(t, "pA")
	session.WriteMeta(src)
	sidA := session.UUID(src.Dir, src.Name)
	d.sessions["ses_src"] = sharedMetadata(sidA, "pA")
	b := terminalMeta(t, "pB")
	b.ForkFrom = "ses_src"
	sidB := session.UUID(b.Dir, b.Name)
	t.Cleanup(func() { sids.Remove(sidB); slotModes.Remove(sidB) })
	if _, err := (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	oldEnv := paneEnv
	paneEnv = func(string) (map[string]string, bool) { return map[string]string{"X": "1"}, true }
	t.Cleanup(func() { paneEnv = oldEnv })
	if err := claimSession(d.srv.URL, "pA", sids.Read(sidB), claimOpts{putEnv: true}); !errors.Is(err, errClaimTaken) {
		t.Errorf("the source pane claimed B's copy: %v", err)
	}
}

func mutations(d *sharedDaemon) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

// A resume of a session another slot holds is refused BEFORE anything on it changes: switching the
// model of a conversation that is not this slot's would reach that slot's next turn. The refusal
// also leaves no claim record behind, so the rightful owner's own resume still works.
func TestForeignResumeHasNoSideEffectsAndLeavesNoOwner(t *testing.T) {
	d := newSharedDaemon(t)
	a, b := terminalMeta(t, "owner"), terminalMeta(t, "intruder")
	sidA, sidB := session.UUID(a.Dir, a.Name), session.UUID(b.Dir, b.Name)
	d.sessions["ses_source"] = sharedMetadata(sidA, "owner")
	sids.Write(sidB, "ses_source") // B's mapping points at A's conversation
	sids.Write(sidA, "ses_source")
	t.Cleanup(func() { sids.Remove(sidA); sids.Remove(sidB); slotModes.Remove(sidA); slotModes.Remove(sidB) })
	b.Model, b.Mode = "stub/foreign", "plan"
	if _, err := (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{}); err == nil {
		t.Fatal("a foreign resume must be refused")
	}
	if got := mutations(d); len(got) != 0 {
		t.Errorf("the refused resume changed things: %v", got)
	}
	// The rightful owner can still resume (the refusal released what it took).
	a.Model = "stub/m"
	if _, err := (agentImpl{}).BuildLaunch(a, agents.LaunchOpts{}); err != nil {
		t.Errorf("the owner's own resume was blocked: %v", err)
	}
}

// A claim record that cannot be trusted must not keep the rightful owner out: an empty one (a
// crash between creating and writing, from an older build), and one an abandoned claim of another
// slot left behind. A fresh record of another slot still holds.
func TestAbandonedClaimRecordsDoNotBlockTheRightfulOwner(t *testing.T) {
	d := newSharedDaemon(t)
	a := terminalMeta(t, "legit")
	sidA := session.UUID(a.Dir, a.Name)
	d.sessions["ses_x"] = sharedMetadata(sidA, "legit")
	sids.Write(sidA, "ses_x")
	t.Cleanup(func() { sids.Remove(sidA); slotModes.Remove(sidA) })
	record := filepath.Join(paths.AgentStateDir(), "opencode-claim", "ses_x")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, nil, 0o600); err != nil { // the crash state: created, never written
		t.Fatal(err)
	}
	if _, err := (agentImpl{}).BuildLaunch(a, agents.LaunchOpts{}); err != nil {
		t.Fatalf("an empty record blocked the owner: %v", err)
	}

	// Another slot's record: fresh holds, abandoned (old, and the tag never followed) does not.
	if err := os.WriteFile(record, []byte("someone-else"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.sessions["ses_x"] = map[string]string{} // untagged: that claim never completed
	if err := claimSessionFor(d.srv.URL, a, "ses_x", claimOpts{}); !errors.Is(err, errClaimTaken) {
		t.Errorf("a fresh record of another slot must hold: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(record, old, old); err != nil {
		t.Fatal(err)
	}
	if err := claimSessionFor(d.srv.URL, a, "ses_x", claimOpts{}); err != nil {
		t.Errorf("an abandoned record blocked the owner: %v", err)
	}
	// An old record of a slot whose tag DID follow is a completed claim, not an abandoned one.
	d.sessions["ses_y"] = sharedMetadata("someone-else", "x")
	rec2 := filepath.Join(filepath.Dir(record), "ses_y")
	_ = os.WriteFile(rec2, []byte("someone-else"), 0o600)
	_ = os.Chtimes(rec2, old, old)
	if err := claimSessionFor(d.srv.URL, a, "ses_y", claimOpts{}); !errors.Is(err, errClaimTaken) {
		t.Errorf("a completed claim must hold however old: %v", err)
	}
}

// A copy the launch cannot attribute must not be left behind: an API failure while claiming it
// deletes it; once it is claimed it is recorded before anything else can fail, so a retry resumes it.
func TestFailedForkNeverLeavesAnUnrecordedCopy(t *testing.T) {
	d := newSharedDaemon(t)
	d.sessions["ses_src"] = map[string]string{} // a source with no tag (standalone history)
	b := terminalMeta(t, "forker")
	b.ForkFrom = "ses_src"
	sidB := session.UUID(b.Dir, b.Name)
	t.Cleanup(func() { sids.Remove(sidB); slotModes.Remove(sidB) })

	d.failGet = map[string]bool{"ses_fork1": true}
	if _, err := (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{}); err == nil {
		t.Fatal("the launch must fail when the copy cannot be claimed")
	}
	d.mu.Lock()
	_, left := d.sessions["ses_fork1"]
	d.mu.Unlock()
	if left || sids.Read(sidB) != "" {
		t.Errorf("an unattributed copy was left (exists=%v, mapping=%q)", left, sids.Read(sidB))
	}

	// Claimed, then the model switch fails: the copy is the slot's and is recorded.
	d.failGet = nil
	d.failSwitch = true
	b.Model = "stub/m"
	if _, err := (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{}); err == nil {
		t.Fatal("a failed switch must fail the launch")
	}
	rec := sids.Read(sidB)
	if rec == "" {
		t.Fatal("a claimed copy must be recorded before the switch")
	}
	d.mu.Lock()
	tag := d.sessions[rec][metaSID]
	d.mu.Unlock()
	if tag != sidB {
		t.Errorf("recorded copy %s is tagged %q", rec, tag)
	}
	d.failSwitch = false
	before := len(d.callsMatching("POST /api/session/ses_src/fork"))
	if _, err := (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := len(d.callsMatching("POST /api/session/ses_src/fork")); got != before {
		t.Errorf("the retry forked again instead of resuming the recorded copy")
	}
}

// The cleanup of a copy that could not be claimed must not delete a conversation another pane
// claimed in the meantime: the source's pane sees the copy under the inherited tag and may take it
// between this slot's failed claim (record released) and the DELETE.
func TestFailedForkCleanupPreservesACopyAnotherPaneClaimed(t *testing.T) {
	d := newSharedDaemon(t)
	src := terminalMeta(t, "srcpane")
	session.WriteMeta(src)
	sidA := session.UUID(src.Dir, src.Name)
	d.sessions["ses_src"] = sharedMetadata(sidA, "srcpane")
	oldEnv := paneEnv
	paneEnv = func(name string) (map[string]string, bool) { return map[string]string{"N": name}, true }
	t.Cleanup(func() { paneEnv = oldEnv })
	b := terminalMeta(t, "forker2")
	b.ForkFrom = "ses_src"
	sidB := session.UUID(b.Dir, b.Name)
	t.Cleanup(func() { sids.Remove(sidB); slotModes.Remove(sidB) })

	d.failGet = map[string]bool{"ses_fork1": true} // B's claim fails on the API; its record is given back
	var aErr error
	claimed := false
	// The race, deterministic: the source pane's claim lands between B's release and B's cleanup.
	afterFork = func(addr, ses string) {}
	t.Cleanup(func() { afterFork = func(string, string) {} })
	oldDiscard := beforeDiscard
	beforeDiscard = func(addr, ses string) {
		d.mu.Lock()
		d.failGet = nil
		d.mu.Unlock()
		aErr = claimSession(addr, "srcpane", ses, claimOpts{putEnv: true})
		claimed = true
	}
	t.Cleanup(func() { beforeDiscard = oldDiscard })

	_, _ = (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{})
	if !claimed {
		t.Fatal("the race was not set up")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if aErr != nil {
		t.Fatalf("the source pane's claim: %v", aErr)
	}
	if got := d.sessions["ses_fork1"]; got == nil || got[metaSID] != sidA {
		t.Errorf("the cleanup deleted or altered a copy the source pane had claimed: %v", got)
	}
}

// And while the cleanup holds the record, a pane that tries to claim the copy is refused rather than
// racing the DELETE.
func TestFailedForkCleanupHoldsTheClaimUntilTheCopyIsGone(t *testing.T) {
	d := newSharedDaemon(t)
	src := terminalMeta(t, "srcpane2")
	session.WriteMeta(src)
	d.sessions["ses_src"] = sharedMetadata(session.UUID(src.Dir, src.Name), "srcpane2")
	b := terminalMeta(t, "forker3")
	b.ForkFrom = "ses_src"
	t.Cleanup(func() { sids.Remove(session.UUID(b.Dir, b.Name)); slotModes.Remove(session.UUID(b.Dir, b.Name)) })
	d.failGet = map[string]bool{"ses_fork1": true}
	var during error
	d.deleteGate = func(id string) {
		d.mu.Lock()
		d.failGet = nil
		d.mu.Unlock()
		during = claimSession(d.srv.URL, "srcpane2", id, claimOpts{})
	}
	_, _ = (agentImpl{}).BuildLaunch(b, agents.LaunchOpts{})
	if !errors.Is(during, errClaimTaken) {
		t.Errorf("a claim during the cleanup's DELETE must be refused, got %v", during)
	}
}

// stubTerminalEnsure makes ensureForTerminals fast and countable: no backoff, and every attempt
// answers with the next error from errs (nil once they run out).
func stubTerminalEnsure(t *testing.T, errs ...error) *int {
	t.Helper()
	calls := 0
	oldBackoff, oldEnsure := terminalEnsureBackoff, ensureTerminalServe
	terminalEnsureBackoff = []time.Duration{0, 0, 0}
	ensureTerminalServe = func() error {
		calls++
		if calls <= len(errs) {
			return errs[calls-1]
		}
		return nil
	}
	t.Cleanup(func() { terminalEnsureBackoff, ensureTerminalServe = oldBackoff, oldEnsure })
	return &calls
}

// A replacement serve that fails to start once must not strand the Terminal panes: nothing else
// retries for them.
func TestEnsureForTerminalsRetriesAFailedStart(t *testing.T) {
	m := session.Meta{Name: "oc-term", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{m} })
	timeout := errors.New("opencode serve did not start in time")
	calls := stubTerminalEnsure(t, timeout, timeout)
	ensureForTerminals("test")
	if *calls != 3 {
		t.Fatalf("attempts = %d, want 3 (two failures, then the start that works)", *calls)
	}
}

func TestEnsureForTerminalsGivesUp(t *testing.T) {
	m := session.Meta{Name: "oc-term", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{m} })
	timeout := errors.New("opencode serve did not start in time")

	t.Run("bounded", func(t *testing.T) {
		calls := stubTerminalEnsure(t, timeout, timeout, timeout, timeout, timeout)
		ensureForTerminals("test")
		if *calls != 4 {
			t.Fatalf("attempts = %d, want 4 (one plus one per backoff step)", *calls)
		}
	})
	t.Run("not connected", func(t *testing.T) {
		calls := stubTerminalEnsure(t, ErrNotConnected)
		ensureForTerminals("test")
		if *calls != 1 {
			t.Fatalf("attempts = %d, want 1: retrying cannot connect opencode", *calls)
		}
	})
	t.Run("no panes left", func(t *testing.T) {
		stubTerminalSlots(t, func() []session.Meta { return nil })
		calls := stubTerminalEnsure(t, timeout)
		ensureForTerminals("test")
		if *calls != 1 {
			t.Fatalf("attempts = %d, want 1: no pane needs the serve any more", *calls)
		}
	})
}
