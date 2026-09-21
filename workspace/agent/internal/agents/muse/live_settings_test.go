package muse

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// Behavioural tests for the clamps, against the real binary (MUSE_LIVE=1).
//
// ADR 0095 B1-3 makes these mandatory rather than optional, and says why: a misspelling inside
// a strictly parsed section makes `muse serve` exit rc=3 before `initialize`, while a
// misspelling ANYWHERE ELSE starts the host happily with the clamp simply not in effect and no
// diagnostic at all. "We wrote the JSON" is not evidence that a clamp is on.
//
// Every oracle here is free — no model call, no subscription quota:
//
//   - `muse config validate --plane defaults` names the exact failing member, offline;
//   - `muse exec --provider echo` records the assembled toolset in its durable log, so the
//     EFFECT of the tool clamps is observable without a credential;
//   - `muse skills list` reports each skill's activation;
//   - `muse serve` answering `initialize` proves the file did not make the host refuse to boot.

// liveBin resolves the binary for the tests in this file.
func liveBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("AGENT_MUSE_BIN"); p != "" {
		return p
	}
	p, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("no muse binary: set AGENT_MUSE_BIN")
	}
	return p
}

// clampedHome writes the clamps into a throwaway HOME with the real writer, and returns the
// environment a muse command should run under. It deliberately goes through EnsureClamps
// rather than a hand-written file: the thing under test is what AF actually produces.
func clampedHome(t *testing.T, clamped bool) (home string, env []string) {
	t.Helper()
	home = t.TempDir()
	for _, sub := range []string{".config", ".local/share", "ws"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if clamped {
		t.Setenv("HOME", home)
		if err := EnsureClamps(); err != nil {
			t.Fatalf("EnsureClamps: %v", err)
		}
	}
	return home, []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"MUSE_NO_AUTO_UPDATE=1",
		"PATH=" + os.Getenv("PATH"),
	}
}

