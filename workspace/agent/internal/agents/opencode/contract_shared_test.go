//go:build contract

// Terminal sessions on one shared opencode 2 serve (issue #2120), end to end against the REAL
// binary: af's Supervisor, the real BuildLaunch, real tmux panes running the real TUI through
// `workspace-agent opencode-attach`, the shipped plugins, and a loopback stub provider standing
// in for the model (no credentials, no billing). Skipped on a v1 binary.
//
//	PATH=<dir with opencode 2>:$PATH go test -tags contract -run SharedServe ./internal/agents/opencode/
//
// CI runs it in the opencode 2 job of opencode-contract.yml.
// It needs tmux and node-free plugins only; go builds the workspace-agent it drives.
package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// sharedStub answers every chat request: a user text starting with ENVQ makes it call the shell
// tool once (printing the variables that tell the sessions apart), anything else gets "ok". It
// records the Authorization of each request, which is how the per-session engine token is seen.
type sharedStub struct {
	mu   sync.Mutex
	auth []string // "<text>|<authorization>"
}

func (s *sharedStub) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	text, afterTool := "", false
	for _, m := range body.Messages {
		if m.Role == "user" {
			text = fmt.Sprint(m.Content)
		}
		afterTool = m.Role == "tool"
	}
	s.mu.Lock()
	s.auth = append(s.auth, text+"|"+r.Header.Get("Authorization"))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(delta map[string]any, fin any) {
		b, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "created": 0, "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	if strings.HasPrefix(text, "ENVQ") && !afterTool {
		send(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": `{"command":"env | grep -E '^(AF_SESSION_SID|AF_SESSION_NAME|AF_ENGINE_TOKEN|OPENCODE_SERVER|PANE_MARK|TOOLCHAIN_MARK)=' | sort","description":"env"}`}}}}, nil)
		send(map[string]any{}, "tool_calls")
	} else {
		send(map[string]any{"role": "assistant", "content": "ok"}, nil)
		send(map[string]any{}, "stop")
	}
	b, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "created": 0, "model": "m", "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
}

