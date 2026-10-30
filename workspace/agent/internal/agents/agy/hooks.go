package agy

// agy's lifecycle hooks as the primary signal for a slot's conversation UUID and its
// working / idle state (#2156). agy runs the commands in ~/.gemini/config/hooks.json at
// SessionStart / PreInvocation / PreToolUse / Stop with a camelCase JSON object on stdin and
// the launching pane's environment (measured on 1.3.5: AF_SESSION_NAME arrives, so the slot is
// named by the env af set at launch, never guessed from a cwd or a conversation id).
//
// Invariants:
//   - Hooks fire only once the first prompt is submitted, and an Esc cancel fires NO Stop
//     (measured). A hook "working" is therefore never trusted past the pane and the staleAfter
//     bound (hookLiveState), and every consumer falls back to the DB / brain paths when no
//     usable record exists.
//   - The command's stdout must be a JSON object, and the command must never fail or block agy:
//     every error is swallowed and "{}" is printed. PreToolUse is the exception: agy 1.4.4 reads
//     "{}" there as an empty decision and denies the tool call with an empty reason, so every
//     tool call of the session fails (measured by running a tool, #2470). Its reply is empty,
//     which agy takes as no opinion; "{"decision":"allow"}" would also run the tool but speaks
//     for the permission check, and a "hookSpecificOutput" key is an unknown field there.
//   - hooks.json is shared with the user: only the entry named hookName is ours.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// HookSubcommand is the workspace-agent subcommand the hooks.json entry runs.
const HookSubcommand = "agy-hook"

// hookName is the key of af's entry in hooks.json. agy merges named hooks across files, so a
// user's own names are untouched by ours.
const hookName = "agent-fleet"

// hookTimeout is the handler timeout agy enforces on us. The command only writes a small file.
const hookTimeout = 10

// hookRecord is the latest lifecycle event of one slot, written by the hook process and read
// by the agent's list / mirror routes.
type hookRecord struct {
	ConvID string `json:"conv,omitempty"`
	// State is "working" or "idle"; "" for SessionStart, which only names the conversation.
	State string `json:"state,omitempty"`
	At    string `json:"at"` // RFC3339
	// Gen is the launch generation the writer carried. A record whose Gen is not the slot's
	// current one belongs to an earlier launch and is never read, whenever it was written.
	Gen string `json:"gen,omitempty"`
	// StepIdx is the conversation's last step at a Stop (nil when the DB could not be read): a
	// later step is the evidence of a new turn whose PreInvocation went missing.
	StepIdx *int `json:"stepIdx,omitempty"`
	// StatusRev is the Rev of the slot's status record as the Stop saw it ("" when there was
	// none), nil on a record that is not a Stop. Every write of that record mints a new Rev, so
	// "working with another Rev" is an input written after the Stop at any sub-second distance.
	StatusRev *string `json:"statusRev,omitempty"`
}

// currentRecord reads the slot's record, only when it was written under the current launch.
func currentRecord(slotSid string) (hookRecord, bool) {
	rec, ok := hookRecords.Read(slotSid)
	if !ok || rec.Gen == "" || rec.Gen != launchGens.Read(slotSid) {
		return hookRecord{}, false
	}
	return rec, true
}

// launchEnvVar names the launch generation in the pane's environment. agy passes it on to its
// hook children, and the hook is ignored unless it matches the slot's current generation, so a
// late hook of a process from before a restart cannot write into this launch.
const launchEnvVar = "AF_AGY_LAUNCH"

var launchGens = agents.NewSidStore("agy-launch")

// newLaunchGen records a fresh generation for the slot and returns the env entry for the pane.
func newLaunchGen(slotSid string) string {
	gen := strconv.FormatUint(rand.Uint64(), 36)
	launchGens.Write(slotSid, gen)
	return launchEnvVar + "=" + gen
}

var hookRecords = fstore.JSON[hookRecord](paths.AgentStateDir, "agy-hook", ".json")

