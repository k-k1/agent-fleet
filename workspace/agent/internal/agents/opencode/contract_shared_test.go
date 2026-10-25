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
	"bytes"
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
	// command, when set, is what an ENVQ turn runs in the shell instead of the variable filter.
	command string
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
		args := `{"command":"env | grep -E '^(AF_SESSION_SID|AF_SESSION_NAME|AF_ENGINE_TOKEN|OPENCODE_SERVER|PANE_MARK|TOOLCHAIN_MARK)=' | sort","description":"env"}`
		if s.command != "" {
			b, _ := json.Marshal(map[string]string{"command": s.command, "description": "env"})
			args = string(b)
		}
		send(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": args}}}}, nil)
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

// waitHomeVacated waits until no other process runs with HOME=home, and reports the ones that
// outlive within. It never kills them: a PID can be reused between the last scan and a signal,
// and on a shared host that signal would land on another session's process. It matches on the
// environment rather than on pane PIDs: that also covers the tmux server, a pane killed
// mid-test and anything a TUI spawned. A zombie's environ reads empty, so it counts as gone.
func waitHomeVacated(t *testing.T, home string, within time.Duration) {
	t.Helper()
	want := []byte("\x00HOME=" + home + "\x00")
	holders := func() []int {
		var pids []int
		ents, _ := os.ReadDir("/proc")
		for _, e := range ents {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid == os.Getpid() {
				continue
			}
			b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
			if err == nil && bytes.Contains(append([]byte{0}, b...), want) {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	start, left := time.Now(), holders()
	n := len(left)
	for len(left) > 0 && time.Since(start) < within {
		time.Sleep(100 * time.Millisecond)
		left = holders()
	}
	if len(left) == 0 {
		t.Logf("%d process(es) still ran with the test HOME after kill-server; gone in %v", n, time.Since(start).Round(time.Millisecond))
		return
	}
	t.Errorf("processes still running with HOME=%s %v after kill-server: %v", home, within, left)
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
	sock := testguard.IsolateTmux(t)
	// Registered after home's TempDir, so it runs before that RemoveAll (cleanups are LIFO).
	// kill-server returns once SIGHUP is sent, and a TUI still shutting down writes under
	// $HOME/.local while RemoveAll walks it: "directory not empty" (#2544).
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
		waitHomeVacated(t, home, 30*time.Second)
	})

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
	// dumpPanes captures every pane a failure may involve: one TUI failing is often another's cue.
	dumpPanes := func() string {
		var panes strings.Builder
		for _, pn := range []string{"t1", "t2", "t3", "t4"} {
			if c := pane(pn); c != "" {
				fmt.Fprintf(&panes, "--- pane %s ---\n%s\n", pn, c)
			}
		}
		return panes.String()
	}
	// A TUI cold-starting next to the serve and its siblings is slow on a loaded host, so the wait is generous; a TUI that never
	// comes up fails with the captures of every pane, not only the one waited on.
	waitPane := func(name, want string) {
		t.Helper()
		for d := time.Now().Add(90 * time.Second); time.Now().Before(d); time.Sleep(300 * time.Millisecond) {
			if strings.Contains(pane(name), want) {
				return
			}
		}
		t.Fatalf("pane %s never showed %q (serve generation %d):\n%s", name, want, Serve().Generation(), dumpPanes())
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
		var raw any
		_ = api("GET", "/api/session/"+ses+"/message?order=asc&limit=200", nil, &raw)
		msgs, _ := json.Marshal(raw)
		t.Fatalf("session %s never ran %d shell command(s); had %d\nmessages: %.1500s\n%s", ses, n, len(shellOutputs(ses)), msgs, dumpPanes())
		return nil
	}
	// userMessages counts the session's user messages whose text is exactly text (exact, so "ENVQ
	// one" never counts "ENVQ one more"). A failed read returns -1: unknown is not zero, or a blip
	// right after a daemon swap would read as "nothing sent" and trigger a duplicate Enter.
	userMessages := func(ses, text string) int {
		var w struct {
			Data []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"data"`
		}
		if err := api("GET", "/api/session/"+ses+"/message?order=asc&limit=200", nil, &w); err != nil {
			return -1
		}
		n := 0
		for _, m := range w.Data {
			if m.Type == "user" && m.Text == text {
				n++
			}
		}
		return n
	}
	// submitPrompt types text into pane name and presses Enter until the server holds the user
	// message, and returns how many such messages the session must hold afterwards. A TUI that has
	// just reconnected to a replacement daemon can take the text and swallow the Enter, leaving the
	// prompt filled and nothing sent; a second Enter is then the input the member would give. It is
	// only sent while the text still sits in the prompt and the server, asked again right before,
	// has no such message, so a slow first Enter is never doubled. Worst case 3 x
	// statusWaitTimeout per call; budget for the sum when changing either.
	var confirmPrompt func(name, ses, text string, want int) int
	submitPrompt := func(name, ses, text string) int {
		t.Helper()
		base := userMessages(ses, text)
		if base < 0 {
			base = 0
		}
		typeInto(name, text, "Enter")
		return confirmPrompt(name, ses, text, base+1)
	}
	// confirmPrompt waits for the server to hold want user messages text, pressing Enter again while
	// the text still sits in the prompt (3 times at most), and returns want.
	confirmPrompt = func(name, ses, text string, want int) int {
		t.Helper()
		for try := 1; try <= 3; try++ {
			if pollUntil(statusWaitTimeout, 200*time.Millisecond, func() bool { return userMessages(ses, text) >= want }) {
				return want
			}
			n := userMessages(ses, text)
			if n >= want {
				return want
			}
			if !strings.Contains(pane(name), text) {
				return want // the prompt was cleared: the turn is on its way, waitShell reports if it is not
			}
			t.Logf("%s: %q still in the prompt after %v, pressing Enter again (try %d)", name, text, statusWaitTimeout, try)
			typeInto(name, "Enter")
		}
		t.Fatalf("%s: %q never reached the server after 3 Enters (user messages %d, want %d, serve generation %d):\n%s", name, text, userMessages(ses, text), want, Serve().Generation(), dumpPanes())
		return want
	}
	// wantSentOnce fails when a retried Enter submitted the prompt twice.
	wantSentOnce := func(name, ses, text string, want int) {
		t.Helper()
		if got := userMessages(ses, text); got != want {
			t.Errorf("%s: session %s holds %d user message(s) %q, want %d", name, ses, got, text, want)
		}
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
	sentOne := map[string]int{}
	for _, n := range names {
		sentOne[n] = submitPrompt(n, first[n], "ENVQ one")
	}
	for _, n := range names {
		o := waitShell(first[n], 1)
		wantEnv(n+" first", o[0], n)
		wantSentOnce(n, first[n], "ENVQ one", sentOne[n])
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
	// The session id is only known now, so submitPrompt could not type it; the Enter may have been
	// dropped while the TUI switched sessions.
	wNew := confirmPrompt("t1", second, "ENVQ second", 1)
	o := waitShell(second, 1)
	wantEnv("t1 /new", o[0], "t1")
	wantSentOnce("t1", second, "ENVQ second", wNew)
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
	wForked := submitPrompt("t4", forkSes, "ENVQ forked")
	fo := waitShell(forkSes, 2) // the copied turn of t2 plus the new one
	wantEnv("t4 fork", fo[len(fo)-1], "t4")
	wantSentOnce("t4", forkSes, "ENVQ forked", wForked)

	// --- resume keeps identity: relaunching a stopped slot attaches to ITS session ---
	_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(session.TmuxName("t3"))).Run()
	launch("t3", nil)
	waitPane("t3", "auto")
	if got := sids.Read(session.UUID(work, "t3")); got != first["t3"] {
		t.Errorf("t3 resumed %s, want its own %s", got, first["t3"])
	}
	wResumed := submitPrompt("t3", first["t3"], "ENVQ resumed")
	ro := waitShell(first["t3"], 2)
	wantEnv("t3 resume", ro[1], "t3")
	wantSentOnce("t3", first["t3"], "ENVQ resumed", wResumed)

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
	// The replacement daemon is up, but a TUI reconnects on its own schedule and may drop the Enter
	// of a prompt typed in that window.
	w2 := submitPrompt("t2", first["t2"], "ENVQ after restart")
	w4 := submitPrompt("t4", forkSes, "ENVQ after restart")
	ao := waitShell(first["t2"], 2)
	wantEnv("t2 after restart", ao[1], "t2")
	wantSentOnce("t2", first["t2"], "ENVQ after restart", w2)
	fo = waitShell(forkSes, 3)
	wantEnv("t4 after restart", fo[len(fo)-1], "t4")
	wantSentOnce("t4", forkSes, "ENVQ after restart", w4)
	// A pane whose TUI plugin re-sends the environment for the /new session it claimed.
	w1 := submitPrompt("t1", second, "ENVQ third")
	so := waitShell(second, 2)
	wantEnv("t1 /new after restart", so[1], "t1")
	wantSentOnce("t1", second, "ENVQ third", w1)
	rss("serve+", []int{daemonPID(addr)})
}