func (s *sharedStub) authFor(textPrefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.auth {
		if t, au, _ := strings.Cut(a, "|"); strings.HasPrefix(t, textPrefix) {
			out = append(out, au)
		}
	}
	return out
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() {
			copyDir(t, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rssKB(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if f := strings.Fields(ln); len(f) >= 2 && f[0] == "VmRSS:" {
			n, _ := strconv.Atoi(f[1])
			return n
		}
	}
	return 0
}

func descendants(root int) []int {
	out := []int{root}
	for i := 0; i < len(out); i++ {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", out[i], out[i]))
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func TestContractSharedServeTerminalSessions(t *testing.T) {
	requireOpencode(t)
	if !strings.HasPrefix(strings.TrimPrefix(opencodeVersion(t), "opencode v"), "2.") {
		skipUnlessV2Required(t, "needs opencode 2")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		skipUnlessV2Required(t, "needs tmux")
	}
	stub := &sharedStub{}
	llm := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(func() { llm.CloseClientConnections(); llm.Close() })

	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"AF_SESSIONS_DIR": t.TempDir(),
	} {
		t.Setenv(k, v)
	}
	testguard.IsolateTmux(t)

	cfgDir := filepath.Join(home, ".config", "opencode")
	cfg, _ := json.Marshal(map[string]any{
		"model": "stub/m", "small_model": "stub/m", "enabled_providers": []string{"stub"},
		"permission": map[string]any{"*": "allow"},
		"provider": map[string]any{"stub": map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": "stub",
			"options": map[string]any{"baseURL": llm.URL + "/v1", "apiKey": "{env:AF_ENGINE_TOKEN}"},
			"models":  map[string]any{"m": map[string]any{"name": "m", "tool_call": true, "limit": map[string]any{"context": 8000, "output": 1000}}},
		}},
	})
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "service.json"), []byte(`{"disabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginSrc := filepath.Join("..", "..", "..", "..", "opencode-plugin")
	for _, f := range []string{"agent-fleet-status.js", "agent-fleet-shared.js"} {
		b, err := os.ReadFile(filepath.Join(pluginSrc, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(cfgDir, "plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "plugin", f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyDir(t, filepath.Join(pluginSrc, "agent-fleet-tui"), filepath.Join(cfgDir, "plugins", "agent-fleet-tui"))

	// The real workspace-agent (attach), behind a wrapper that answers the engine-token query the
	// way the Control Plane would: one token per session name.
	bin := t.TempDir()
	real := filepath.Join(bin, "workspace-agent.real")
	build := exec.Command("go", "build", "-o", real, ".")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build workspace-agent: %v\n%s", err, out)
	}
	statusLog := filepath.Join(bin, "status.log")
	wrapper := "#!/bin/sh\nif [ \"$1\" = opencode-engine-token ]; then echo \"tok-$2\"; exit 0; fi\n" +
		"if [ \"$1\" = session-status ]; then echo \"$*\" >> " + statusLog + "; exit 0; fi\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "workspace-agent"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_WORKSPACE_AGENT_BIN", filepath.Join(bin, "workspace-agent"))

	oldPref, oldEngine := UsagePref, EngineEnv
	UsagePref = func() string { return UsageFree }
	EngineEnv = func(name string) []string { return []string{EngineProviderKeyEnv + "=tok-" + name} }
	t.Cleanup(func() { UsagePref, EngineEnv = oldPref, oldEngine })

	addr := "http://127.0.0.1:" + freePort(t)
	t.Setenv(serveAddrEnv, addr)
	detectedMajor.Store(int32(apiV1))
	t.Cleanup(func() { Serve().Shutdown(); detectedMajor.Store(int32(apiV1)); resetServeAuth("") })
	if !sharedTUI() {
		t.Fatal("sharedTUI() is false on an opencode 2 binary")
	}

	work := t.TempDir()
	launch := func(name string, extra func(*session.Meta)) session.Meta {
		t.Helper()
		m := session.Meta{Name: name, Dir: work, Kind: session.KindOpencode, Driver: session.DriverTUI, Model: "stub/m"}
		if extra != nil {
			extra(&m)
		}
		plan, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
		if err != nil {
			t.Fatalf("BuildLaunch %s: %v", name, err)
		}
		session.WriteMeta(m)
		if strings.Contains(plan.Program, servePassword()) {
			t.Fatalf("the server password is on the command line: %s", plan.Program)
		}
		args := []string{"new-session", "-d", "-s", session.TmuxName(name), "-x", "140", "-y", "40", "-c", plan.Cwd}
		for _, kv := range append(plan.Env, "AF_SESSION_NAME="+name, "PANE_MARK="+name) {
			args = append(args, "-e", kv)
		}
		args = append(args, "export TOOLCHAIN_MARK=tc-"+name+"; "+plan.Program)
		if out, err := tmuxx.Cmd(args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s: %v %s", name, err, out)
		}
		return m
	}
	pane := func(name string) string { return tmuxx.CapturePane(session.TmuxName(name)) }
	waitPane := func(name, want string) {
		t.Helper()
		for d := time.Now().Add(40 * time.Second); time.Now().Before(d); time.Sleep(300 * time.Millisecond) {
			if strings.Contains(pane(name), want) {
				return
			}
		}
		t.Fatalf("pane %s never showed %q:\n%s", name, want, pane(name))
	}

	api := func(method, path string, in, out any) error {
		t.Helper()
		return apiJSON(method, addr+path, in, out)
	}
	paneTUIPID := func(name string) int {
		pid, _ := strconv.Atoi(strings.TrimSpace(func() string {
			out, _ := tmuxx.Cmd("display-message", "-p", "-t", tmuxx.SessionPaneID(session.TmuxName(name)), "#{pane_pid}").Output()
			return string(out)
		}()))
		return pid
	}
	typeInto := func(name string, keys ...string) {
		t.Helper()
		args := append([]string{"send-keys", "-t", tmuxx.SessionPaneID(session.TmuxName(name))}, keys...)
		if out, err := tmuxx.Cmd(args...).CombinedOutput(); err != nil {
			t.Fatalf("send-keys %s: %v %s", name, err, out)
		}
	}
	// shellOutputs are the environments the session's shell tool printed, oldest first.
	shellOutputs := func(ses string) []map[string]string {
		var w struct {
			Data []struct {
				Content []struct {
					Type  string `json:"type"`
					State struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"state"`
				} `json:"content"`
			} `json:"data"`
		}
		if err := api("GET", "/api/session/"+ses+"/message?order=asc&limit=200", nil, &w); err != nil {
			return nil
		}
		var out []map[string]string
		for _, m := range w.Data {
			for _, c := range m.Content {
				if c.Type != "tool" || len(c.State.Content) == 0 {
					continue
				}
				env := map[string]string{}
				for _, ln := range strings.Split(c.State.Content[0].Text, "\n") {
					if k, v, ok := strings.Cut(ln, "="); ok {
						env[k] = v
					}
				}
				out = append(out, env)
			}
		}
		return out
	}
	waitShell := func(ses string, n int) []map[string]string {
		t.Helper()
		for d := time.Now().Add(40 * time.Second); time.Now().Before(d); time.Sleep(300 * time.Millisecond) {
			if o := shellOutputs(ses); len(o) >= n {
				return o
			}
		}
		t.Fatalf("session %s never ran %d shell command(s)", ses, n)
		return nil
	}
	// wantEnv: the shell of slot name sees ITS pane's environment and nothing of the server's.
	wantEnv := func(label string, env map[string]string, name string) {
		t.Helper()
		sid := session.UUID(work, name)
		for k, want := range map[string]string{"AF_SESSION_SID": sid, "AF_SESSION_NAME": name, "PANE_MARK": name, "TOOLCHAIN_MARK": "tc-" + name, "AF_ENGINE_TOKEN": "tok-" + name} {
			if env[k] != want {
				t.Errorf("%s: shell %s = %q, want %q (%v)", label, k, env[k], want, env)
			}
		}
		for k := range env {
			if strings.HasPrefix(k, "OPENCODE_SERVER") {
				t.Errorf("%s: the server credential %s reached the shell", label, k)
			}
		}
	}
	sessionsOf := func(sid string) []string {
		var w struct {
			Data []struct {
				ID       string            `json:"id"`
				Metadata map[string]string `json:"metadata"`
			} `json:"data"`
		}
		if err := api("GET", "/api/session?directory="+work+"&limit=100", nil, &w); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range w.Data {
			if s.Metadata[metaSID] == sid {
				ids = append(ids, s.ID)
			}
		}
		return ids
	}

	// --- three Terminal panes on one serve ---
	names := []string{"t1", "t2", "t3"}
	start := time.Now()
	for _, n := range names {
		launch(n, nil)
	}
	genWarm := Serve().Generation()
	t.Logf("3 panes launched in %v (daemon start included)", time.Since(start))
	if g := Serve().Generation(); g != genWarm {
		t.Fatalf("generation = %d, want one daemon for three panes", g)
	}
	pw := servePassword()
	first := map[string]string{}
	for _, n := range names {
		waitPane(n, "auto")
		first[n] = sids.Read(session.UUID(work, n))
		if first[n] == "" {
			t.Fatalf("%s: slot not recorded", n)
		}
		if got := sessionsOf(session.UUID(work, n)); len(got) != 1 || got[0] != first[n] {
			t.Fatalf("%s: tagged sessions %v, slot %s", n, got, first[n])
		}
	}
	for _, n := range names {
		typeInto(n, "ENVQ one", "Enter")
	}
	for _, n := range names {
		o := waitShell(first[n], 1)
		wantEnv(n+" first", o[0], n)
	}
	// Every request of those turns (the model's two steps, the title) carried its own session's
	// engine token — never the workspace-scoped one the server's environment holds.
	{
		seen := map[string]int{}
		for _, a := range stub.authFor("ENVQ one") {
			seen[a]++
		}
		for _, n := range names {
			if seen["Bearer tok-"+n] < 2 {
				t.Errorf("engine token of %s not on its requests: %v", n, seen)
			}
		}
		if len(seen) != len(names) {
			t.Errorf("requests carried tokens other than the per-session ones: %v", seen)
		}
	}
	// The status plugin reported each turn under the sid of the slot it ran in. The idle report
	// trails the shell output under CI load, so wait for it rather than reading the log once.
	for _, n := range names {
		sid := session.UUID(work, n)
		read := func() string { b, _ := os.ReadFile(statusLog); return string(b) }
		if miss, last := awaitStatusReports(read, sid, []string{"working", "idle"}, statusWaitTimeout, 100*time.Millisecond); len(miss) > 0 {
			t.Errorf("no %v report for %s within %v in:\n%s", miss, n, statusWaitTimeout, last)
		}
	}
	for _, n := range names {
		m := session.Meta{Name: n, Dir: work, Kind: session.KindOpencode, Driver: session.DriverTUI}
		if !pollUntil(statusWaitTimeout, 100*time.Millisecond, func() bool { return LiveState(m) == "idle" }) {
			t.Errorf("%s reads %q after its turn (waited %v)", n, LiveState(m), statusWaitTimeout)
		}
	}

	// --- a session made with /new inside the TUI is tagged and gets its pane's environment ---
	typeInto("t1", "/new")
	time.Sleep(time.Second) // the slash menu has to be up before the Enter picks its entry
	typeInto("t1", "Enter")
	time.Sleep(2 * time.Second)
	typeInto("t1", "ENVQ second", "Enter")
	var second string
	for d := time.Now().Add(40 * time.Second); time.Now().Before(d) && second == ""; time.Sleep(300 * time.Millisecond) {
		for _, id := range sessionsOf(session.UUID(work, "t1")) {
			if id != first["t1"] {
				second = id
			}
		}
	}
	if second == "" {
		var all map[string]any
		_ = api("GET", "/api/session?directory="+work+"&limit=100", nil, &all)
		dump, _ := json.Marshal(all)
		t.Fatalf("the /new session was never tagged: %s\n%s", dump, pane("t1"))
	}
	o := waitShell(second, 1)
	wantEnv("t1 /new", o[0], "t1")
	if got := sids.Read(session.UUID(work, "t1")); got != second {
		t.Errorf("slot t1 = %s after /new, want %s", got, second)
	}
	for d := time.Now().Add(10 * time.Second); time.Now().Before(d); time.Sleep(200 * time.Millisecond) {
		if b, _ := os.ReadFile(statusLog); strings.Count(string(b), "session-status idle "+session.UUID(work, "t1")+"\n") >= 2 {
			break
		}
	}
	if b, _ := os.ReadFile(statusLog); strings.Count(string(b), "session-status working "+session.UUID(work, "t1")+"\n") < 2 {
		t.Errorf("the /new turn was not reported under t1:\n%s", b)
	}

	// --- fork keeps identity: the copy belongs to the new slot, not to the source ---
	forkMeta := launch("t4", func(m *session.Meta) { m.ForkFrom = first["t2"] })
	waitPane("t4", "auto")
	forkSes := sids.Read(session.UUID(work, forkMeta.Name))
	if forkSes == "" || forkSes == first["t2"] {
		t.Fatalf("fork slot = %q (source %s)", forkSes, first["t2"])
	}
	if got := sessionsOf(session.UUID(work, "t2")); len(got) != 1 || got[0] != first["t2"] {
		t.Errorf("the fork changed t2's tagged sessions: %v", got)
	}
	if got := sessionsOf(session.UUID(work, "t4")); len(got) != 1 || got[0] != forkSes {
		t.Errorf("the fork is not tagged for t4: %v", got)
	}
	typeInto("t4", "ENVQ forked", "Enter")
	fo := waitShell(forkSes, 2) // the copied turn of t2 plus the new one
	wantEnv("t4 fork", fo[len(fo)-1], "t4")

	// --- resume keeps identity: relaunching a stopped slot attaches to ITS session ---
	_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(session.TmuxName("t3"))).Run()
	launch("t3", nil)
	waitPane("t3", "auto")
	if got := sids.Read(session.UUID(work, "t3")); got != first["t3"] {
		t.Errorf("t3 resumed %s, want its own %s", got, first["t3"])
	}
	typeInto("t3", "ENVQ resumed", "Enter")
	ro := waitShell(first["t3"], 2)
	wantEnv("t3 resume", ro[1], "t3")

	// --- RSS ---
	rss := func(label string, pids []int) {
		sum := 0
		for _, p := range pids {
			sum += rssKB(p)
		}
		t.Logf("RSS %-8s %5d MB", label, sum/1024)
	}
	serverPID := daemonPID(addr)
	rss("serve", []int{serverPID})
	for _, n := range []string{"t1", "t2", "t3", "t4"} {
		rss("tui "+n, descendants(paneTUIPID(n)))
	}

	// --- a new session nobody has tagged yet is held, then confirmed by the Agent once tagged ---
	{
		var created struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := api("POST", "/api/session", map[string]any{"location": map[string]string{"directory": work},
			"model": map[string]string{"providerID": "stub", "id": "m"}, "permissions": allowAll}, &created); err != nil {
			t.Fatal(err)
		}
		ses := created.Data.ID
		if err := api("POST", "/api/session/"+ses+"/prompt", map[string]string{"text": "ENVQ gate"}, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		if o := shellOutputs(ses); len(o) != 0 {
			t.Fatalf("the shell ran for an untagged new session before it was claimed: %v", o)
		}
		if err := api("PATCH", "/api/session/"+ses, map[string]any{"metadata": sharedMetadata(session.UUID(work, "t3"), "t3")}, nil); err != nil {
			t.Fatal(err)
		}
		tagged := time.Now()
		o := waitShell(ses, 1)
		if d := time.Since(tagged); d > 4*time.Second {
			t.Errorf("the held command took %v after the tag", d)
		}
		wantEnv("t3 claimed", o[0], "t3")
	}

	// --- a child, a fork and a session af cannot place: the gate asks the Agent per session ---
	{
		// A subagent's session inherits the tags and permissions, not the environment.
		var kid struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := api("POST", "/api/session", map[string]any{"parentID": first["t2"], "location": map[string]string{"directory": work},
			"model": map[string]string{"providerID": "stub", "id": "m"}}, &kid); err != nil {
			t.Fatal(err)
		}
		if err := api("POST", "/api/session/"+kid.Data.ID+"/prompt", map[string]string{"text": "ENVQ child"}, nil); err != nil {
			t.Fatal(err)
		}
		ko := waitShell(kid.Data.ID, 1)
		wantEnv("t2 child", ko[0], "t2")

		// A fork made inside a TUI (or any client) copies the tag, not the environment.
		var cp struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := api("POST", "/api/session/"+first["t2"]+"/fork", map[string]string{}, &cp); err != nil {
			t.Fatal(err)
		}
		if err := api("POST", "/api/session/"+cp.Data.ID+"/prompt", map[string]string{"text": "ENVQ tuifork"}, nil); err != nil {
			t.Fatal(err)
		}
		co := waitShell(cp.Data.ID, 2)
		wantEnv("t2 fork", co[len(co)-1], "t2")

		// Tagged for a slot with no pane: the Agent cannot confirm, so the command is refused and
		// the server's environment is never printed.
		var ghost struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := api("POST", "/api/session", map[string]any{"location": map[string]string{"directory": work},
			"model": map[string]string{"providerID": "stub", "id": "m"}, "metadata": sharedMetadata("ghost-sid", "ghost")}, &ghost); err != nil {
			t.Fatal(err)
		}
		if err := api("POST", "/api/session/"+ghost.Data.ID+"/prompt", map[string]string{"text": "ENVQ ghost"}, nil); err != nil {
			t.Fatal(err)
		}
		var raw string
		for d := time.Now().Add(40 * time.Second); time.Now().Before(d); time.Sleep(500 * time.Millisecond) {
			req, _ := http.NewRequest("GET", addr+"/api/session/"+ghost.Data.ID+"/message?order=asc&limit=50", nil)
			if res, err := serveClient.Do(req); err == nil {
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				raw = string(b)
			}
			if strings.Contains(raw, "could not set this session") {
				break
			}
		}
		if !strings.Contains(raw, "could not set this session") || strings.Contains(raw, "OPENCODE_SERVER_PASSWORD") || strings.Contains(raw, "AF_ENGINE_TOKEN=") {
			t.Errorf("the command of an unplaceable session was not refused cleanly: %.600s", raw)
		}
	}

	// --- a server restart: environments come back, TUIs recover, sessions are confirmed again ---
	// The switch only decides the next launch: flipping it must not strand the panes already on the
	// serve (password, plugin mode, restore).
	t.Setenv(standaloneEnv, "1")
	genBefore := Serve().Generation()
	if !Serve().Restart("contract") {
		t.Fatal("the daemon was not replaced")
	}
	// Restart drops the daemon; the reconcile it kicks (a Terminal pane depends on it) brings the
	// replacement up. Wait for it the way the Console would: by asking.
	for d := time.Now().Add(60 * time.Second); Serve().Generation() == genBefore && time.Now().Before(d); time.Sleep(200 * time.Millisecond) {
	}
	if Serve().Generation() == genBefore {
		t.Fatal("no replacement daemon came up")
	}
	if servePassword() != pw {
		t.Error("the server password changed across generations: attached TUIs could not reconnect")
	}
	for _, n := range []string{"t2", "t4"} {
		typeInto(n, "ENVQ after restart", "Enter")
	}
	ao := waitShell(first["t2"], 2)
	wantEnv("t2 after restart", ao[1], "t2")
	fo = waitShell(forkSes, 3)
	wantEnv("t4 after restart", fo[len(fo)-1], "t4")
	// A pane whose TUI plugin re-sends the environment for the /new session it claimed.
	typeInto("t1", "ENVQ third", "Enter")
	so := waitShell(second, 2)
	wantEnv("t1 /new after restart", so[1], "t1")
	rss("serve+", []int{daemonPID(addr)})
}