// hookInput is the subset of the hook's stdin the agent reads.
type hookInput struct {
	ConversationID string `json:"conversationId"`
	// FullyIdle is a pointer so a Stop without the key reads as "unknown" rather than busy.
	FullyIdle *bool `json:"fullyIdle"`
}

// hookStateFor maps an event to the state it proves; "" proves none.
func hookStateFor(event string, in hookInput) string {
	switch event {
	case "PreInvocation", "PreToolUse":
		return "working"
	case "Stop":
		if in.FullyIdle != nil && !*in.FullyIdle {
			return "working"
		}
		return "idle"
	}
	return ""
}

// RunHook is `workspace-agent agy-hook <Event>`. It returns normally and prints "{}", or nothing for PreToolUse (see the invariants above):
// a failing hook must not disturb the agy turn it observes. Input that does not decode, or that
// names no conversation, records nothing.
func RunHook(args []string) {
	defer func() {
		if out := neutralOutput(args); out != "" {
			fmt.Fprintln(os.Stdout, out)
		}
	}()
	if len(args) == 0 {
		return
	}
	var in hookInput
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in) != nil {
		return
	}
	recordHook(os.Getenv(agents.SessionNameEnvVar), os.Getenv(launchEnvVar), args[0], in, time.Now())
}

// neutralOutput is the stdout that lets agy go on: "{}" for every event but PreToolUse, which
// prints nothing (see the invariants above).
func neutralOutput(args []string) string {
	if len(args) > 0 && args[0] == "PreToolUse" {
		return ""
	}
	return "{}"
}

