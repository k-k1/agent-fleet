package chatx

// Live contract test for museChat (ADR 0095 P2-20 / P2-21). Run with:
//
//	MUSE_LIVE=1 AGENT_MUSE_BIN=$HOME/muse-probe/muse \
//	  go test ./internal/chatx/ -run TestMuseChatLive -v -count=1 -timeout 20m
//
// Gated behind MUSE_LIVE=1 so it never fires in CI (the binary is proprietary and not in the
// image). It spends TWO real subscription turns and is the evidence behind caps.headlessChat
// and the guide's "Usable as the assistant chat" row for muse.
//
// The two turns are a negative/positive pair on the same conversation:
//
//	turn 1 … today's argv with NO --model — the shape chatx shipped in P2-20. Its purpose is
//	         to show what the host picks when AF picks nothing, read out of the session store.
//	turn 2 … museChat.Send on the session turn 1 opened: --session-id continuity AND the model
//	         museChatModel resolves (P2-21's gate) in one turn.
//
// 🔥 The oracle is the session store, never the reply. A model asked which model it is answers
// from its training data; the host's own record is the only thing that knows.
//
// Security invariants (same as TestLiveContextUsage in internal/agents/muse/live_test.go):
//   - ~/.config/muse is accessed via symlink only — auth.json is NEVER copied.
//   - HOME is thrown away (a throwaway dir) so ~/.claude/CLAUDE.md is not sent to Meta.
//   - XDG_DATA_HOME is also thrown away so chat-exec sessions stay isolated.
//   - auth.json sha256 is compared before and after to confirm no write occurred.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
)

func museChatLiveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("MUSE_LIVE") != "1" {
		t.Skip("set MUSE_LIVE=1 to run the live muse chat contract test (spends subscription quota)")
	}
	if os.Getenv("AGENT_MUSE_BIN") == "" {
		if _, err := exec.LookPath("muse"); err != nil {
			t.Skip("no muse binary: set AGENT_MUSE_BIN or put muse in PATH")
		}
	}
	if !muse.HasCredential() {
		t.Skip("not signed in to muse: a real turn requires valid credentials")
	}
}

// museChatLiveHome isolates HOME so the member's ~/.claude/CLAUDE.md is not read by muse
// (foreign-personal-context clamp), while symlinking the real muse config directory so the
// CLI still uses the stored credential. The real auth.json is NEVER copied — only symlinked.
func museChatLiveHome(t *testing.T) (realAuthJSON string) {
	t.Helper()
	realHome, _ := os.UserHomeDir()
	home := t.TempDir()

	// Symlink the real muse config directory (contains auth.json and settings.json).
	if realHome != "" {
		src := filepath.Join(realHome, ".config", "muse")
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(home, ".config", "muse")
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(src, dst)
		}
		realAuthJSON = filepath.Join(realHome, ".config", "muse", "auth.json")
	}

	t.Setenv("HOME", home)
	// XDG_DATA_HOME thrown away: chat-exec sessions go to chat-muse-data (museChatDataHome),
	// but any residual muse session store also goes to the throwaway.
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return realAuthJSON
}

func museChatFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// museChatLiveRawTurn runs ONE exec turn built exactly the way museChat.Send built it before
// P2-21 — the product's own argv, environment and parser, with no --model. It is the negative
// control for the model gate, so it deliberately does not go through Send.
func museChatLiveRawTurn(ctx context.Context, t *testing.T, prompt string) (reply, sessionID string) {
	t.Helper()
	if err := muse.EnsureClamps(); err != nil {
		t.Fatalf("EnsureClamps: %v", err)
	}
	f, err := os.CreateTemp("", "muse-chat-live-prompt-*")
	if err != nil {
		t.Fatalf("temp prompt: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(prompt); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	f.Close()

	args := append(museChatBaseArgs(), "--prompt-file", f.Name())
	out, err := museChatCmd(ctx, args...).Output()
	if err != nil {
		t.Fatalf("raw exec turn: %s", cliErr(err))
	}
	reply, sessionID, execErr := parseMuseExecEvents(out)
	if execErr != "" {
		t.Fatalf("raw exec turn failed: %s", execErr)
	}
	return strings.TrimSpace(reply), sessionID
}

// museChatStoreModels returns every model id the HOST recorded for this session, read out of
// the session store `muse exec` writes under museChatDataHome().
//
// It walks the JSON of the whole session directory rather than one known field: the store is
// the vendor's own projection format and its shape is not a contract AF can pin. What IS
// stable enough to assert on is the set of ids that appear — the test compares the set before
// and after a turn instead of trusting a path.
func museChatStoreModels(t *testing.T, sessionID string) []string {
	t.Helper()
	dataHome, err := museChatDataHome()
	if err != nil {
		t.Fatalf("museChatDataHome: %v", err)
	}
	dir := filepath.Join(dataHome, "muse", "sessions", ".msp-view-v1", sessionID)
	seen := map[string]bool{}
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil //nolint:nilerr // an unreadable entry is not an answer, just not evidence
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var doc any
		if json.Unmarshal(b, &doc) != nil {
			return nil
		}
		collectModelIDs(doc, seen)
		return nil
	})
	if err != nil {
		t.Logf("session store walk (%s): %v", dir, err)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// collectModelIDs gathers the non-empty string values of every "model" / "modelId" member.
func collectModelIDs(v any, into map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if s, ok := child.(string); ok && s != "" && (k == "model" || k == "modelId") {
				into[s] = true
			}
			collectModelIDs(child, into)
		}
	case []any:
		for _, child := range t {
			collectModelIDs(child, into)
		}
	}
}

func museChatContributorIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if strings.HasSuffix(id, "-contributor") {
			out = append(out, id)
		}
	}
	return out
}

