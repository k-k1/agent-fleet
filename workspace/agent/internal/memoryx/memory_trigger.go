package memoryx

// Agent memory version control (docs/log/39 / ADR 0022) — the trigger for automatic snapshots.
//
// docs/log/39 defines the trigger as "every claude session goes idle + debounce", but that
// transition is observed by `workspace-agent session-status`, a separate hook process
// (session_status.go), so the debounce timer cannot live in the resident process. Handing it
// over through a marker file is possible, but then a dropped hook translates directly into
// "no snapshot is ever stacked". The same semantics are therefore expressed as polling on the
// resident side:
//
//	every tick (1 minute by default): look at the newest mtime under the roots
//	  → something changed since the last snapshot
//	  → that change has been quiet for at least the debounce (5 minutes by default)
//	  → no session of the target kind is working
//	then snapshot.
//
// The walk is limited by glob to projects/*/memory, so the 883MB of transcripts on the same
// mount are never stat'ed. Being a poll, a dropped hook cannot leave a hole in the history,
// and it doubles as docs/log/39's "15-minute tick as insurance".
//
// Deferring on busy has a cap (MaxDefer, 30 minutes by default). As the false-idle work
// showed, state markers can be wrong (a stopped session still marked working), so a mistaken
// busy verdict must not turn into the worst failure mode, "history is never stacked again".

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

const (
	memoryDefaultInterval = time.Minute
	memoryDefaultDebounce = 5 * time.Minute
	memoryDefaultMaxDefer = 30 * time.Minute
)

// memoryAutoLocked is the operator's forced OFF through the environment
// (AF_MEMORY_SNAPSHOT=off). Kept as its own function to make explicit that the operator's
// setting beats the UI toggle.
func memoryAutoLocked() bool {
	v := strings.TrimSpace(os.Getenv("AF_MEMORY_SNAPSHOT"))
	return v == "0" || strings.EqualFold(v, "off") || strings.EqualFold(v, "false")
}

// memoryPrefs is the setting the Console's UI toggle flips (docs/log/39 resolution #1: the
// global OFF is a UI toggle, P2). It sits on the same mount as the repo, so it survives
// recreate / clean-home.
type memoryPrefs struct {
	Auto *bool `json:"auto,omitempty"` // nil = unset (= ON by default)
}

func memoryPrefsPath() string { return filepath.Join(claude.ConfigDir(), "af-memory.json") }

func memoryLoadPrefs() memoryPrefs {
	var p memoryPrefs
	b, err := os.ReadFile(memoryPrefsPath())
	if err != nil {
		return p
	}
	_ = json.Unmarshal(b, &p) // corrupt ⇒ the default (auto ON): a stalled history is worse
	return p
}

