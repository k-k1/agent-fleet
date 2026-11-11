package memoryx

// The Agent memory switch carries claude's native memory across by itself (#1972, ADR 0108
// decision 6): switching on imports it, switching off writes AF memory back.
//
// It is a level-triggered reconciler, not an edge handler. The phase is persisted beside the
// store and the loop compares it with the switch on every wake-up and at Agent start, so a pref
// that changed while the workspace was stopped, or a crash in the middle of a run, converges
// the same way. Both operations are idempotent, so a run is resumed by running it again.
//
//	off ──want on──▶ importing ──done──▶ on ──want off──▶ writing_back ──done──▶ off
//
// A flip back during a run turns the phase around (importing ⇄ writing_back); the run in flight
// stops at its next chunk boundary. claude's native memory is off in every phase but `off`
// (AutoMemoryOff), so a session launched between the flip and the write-back cannot write native
// files that the write-back would then see as conflicts.
//
// The run drives the same evaluation, secret scan and publish path as the member's own import
// and write-back, under the same store locks, with two differences: the import reports an AF
// copy edited in AF as a conflict instead of replacing it, and the write-back never overwrites.
// Values never reach a log or the run record: only names the scan vouched for, rule names and
// counts.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	autoPhaseOff         = "off"
	autoPhaseImporting   = "importing"
	autoPhaseOn          = "on"
	autoPhaseWritingBack = "writing_back"

	autoRunImport    = "import"
	autoRunWriteBack = "write_back"
	autoRunCatchUp   = "catch_up"

	// autoImportChunk is how many files one apply takes (both store locks are held for a commit
	// per file); the switch is looked at between chunks.
	autoImportChunkDefault = 25
	// autoImportPasses bounds how often a project is evaluated again because files changed under
	// the apply.
	autoImportPasses = 3
	// autoRowsMax bounds the skipped and conflict rows kept in the run record.
	autoRowsMax = 100
	// autoFailureReport is the attempt after which a failing write-back is reported as failing;
	// it keeps retrying at the longest delay.
	autoFailureReport = 3
	// autoPoll is how often the loop looks at the sessions that still hold the catch-up.
	autoPoll = 30 * time.Second
)

var (
	// AutoSyncWant is the Agent memory switch (ui-prefs agentMemory), wired by package main. A nil
	// hook reads as off.
	AutoSyncWant func() bool
	// AutoSyncLiveClaudeSessions names the claude sessions running now. Package main wires it.
	AutoSyncLiveClaudeSessions func() []string
	// AutoSyncCodexFlag reads codex's features.memories (found=false: the key is absent), and
	// AutoSyncCodexSet writes it. Stage A of codex support is this flag only (#1972 §6). Nil hooks
	// leave codex alone.
	AutoSyncCodexFlag func() (val, found bool)
	AutoSyncCodexSet  func(on bool) error
	// AutoSyncNotify is told once about a finished run that left conflicts or skipped files.
	AutoSyncNotify func(run AutoSyncRun)

	// autoImportChunk is a variable so a test can cut a run at a chunk boundary.
	autoImportChunk = autoImportChunkDefault

	// autoChunkHook runs before every chunk and project boundary check; nil outside tests.
	autoChunkHook func()

	autoRunMu   sync.Mutex // one run at a time: the single flight
	autoMu      sync.Mutex // the state file
	autoWake    = make(chan struct{}, 1)
	autoBackoff = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	// autoNow is the clock of the retry schedule; tests move it.
	autoNow = time.Now
)

func autoWant() bool { return AutoSyncWant != nil && AutoSyncWant() }

func autoStatePath() string { return filepath.Join(claude.ConfigDir(), "af-agent-memory.mode.json") }

// autoSyncState is what is persisted.
type autoSyncState struct {
	Phase string `json:"phase"`
	Since string `json:"since"`
	// Run is the run in flight or the last one that finished.
	Run *AutoSyncRun `json:"run,omitempty"`
	// Attempts counts the failed tries of the write-back in a row; NextAttempt is when the next
	// one is due.
	Attempts    int    `json:"attempts,omitempty"`
	NextAttempt string `json:"nextAttempt,omitempty"`
	// CatchUp names the claude sessions that were running at the switch-on and so launched with
	// native memory on. When the last of them ends, the import runs once more (#1972 §3).
	CatchUp []string `json:"catchUp,omitempty"`
	// NativeBackOn: the member chose to switch claude's native memory back on although the
	// write-back had not finished. Cleared by the next switch-on.
	NativeBackOn bool `json:"nativeBackOn,omitempty"`
	// Codex records codex's own features.memories as it was when AF memory went on.
	Codex *autoSyncCodex `json:"codex,omitempty"`
}