// recordHook stores the event for the slot named by the launch env. The conversation of a
// launch is owned by its first SessionStart: an agy started from the pane's own run_command
// inherits both env vars, so any later event naming another conversation (or one other than the
// adopted / resumed UUID) is dropped rather than allowed to replace the record.
func recordHook(name, gen, event string, in hookInput, now time.Time) {
	if name == "" || in.ConversationID == "" {
		return // an agy the user runs by hand, or a payload that cannot be attributed
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Kind != session.KindAgy {
		return
	}
	slotSid := session.UUID(m.Dir, m.Name)
	if want := launchGens.Read(slotSid); want == "" || gen != want {
		return
	}
	if conv := sids.Read(slotSid); conv != "" && conv != in.ConversationID {
		return
	}
	state := hookStateFor(event, in)
	if state == "" && event != "SessionStart" {
		return
	}
	prev, hadPrev := currentRecord(slotSid)
	if hadPrev && prev.ConvID != "" && prev.ConvID != in.ConversationID {
		return
	}
	rec := hookRecord{ConvID: in.ConversationID, State: state, At: now.Format(time.RFC3339), Gen: gen}
	if event == "Stop" {
		st, _ := status.Read(slotSid)
		rec.StatusRev = &st.Rev
		if idx, ok := lastStepIdx(in.ConversationID); ok {
			rec.StepIdx = &idx
		}
	}
	if state == "" && hadPrev {
		// SessionStart names the conversation but says nothing about the turn.
		rec.State = prev.State
	}
	_ = hookRecords.Write(slotSid, rec)
}

// idleGrace is how long after a Stop the conversation DB may still be landing the turn's own
// last steps; a Stop is not contradicted by the DB before it has passed.
const idleGrace = 10 * time.Second

// hookConversation is the conversation UUID the slot's hook reported, "" when none did.
func hookConversation(slotSid string) string {
	rec, ok := currentRecord(slotSid)
	if !ok {
		return ""
	}
	return rec.ConvID
}

// hookLiveState is the hook's verdict on the slot, "" when it has none to give (no record,
// another conversation's, or a "working" too old to believe) — the caller then uses the DB path.
// "idle" is a turn boundary agy itself announced. "working" is only as good as the pane:
// a cancelled turn leaves it behind, so an idle composer footer overrides it.
func hookLiveState(m session.Meta, p liveProbes, r lastStepRow, haveStep bool) string {
	slotSid := session.UUID(m.Dir, m.Name)
	rec, ok := currentRecord(slotSid)
	if !ok || rec.State == "" {
		return ""
	}
	if conv := sids.Read(slotSid); conv != "" && rec.ConvID != "" && conv != rec.ConvID {
		return ""
	}
	at, err := time.Parse(time.RFC3339, rec.At)
	if err != nil {
		return ""
	}
	if rec.State == "idle" {
		// A new input recorded after the Stop is evidence of a new turn that needs no DB write
		// and no grace: the status record holds the "working" the input route stamped, and
		// reading idle here would let the turn-end poll consume that turn as completed.
		if newInputSince(slotSid, rec, at) {
			return ""
		}
		// A Stop is only as current as the next PreInvocation. When that hook went missing
		// (timeout, edited hooks.json), a working step past the one the Stop saw is a new turn.
		// Until idleGrace has passed the DB may still be landing the turn's own last steps, so
		// it is not consulted; after it, a step beyond the Stop's that is still working wins.
		if haveStep && dbState(r) == "working" && p.now().Sub(at) > idleGrace {
			// Without the Stop's step idx nothing separates the turn's own tail from a new turn
			// (input typed in the terminal leaves no status record), and a step still working
			// past the grace is no tail: the DB decides.
			if rec.StepIdx == nil || r.idx > *rec.StepIdx {
				return ""
			}
		}
		return "idle"
	}
	if p.now().Sub(at) >= staleAfter {
		return ""
	}
	if p.paneIdleSettled(m.Name) {
		return "idle"
	}
	return "working"
}

// newInputSince reports whether the slot's status record says "working" from a write after the
// Stop. A Stop that captured the record's Rev is compared by Rev (second-precision timestamps
// cannot order an input in the Stop's own second); a record without one falls back to the
// timestamp, strictly after.
func newInputSince(slotSid string, rec hookRecord, t time.Time) bool {
	st, ok := status.Read(slotSid)
	if !ok || st.State != "working" {
		return false
	}
	if rec.StatusRev != nil {
		return st.Rev != *rec.StatusRev
	}
	ts, err := time.Parse(time.RFC3339, st.TS)
	return err == nil && ts.After(t)
}

// clearHook forgets the slot's record: a hook state of an earlier launch says nothing about
// this one.
func clearHook(slotSid string) { hookRecords.Remove(slotSid) }

func hooksPath() string { return filepath.Join(paths.GeminiHome(), "config", "hooks.json") }

// hookEntry is af's hooks.json entry. SessionStart / PreInvocation / Stop take handlers
// directly; the tool events take {matcher, hooks} groups (a grouped SessionStart never fired —
// measured on 1.3.5).
func hookEntry(exe string) map[string]any {
	handler := func(event string) map[string]any {
		return map[string]any{
			"type":    "command",
			"command": session.ShellQuote(exe) + " " + HookSubcommand + " " + event,
			"timeout": hookTimeout,
		}
	}
	return map[string]any{
		"SessionStart":  []any{handler("SessionStart")},
		"PreInvocation": []any{handler("PreInvocation")},
		"PreToolUse":    []any{map[string]any{"matcher": "*", "hooks": []any{handler("PreToolUse")}}},
		"Stop":          []any{handler("Stop")},
	}
}

// EnsureHooks installs af's named entry into the global hooks.json, leaving every other name
// as the user wrote it (values preserved; whitespace is normalised). Idempotent: nothing is written when the entry is already
// current. Run at every launch because the command embeds this binary's path, which a rebuilt
// or relocated agent invalidates. A file that is not plain JSON, or a symlink that resolves to
// nothing, is left alone (the error says why) and the slot runs on the DB path. The caller logs.
func EnsureHooks() error { return EnsureHooksFor(paths.ExePath()) }

// EnsureHooksFor is EnsureHooks with the hook command pointing at exe, for the contract test
// that drives a real agy through a stub handler.
func EnsureHooksFor(exe string) error { return ensureHooksAt(hooksPath(), exe) }

// ensureHooksMaxTries bounds the re-read loop when the user saves the file between our read
// and our rename.
const ensureHooksMaxTries = 3

func ensureHooksAt(path, exe string) error {
	// Resolve the real target first so a rename never replaces a symlink, whether the link is the
	// file or the config directory. A link whose target is missing is not ours to create through.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	target := filepath.Join(dir, filepath.Base(path))
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if target, err = filepath.EvalSymlinks(target); err != nil {
			return fmt.Errorf("%s is a dangling symlink, leaving it alone: %w", path, err)
		}
	}
	unlock, err := lockHooks(target)
	if err != nil {
		return err
	}
	defer unlock()
	for range ensureHooksMaxTries {
		done, err := ensureHooksOnce(target, exe)
		if err != nil || done {
			return err
		}
	}
	return fmt.Errorf("%s kept changing while it was being updated, leaving it alone", path)
}