// memorySetAuto persists the UI toggle. It does not overwrite the environment's forced OFF,
// which wins on the read side.
func memorySetAuto(on bool) error {
	if err := os.MkdirAll(claude.ConfigDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(memoryPrefs{Auto: &on})
	if err != nil {
		return err
	}
	return os.WriteFile(memoryPrefsPath(), b, 0o600)
}

// memoryAutoEnabled reports whether automatic snapshots run; they are ON by default
// (docs/log/39 resolution #1). The environment can force them off, and the Console toggle can
// stop them too. Re-read every tick, so the toggle takes effect immediately.
func memoryAutoEnabled() bool {
	if memoryAutoLocked() {
		return false
	}
	if a := memoryLoadPrefs().Auto; a != nil {
		return *a
	}
	return true
}

// memoryEnvDuration reads an AF_MEMORY_* duration override (an invalid value falls back to
// the default).
func memoryEnvDuration(key string, def, min time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min {
		log.Printf("memory-snapshot: invalid %s %q; using %v", key, v, def)
		return def
	}
	return d
}

func memorySnapshotInterval() time.Duration {
	return memoryEnvDuration("AF_MEMORY_SNAPSHOT_INTERVAL", memoryDefaultInterval, 5*time.Second)
}

func memorySnapshotDebounce() time.Duration {
	return memoryEnvDuration("AF_MEMORY_SNAPSHOT_DEBOUNCE", memoryDefaultDebounce, 0)
}

func memorySnapshotMaxDefer() time.Duration {
	return memoryEnvDuration("AF_MEMORY_SNAPSHOT_MAX_DEFER", memoryDefaultMaxDefer, time.Minute)
}

// memoryTriggerInput bundles only the facts the trigger decision needs. Splitting the
// decision out as a pure function makes it testable without waiting on real time.
type memoryTriggerInput struct {
	Now          time.Time
	NewestChange time.Time     // newest mtime under the roots (zero = no target file)
	LastSnapshot time.Time     // time of the most recent snapshot (zero = none yet)
	Busy         bool          // at least one session of a target kind is working
	Debounce     time.Duration // how long the change must stay quiet
	MaxDefer     time.Duration // cap on deferring because of busy
}

// memoryShouldSnapshot reports whether a snapshot should be stacked now.
func memoryShouldSnapshot(in memoryTriggerInput) bool {
	if in.NewestChange.IsZero() {
		return false // not a single target file
	}
	if !in.LastSnapshot.IsZero() && !in.NewestChange.After(in.LastSnapshot) {
		return false // unchanged since the last snapshot (rejected ahead of git's no-change skip)
	}
	if in.Now.Before(in.NewestChange.Add(in.Debounce)) {
		return false // the write may not have finished yet
	}
	if in.Busy {
		// Wait while a session is running. But waiting forever would leave a permanent hole
		// in the history, so once MaxDefer has passed since the change, push through busy.
		return in.MaxDefer > 0 && !in.Now.Before(in.NewestChange.Add(in.MaxDefer))
	}
	return true
}

// memoryNewestChange returns the newest mtime among the allowlisted files of every root.
func memoryNewestChange() time.Time {
	var newest time.Time
	for _, r := range memoryRoots() {
		for _, f := range memoryCollect(r) {
			if t := time.Unix(f.MTime, 0); t.After(newest) {
				newest = t
			}
		}
	}
	return newest
}

// memoryBusyKinds returns the version-controlled kinds that have a working session. State
// comes from the existing state detection (the status store) — snapshots do not add a new
// observation path. restore warns per kind, so this returns a set rather than a bool.
func memoryBusyKinds() map[string]bool {
	target := map[string]bool{}
	for _, r := range memoryRoots() {
		target[r.Kind] = true
	}
	busy := map[string]bool{}
	for _, m := range session.ListMetas() {
		if !target[m.Kind] || busy[m.Kind] {
			continue
		}
		if status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
			busy[m.Kind] = true
		}
	}
	return busy
}

// memoryKindsBusy reports whether any target kind has a working session (the deferral test
// for automatic snapshots).
func memoryKindsBusy() bool { return len(memoryBusyKinds()) > 0 }

// StartMemorySnapshotLoop starts the automatic snapshot polling loop. Only the environment's
// forced OFF skips building the loop at all — the UI toggle flips while the process runs, so
// it is re-read every tick (no restart required).
func StartMemorySnapshotLoop() {
	if memoryAutoLocked() {
		log.Printf("memory-snapshot: auto snapshot disabled (AF_MEMORY_SNAPSHOT)")
		return
	}
	interval, debounce, maxDefer := memorySnapshotInterval(), memorySnapshotDebounce(), memorySnapshotMaxDefer()
	go func() {
		time.Sleep(45 * time.Second) // stay out of the boot-time rush (reconcile / cred seeding)
		for {
			if memoryAutoEnabled() {
				memorySnapshotTick(time.Now(), debounce, maxDefer)
			}
			time.Sleep(interval)
		}
	}()
}

// lastMemorySnapshotErr suppresses repeated logging of the same failure (a 1-minute loop must
// not fill the log).
var lastMemorySnapshotErr string

// memorySnapshotTick is one period's decision and execution, split out of the loop body so it
// can be tested.
func memorySnapshotTick(now time.Time, debounce, maxDefer time.Duration) bool {
	if len(memoryRoots()) == 0 {
		return false
	}
	in := memoryTriggerInput{
		Now: now, NewestChange: memoryNewestChange(), LastSnapshot: memoryHeadTime(),
		Busy: memoryKindsBusy(), Debounce: debounce, MaxDefer: maxDefer,
	}
	if !memoryShouldSnapshot(in) {
		return false
	}
	res, err := memorySnapshot(memoryTriggerAuto, now)
	if err != nil {
		if msg := err.Error(); lastMemorySnapshotErr != msg {
			lastMemorySnapshotErr = msg
			log.Printf("memory-snapshot: %v", err)
		}
		return false
	}
	lastMemorySnapshotErr = ""
	if res.Committed {
		log.Printf("memory-snapshot: %s (%d files changed)", res.Rev[:8], len(res.Changed))
	}
	return res.Committed
}