type autoSyncCodex struct {
	// Restore is the value to put back when AF memory goes off. Only `true` is ever restored: AF
	// never forces codex's memories on.
	Restore bool `json:"restore"`
	// Last is the value AF last saw or wrote; a different value later is the member's edit.
	Last bool `json:"last"`
	// Forcing: the flag was `true` when AF memory went on and is still to be switched off. Set
	// before the flag is touched, so a failed write is retried and a kill loses nothing.
	Forcing bool `json:"forcing,omitempty"`
}

// AutoSyncRun is the result of one run, shown on Settings > Agent memory.
type AutoSyncRun struct {
	Kind     string `json:"kind"`
	Started  string `json:"started"`
	Finished string `json:"finished,omitempty"`
	// Error is the kind of failure that stopped the run ("" when it completed).
	Error string `json:"error,omitempty"`
	// Complete says every project was handled; skipped and conflict rows can remain.
	Complete bool              `json:"complete"`
	Projects []AutoSyncProject `json:"projects"`
	// Unmapped counts claude directories no project maps to (no_project) or two do (ambiguous);
	// NoRoot counts AF projects with no main working copy to write back to.
	Unmapped int           `json:"unmapped,omitempty"`
	NoRoot   int           `json:"noRoot,omitempty"`
	Rows     []AutoSyncRow `json:"rows,omitempty"`
	// MuseUnmapped counts muse project folders no AF project maps to (or two do); MuseFailed counts
	// muse folders that could not be read or applied. Neither fails the run: muse is carried
	// across on a best-effort basis beside claude. MusePersonal counts the notes of muse's
	// personal store that are waiting for the member's explicit choice (they go to the user scope).
	MuseUnmapped int `json:"museUnmapped,omitempty"`
	MuseFailed   int `json:"museFailed,omitempty"`
	MusePersonal int `json:"musePersonal,omitempty"`
	// RowsCut counts the rows left out of Rows.
	RowsCut int `json:"rowsCut,omitempty"`
	// Notified latches once the Console was told about the leftovers of this run.
	Notified bool `json:"notified,omitempty"`
}

// AutoSyncProject is one project's counts: import (imported, updated, unchanged, conflict,
// secret, invalid, forgotten) or write-back (written, updated, removed, unchanged, conflict,
// secret, skipped).
type AutoSyncProject struct {
	// Agent names the memory the counts come from; empty is claude, "muse" is muse's own stores.
	Agent   string         `json:"agent,omitempty"`
	ID      string         `json:"id"`
	Display string         `json:"display,omitempty"`
	Counts  map[string]int `json:"counts"`
}

