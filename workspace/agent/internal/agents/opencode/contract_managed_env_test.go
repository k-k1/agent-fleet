//go:build contract

// A Managed opencode session's shell sees its own AF_WORK_DIR and AF_SESSION_NAME and an
// otherwise unchanged environment (#2710), against the REAL opencode 2 binary: af's Supervisor
// starts the shared serve, the real managed driver creates the sessions, the shipped env-gate
// plugin asks the real workspace-agent before each shell, and a loopback stub provider stands in
// for the model. HOME, the XDG roots, PATH and the Agent's session store are all throwaway.
// Skipped on a v1 binary (no per-session channel there).
//
//	PATH=<dir with opencode 2>:$PATH go test -tags contract -run ManagedShellEnv ./internal/agents/opencode/
package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestContractManagedShellEnvPerSessionAndAcrossRestart(t *testing.T) {
	requireOpencode(t)
	if !strings.HasPrefix(strings.TrimPrefix(opencodeVersion(t), "opencode v"), "2.") {
		skipUnlessV2Required(t, "needs opencode 2")
	}
	stub := &sharedStub{command: "env"}
	llm := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(func() { llm.CloseClientConnections(); llm.Close() })

	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"AF_SESSIONS_DIR": t.TempDir(),
		// What the shell must keep: a toolchain export the server was started with. The
		// secret-looking one is in the server's environment today, so the shell sees it today.
		"TOOLCHAIN_MARK": "tc-server", "OPENAI_API_KEY": "sk-probe-not-real",
	} {
		t.Setenv(k, v)
	}
	// The whole sandbox is gone when the test is: a serve still shutting down writes under home.
	cfgDir := filepath.Join(home, ".config", "opencode")
	cfg, _ := json.Marshal(map[string]any{
		"model": "stub/m", "small_model": "stub/m", "enabled_providers": []string{"stub"},
		"permission": map[string]any{"*": "allow"},
		"provider": map[string]any{"stub": map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": "stub",
			"options": map[string]any{"baseURL": llm.URL + "/v1", "apiKey": "x"},
			"models":  map[string]any{"m": map[string]any{"name": "m", "tool_call": true, "limit": map[string]any{"context": 8000, "output": 1000}}},
		}},
	})
	if err := os.MkdirAll(filepath.Join(cfgDir, "plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	plugin, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "opencode-plugin", "agent-fleet-shared.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "plugin", "agent-fleet-shared.js"), plugin, 0o644); err != nil {
		t.Fatal(err)
	}

	// The real workspace-agent is what the plugin runs; PATH gets a private bin in front.
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "workspace-agent"), ".")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build workspace-agent: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_WORKSPACE_AGENT_BIN", filepath.Join(bin, "workspace-agent"))

	oldPref := UsagePref
	UsagePref = func() string { return UsageFree }
	t.Cleanup(func() { UsagePref = oldPref })

	addr := "http://127.0.0.1:" + freePort(t)
	t.Setenv(serveAddrEnv, addr)
	detectedMajor.Store(int32(apiV1))
	t.Cleanup(func() { Serve().Shutdown(); detectedMajor.Store(int32(apiV1)); resetServeAuth("") })

	work := t.TempDir()
	start := func(name string) *threadHandle {
		t.Helper()
		m := session.Meta{Name: name, Dir: work, Kind: session.KindOpencode, Driver: session.DriverManaged}
		session.WriteMeta(m)
		h, err := managedDriver{}.resume(m)
		if err != nil {
			t.Fatalf("resume %s: %v", name, err)
		}
		t.Cleanup(func() { DropHandle(name) })
		return h.(*threadHandle)
	}
	st := agents.ThreadSettings{Model: "stub/m"}
	// shell runs `env` in the session as one turn and returns what THAT turn's command printed:
	// the output is matched by the turn's nonce, never by "the latest one", so a refused command
	// cannot pass on an earlier generation's output. The turn must also end without an error.
	shell := func(h *threadHandle, nonce string) (map[string]string, error) {
		req, err := newTurnRequest(h.addr, h.ses, work, agents.TurnInput{Prompt: "ENVQ " + nonce}, st)
		if err != nil {
			return nil, err
		}
		res, err := sendTurn(h.addr, h.ses, work, req, st)
		if err != nil {
			return nil, fmt.Errorf("turn %s: %w", nonce, err)
		}
		defer res.Body.Close()
		if e, has := decodeTurnError(res.Body); has {
			return nil, fmt.Errorf("turn %s failed: %+v", nonce, e)
		}
		var w struct {
			Data []struct {
				Content []struct {
					Type  string `json:"type"`
					State struct {
						Status  string `json:"status"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"state"`
				} `json:"content"`
			} `json:"data"`
		}
		var last string
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
			if err := apiJSON("GET", h.addr+"/api/session/"+h.ses+"/message?order=asc&limit=200", nil, &w); err != nil {
				continue
			}
			for _, m := range w.Data {
				for _, c := range m.Content {
					if c.Type != "tool" || len(c.State.Content) == 0 {
						continue
					}
					text := c.State.Content[0].Text
					last = text
					if !strings.Contains(text, "TURN_NONCE="+nonce+"\n") && !strings.HasSuffix(text, "TURN_NONCE="+nonce) {
						continue
					}
					out := map[string]string{}
					for _, ln := range strings.Split(text, "\n") {
						if k, v, ok := strings.Cut(ln, "="); ok {
							out[k] = v
						}
					}
					return out, nil
				}
			}
		}
		return nil, fmt.Errorf("session %s never printed turn %s's environment (last tool output: %d bytes; not shown, it is an environment)", h.ses, nonce, len(last))
	}
	mustShell := func(h *threadHandle, nonce string) map[string]string {
		t.Helper()
		out, err := shell(h, nonce)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// A shell that PUT nothing is the baseline: the server's own environment, as it is today.
	// A session of a version without the name is exactly that (the plugin lets it run).
	baseline := func() map[string]string {
		h := start("mgdbase")
		if err := apiJSON("PATCH", sessionPathURL(h.addr, h.ses), map[string]any{"metadata": map[string]string{metaManaged: "1"}}, nil); err != nil {
			t.Fatal(err)
		}
		return mustShell(h, "baseline")
	}()
	for _, k := range []string{"AF_WORK_DIR", "AF_SESSION_NAME"} {
		if v, has := baseline[k]; has {
			t.Fatalf("the baseline shell already has %s (%d bytes), the probe proves nothing", k, len(v))
		}
	}
	if baseline["TOOLCHAIN_MARK"] != "tc-server" || baseline["OPENAI_API_KEY"] == "" {
		t.Fatalf("the baseline shell is not the server's environment (TOOLCHAIN_MARK ok: %v, OPENAI_API_KEY present: %v, %d keys)",
			baseline["TOOLCHAIN_MARK"] == "tc-server", baseline["OPENAI_API_KEY"] != "", len(baseline))
	}

	// expect is the baseline plus the session's two variables, nothing else changed.
	check := func(when, name string, got map[string]string) {
		t.Helper()
		// opencode itself stamps each session's own id on its shell; that one differs by design.
		if got["OPENCODE_SESSION_ID"] == "" || got["OPENCODE_SESSION_ID"] == baseline["OPENCODE_SESSION_ID"] {
			t.Errorf("%s %s: OPENCODE_SESSION_ID = %q", when, name, got["OPENCODE_SESSION_ID"])
		}
		delete(got, "OPENCODE_SESSION_ID")
		delete(got, "TURN_NONCE") // matched to its turn in shell
		want := map[string]string{}
		for k, v := range baseline {
			want[k] = v
		}
		delete(want, "OPENCODE_SESSION_ID")
		delete(want, "TURN_NONCE")
		want["AF_SESSION_NAME"], want["AF_WORK_DIR"] = name, paths.SessionWorkDir(name)
		for k, v := range want {
			if got[k] != v {
				// The environment holds real tokens (the serve inherits this process's), so a
				// mismatch names the key and never prints a value; the two variables under
				// test are the only values that are safe to show.
				if k == "AF_SESSION_NAME" || k == "AF_WORK_DIR" {
					t.Errorf("%s %s: %s = %q, want %q", when, name, k, got[k], v)
				} else {
					t.Errorf("%s %s: %s differs from the baseline (present: %v)", when, name, k, got[k] != "")
				}
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok && k != "_" && k != "SHLVL" && k != "OLDPWD" {
				t.Errorf("%s %s: %s is new in the shell", when, name, k)
			}
		}
		if st, err := os.Stat(got["AF_WORK_DIR"]); err != nil || !st.IsDir() {
			t.Errorf("%s %s: AF_WORK_DIR %q is not a directory (%v)", when, name, got["AF_WORK_DIR"], err)
		}
	}
	a, b := start("mgda"), start("mgdb")
	// Both sessions run their shell at the same moment, on the one server.
	both := func(when, na, nb string) {
		t.Helper()
		var ga, gb map[string]string
		var ea, eb error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); ga, ea = shell(a, na) }()
		go func() { defer wg.Done(); gb, eb = shell(b, nb) }()
		wg.Wait()
		if ea != nil || eb != nil {
			t.Fatalf("%s: %v / %v", when, ea, eb)
		}
		check(when, "mgda", ga)
		check(when, "mgdb", gb)
	}
	both("first", "a1", "b1")

	gen := Serve().Generation()
	if !Serve().Restart("contract: managed env") {
		t.Fatal("the serve was not replaced")
	}
	// The new generation holds no environment for either session; the next shell must ask again.
	a, b = start("mgda"), start("mgdb")
	if Serve().Generation() == gen {
		t.Fatal("the sessions are still on the first server generation")
	}
	both("after restart", "a2", "b2")
}
