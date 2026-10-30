package cursor

// The `stop` hook as the source of per-turn token usage for Terminal sessions (#2286). The
// interactive TUI runs the commands in ~/.cursor/hooks.json with a JSON object on stdin, in the
// launching pane's environment (measured on 2026.10.16: AF_SESSION_NAME arrives, so the slot is
// named by the env af set at launch). Its `stop` payload carries `input_tokens`,
// `output_tokens`, `cache_read_tokens` and `cache_write_tokens` for a `completed` turn
// (docs/log/132 §2, §5). Nothing else in the cursor CLI reports tokens on the live path.
//
// Invariants:
//   - The command never blocks or fails the CLI: every error is swallowed, nothing is printed,
//     and it only appends one small row to the ledger.
//   - Only ids and token counts are read from stdin and written down. The payload's paths,
//     e-mail and transcript location are never stored (ADR 0029: no content in the ledger).
//   - ~/.cursor/hooks.json is shared with the user: only the `stop` entry whose command runs
//     HookSubcommand is ours. Anything about the file we do not understand leaves it untouched.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// HookSubcommand is the workspace-agent subcommand the hooks.json entry runs.
const HookSubcommand = "cursor-hook"

// hookTimeout is the handler timeout (seconds) cursor enforces on us; the command only
// appends a row.
const hookTimeout = 10

// stopInput is the subset of the `stop` payload the hook reads. The token fields are pointers:
// an `aborted` / `error` stop carries none, and an absent count is not a zero count.
type stopInput struct {
	ConversationID  string `json:"conversation_id"`
	GenerationID    string `json:"generation_id"`
	Model           string `json:"model"`
	Status          string `json:"status"`
	InputTokens     *int   `json:"input_tokens"`
	OutputTokens    *int   `json:"output_tokens"`
	CacheReadTokens *int   `json:"cache_read_tokens"`
	CacheWriteToken *int   `json:"cache_write_tokens"`
}

// RunHook is `workspace-agent cursor-hook <event>`. It prints nothing and returns normally:
// a failing hook must not disturb the turn it observes.
func RunHook(args []string) {
	defer func() { _ = recover() }()
	if len(args) == 0 || args[0] != "stop" {
		return
	}
	var in stopInput
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in) != nil {
		return
	}
	recordStop(os.Getenv(agents.SessionNameEnvVar), in, time.Now())
}