// AutoSyncRow is one file the run did not apply. Name passed the scan to be shown; secret rows
// carry the rule names only.
type AutoSyncRow struct {
	Agent   string   `json:"agent,omitempty"` // as AutoSyncProject.Agent
	Project string   `json:"project"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	Rules   []string `json:"rules,omitempty"`
}

func (r *AutoSyncRun) leftovers() int {
	n := len(r.Rows) + r.RowsCut
	return n
}

func (r *AutoSyncRun) addRow(row AutoSyncRow) {
	if len(r.Rows) >= autoRowsMax {
		r.RowsCut++
		return
	}
	r.Rows = append(r.Rows, row)
}

// autoLoadState says what the state file holds.
type autoLoadState int

const (
	autoStateMissing autoLoadState = iota // no file: a first start
	autoStateLoaded
	autoStateUnreadable // a file that cannot be read or parsed: what it said is not known
)

func autoLoadLocked() (autoSyncState, autoLoadState) {
	var st autoSyncState
	b, err := os.ReadFile(autoStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return autoSyncState{}, autoStateMissing
	}
	if err != nil || json.Unmarshal(b, &st) != nil {
		return autoSyncState{}, autoStateUnreadable
	}
	switch st.Phase {
	case autoPhaseOff, autoPhaseImporting, autoPhaseOn, autoPhaseWritingBack:
		return st, autoStateLoaded
	}
	return autoSyncState{}, autoStateUnreadable
}

func autoSaveLocked(st autoSyncState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := autoStatePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, autoStatePath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Problems the Agent could not resolve by itself, shown in the status. Each is cleared by the
// pass that succeeds.
const (
	autoProblemUnreadable = "state_unreadable"
	autoProblemNotSaved   = "state_not_saved"
	autoProblemCodex      = "codex_flag"
)

var (
	autoProblemMu sync.Mutex
	autoProblems  = map[string]bool{}
)

func autoProblem(code string, on bool) {
	autoProblemMu.Lock()
	defer autoProblemMu.Unlock()
	if on {
		autoProblems[code] = true
	} else {
		delete(autoProblems, code)
	}
}

func autoProblemList() []string {
	autoProblemMu.Lock()
	defer autoProblemMu.Unlock()
	var out []string
	for k := range autoProblems {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errAutoState: the phase could not be read or saved, so nothing that depends on it was started.
var errAutoState = errors.New("agent memory auto sync: state unavailable")

// autoUpdate loads the state (`off` when there is no file), lets f change it and saves it. The
// state it returns is the durable one: when the save fails, or the file cannot be read, it
// returns errAutoState and the unchanged state, so a caller never acts on a phase that is not on
// disk. Nothing is overwritten when the file exists but cannot be read.
func autoUpdate(f func(st *autoSyncState)) (autoSyncState, error) {
	autoMu.Lock()
	defer autoMu.Unlock()
	st, ls := autoLoadLocked()
	if ls == autoStateUnreadable {
		autoProblem(autoProblemUnreadable, true)
		return autoSyncState{Phase: autoPhaseOff}, errAutoState
	}
	autoProblem(autoProblemUnreadable, false)
	if ls == autoStateMissing {
		st = autoSyncState{Phase: autoPhaseOff, Since: autoNow().UTC().Format(time.RFC3339)}
	}
	// Compared as written: st holds pointers, so a struct copy would change with it.
	before, _ := json.Marshal(st)
	durable := st
	if durable.Codex != nil {
		c := *durable.Codex
		durable.Codex = &c
	}
	f(&st)
	if after, _ := json.Marshal(st); ls == autoStateMissing || string(before) != string(after) {
		if err := autoSaveLocked(st); err != nil {
			log.Printf("agent memory: auto sync: state not saved (%s)", agentMemErrKind(err))
			autoProblem(autoProblemNotSaved, true)
			return durable, errAutoState
		}
		// Cleared only by a save that happened: an update that changes nothing proves nothing about
		// the disk, and must not hide a transition that could not be recorded.
		autoProblem(autoProblemNotSaved, false)
	}
	return st, nil
}

// autoAdopt records the phase an Agent with no state file is in: one that already had the switch
// on (it predates this reconciler) has nothing owed, so it starts at `on` and no import runs
// unasked. It is done once, at start, only when there is no file at all, and never by a flip: a
// flip on an Agent with no state must import. A file that exists but cannot be read is not
// "first start": its phase is unknown, so nothing is adopted over it.
func autoAdopt() {
	autoMu.Lock()
	defer autoMu.Unlock()
	switch _, ls := autoLoadLocked(); ls {
	case autoStateLoaded:
		return
	case autoStateUnreadable:
		autoProblem(autoProblemUnreadable, true)
		return
	}
	st := autoSyncState{Phase: autoPhaseOff, Since: autoNow().UTC().Format(time.RFC3339)}
	if autoWant() {
		st.Phase = autoPhaseOn
	}
	if err := autoSaveLocked(st); err != nil {
		log.Printf("agent memory: auto sync: state not saved (%s)", agentMemErrKind(err))
		autoProblem(autoProblemNotSaved, true)
	}
}

// AutoSyncNativeMemoryOff says whether a claude launch must switch its native memory off: in
// every phase but `off`, whenever the switch itself is on, and whenever the phase is not known
// (an unreadable state file). Before the first state is written (an Agent that has never
// reconciled) the switch alone decides.
func AutoSyncNativeMemoryOff() bool {
	if autoWant() {
		return true
	}
	autoMu.Lock()
	defer autoMu.Unlock()
	st, ls := autoLoadLocked()
	return ls == autoStateUnreadable || ls == autoStateLoaded && st.Phase != autoPhaseOff
}

// The codex flag (stage A) is reconciled from the persisted record on every pass, in every
// phase, so a failed write is retried and a crash cannot lose the value to restore. The order is
// always: record durably, then touch the flag, then record that it is done.

// autoCodexWatch notices an edit of the flag the member made: their value replaces the recorded
// one. It only reads, so it can run inside an update.
func autoCodexWatch(st *autoSyncState) {
	if st.Codex == nil || st.Codex.Forcing || AutoSyncCodexFlag == nil {
		return
	}
	if cur, _ := AutoSyncCodexFlag(); cur != st.Codex.Last {
		st.Codex.Restore, st.Codex.Last = cur, cur
	}
}

// autoCodexMu makes the whole of a codex reconcile (record, touch the flag, mark done) one
// flight, whoever calls it: the loop, or a member action such as abandon. Lock order: autoCodexMu
// before autoMu, never the other way round.
var autoCodexMu sync.Mutex

func autoCodexReconcile() {
	if AutoSyncCodexFlag == nil || AutoSyncCodexSet == nil {
		return
	}
	autoCodexMu.Lock()
	defer autoCodexMu.Unlock()
	st, err := autoUpdate(func(st *autoSyncState) {
		switch {
		case st.Phase != autoPhaseOff && st.Codex == nil:
			// Absent and false are both "off" for codex; only a `true` is switched off, and only
			// a `true` is restored.
			val, found := AutoSyncCodexFlag()
			on := found && val
			st.Codex = &autoSyncCodex{Restore: on, Last: on, Forcing: on}
		default:
			autoCodexWatch(st)
		}
	})
	if err != nil || st.Codex == nil {
		return
	}
	ok := true
	switch {
	case st.Phase != autoPhaseOff && st.Codex.Forcing:
		if cur, _ := AutoSyncCodexFlag(); cur {
			if err := AutoSyncCodexSet(false); err != nil {
				log.Printf("agent memory: auto sync: codex memories flag not switched off (%s)", agentMemErrKind(err))
				ok = false
				break
			}
		}
		if _, err := autoUpdate(func(s *autoSyncState) {
			if s.Codex != nil && s.Codex.Forcing {
				s.Codex.Forcing, s.Codex.Last = false, false
			}
		}); err != nil {
			ok = false // done but not recorded: the next pass records it
		}
	case st.Phase == autoPhaseOff:
		if st.Codex.Restore {
			if cur, _ := AutoSyncCodexFlag(); !cur {
				if err := AutoSyncCodexSet(true); err != nil {
					log.Printf("agent memory: auto sync: codex memories flag not restored (%s)", agentMemErrKind(err))
					ok = false
					break
				}
			}
		}
		if _, err := autoUpdate(func(s *autoSyncState) {
			if s.Phase == autoPhaseOff {
				s.Codex = nil
			}
		}); err != nil {
			ok = false
		}
	}
	autoProblem(autoProblemCodex, !ok)
}

// autoAdvance applies the phase edges the switch asks for. It only changes the recorded phase,
// so the ui-prefs PUT can call it before it answers: a claude session launched right after the
// flip already sees native memory off. Nothing else starts before the new phase is on disk; when
// it cannot be saved the error says so and the old phase stays.
func autoAdvance() (autoSyncState, error) {
	want := autoWant()
	return autoUpdate(func(st *autoSyncState) {
		now := autoNow().UTC().Format(time.RFC3339)
		switch {
		case st.Phase == autoPhaseOff && want:
			st.Phase, st.Since, st.Run, st.NativeBackOn = autoPhaseImporting, now, nil, false
			st.CatchUp = nil
			if AutoSyncLiveClaudeSessions != nil {
				st.CatchUp = AutoSyncLiveClaudeSessions()
			}
		case st.Phase == autoPhaseOn && !want, st.Phase == autoPhaseImporting && !want:
			// The member's last edit of codex's flag is read before the phase leaves `on`, not at
			// the next poll. CatchUp is kept: sessions that launched with native memory on are
			// still owed their catch-up if the switch comes back before the write-back finishes.
			autoCodexWatch(st)
			st.Phase, st.Since, st.Attempts, st.NextAttempt = autoPhaseWritingBack, now, 0, ""
		case st.Phase == autoPhaseWritingBack && want:
			st.Phase, st.Since, st.Attempts, st.NextAttempt, st.NativeBackOn = autoPhaseImporting, now, 0, "", false
		}
	})
}

// AutoSyncWake is called when the switch may have changed. It moves the phase at once and wakes
// the loop for the work.
func AutoSyncWake() {
	_, _ = autoAdvance() // a failure is in the status and the loop tries again
	select {
	case autoWake <- struct{}{}:
	default:
	}
}

// AutoSyncStart runs the reconciler for the life of the process. Starting it is what makes a
// restart, or a switch that changed while the workspace was stopped, converge.
func AutoSyncStart() {
	autoAdopt()
	go func() {
		for {
			wait := autoSyncStep()
			select {
			case <-autoWake:
			case <-time.After(wait):
			}
		}
	}()
}

// AutoSyncRunUntilIdle runs the loop's steps synchronously until there is nothing to do now. The
// loop does this on its own in production; package main's tests call it instead of starting one.
func AutoSyncRunUntilIdle() {
	for i := 0; i < 20; i++ {
		if autoSyncStep() > 0 {
			return
		}
	}
}

// autoSyncStep does what the recorded phase calls for and says how long to wait for the next one
// (0 asks to be called again at once).
func autoSyncStep() time.Duration {
	autoRunMu.Lock()
	defer autoRunMu.Unlock()
	st, err := autoAdvance()
	if err != nil {
		// The phase is not known or not saved: start nothing, keep native memory off (see
		// AutoSyncNativeMemoryOff) and look again later.
		return autoBackoff[0]
	}
	autoCodexReconcile()
	defer autoCodexReconcile()
	switch st.Phase {
	case autoPhaseImporting:
		run := autoNewRun(autoRunImport)
		stopped := autoImportAll(&run, true, func() bool { return !autoWant() })
		return autoFinishImport(run, stopped)
	case autoPhaseWritingBack:
		due, _ := time.Parse(time.RFC3339, st.NextAttempt)
		if now := autoNow(); st.NextAttempt != "" && now.Before(due) {
			return due.Sub(now)
		}
		run := autoNewRun(autoRunWriteBack)
		err := autoWriteBackAll(&run, func() bool { return !autoWant() })
		return autoFinishWriteBack(run, err)
	case autoPhaseOn:
		return autoOnStep()
	}
	return autoPoll
}

func autoNewRun(kind string) AutoSyncRun {
	return AutoSyncRun{Kind: kind, Started: autoNow().UTC().Format(time.RFC3339), Projects: []AutoSyncProject{}}
}

func autoFinishImport(run AutoSyncRun, stopped bool) time.Duration {
	run.Finished = autoNow().UTC().Format(time.RFC3339)
	if stopped {
		// The switch moved under the run; the next step reconciles toward the new target.
		return 0
	}
	run.Complete = run.Error == ""
	if !run.Complete {
		// A failure that is not the switch (the store could not be read): stay in `importing`
		// and try again later; the claude side is untouched either way.
		autoUpdate(func(st *autoSyncState) {
			if st.Phase == autoPhaseImporting {
				st.Run = &run
			}
		})
		return autoBackoff[0]
	}
	if _, err := autoUpdate(func(st *autoSyncState) {
		if st.Phase == autoPhaseImporting {
			st.Phase, st.Since, st.Run = autoPhaseOn, run.Finished, &run
		}
	}); err != nil {
		// Imported but not recorded: the phase is still `importing`, so wait and run it again
		// (idempotent) rather than spin; the status carries state_not_saved meanwhile.
		return autoBackoff[0]
	}
	autoNotify(run.Kind)
	return 0
}

func autoFinishWriteBack(run AutoSyncRun, err error) time.Duration {
	run.Finished = autoNow().UTC().Format(time.RFC3339)
	if errors.Is(err, errAutoStopped) {
		return 0
	}
	var delay time.Duration
	run.Complete = err == nil
	if err != nil {
		run.Error = autoErrKind(err)
	}
	_, uerr := autoUpdate(func(st *autoSyncState) {
		if st.Phase != autoPhaseWritingBack {
			return
		}
		st.Run = &run
		if err != nil {
			st.Attempts++
			delay = autoBackoff[min(st.Attempts-1, len(autoBackoff)-1)]
			st.NextAttempt = autoNow().Add(delay).UTC().Format(time.RFC3339)
			return
		}
		st.Phase, st.Since, st.Attempts, st.NextAttempt, st.NativeBackOn = autoPhaseOff, run.Finished, 0, "", false
		st.CatchUp = nil
	})
	if uerr != nil && err == nil {
		return autoBackoff[0] // done, but not recorded: the next pass writes back again (idempotent)
	}
	if err == nil {
		autoNotify(run.Kind)
		return 0
	}
	return delay
}

// autoOnStep is the `on` phase's upkeep: notice the member's own edit of codex's flag, and run
// the catch-up import when the last session that launched with native memory on has ended.
func autoOnStep() time.Duration {
	st, err := autoUpdate(func(st *autoSyncState) { autoCodexWatch(st) })
	if err != nil {
		return autoBackoff[0]
	}
	if len(st.CatchUp) == 0 {
		return autoPoll
	}
	live := map[string]bool{}
	if AutoSyncLiveClaudeSessions != nil {
		for _, n := range AutoSyncLiveClaudeSessions() {
			live[n] = true
		}
	}
	var still []string
	for _, n := range st.CatchUp {
		if live[n] {
			still = append(still, n)
		}
	}
	if len(still) > 0 {
		if len(still) != len(st.CatchUp) {
			autoUpdate(func(s *autoSyncState) { s.CatchUp = still })
		}
		return autoPoll
	}
	// Recorded as done before it runs: a crash in the middle repeats nothing the next import
	// would not skip anyway, and a failing one must not hold the phase back.
	run := autoNewRun(autoRunCatchUp)
	stopped := autoImportAll(&run, true, func() bool { return !autoWant() })
	if stopped {
		return 0
	}
	run.Finished = autoNow().UTC().Format(time.RFC3339)
	run.Complete = run.Error == ""
	if _, err := autoUpdate(func(s *autoSyncState) {
		if s.Phase == autoPhaseOn {
			s.CatchUp, s.Run = nil, &run
		}
	}); err != nil {
		return autoBackoff[0]
	}
	autoNotify(run.Kind)
	return 0
}

// autoNotify tells the Console once when a finished run left something for the member to look at.
func autoNotify(kind string) {
	if AutoSyncNotify == nil {
		return
	}
	var run *AutoSyncRun
	_, err := autoUpdate(func(st *autoSyncState) {
		if st.Run != nil && st.Run.Kind == kind && !st.Run.Notified && st.Run.leftovers() > 0 {
			st.Run.Notified = true
			c := *st.Run
			run = &c
		}
	})
	if run != nil && err == nil {
		AutoSyncNotify(*run)
	}
}

// errAutoStopped: the switch moved while a write-back was between projects.
var errAutoStopped = errors.New("switch changed")

// autoErrKind names a failure without any text that came from the filesystem or git.
func autoErrKind(err error) string {
	var ue *memoryUserErr
	if errors.As(err, &ue) {
		return ue.Code
	}
	return agentMemErrKind(err)
}

// autoShowable says whether a project's display name may be put in the record.
func autoShowable(s string) string {
	if s != "" && agentMemImportShowable(s) {
		return s
	}
	return ""
}

// autoImportAll imports every mapped claude project under the automatic rules. stop is asked
// between chunks; true ends the run early (stopped=true). A claude directory that no project (or
// more than one) maps to is counted, not imported.
func autoImportAll(run *AutoSyncRun, auto bool, stop func() bool) (stopped bool) {
	list := agentMemImportList()
	for _, s := range list.Sources {
		if s.Project == nil {
			run.Unmapped++
			continue
		}
		pr := AutoSyncProject{ID: s.Project.ID, Display: autoShowable(s.Project.Display), Counts: map[string]int{}}
		for pass := 0; pass <= autoImportPasses; pass++ {
			if stop() {
				return true
			}
			agentMemMu.RLock()
			pv, err := agentMemImportBuildAuto(s.Slug, auto)
			agentMemMu.RUnlock()
			if err != nil {
				run.Error = autoErrKind(err)
				return false
			}
			if pv.Project == nil {
				break
			}
			var todo []agentMemImportReqItem
			for _, it := range pv.Items {
				if it.Status == claudeImportNew || it.Status == claudeImportUpdate {
					todo = append(todo, agentMemImportReqItem{Name: it.Name, SourceHash: it.SourceHash})
				}
			}
			if len(todo) == 0 || pass == autoImportPasses {
				// What is left after the applies: the last evaluation says what the run did not
				// bring in, and why.
				autoImportLeftovers(run, &pr, pv.Items)
				break
			}
			for at := 0; at < len(todo); at += autoImportChunk {
				if autoChunkHook != nil {
					autoChunkHook()
				}
				if stop() {
					return true
				}
				end := min(at+autoImportChunk, len(todo))
				res, err := agentMemImportApplyAuto(agentMemImportReq{Project: pv.Project.ID, Slug: s.Slug, Items: todo[at:end]}, autoNow(), auto)
				if err != nil {
					run.Error = autoErrKind(err)
					return false
				}
				for _, r := range res.Results {
					if r.Result == "imported" || r.Result == "updated" {
						pr.Counts[r.Result]++
					}
				}
			}
		}
		run.Projects = append(run.Projects, pr)
	}
	return autoImportMuse(run, stop)
}

// autoImportLeftovers counts what an evaluation still lists and records the files the member has
// to look at. A forgotten memory is counted and never listed: it is gone on purpose.
func autoImportLeftovers(run *AutoSyncRun, pr *AutoSyncProject, items []agentMemImportItem) {
	row := func(it agentMemImportItem, status, reason string) AutoSyncRow {
		return AutoSyncRow{Agent: pr.Agent, Project: pr.ID, Name: it.Name, Status: status, Reason: reason}
	}
	for _, it := range items {
		switch it.Status {
		case claudeImportNew, claudeImportUpdate:
			// Still to do after the passes: the file kept changing under the apply.
			pr.Counts["skipped"]++
			run.addRow(row(it, "skipped", "changed_since_preview"))
		case claudeImportUnchanged, claudeImportForgotten:
			pr.Counts[it.Status]++
		case claudeImportConflict, claudeImportInvalid, museImportDiverged:
			// diverged is muse's conflict: AF holds its own version, which the import never replaces.
			pr.Counts[it.Status]++
			run.addRow(row(it, it.Status, it.Reason))
		case claudeImportSecret:
			pr.Counts[it.Status]++
			r := row(it, it.Status, "")
			for _, f := range it.Findings {
				r.Rules = appendUnique(r.Rules, f.Rule)
			}
			run.addRow(r)
		}
	}
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// autoWriteBackAll writes every AF project back into claude's memory, never overwriting. A
// project that cannot be written (the snapshot failed, a leaf changed under the apply) fails the
// run, which the phase retries; conflict and secret files are kept and listed. keepGoing is
// asked between projects.
func autoWriteBackAll(run *AutoSyncRun, keepGoing func() bool) error {
	list, err := agentMemExportList()
	if err != nil {
		return err
	}
	var failed error
	for _, row := range list.Projects {
		if row.Reason != "" {
			run.NoRoot++
			continue
		}
		if !keepGoing() {
			return errAutoStopped
		}
		id := row.Project.ID
		pr := AutoSyncProject{ID: id, Display: autoShowable(row.Project.Display), Counts: map[string]int{}}
		pv, err := agentMemExportPreviewFor(id)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if pv.Truncated {
			failed = errors.Join(failed, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "claude's memory directory has too many files to write back safely"))
			continue
		}
		res, err := agentMemExportApply(agentMemExportReq{Project: id, Token: pv.Token}, autoNow())
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		for _, r := range res.Results {
			switch r.Result {
			case "written", "updated", "removed":
				pr.Counts[r.Result]++
			case "skipped":
				pr.Counts["skipped"]++
				if r.Reason == "changed_since_preview" || r.Reason == "write_failed" {
					failed = errors.Join(failed, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "a file changed during the write-back"))
				}
			}
		}
		if res.Index == "failed" {
			failed = errors.Join(failed, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "MEMORY.md could not be written"))
		}
		pr.Counts[claudeExportUnchanged] += pv.Counts[claudeExportUnchanged]
		for _, it := range pv.Items {
			switch it.Status {
			case claudeExportConflict:
				pr.Counts[it.Status]++
				run.addRow(AutoSyncRow{Project: id, Name: it.Name, Status: it.Status, Reason: it.Reason})
			case claudeExportSecret:
				pr.Counts[it.Status]++
				r := AutoSyncRow{Project: id, Name: it.Name, Status: it.Status}
				for _, f := range it.Findings {
					r.Rules = appendUnique(r.Rules, f.Rule)
				}
				run.addRow(r)
			}
		}
		run.Projects = append(run.Projects, pr)
	}
	return failed
}

// AutoSyncStatus is the wire form of the reconciler's state.
type AutoSyncStatus struct {
	Phase string `json:"phase"`
	// Want is the switch as the Agent reads it now; Settled is phase and switch agreeing.
	Want    bool   `json:"want"`
	Settled bool   `json:"settled"`
	Since   string `json:"since,omitempty"`
	// Failing: a write-back has failed Attempts times in a row. NativeMemoryOff says claude's
	// native memory is off for new sessions now.
	Failing         bool   `json:"failing,omitempty"`
	Attempts        int    `json:"attempts,omitempty"`
	NextAttempt     string `json:"nextAttempt,omitempty"`
	CatchUpPending  int    `json:"catchUpPending,omitempty"`
	NativeMemoryOff bool   `json:"nativeMemoryOff"`
	NativeBackOn    bool   `json:"nativeBackOn,omitempty"`
	// Problems name what the Agent could not do by itself: state_unreadable, state_not_saved,
	// codex_flag. An unreadable state file shows as phase "unreadable" and failing, with native
	// memory off, until the member retries or gives up.
	Problems []string     `json:"problems,omitempty"`
	Run      *AutoSyncRun `json:"run,omitempty"`
}

func autoStatus() AutoSyncStatus {
	autoMu.Lock()
	st, ls := autoLoadLocked()
	autoMu.Unlock()
	want := autoWant()
	if ls == autoStateUnreadable {
		autoProblem(autoProblemUnreadable, true)
		return AutoSyncStatus{Phase: "unreadable", Want: want, NativeMemoryOff: true, Failing: true, Problems: autoProblemList()}
	}
	if ls == autoStateMissing {
		st.Phase = autoPhaseOff
		if want {
			st.Phase = autoPhaseOn
		}
	}
	out := AutoSyncStatus{
		Phase: st.Phase, Want: want, Since: st.Since, Attempts: st.Attempts, NextAttempt: st.NextAttempt,
		CatchUpPending: len(st.CatchUp), NativeBackOn: st.NativeBackOn, Run: st.Run,
		NativeMemoryOff: want || st.Phase != autoPhaseOff, Problems: autoProblemList(),
	}
	out.Settled = (want && st.Phase == autoPhaseOn) || (!want && st.Phase == autoPhaseOff)
	out.Failing = st.Phase == autoPhaseWritingBack && st.Attempts >= autoFailureReport
	return out
}

// autoAbandon is the member's "switch native memory back on anyway" after a write-back that
// keeps failing: claude's native memory resumes without AF memory having been written into it.
// It applies while the switch is off and the write-back is pending, and to a state file that
// cannot be read, which it replaces with a fresh `off` (the member has chosen to stop waiting).
func autoAbandon() error {
	autoMu.Lock()
	_, ls := autoLoadLocked()
	if ls == autoStateUnreadable {
		defer autoMu.Unlock()
		st := autoSyncState{Phase: autoPhaseOff, Since: autoNow().UTC().Format(time.RFC3339), NativeBackOn: true}
		if err := autoSaveLocked(st); err != nil {
			return memoryErrf(http.StatusInternalServerError, errCodeMemorySnapshotFailed, "the state file could not be replaced")
		}
		autoProblem(autoProblemUnreadable, false)
		return nil
	}
	autoMu.Unlock()
	if autoWant() {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, "Agent memory is on; there is no write-back to give up")
	}
	var done bool
	if _, err := autoUpdate(func(st *autoSyncState) {
		if st.Phase != autoPhaseWritingBack {
			return
		}
		st.Phase, st.Since, st.Attempts, st.NextAttempt, st.NativeBackOn = autoPhaseOff, autoNow().UTC().Format(time.RFC3339), 0, "", true
		st.CatchUp = nil
		done = true
	}); err != nil {
		return memoryErrf(http.StatusInternalServerError, errCodeMemorySnapshotFailed, "the state could not be saved")
	}
	if !done {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, "no write-back is pending")
	}
	autoCodexReconcile()
	return nil
}

func autoRetryNow() {
	_, _ = autoUpdate(func(st *autoSyncState) {
		if st.Phase == autoPhaseWritingBack {
			st.NextAttempt = ""
		}
	})
	select {
	case autoWake <- struct{}{}:
	default:
	}
}

// AutoSyncPreview counts what switching on (an import of every mapped claude project) or off (a
// write-back of every AF project) would do, so the confirmation can say it first. It writes
// nothing.
type AutoSyncPreview struct {
	To       string            `json:"to"`
	Projects []AutoSyncProject `json:"projects"`
	// Totals sums the projects' statuses. Apply is what a run would write: new + update for the
	// import, new + update + remove for the write-back.
	Totals   map[string]int `json:"totals"`
	Apply    int            `json:"apply"`
	Unmapped int            `json:"unmapped,omitempty"`
	NoRoot   int            `json:"noRoot,omitempty"`
	// MuseUnmapped and MusePersonal are the run's fields of the same names (muse's carry-over).
	MuseUnmapped int `json:"museUnmapped,omitempty"`
	MusePersonal int `json:"musePersonal,omitempty"`
}

func autoPreview(to string) (AutoSyncPreview, error) {
	out := AutoSyncPreview{To: to, Projects: []AutoSyncProject{}, Totals: map[string]int{}}
	switch to {
	case "on":
		for _, s := range agentMemImportList().Sources {
			if s.Project == nil {
				out.Unmapped++
				continue
			}
			agentMemMu.RLock()
			pv, err := agentMemImportBuildAuto(s.Slug, true)
			agentMemMu.RUnlock()
			if err != nil {
				return out, err
			}
			out.Projects = append(out.Projects, AutoSyncProject{ID: s.Project.ID, Display: autoShowable(s.Project.Display), Counts: pv.Counts})
			for k, n := range pv.Counts {
				out.Totals[k] += n
			}
		}
		autoMusePreview(&out)
		out.Apply = out.Totals[claudeImportNew] + out.Totals[claudeImportUpdate]
	case "off":
		list, err := agentMemExportList()
		if err != nil {
			return out, err
		}
		for _, row := range list.Projects {
			if row.Reason != "" {
				out.NoRoot++
				continue
			}
			pv, err := agentMemExportPreviewFor(row.Project.ID)
			if err != nil {
				return out, err
			}
			out.Projects = append(out.Projects, AutoSyncProject{ID: row.Project.ID, Display: autoShowable(row.Project.Display), Counts: pv.Counts})
			for k, n := range pv.Counts {
				out.Totals[k] += n
			}
		}
		out.Apply = out.Totals[claudeExportNew] + out.Totals[claudeExportUpdate] + out.Totals[claudeExportRemove]
	default:
		return out, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "to must be \"on\" or \"off\"")
	}
	sort.SliceStable(out.Projects, func(i, j int) bool {
		a, b := out.Projects[i], out.Projects[j]
		return a.ID < b.ID || a.ID == b.ID && a.Agent < b.Agent
	})
	return out, nil
}

// HandleAutoSyncStatus answers where the reconciler is and what the last run did.
func HandleAutoSyncStatus(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, autoStatus())
}

// HandleAutoSyncPreview answers the counts the switch's confirmation shows.
func HandleAutoSyncPreview(w http.ResponseWriter, r *http.Request) {
	out, err := autoPreview(r.URL.Query().Get("to"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type autoSyncAction struct {
	Action string `json:"action"` // wake | retry | abandon
}

// HandleAutoSyncAction takes the member's part: waking the reconciler once the Console flipped
// the switch, retrying a failed write-back now, or giving it up ("switch native memory back on
// anyway"). Wrapped in the member-origin mark by the route.
func HandleAutoSyncAction(w http.ResponseWriter, r *http.Request) {
	var req autoSyncAction
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	switch req.Action {
	case "wake":
		AutoSyncWake()
	case "retry":
		autoRetryNow()
	case "abandon":
		if err := autoAbandon(); err != nil {
			agentMemWriteErr(w, err)
			return
		}
	default:
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "action must be wake, retry or abandon")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, autoStatus())
}