// ensureHooksOnce merges once. done=false means the file changed under us before the rename and
// the caller should read it again.
func ensureHooksOnce(target, exe string) (done bool, err error) {
	orig, err := os.ReadFile(target)
	var mode os.FileMode = 0o600
	root := map[string]json.RawMessage{}
	switch {
	case err == nil:
		if err := json.Unmarshal(orig, &root); err != nil || root == nil {
			return false, fmt.Errorf("%s is not a plain JSON object, leaving it alone", target)
		}
		if fi, err := os.Stat(target); err == nil {
			mode = fi.Mode().Perm()
		}
	case !os.IsNotExist(err):
		return false, err
	default:
		orig = nil
	}
	want, err := json.Marshal(hookEntry(exe))
	if err != nil {
		return false, err
	}
	if cur, ok := root[hookName]; ok && jsonEqual(cur, want) {
		return true, nil
	}
	root[hookName] = want
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".hooks.json.af-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(mode); err != nil { // keep the user's mode; a new file is 0600
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	// An editor may have saved since our read: do not publish a merge of the old content.
	if now, err := os.ReadFile(target); !(err == nil && string(now) == string(orig)) && !(orig == nil && os.IsNotExist(err)) {
		return false, nil
	}
	return true, os.Rename(tmp.Name(), target)
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// hooksLockPath is the cross-process lock for hooks.json, kept in the agent's own state so
// nothing is added to the user's config directory. Several workspace-agent processes can
// launch slots at once; hooksMu alone covers only one.
func hooksLockPath() string { return filepath.Join(paths.AgentStateDir(), "agy-hooks.lock") }

var hooksMu sync.Mutex

// lockHooks serialises writers of hooks.json across processes. The wait is bounded: a launch
// must not hang on a stuck holder, and giving up only costs the hook signal.
func lockHooks(string) (func(), error) {
	hooksMu.Lock()
	if err := os.MkdirAll(filepath.Dir(hooksLockPath()), 0o700); err != nil {
		hooksMu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(hooksLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		hooksMu.Unlock()
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for !tryFlock(f) {
		if time.Now().After(deadline) {
			_ = f.Close()
			hooksMu.Unlock()
			return nil, fmt.Errorf("timed out waiting for the hooks.json lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() { _ = f.Close(); hooksMu.Unlock() }, nil
}

// lastStepIdx is the conversation DB's newest step index, read-only; ok=false when unreadable.
func lastStepIdx(conv string) (int, bool) {
	db, err := sql.Open("sqlite", "file:"+conversationDBPath(conv)+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return 0, false
	}
	defer db.Close()
	var idx int
	if db.QueryRow(`SELECT idx FROM steps ORDER BY idx DESC LIMIT 1`).Scan(&idx) != nil {
		return 0, false
	}
	return idx, true
}
