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
