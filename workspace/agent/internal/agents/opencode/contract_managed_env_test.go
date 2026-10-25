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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// shell runs `env` in the session and returns what it printed.
	shell := func(h *threadHandle, text string) map[string]string {
		t.Helper()
		req, err := newTurnRequest(h.addr, h.ses, work, agents.TurnInput{Prompt: text}, st)
		if err != nil {
			t.Fatal(err)
		}
		res, err := sendTurn(h.addr, h.ses, work, req, st)
		if err != nil {
			t.Fatalf("turn %q: %v", text, err)
		}
		res.Body.Close()
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
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(300 * time.Millisecond) {
			var last map[string]string
			if err := apiJSON("GET", h.addr+"/api/session/"+h.ses+"/message?order=asc&limit=200", nil, &w); err == nil {
				for _, m := range w.Data {
					for _, c := range m.Content {
						if c.Type == "tool" && len(c.State.Content) > 0 && strings.Contains(c.State.Content[0].Text, "TOOLCHAIN_MARK=") {
							last = map[string]string{}
							for _, ln := range strings.Split(c.State.Content[0].Text, "\n") {
								if k, v, ok := strings.Cut(ln, "="); ok {
									last[k] = v
								}
							}
						}
					}
				}
			}
			if last != nil {
				return last
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s never printed its environment", h.ses)
			}
		}
	}
	// A shell that PUT nothing is the baseline: the server's own environment, as it is today.
	// A session of a version without the name is exactly that (the plugin lets it run).
	baseline := func() map[string]string {
		h := start("mgdbase")
		if err := apiJSON("PATCH", sessionPathURL(h.addr, h.ses), map[string]any{"metadata": map[string]string{metaManaged: "1"}}, nil); err != nil {
			t.Fatal(err)
		}
		return shell(h, "ENVQ baseline")
	}()
	for _, k := range []string{"AF_WORK_DIR", "AF_SESSION_NAME"} {
		if v, has := baseline[k]; has {
			t.Fatalf("the baseline shell already has %s=%s, the probe proves nothing", k, v)
		}
	}
	if baseline["TOOLCHAIN_MARK"] != "tc-server" || baseline["OPENAI_API_KEY"] == "" {
		t.Fatalf("the baseline shell is not the server's environment: %v", baseline)
	}

	// expect is the baseline plus the session's two variables, nothing else changed.
	check := func(when, name string, got map[string]string) {
		t.Helper()
		// opencode itself stamps each session's own id on its shell; that one differs by design.
		if got["OPENCODE_SESSION_ID"] == "" || got["OPENCODE_SESSION_ID"] == baseline["OPENCODE_SESSION_ID"] {
			t.Errorf("%s %s: OPENCODE_SESSION_ID = %q", when, name, got["OPENCODE_SESSION_ID"])
		}
		delete(got, "OPENCODE_SESSION_ID")
		want := map[string]string{}
		for k, v := range baseline {
			want[k] = v
		}
		delete(want, "OPENCODE_SESSION_ID")
		want["AF_SESSION_NAME"], want["AF_WORK_DIR"] = name, paths.SessionWorkDir(name)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s %s: %s = %q, want %q", when, name, k, got[k], v)
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
	check("first", "mgda", shell(a, "ENVQ a1"))
	check("first", "mgdb", shell(b, "ENVQ b1"))

	gen := Serve().Generation()
	if !Serve().Restart("contract: managed env") {
		t.Fatal("the serve was not replaced")
	}
	// The new generation holds no environment for either session; the next shell must ask again.
	a, b = start("mgda"), start("mgdb")
	if Serve().Generation() == gen {
		t.Fatal("the sessions are still on the first server generation")
	}
	check("after restart", "mgda", shell(a, "ENVQ a2"))
	check("after restart", "mgdb", shell(b, "ENVQ b2"))
}