// recordStop appends the ledger row of one completed turn. The generation id is the dedup key
// (usagex.Record.Key): an interrupted turn fires `stop` twice with one id, and a retried
// delivery is possible, so the aggregation counts one row per key.
func recordStop(name string, in stopInput, now time.Time) {
	if name == "" || !session.ValidName(name) || in.GenerationID == "" {
		return // a cursor-agent the user runs by hand, or a payload that cannot be attributed
	}
	// Only a completed turn that carries tokens is a row. aborted / error stops carry none and
	// are never rows; the missing counts must not be read as zeros.
	if in.Status != "completed" || in.InputTokens == nil || in.OutputTokens == nil {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Kind != session.KindCursor || m.DriverKind() == session.DriverManaged {
		return
	}
	// A cursor-agent started from inside the pane (a run_command) inherits the env but is not
	// the slot's conversation.
	if want := sids.Read(session.UUID(m.Dir, m.Name)); want != "" && in.ConversationID != "" && want != in.ConversationID {
		return
	}
	for _, p := range []*int{in.CacheReadTokens, in.CacheWriteToken} {
		if p != nil && *p < 0 {
			return
		}
	}
	if *in.InputTokens < 0 || *in.OutputTokens < 0 {
		return
	}
	cread, cwrite := intOr0(in.CacheReadTokens), intOr0(in.CacheWriteToken)
	// input_tokens already includes cache_read_tokens (docs/log/132 §5); the ledger's In is
	// the fresh part, so spend and the price estimate do not count the cached prefix twice.
	// cache_write_tokens was 0 in every measured turn, so it is kept as ccreate and not
	// subtracted from input.
	fresh := max(*in.InputTokens-cread, 0)
	rec := usagex.Record{
		TS: now.UTC().Format(time.RFC3339), Call: in.GenerationID, Feature: usagex.FeatureSession,
		Origin: session.OriginOf(m), OriginConv: m.OriginConv, Kind: m.Kind, Ref: m.Name,
		Key: "cursor-stop:" + in.GenerationID,
		In:  fresh, Out: *in.OutputTokens, CacheRead: cread, CacheCreate: cwrite,
		Spend: usagex.Spend(fresh, cwrite, *in.OutputTokens),
		OK:    true,
		// partial, not exact: the numbers are the CLI's own hook report, the multi-step sum
		// was verified on one version only, and cache_write semantics are unmeasured.
		Measured:   usagex.MeasuredPartial,
		TokensOnly: true,
	}
	// The model is the raw selector: "default" for Auto, which is not the model Auto picked.
	if in.Model != "" && in.Model != "default" {
		rec.Model, rec.ModelRaw, rec.ModelReq, rec.ModelSrc = in.Model, in.Model, in.Model, usagex.ModelRequest
	} else {
		rec.ModelRaw, rec.ModelSrc = in.Model, usagex.ModelUnknown
	}
	_ = usagex.AppendRows([]usagex.Record{rec})
}

func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func hooksPath() string { return filepath.Join(Home(), "hooks.json") }

// hookCommand is the hooks.json command string; ours is recognised by running HookSubcommand.
func hookCommand(exe string) string {
	return session.ShellQuote(exe) + " " + HookSubcommand + " stop"
}

// isOurs recognises only the exact entry hookCommand generates: the command is one
// single-quoted absolute path followed by " cursor-hook stop", and the entry has no key besides
// command and timeout. Anything that merely mentions the subcommand (an argument to another
// tool, a wrapper, a script) is the user's and is kept. hooks.json has no field for a name, and
// an extra key's effect on cursor is unmeasured, so the shape itself is the marker.
func isOurs(entry json.RawMessage) bool {
	var e map[string]json.RawMessage
	if json.Unmarshal(entry, &e) != nil {
		return false
	}
	for k := range e {
		if k != "command" && k != "timeout" {
			return false
		}
	}
	var cmd string
	if json.Unmarshal(e["command"], &cmd) != nil {
		return false
	}
	const suffix = "' " + HookSubcommand + " stop"
	if !strings.HasPrefix(cmd, "'/") || !strings.HasSuffix(cmd, suffix) {
		return false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(cmd, "'"), suffix)
	// ShellQuote writes a quote inside the path as '\'' ; any other quote ends the word early.
	return !strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'")
}

// EnsureHooks installs af's `stop` entry into the global hooks.json. Idempotent: nothing is
// written when the entry is already current. Run at every launch because the command embeds
// this binary's path, which a rebuilt or relocated agent invalidates. The global file (not the
// project's .cursor/hooks.json) is used because a project file would dirty the user's repo.
//
// The merge fails closed: a file that is not a plain JSON object, whose `version` is not 1,
// whose `hooks` is not an object or whose `hooks.stop` is not an array is left byte-for-byte
// alone (the error says why) and the session just records no usage. The user's other events
// and `stop` entries are kept as written (values preserved; whitespace and key order are
// normalised). The caller logs.
func EnsureHooks() error { return ensureHooksAt(hooksPath(), paths.ExePath()) }

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
	unlock, err := lockHooks()
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

// mergeStop returns the file content with af's stop entry in place, and whether it differs
// from orig. orig == nil means the file does not exist.
func mergeStop(orig []byte, exe string) (out []byte, changed bool, err error) {
	root := map[string]json.RawMessage{}
	if orig != nil {
		if err := json.Unmarshal(orig, &root); err != nil || root == nil {
			return nil, false, fmt.Errorf("not a plain JSON object")
		}
	}
	if v, ok := root["version"]; ok {
		var n float64
		if json.Unmarshal(v, &n) != nil || n != 1 {
			return nil, false, fmt.Errorf("unknown version %s", v)
		}
	} else if orig == nil {
		root["version"] = json.RawMessage("1")
	} else {
		return nil, false, fmt.Errorf(`no "version": 1`)
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := root["hooks"]; ok {
		if json.Unmarshal(raw, &hooks) != nil || hooks == nil {
			return nil, false, fmt.Errorf(`"hooks" is not an object`)
		}
	}
	var stops []json.RawMessage
	if raw, ok := hooks["stop"]; ok {
		// null decodes into a nil slice without error, so the type is checked on the text.
		if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' || json.Unmarshal(raw, &stops) != nil {
			return nil, false, fmt.Errorf(`"hooks.stop" is not an array`)
		}
	}
	ours, err := json.Marshal(map[string]any{"command": hookCommand(exe), "timeout": hookTimeout})
	if err != nil {
		return nil, false, err
	}
	// Replace our entry where it stands (drop any duplicates), else append after the user's.
	merged := make([]json.RawMessage, 0, len(stops)+1)
	placed := false
	for _, e := range stops {
		if !isOurs(e) {
			merged = append(merged, e)
			continue
		}
		if !placed {
			merged = append(merged, ours)
			placed = true
		}
	}
	if !placed {
		merged = append(merged, ours)
	}
	if reflect.DeepEqual(canon(stops), canon(merged)) {
		return orig, false, nil
	}
	if hooks["stop"], err = json.Marshal(merged); err != nil {
		return nil, false, err
	}
	if root["hooks"], err = json.Marshal(hooks); err != nil {
		return nil, false, err
	}
	out, err = json.MarshalIndent(root, "", "  ")
	return append(out, '\n'), true, err
}

// canon decodes entries so that two spellings of one JSON value compare equal.
func canon(in []json.RawMessage) []any {
	out := make([]any, len(in))
	for i, e := range in {
		_ = json.Unmarshal(e, &out[i])
	}
	return out
}

// maxHooksSize bounds what is read of the user's file; a larger one is not a hooks.json.
const maxHooksSize = 1 << 20

// readHooks reads path only when it is a regular file. A FIFO or device would block the
// launch forever, so it is opened non-blocking and checked with fstat before any read.
// exists=false means there is no file.
func readHooks(path string) (data []byte, mode os.FileMode, exists bool, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%s is not a regular file", path)
	}
	data, err = io.ReadAll(io.LimitReader(f, maxHooksSize+1))
	if err != nil {
		return nil, 0, false, err
	}
	if len(data) > maxHooksSize {
		return nil, 0, false, fmt.Errorf("%s is larger than %d bytes", path, maxHooksSize)
	}
	return data, fi.Mode().Perm(), true, nil
}