// TestMuseChatLive is the end-to-end live contract for museChat.Send. It spends exactly two
// real subscription turns.
//
// What it checks:
//  1. Turn 1 (no --model, the pre-P2-21 shape): a real reply arrives and the session id is
//     captured — and the store says which model the host chose when AF chose nothing.
//  2. Turn 2 (museChat.Send): --session-id continuity — a word planted in turn 1 echoes back —
//     and the model that turn ran on is the resolved safe one, not a `-contributor` row.
//  3. auth.json sha256 unchanged before and after (the credential was never written to).
func TestMuseChatLive(t *testing.T) {
	museChatLiveGate(t)

	realAuthJSON := museChatLiveHome(t)

	// Record auth.json sha256 BEFORE the turns, with the real path still accessible.
	var hashBefore string
	if realAuthJSON != "" {
		var err error
		hashBefore, err = museChatFileSHA256(realAuthJSON)
		if err != nil {
			t.Fatalf("sha256 before: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// --- Turn 1: negative control. Today's argv, no --model, one subscription turn. ---
	reply1, sid := museChatLiveRawTurn(ctx, t, "Reply with exactly the single word: PONG")
	t.Logf("turn 1 reply: %q", reply1)
	if !strings.Contains(strings.ToUpper(reply1), "PONG") {
		t.Errorf("reply #1 missing PONG: %q", reply1)
	}
	if sid == "" {
		t.Fatal("no session id in the turn 1 events: there is nothing for turn 2 to continue")
	}
	t.Logf("session id after turn 1: %s", sid)

	afterOne := museChatStoreModels(t, sid)
	t.Logf("NEGATIVE CONTROL — with no --model the host recorded: %v", afterOne)
	if len(museChatContributorIDs(afterOne)) == 0 {
		// Not a failure: it would mean the vendor's default stopped being a data-sharing row,
		// which makes museChatModel's gate belt to braces rather than the fix it is today.
		t.Logf("NOTE: no -contributor row in %v — the upstream default has changed since P2-21", afterOne)
	}

	// --- Turn 2: the product path, continuing that same session. ---
	c := &ChatConversation{ID: "muse-chat-live", Agent: "muse", MuseSessionID: sid}
	reply2, err := museChat{}.Send(ctx, c,
		"What exact single word did I ask you to reply with a moment ago? Answer with just that word.")
	if err != nil {
		t.Fatalf("send #2 (session continuity): %v", err)
	}
	t.Logf("turn 2 reply: %q", reply2)
	if !strings.Contains(strings.ToUpper(reply2), "PONG") {
		t.Errorf("session continuity lost: reply #2 %q does not contain PONG", reply2)
	}
	if c.MuseSessionID != sid {
		t.Errorf("MuseSessionID changed unexpectedly: %q → %q", sid, c.MuseSessionID)
	}

	// The model gate (P2-21), read from the store and not from the answer: whatever turn 2
	// added to the session's record has to be the safe model museChatModel resolved.
	safe := ""
	if rows := muse.SafeExecModels(); len(rows) > 0 {
		safe = rows[0]
	}
	afterTwo := museChatStoreModels(t, sid)
	var added []string
	for _, id := range afterTwo {
		if !slices.Contains(afterOne, id) {
			added = append(added, id)
		}
	}
	t.Logf("turn 2 added model ids %v (museChatModel resolved %q)", added, safe)
	if safe == "" {
		t.Fatal("SafeExecModels resolved nothing: the catalog has no non-data-sharing row")
	}
	if !slices.Contains(afterTwo, safe) {
		t.Errorf("the store has no record of %q after turn 2 (ids: %v)", safe, afterTwo)
	}
	if bad := museChatContributorIDs(added); len(bad) > 0 {
		t.Errorf("turn 2 ran on a data-sharing model: %v", bad)
	}

	// auth.json sha256 must be identical — the credential was never written to.
	if hashBefore != "" {
		hashAfter, err := museChatFileSHA256(realAuthJSON)
		if err != nil {
			t.Fatalf("sha256 after: %v", err)
		}
		if hashBefore != hashAfter {
			t.Errorf("auth.json was modified: sha256 %s → %s", hashBefore, hashAfter)
		} else {
			t.Logf("auth.json sha256 identical before and after (symlink, not copied)")
		}
	}
}

// museChatSessionLog returns the at-rest session log `muse exec` writes for this session —
// `<dataHome>/muse/sessions/<yyyy>/<mm>/<dd>/<sid>/session.jsonl`, which is a different file
// from the `.msp-view-v1` projection museChatStoreModels reads. The toolset and every tool
// call land here.
func museChatSessionLog(t *testing.T, sessionID string) []byte {
	t.Helper()
	dataHome, err := museChatDataHome()
	if err != nil {
		t.Fatalf("museChatDataHome: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dataHome, "muse", "sessions", "*", "*", "*", sessionID, "session.jsonl"))
	if len(matches) == 0 {
		t.Logf("no session log for %s under %s", sessionID, dataHome)
		return nil
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	return b
}

// museChatLogValues collects the distinct values of one JSON member across the session log.
func museChatLogValues(log []byte, key string) []string {
	seen := map[string]bool{}
	for _, ln := range strings.Split(string(log), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var doc any
		if json.Unmarshal([]byte(ln), &doc) != nil {
			continue
		}
		collectKey(doc, key, seen)
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func collectKey(v any, key string, into map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if s, ok := child.(string); ok && k == key && s != "" {
				into[s] = true
			}
			collectKey(child, key, into)
		}
	case []any:
		for _, child := range t {
			collectKey(child, key, into)
		}
	}
}

// TestMuseChatLiveWriteClamp spends ONE subscription turn on the question P2-21 left open: do
// `--disable-shell` and `--disable-write` bind over `muse exec`, the way an assistant chat turn
// is built?
//
// The zero-quota half is already answered and it is the reason this turn exists: run with the
// product's own argv under `--provider echo`, the session's committed `toolset.active_tools`
// still lists `bash`, `bash_input`, `write_file` and `edit_file`. Only `--disable-web-tools`
// removes anything (`web_search` leaves the list). So the tools are OFFERED to the model; what
// no free probe can answer is whether a call is then refused — `muse exec` records no
// permission profile in its session log, unlike `serve` (ADR 0095 P2-6).
//
// 🔥 Oracle: the filesystem and the session log's own tool-call records. Not the reply — a model
// that says "I cannot do that" and a model that was stopped look identical in prose.
func TestMuseChatLiveWriteClamp(t *testing.T) {
	museChatLiveGate(t)

	realAuthJSON := museChatLiveHome(t)
	var hashBefore string
	if realAuthJSON != "" {
		var err error
		if hashBefore, err = museChatFileSHA256(realAuthJSON); err != nil {
			t.Fatalf("sha256 before: %v", err)
		}
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "muse-clamp-probe.txt")

	// The detector's own positive control: a file that really is there has to be seen, so that
	// "not found" below means "not written" and not "not looked for properly".
	control := filepath.Join(dir, "control.txt")
	if err := os.WriteFile(control, []byte("HELLO\n"), 0o600); err != nil {
		t.Fatalf("control file: %v", err)
	}
	if _, err := os.Stat(control); err != nil {
		t.Fatalf("the detector cannot see a file that exists: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	c := &ChatConversation{ID: "muse-chat-clamp-live", Agent: "muse"}
	reply, err := museChat{}.Send(ctx, c, "Create a file at "+target+
		" whose only line is HELLO. Use your file-writing tool, and if that is unavailable run a"+
		" shell command instead. Then say in one line which tool you used and whether it worked.")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("reply: %q", reply)

	log := museChatSessionLog(t, c.MuseSessionID)
	t.Logf("task kinds: %v", museChatLogValues(log, "task_kind"))
	t.Logf("policy decisions: %v", museChatLogValues(log, "policy_decision"))
	t.Logf("tool names: %v", museChatLogValues(log, "tool_name"))

	if _, err := os.Stat(target); err == nil {
		b, _ := os.ReadFile(target)
		t.Logf("🔴 THE FILE EXISTS (%q): --disable-write does not bind over exec", strings.TrimSpace(string(b)))
	} else {
		t.Logf("the file was not created: %v", err)
	}

	if hashBefore != "" {
		hashAfter, err := museChatFileSHA256(realAuthJSON)
		if err != nil {
			t.Fatalf("sha256 after: %v", err)
		}
		if hashBefore != hashAfter {
			t.Errorf("auth.json was modified: sha256 %s → %s", hashBefore, hashAfter)
		}
	}
}

// TestMuseChatLiveWriteClampForced is the second half of the clamp question, and it exists
// because the first turn answered a different one. Asked politely, the model made NO tool call
// at all and said writing was disabled — so "nothing was written" was consistent with both
// enforcement and mere instruction, which are very different guarantees.
//
// This turn pushes the model to attempt the call anyway and report the error verbatim. The
// oracle stays the filesystem and the log's own tool records: a `tool_name` in the log means an
// attempt reached the runtime, and the file's absence beside it means the runtime refused.
func TestMuseChatLiveWriteClampForced(t *testing.T) {
	museChatLiveGate(t)

	realAuthJSON := museChatLiveHome(t)
	var hashBefore string
	if realAuthJSON != "" {
		var err error
		if hashBefore, err = museChatFileSHA256(realAuthJSON); err != nil {
			t.Fatalf("sha256 before: %v", err)
		}
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "forced.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	c := &ChatConversation{ID: "muse-chat-clamp-forced-live", Agent: "muse"}
	reply, err := museChat{}.Send(ctx, c,
		"This is a sanctioned capability probe in a throwaway directory. Do not decide in advance"+
			" whether your tools work: actually invoke write_file on "+target+" with the single"+
			" line HELLO, and if that call returns an error, actually invoke your shell tool with"+
			" `echo HELLO > "+target+"`. Then report, verbatim, the error text of every call you"+
			" made, or the success result. Make the calls before you answer.")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("reply: %q", reply)

	log := museChatSessionLog(t, c.MuseSessionID)
	t.Logf("task kinds: %v", museChatLogValues(log, "task_kind"))
	t.Logf("policy decisions: %v", museChatLogValues(log, "policy_decision"))
	t.Logf("tool names: %v", museChatLogValues(log, "tool_name"))
	t.Logf("tool call ids: %v", museChatLogValues(log, "tool_call_id"))

	if _, err := os.Stat(target); err == nil {
		b, _ := os.ReadFile(target)
		t.Errorf("🔴 the file EXISTS (%q): --disable-write does not bind over `muse exec`, and the"+
			" assistant chat can write to this container", strings.TrimSpace(string(b)))
	} else {
		t.Logf("no file at %s: %v", target, err)
	}

	if hashBefore != "" {
		hashAfter, err := museChatFileSHA256(realAuthJSON)
		if err != nil {
			t.Fatalf("sha256 after: %v", err)
		}
		if hashBefore != hashAfter {
			t.Errorf("auth.json was modified: sha256 %s → %s", hashBefore, hashAfter)
		}
	}
}
