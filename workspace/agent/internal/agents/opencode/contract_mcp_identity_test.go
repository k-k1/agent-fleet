//go:build clicontract

// Can a managed opencode session's MCP child be told WHICH session it serves?
//
// The codex answer is yes: a thread's `config.mcp_servers` reaches the spawned child
// (docs/27 §9.3.1). opencode is the other managed runtime and the question decides
// whether propose_session_handoff can ever stop guessing from cwd there. Two things
// have to hold for an injection point to exist: the API must accept per-session MCP
// configuration, and the daemon must spawn a child per session to receive it. These
// tests measure both against a real `opencode serve`.
package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// globalProbeValue is the only identity a global config can hand an MCP child: one
// fixed string for every session on the daemon.
const globalProbeValue = "from_global_config"

// TestContractOpencodeMCPHasNoPerSessionConfigPoint reads the live API description
// rather than the docs: a per-session MCP hook would have to surface either on session
// creation or on the /mcp face. Neither exists today, and this test is written to FAIL
// when one appears — that failure is the signal that the cwd guess can be retired.
func TestContractOpencodeMCPHasNoPerSessionConfigPoint(t *testing.T) {
	addr, _ := startServe(t)
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
			} `json:"parameters"`
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	body := getJSON(t, addr+"/doc")
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode /doc from opencode %s: %v", opencodeVersion(t), err)
	}

	create, ok := doc.Paths["/session"]["post"]
	if !ok {
		t.Fatalf("POST /session is gone from the API description — the driver's session "+
			"creation path is broken, not just this measurement (opencode %s)", opencodeVersion(t))
	}
	props := create.RequestBody.Content["application/json"].Schema.Properties
	for name := range props {
		switch strings.ToLower(name) {
		case "mcp", "config", "environment", "env":
			t.Fatalf("POST /session now accepts %q: opencode grew a per-session config point, "+
				"so a managed session's MCP child CAN be handed its session name and "+
				"mcpOwningSession's cwd guess should be replaced for opencode too", name)
		}
	}

	for _, method := range []string{"get", "post"} {
		op, ok := doc.Paths["/mcp"][method]
		if !ok {
			continue
		}
		for _, p := range op.Parameters {
			if strings.Contains(strings.ToLower(p.Name), "session") {
				t.Fatalf("/mcp %s is now scoped by %q — the MCP face became per-session, "+
					"which is the injection point this test exists to notice", method, p.Name)
			}
		}
	}
	t.Logf("opencode %s: POST /session props=%v, /mcp is project-scoped only — no per-session MCP config point",
		opencodeVersion(t), sortedKeys(props))
}

// TestContractOpencodeMCPChildIsSharedAcrossSessions is the other half: even a
// per-session config would be useless if one child served everybody. It creates three
// sessions — two sharing a directory, which is exactly the shape that defeats the cwd
// guess — and records what each spawned MCP child was actually told.
func TestContractOpencodeMCPChildIsSharedAcrossSessions(t *testing.T) {
	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "spawns.log")
	seedProbeMCP(t, home, log)
	addr, _ := startServeIn(t, home)

	shared, other := t.TempDir(), t.TempDir()
	for _, s := range []struct{ dir, title string }{
		{shared, "first"}, {shared, "second"}, {other, "third"},
	} {
		if _, err := serveCreateSession(addr, s.dir, s.title); err != nil {
			t.Fatalf("serveCreateSession(%s) against opencode %s: %v", s.title, opencodeVersion(t), err)
		}
	}
	// Touch the MCP face for both projects: if instantiation is lazy, this is what
	// forces it, so a zero count afterwards means "never spawned", not "not yet".
	// Best-effort by design — /mcp blocks while it tries to complete a handshake the
	// probe will never finish, so a timeout here IS the request doing its job.
	for _, dir := range []string{shared, other} {
		if _, err := (&http.Client{Timeout: 20 * time.Second}).Get(addr + "/mcp?directory=" + dir); err != nil {
			t.Logf("GET /mcp?directory=%s: %v (expected while the probe's handshake hangs)", dir, err)
		}
	}

	spawns := waitProbeSpawns(t, log)
	if len(spawns) == 0 {
		t.Skipf("opencode %s never spawned the configured MCP child (a failed handshake is "+
			"expected, no spawn at all is not) — measurement inconclusive, not a contract break",
			opencodeVersion(t))
	}
	for _, got := range spawns {
		if got != globalProbeValue {
			t.Fatalf("an MCP child was spawned with %q, not the single global config value %q: "+
				"opencode is now varying MCP config per session/project, which would be the "+
				"injection point for a session identity", got, globalProbeValue)
		}
	}
	// 3 sessions across 2 directories. One child per session would be 3; the measured
	// granularity is the project directory, which is exactly the granularity that
	// CANNOT separate two sessions sharing a worktree — the shape that broke the handoff.
	if len(spawns) >= 3 {
		t.Fatalf("%d MCP spawns for 3 sessions in 2 directories: opencode now spawns per "+
			"SESSION, so a session identity finally has somewhere to live and the cwd "+
			"guess should be replaced for opencode too", len(spawns))
	}
	t.Logf("opencode %s: 3 sessions across 2 directories → %d MCP spawn(s), all told %q — "+
		"MCP is instantiated per project directory, so sessions sharing a worktree share a child",
		opencodeVersion(t), len(spawns), globalProbeValue)
}

// seedProbeMCP writes an `mcp` entry whose "server" is a probe: it appends the identity
// it was handed and then idles (so opencode sees a live process whose handshake merely
// fails, rather than an instant exit it might respawn in a loop).
func seedProbeMCP(t *testing.T, home, log string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			"af_identity_probe": map[string]any{
				"type": "local",
				"command": []string{"/bin/sh", "-c",
					// Idles rather than exiting: an instant exit could be respawned in a loop
					// and inflate the count. 45s outlives the measurement window without
					// leaving a long-lived orphan on a shared host.
					`printf '%s\n' "${AF_SESSION_NAME-<unset>}" >> "$0"; exec sleep 45`, log},
				"environment": map[string]string{"AF_SESSION_NAME": globalProbeValue},
				"enabled":     true,
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitProbeSpawns lets the count settle: it returns once the log has stopped growing,
// so a slow second spawn is not read as "only one child".
func waitProbeSpawns(t *testing.T, log string) []string {
	t.Helper()
	var last []string
	stable := 0
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		lines := probeLines(log)
		if len(lines) == len(last) {
			if stable++; stable >= 5 && len(lines) > 0 {
				return lines
			}
		} else {
			stable = 0
		}
		last = lines
		time.Sleep(500 * time.Millisecond)
	}
	return last
}

func probeLines(log string) []string {
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func getJSON(t *testing.T, url string) []byte {
	t.Helper()
	res, err := serveClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", url, res.StatusCode, truncateProbe(string(b)))
	}
	return b
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func truncateProbe(s string) string {
	if len(s) > 200 {
		return fmt.Sprintf("%s… (%d bytes)", s[:200], len(s))
	}
	return s
}