// Test seams: hookAfterRead runs between the read and the final check, hookBeforeSwap right
// before the atomic exchange, hookAfterSwap right after it. They are where an editor's save or
// a killed process would land.
var hookAfterRead, hookBeforeSwap, hookAfterSwap func()

func seam(f func()) {
	if f != nil {
		f()
	}
}

// maxPublishTries bounds the re-merge loop when the user keeps saving during the publish.
const maxPublishTries = 4

// ensureHooksOnce merges once. done=false means the file changed under us and the caller should
// read it again.
//
// An existing file is replaced with an atomic exchange of the path with our temp file, so the
// inode that was REALLY at the path is the one that ends up under the temp name, whatever
// replaced it since our read. That is compared with what was merged from: equal means nothing
// was lost; different means the user saved in the window (replacing the file or writing into
// it), and the swapped-out content is merged and published in turn. Guarantees:
//   - The path is never empty or missing; a process killed anywhere leaves a complete file.
//   - No inode that ever held user content is unlinked: the original, and every user version
//     swapped out of the path, stay beside it as hooks.json.af-prev.<random> (never overwriting
//     anything that already exists). They accumulate by one per update, and updates only
//     happen when the entry changes, i.e. when this binary moves.
//   - Our own earlier publish, once swapped out and verified byte-equal, is removed; nothing
//     else is. A write through a descriptor held on a kept inode after our comparison lands
//     only in that kept file.
//
// Where the exchange is unavailable (not Linux, or a filesystem without it) an existing file is
// not updated at all.
func ensureHooksOnce(target, exe string) (done bool, err error) {
	orig, mode, exists, err := readHooks(target)
	if err != nil {
		return false, fmt.Errorf("%w, leaving it alone", err)
	}
	if !exists {
		orig, mode = nil, 0o600
	}
	out, changed, err := mergeStop(orig, exe)
	if err != nil {
		return false, fmt.Errorf("%s: %w, leaving it alone", target, err)
	}
	if !changed {
		return true, nil
	}
	seam(hookAfterRead)
	if !exists {
		tmp, err := writeTemp(target, out, mode)
		if err != nil {
			return false, err
		}
		defer os.Remove(tmp)
		// Link fails with EEXIST when the user created the file since our read.
		if err := os.Link(tmp, target); err != nil {
			if os.IsExist(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	cur, published := orig, []byte(nil) // what the path is expected to hold, and our last publish
	for try := 0; try < maxPublishTries; try++ {
		merged, changed, err := mergeStop(cur, exe)
		if err != nil {
			// The user's newest content cannot be merged: put it back as written.
			if published != nil {
				// Only our own publish may be dropped; whatever else the exchange pushed out
				// (a newer save of the user's) is kept and the error says so.
				if oldB, p, serr := swapIn(target, cur, mode); serr == nil {
					if bytes.Equal(oldB, published) {
						_ = os.Remove(p)
					} else {
						keepAside(p, target)
						return false, fmt.Errorf("%s: %w; a newer save was kept beside it as %s.af-prev.*", target, err, target)
					}
				}
			}
			return false, fmt.Errorf("%s: %w, restored as saved", target, err)
		}
		if !changed && published == nil {
			return true, nil
		}
		// (unchanged after a re-read: merged is the user's content itself, to be put back)
		seam(hookBeforeSwap)
		old, p, err := swapIn(target, merged, mode)
		if err != nil {
			return false, err
		}
		seam(hookAfterSwap)
		want := orig
		if published != nil {
			want = published
		}
		if bytes.Equal(old, want) {
			if published != nil {
				_ = os.Remove(p) // our own earlier publish, nothing of the user's
			} else {
				keepAside(p, target)
			}
			return true, nil
		}
		// The path held something else: keep it and merge again from it.
		keepAside(p, target)
		cur, published = old, merged
	}
	return false, fmt.Errorf("%s kept changing while it was being updated, leaving it alone", target)
}

// swapIn exchanges content into target's place and returns what was there, with the path now
// holding it.
func swapIn(target string, content []byte, mode os.FileMode) (old []byte, oldPath string, err error) {
	tmp, err := writeTemp(target, content, mode)
	if err != nil {
		return nil, "", err
	}
	if err := exchange(tmp, target); err != nil {
		os.Remove(tmp)
		return nil, "", fmt.Errorf("cannot replace %s atomically: %w", target, err)
	}
	old, _, _, err = readHooks(tmp)
	if err != nil {
		// Unreadable swapped-out file: it stays where it is, and the content is unknown.
		return nil, tmp, err
	}
	return old, tmp, nil
}

// keepAside gives a swapped-out file a visible name beside the user's file. Link never
// overwrites; if it fails the file simply keeps its temp name.
func keepAside(p, target string) {
	var r [4]byte
	_, _ = rand.Read(r[:])
	name := target + ".af-prev." + hex.EncodeToString(r[:])
	if os.Link(p, name) == nil {
		_ = os.Remove(p)
	}
}

// writeTemp writes content to a new temp file beside target with the given mode.
func writeTemp(target string, content []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), ".hooks.json.af-*")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(content)
	cerr := f.Chmod(mode) // keep the user's mode; a new file is 0600
	if err := f.Close(); werr == nil && cerr == nil {
		werr = err
	}
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", werr
	}
	return f.Name(), nil
}

// hooksLockPath is the cross-process lock for hooks.json, kept in the agent's own state so
// nothing is added to the user's config directory.
func hooksLockPath() string { return filepath.Join(paths.AgentStateDir(), "cursor-hooks.lock") }

var hooksMu sync.Mutex

// lockHooks serialises writers of hooks.json across processes. The wait is bounded: a launch
// must not hang on a stuck holder, and giving up only costs the usage signal.
func lockHooks() (func(), error) {
	if !lockSupported {
		// Without an advisory lock two agent processes can race each other; giving up only
		// costs the usage numbers.
		return nil, fmt.Errorf("no cross-process lock on this OS")
	}
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