// echoToolset runs a credential-free echo turn and returns the tool names muse assembled for
// it, read out of the durable log it writes.
func echoToolset(t *testing.T, bin, home string, env []string) []string {
	t.Helper()
	// --trust-workspace matters here and not only in production: measured, an untrusted
	// workspace reports "Agent delegation: auto unavailable" and the subagent tools never
	// appear at all, so an untrusted probe would show a clamped toolset either way.
	cmd := exec.Command(bin, "exec", "--provider", "echo",
		"--workspace", filepath.Join(home, "ws"), "--trust-workspace", "hi")
	cmd.Env = env
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("muse exec: %v\n%s", err, b)
	}

	var logPath string
	root := filepath.Join(home, ".local", "share", "muse")
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.Name() == "session.jsonl" && logPath == "" {
			logPath = p
		}
		return nil
	})
	if logPath == "" {
		t.Fatalf("no session.jsonl under %s", root)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, "active_tools") {
			continue
		}
		var rec struct {
			Payload struct {
				Event struct {
					Toolset struct {
						ActiveTools []string `json:"active_tools"`
					} `json:"toolset"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if len(rec.Payload.Event.Toolset.ActiveTools) > 0 {
			return rec.Payload.Event.Toolset.ActiveTools
		}
	}
	t.Fatal("the durable log records no assembled toolset")
	return nil
}

func countTools(tools []string, substr string) int {
	n := 0
	for _, s := range tools {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

// The spelling oracle, with its own positive control. A clamp path that stops existing in a
// later release turns this red instead of becoming a silently ineffective key.
func TestLiveClampSpellingsAreRealMembers(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)

	settings := map[string]any{}
	for _, c := range ownedClamps() {
		if len(c.path) == 1 && c.path[0] == "schema_version" {
			continue // the document's own member, not a settings member
		}
		setPath(settings, c.path, c.value)
	}
	doc := map[string]any{"schema_version": 1, "settings": settings}

	validate := func(d map[string]any) (string, error) {
		f := filepath.Join(t.TempDir(), "doc.json")
		b, _ := json.Marshal(d)
		if err := os.WriteFile(f, b, 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "config", "validate", "--plane", "defaults", "--file", f)
		cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1", "MUSE_EXPERIMENTAL_ENTERPRISE_CONFIG=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := validate(doc)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "valid: plane=defaults") || strings.Contains(out, "unknown_member") {
		t.Errorf("a clamp path is not a real member:\n%s", out)
	}

	// Positive control: without it, a validator that accepted anything would look green.
	broken := map[string]any{"schema_version": 1, "settings": map[string]any{
		"context": map[string]any{"foreign_personal_skil": false},
	}}
	if out, _ := validate(broken); !strings.Contains(out, "unknown_member") {
		t.Errorf("the validator accepted a misspelled key, so the check above proves nothing:\n%s", out)
	}
}

// The effect, not the JSON: with the clamps on, the six subagent tools and the workflow tool
// must be gone from the toolset the model actually gets.
func TestLiveClampsRemoveTheSubagentAndWorkflowTools(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)

	plainHome, plainEnv := clampedHome(t, false)
	plain := echoToolset(t, bin, plainHome, plainEnv)
	if countTools(plain, "subagent") == 0 || countTools(plain, "workflow") == 0 {
		t.Fatalf("the unclamped control has no subagent/workflow tools to remove: %v", plain)
	}

	clampedDir, clampedEnv := clampedHome(t, true)
	clamped := echoToolset(t, bin, clampedDir, clampedEnv)
	if n := countTools(clamped, "subagent"); n != 0 {
		t.Errorf("%d subagent tools survived the clamp: %v", n, clamped)
	}
	if n := countTools(clamped, "workflow"); n != 0 {
		t.Errorf("%d workflow tools survived the clamp: %v", n, clamped)
	}
	if len(clamped) >= len(plain) {
		t.Errorf("the clamped toolset is not smaller: %d vs %d", len(clamped), len(plain))
	}
}

// The activation key is a pack-qualified path, so a rename or a move silently re-enables the
// skill and an entry for a skill that no longer exists is silently accepted. Only asking muse
// catches either.
func TestLiveClampsDisableTheForeignReaderSkills(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)
	home, env := clampedHome(t, true)
	_ = home

	cmd := exec.Command(bin, "skills", "list")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("muse skills list: %v\n%s", err, out)
	}

	activation := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 3 {
			activation[f[0]] = f[2]
		}
	}
	for _, id := range foreignReaderSkills {
		state, ok := activation[id]
		if !ok {
			t.Errorf("bundled skill %q no longer exists; its clamp is now a silently dead key", id)
			continue
		}
		if state != "off" {
			t.Errorf("bundled skill %q is %q, want off", id, state)
		}
	}
	// Negative control: a skill AF does not clamp must still be on, or "everything is off"
	// would pass this test for the wrong reason.
	if state, ok := activation["git"]; ok && state != "on" {
		t.Errorf("an unclamped skill (git) is %q; the clamp is too broad", state)
	}
}

// read-session reads MUSE's own store and its own description forbids probing ~/.claude or
// ~/.codex, so clamping it would remove a working feature for a reason that does not apply.
func TestLiveReadSessionStaysEnabled(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)
	_, env := clampedHome(t, true)

	cmd := exec.Command(bin, "skills", "list")
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 3 && f[0] == "read-session" && f[2] != "on" {
			t.Errorf("read-session is %q; it reads muse's own store, not another agent's", f[2])
		}
	}
}

// The other silent failure mode, and the loud one: a file muse parses strictly must still let
// the host boot. A clamp that kills the child is worse than one that does nothing.
func TestLiveClampedSettingsLetTheHostBoot(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)
	home, env := clampedHome(t, true)

	cmd := exec.Command(bin, "serve", "--disable-sandbox", "--trust-workspace")
	cmd.Dir = filepath.Join(home, "ws")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	client := msp.NewClient(stdin, stdout, msp.Handler{})
	res, err := msp.Handshake(client, clientVersion, nil)
	if err != nil {
		t.Fatalf("the clamped settings file stopped the host from booting: %v", err)
	}
	if res.MuseHome == "" {
		t.Error("the host answered initialize with no museHome")
	}
}

// Clamp 5 behaviourally: with the clamps applied, the member's own ~/.claude/CLAUDE.md must
// not reach the model input. Measured through `muse exec`, which needs no credential; the
// equivalent check over `muse serve` needs a real turn and is owed separately.
func TestLiveForeignPersonalRulesAreNotAssembled(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)
	const marker = "AFPROBE-FOREIGN-RULES-MARKER"

	run := func(clamped bool) string {
		home, env := clampedHome(t, clamped)
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte(marker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The environment clamp is deliberately NOT set here: this test is about the settings
		// keys, and leaving the env route on would hide a settings key that stopped working.
		cmd := exec.Command(bin, "exec", "--provider", "echo",
			"--workspace", filepath.Join(home, "ws"), "--trust-workspace", "hi")
		cmd.Env = env
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("muse exec: %v\n%s", err, b)
		}
		var found string
		filepath.Walk(filepath.Join(home, ".local", "share", "muse"), func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.Name() != "session.jsonl" {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), marker) {
				found = p
			}
			return nil
		})
		return found
	}

	// Negative control first: without the clamps the marker really does reach the log, so an
	// absence below means the clamp worked rather than that the probe was blind.
	if run(false) == "" {
		t.Fatal("the unclamped control did not assemble the foreign rules; this test cannot prove anything")
	}
	if p := run(true); p != "" {
		t.Errorf("the member's ~/.claude/CLAUDE.md still reached the model input, in %s", p)
	}
}

// The instruction layer against the real binary, and it costs nothing: both halves have a
// free oracle. `muse skills list --source user` is the authority on whether a dropped
// SKILL.md is actually registered (gate B1-5 measured that it needs no install step, and this
// is the check that the claim still holds), and `muse config validate` is the authority on
// whether the AGENTS.md beside it left the configuration readable.
//
// What it cannot answer is whether a TURN assembles the file — that needs a model call, and
// gate B1-5 already bought that answer with planted markers.
func TestLiveInstructionLayerIsRegistered(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)
	home, env := clampedHome(t, true)

	// The real writers, not hand-built files: what is under test is what AF produces.
	if err := ApplyFleetNotes("# fleet policy\n\nAFPROBE-FLEET\n"); err != nil {
		t.Fatalf("ApplyFleetNotes: %v", err)
	}
	if err := ApplyUserInstructions("AFPROBE-USER\n"); err != nil {
		t.Fatalf("ApplyUserInstructions: %v", err)
	}
	skill := filepath.Join(SkillsDir(), "af-probe-topic", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: af-probe-topic\ndescription: \"a probe topic\"\n---\n# Probe\n\nbody\n"
	if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// The throwaway environment is what makes this a measurement rather than a reading of the
	// member's own machine — and `skills list` loading at all is the second half of the
	// answer, since it reads the configuration in the same home the AGENTS.md now sits in.
	cmd := exec.Command(bin, "skills", "list", "--source", "user")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("muse skills list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "af-probe-topic") {
		t.Fatalf("the fleet topic skill is not registered:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "muse", "AGENTS.md")); err != nil {
		t.Fatalf("AGENTS.md is not where muse reads it: %v", err)
	}
	// Both blocks in the one file, which is the shape decision 12 settled on.
	b, err := os.ReadFile(AgentsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "AFPROBE-FLEET") || !strings.Contains(string(b), "AFPROBE-USER") {
		t.Fatalf("AGENTS.md does not carry both blocks:\n%s", b)
	}
}

// Open question 8, answered: muse's own `cron_*` tools survive every clamp, and the key that
// would stop them cannot be used. This is the measurement the guide's "Agent Fleet does not
// see those runs" paragraph stands on, and it is free (`--provider echo`).
//
// The search for a key was exhaustive against the vendor's own offline validator, which names
// unknown members: the defaults plane has no `cron` / `scheduler` / `automation` section at
// all, `run` carries no cron member, and the policy plane is `execution.*` / `model_egress`.
// The one lever that removes them is `run.toolset` — and see the test below for why it is not
// the answer.
func TestLiveCronToolsSurviveTheClamps(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)

	home, env := clampedHome(t, true)
	tools := echoToolset(t, bin, home, env)
	if n := countTools(tools, "cron"); n == 0 {
		t.Errorf("no cron tool is left under the clamps: the guide paragraph about muse "+
			"scheduling its own runs is now wrong and should be removed (tools: %v)", tools)
	}
	// The pair, so that "cron survived" cannot be read as "nothing was clamped": the same
	// toolset must already be missing what the clamps DO remove.
	if n := countTools(tools, "subagent"); n != 0 {
		t.Fatalf("the clamps did not apply in this run (%d subagent tools present), so the cron finding is unmeasured", n)
	}
}

// 🔴 Why the key that exists is not adopted. `run.toolset` is a NAMED ALLOW-LIST of the whole
// tool surface, not a cron switch, and it takes two things down with it:
//
//   - every MCP tool, including AF's own `af` server — so a muse session clamped this way is
//     told to call `af_report` and has no such tool, the exact failure P2-12's `ServedKinds`
//     exists to prevent;
//   - the host itself on the next release — an unknown name is not ignored, it is
//     `invalid run configuration: unknown tool names: …` and rc=2 before any session, so a
//     tool renamed in 1.4 would stop every muse session in the fleet from starting.
//
// The MCP arm needs a server that really starts, because a server that failed would leave its
// tool absent in both arms and prove nothing — hence the stdio stub, and the `mode: all` arm
// that shows the tool present before the allow-list is applied.
func TestLiveTheOnlyKeyThatCutsCronCutsMCPToolsToo(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 for the stdio MCP stub")
	}

	const stub = `import sys, json
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try: req = json.loads(line)
    except Exception: continue
    m, i = req.get("method"), req.get("id")
    if m == "initialize":
        send({"jsonrpc":"2.0","id":i,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"afprobe","version":"0.0.1"}}})
    elif m == "tools/list":
        send({"jsonrpc":"2.0","id":i,"result":{"tools":[{"name":"probe_ping","description":"probe","inputSchema":{"type":"object","properties":{}}}]}})
    elif i is not None:
        send({"jsonrpc":"2.0","id":i,"result":{}})
`

	arm := func(toolset []string) []string {
		t.Helper()
		home, env := clampedHome(t, true)
		script := filepath.Join(home, "mcpstub.py")
		if err := os.WriteFile(script, []byte(stub), 0o644); err != nil {
			t.Fatal(err)
		}
		// The member's own file dialect, not the wire's: this is `muse exec`, which has no
		// session/start to carry servers, and the point of the arm is the tool list.
		cur, err := readSettings()
		if err != nil {
			t.Fatal(err)
		}
		setPath(cur, []string{"mcp_servers", "afprobe"}, map[string]any{
			"enabled": true, "transport": "stdio", "command": py, "args": []any{script},
		})
		if len(toolset) > 0 {
			setPath(cur, []string{"run", "toolset"}, toStrAny(toolset))
		}
		if err := writeSettingsAtomic(cur); err != nil {
			t.Fatal(err)
		}
		return echoToolset(t, bin, home, env)
	}

	// Arm 1, the control: with no allow-list the MCP tool is there to lose, and so is cron.
	plain := arm(nil)
	if countTools(plain, "mcp__afprobe") == 0 {
		t.Fatalf("the stub's tool never reached the toolset, so the arm below proves nothing: %v", plain)
	}
	if countTools(plain, "cron") == 0 {
		t.Fatalf("no cron tool in the control arm: %v", plain)
	}

	// Arm 2: the only spelling that removes cron.
	named := arm([]string{"read_file", "search", "bash"})
	if n := countTools(named, "cron"); n != 0 {
		t.Errorf("run.toolset left %d cron tools: %v", n, named)
	}
	if n := countTools(named, "mcp__afprobe"); n != 0 {
		t.Errorf("run.toolset kept the MCP tool (%d), so the reason it is rejected no longer holds: %v", n, named)
	}
}

// The other half of open question 7: AF sends the SUBDIRECTORY as the workspace root when a
// session was launched into one (live_test.go measures that), so the question the member
// actually feels is whether the repository's own AGENTS.md — which sits at the working copy
// root, ABOVE that path — still reaches the model.
//
// 🔴 It does, and ONLY because an AF working copy is a git repository: the walk-up stops at
// the repository root, so the identical tree with no `.git` loads the subfolder's rules alone.
// Three arms, all free, and the last two are what give the first one its meaning — a log that
// simply contained every file on disk, or a walk that ignored trust, would pass arm 1 by
// itself.
func TestLiveSubdirWorkspaceStillLoadsTheRepoRootRules(t *testing.T) {
	liveGate(t)
	bin := liveBin(t)

	const rootMarker = "AFPROBE-ROOT-RULES"
	const subMarker = "AFPROBE-SUB-RULES"

	run := func(repo, trust bool) string {
		home, env := clampedHome(t, true)
		ws := filepath.Join(home, "ws")
		sub := filepath.Join(ws, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for path, marker := range map[string]string{
			filepath.Join(ws, "AGENTS.md"):  rootMarker,
			filepath.Join(sub, "AGENTS.md"): subMarker,
		} {
			if err := os.WriteFile(path, []byte("# rules\n"+marker+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if repo {
			init := exec.Command("git", "init", "-q", ws)
			init.Env = env
			if b, err := init.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, b)
			}
		}
		args := []string{"exec", "--provider", "echo", "--workspace", sub}
		if trust {
			args = append(args, "--trust-workspace")
		}
		cmd := exec.Command(bin, append(args, "hi")...)
		cmd.Env = env
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("muse exec: %v\n%s", err, b)
		}
		var log string
		filepath.Walk(filepath.Join(home, ".local", "share", "muse"), func(p string, info os.FileInfo, err error) error {
			if err == nil && info != nil && info.Name() == "session.jsonl" && log == "" {
				b, err := os.ReadFile(p)
				if err == nil {
					log = string(b)
				}
			}
			return nil
		})
		return log
	}

	// Arm 1: the production shape — a git working copy, trusted, launched into a subfolder.
	inRepo := run(true, true)
	if !strings.Contains(inRepo, subMarker) {
		t.Fatalf("the workspace root's own AGENTS.md did not reach the model input; this test cannot say anything about the one above it")
	}
	if !strings.Contains(inRepo, rootMarker) {
		t.Errorf("the working copy's AGENTS.md did NOT reach a session launched into a subfolder: "+
			"project rules are lost for subdirectory launches (%s)", rootMarker)
	}
	// Arm 2: the boundary, and the reason arm 1 is not "muse reads every ancestor". The same
	// tree without `.git` must load the subfolder's rules and nothing above them.
	if noRepo := run(false, true); strings.Contains(noRepo, rootMarker) {
		t.Errorf("the walk-up crossed a directory that is not a repository root, so it is not "+
			"the repository boundary this test claims (%s)", rootMarker)
	} else if !strings.Contains(noRepo, subMarker) {
		t.Errorf("the no-repository arm assembled no rules at all, so it measures nothing")
	}
	// Arm 3: without the trust flag neither file is assembled.
	if untrusted := run(true, false); strings.Contains(untrusted, rootMarker) || strings.Contains(untrusted, subMarker) {
		t.Error("an untrusted workspace assembled the rules anyway, so arm 1 proves nothing")
	}
}

// toStrAny is the []string -> []any a generic settings document needs.
func toStrAny(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}
