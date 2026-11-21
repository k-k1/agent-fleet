package opencode

import (
	"bufio"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/listenports"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Per-call connection bindings for the session ledger rows of opencode.ai's own routes (ADR 0138
// D5b). The fold runs on usage reads and deletion, possibly long after the call, so the binding
// cannot come from the credentials at fold time: a key replaced in between would label an old
// call with the new key. It comes from evidence recorded while the call could run.
//
// The shared `opencode serve` takes OPENCODE_API_KEY from its environment once, at start, so one
// daemon start fixes the key every call of that daemon was paid with. The Console account is the
// other possible payer (opencode gates paid models on the key OR the account), and it changes
// inside the daemon, so it is recorded as observations. A call is bound only when its interval
// proves exactly one payer; any doubt gives no binding. The account pays (opencode-account binding)
// only for a daemon started with no key, from a sign-in observation of one credential that lies a
// full quiet period behind every Agent flow; a Terminal pane is never bound to the account, because
// a pane started without a key can still inherit one from the tmux server's environment. An
// explicit apiKey in the user's own opencode configuration is a third payer the Agent cannot see,
// as a sign-in by hand is not; the launch record (resolveModel) rests on the same assumption.
//
// What is proven, and what is not: the Agent's own flows are the only account changes it can see
// (oauthDaemon marks every one), the way credbind.Generation sees only the Agent's own login
// changes. A sign-in made by hand in a terminal (`opencode auth login`) is not seen. An
// observation is evidence for calls made after it, never before it, so a call's binding does not
// change when later evidence arrives (a ledger retry must fold the same row it folded first).

const bindIntervalsFile = "opencode-binding-intervals.jsonl"

// bindIntervalsKeep bounds the file. A trim never drops the newest daemon event, so the daemon
// that is running keeps its evidence however long it lives.
const bindIntervalsKeep = 256

const (
	evStart   = "start"   // a daemon this Agent started, once it answered
	evUnknown = "unknown" // a daemon whose environment is not known (adopted)
	evAccount = "account" // the Console account, as observed or as touched by an Agent flow
)

const (
	acctOut   = "out"   // observed signed out
	acctIn    = "in"    // observed signed in
	acctTouch = "touch" // an Agent flow that can change the account ran
)

// bindEvent is one line of the file. It holds bindings and states only, never a key.
type bindEvent struct {
	At   int64  `json:"at"` // unix milliseconds
	Kind string `json:"kind"`
	// Key is the binding of OPENCODE_API_KEY in the started daemon's environment ("" when the
	// key could not be bound); Keyless says the environment held none. Only start events.
	Key     string `json:"key,omitempty"`
	Keyless bool   `json:"keyless,omitempty"`
	// Reg marks a daemon event stamped earlier than the event before it: the wall clock went back,
	// so calls of this daemon cannot be told from the previous one's by time.
	Reg  bool   `json:"reg,omitempty"`
	Acct string `json:"acct,omitempty"`
	// ID, on an acctIn observation, is the binding of the signed-in Console credential
	// (credbind.Account); "" when it could not be bound. It names a payer, never a token.
	ID string `json:"id,omitempty"`
	// Gen is the supervisor generation a daemon event describes, and the one an account event was
	// read from or touched on: an account event counts only for the life of that generation.
	Gen int `json:"gen,omitempty"`
	// Boot and Up stamp the event with the kernel's boot id and uptime in milliseconds, which do not
	// follow the wall clock. Between two events of one boot, wall time and uptime advance together;
	// when they do not, the wall clock was set and the events cannot be ordered by time (Reg).
	Boot string `json:"boot,omitempty"`
	Up   int64  `json:"up,omitempty"`
	// Floor, on a Reg event, is the latest wall time the clock before it can have shown: calls of
	// this and every later life stamped at or below it may belong to an earlier daemon.
	Floor int64 `json:"floor,omitempty"`
	// Carry is the largest Floor of the Reg events a trim dropped before this event.
	Carry int64 `json:"carry,omitempty"`
	// AcctCarry, on a daemon event, is the time before which a sign-in observation of the life
	// proves no payer: account events up to it were dropped, and one of them may have been a touch
	// the quiet period counts from.
	AcctCarry int64 `json:"acarry,omitempty"`
}

// clockStamp is the current boot id and uptime in milliseconds; ok=false when the kernel does not
// say.
var clockStamp = func() (boot string, up int64, ok bool) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", 0, false
	}
	u, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", 0, false
	}
	f, _, _ := strings.Cut(strings.TrimSpace(string(u)), " ")
	sec, err := strconv.ParseFloat(f, 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(string(b)), int64(sec * 1000), true
}

// clockSlack is how far wall time and uptime may drift apart between two events before the wall
// clock counts as set.
const clockSlack = 2000

var bindIntervalsMu sync.Mutex

// seenMaxAt is the newest event time this process has read, so a gap recorded while the file is
// unreadable still reaches every life known before.
var seenMaxAt atomic.Int64

// bumpSeenMax raises seenMaxAt to v and never lowers it: a concurrent older read cannot undo a
// newer one.
func bumpSeenMax(v int64) {
	for {
		cur := seenMaxAt.Load()
		if v <= cur || seenMaxAt.CompareAndSwap(cur, v) {
			return
		}
	}
}

const bindGapsFile = "opencode-binding-intervals.gaps"

// gapsMem holds the gaps of this process, for when the gaps file cannot be written either.
var gapsMem []int64

// bindOff turns binding off for this process when even a gap could not be recorded.
var bindOff atomic.Bool

// A gap is the bound of the lives an event could not be recorded for: every life that began at or
// before it is not proven at all (the missing event may have been the boundary or the account
// touch that ended its proof). It is the later of the failed event's time and the newest time in the
// file, so a clock that was set back cannot place a life beyond it. Kept in the gaps file (append
// only, never cleared) and in memory.
func noteGap(bound int64) {
	gapsMem = append(gapsMem, bound)
	f, err := os.OpenFile(filepath.Join(paths.AgentConfigDir(), bindGapsFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		_, err = f.WriteString(strconv.FormatInt(bound, 10) + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("opencode: binding intervals gap not persisted, binding is off until restart: %v", err)
		bindOff.Store(true)
	}
}

// readGaps returns every recorded gap; ok=false when the file cannot be read whole.
func readGaps() (gaps []int64, ok bool) {
	bindIntervalsMu.Lock()
	gaps = append(gaps, gapsMem...)
	bindIntervalsMu.Unlock()
	b, err := os.ReadFile(filepath.Join(paths.AgentConfigDir(), bindGapsFile))
	if os.IsNotExist(err) {
		return gaps, true
	}
	if err != nil {
		return nil, false
	}
	for _, l := range strings.Fields(string(b)) {
		v, err := strconv.ParseInt(l, 10, 64)
		if err != nil {
			return nil, false
		}
		gaps = append(gaps, v)
	}
	return gaps, true
}

func bindIntervalsPath() string {
	return filepath.Join(paths.AgentConfigDir(), bindIntervalsFile)
}

// readBindEvents returns the recorded events in file order. ok=false when the file cannot be read
// whole or holds a line that does not parse: a lost boundary would stretch the interval before it,
// so nothing is proven then. A missing file is no events, readable.
func readBindEvents() (evs []bindEvent, ok bool) {
	defer func() {
		for _, e := range evs {
			bumpSeenMax(e.At)
		}
	}()
	f, err := os.Open(bindIntervalsPath())
	if os.IsNotExist(err) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e bindEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Kind == "" {
			return nil, false
		}
		evs = append(evs, e)
	}
	if sc.Err() != nil {
		return nil, false
	}
	return evs, true
}

// lastDaemonEvent is the index of the newest start or unknown event, -1 for none.
func lastDaemonEvent(evs []bindEvent) int {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != evAccount {
			return i
		}
	}
	return -1
}

// appendBindEvent adds one line (a single O_APPEND write), trimming the oldest lines when the file
// outgrows its bound. Done under the lock with check, which may refuse the event after looking at
// the file (false = not written, not a failure). A write that fails leaves a gap (noteGap).
func appendBindEvent(e bindEvent, check func(evs []bindEvent, e bindEvent) bool) {
	bindIntervalsMu.Lock()
	defer bindIntervalsMu.Unlock()
	evs, ok := readBindEvents()
	if !ok {
		log.Printf("opencode: binding intervals unreadable; bindings stay unproven")
		if m := seenMaxAt.Load(); m > 0 {
			noteGap(max(e.At, m))
		} else {
			noteGap(max(e.At, time.Now().UnixMilli())) // nothing known: every life up to now
		}
		return
	}
	if e.Boot == "" { // not stamped: nothing orders it against the clock
		e.Reg, e.Floor = true, math.MaxInt64
	}
	if n := len(evs); n > 0 && e.Boot != "" {
		prev := evs[n-1]
		if prev.Boot == e.Boot && prev.Up > 0 {
			if d := (e.At - prev.At) - (e.Up - prev.Up); d > clockSlack || d < -clockSlack {
				e.Reg = true
				e.Floor = max(e.Floor, prev.At+(e.Up-prev.Up)+clockSlack)
			}
		}
	}
	if e.Kind != evAccount {
		for _, o := range evs { // an event stamped after e shows the clock going back too
			if o.At >= e.At {
				e.Reg = true
				e.Floor = max(e.Floor, o.At)
			}
		}
	}
	if check != nil && !check(evs, e) {
		return
	}
	line, _ := json.Marshal(e)
	line = append(line, '\n')
	p := bindIntervalsPath()
	fail := func(err error) {
		log.Printf("opencode: binding intervals: %v", err)
		bound := e.At
		for _, o := range evs {
			bound = max(bound, o.At)
		}
		noteGap(bound)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		fail(err)
		return
	}
	if len(evs) >= bindIntervalsKeep {
		cut := min(len(evs)-bindIntervalsKeep/2, lastDaemonEvent(evs))
		carry := int64(0)
		for _, ev := range evs[:cut] {
			carry = max(carry, ev.Carry)
			if ev.Reg && !unknownClock(ev) {
				carry = max(carry, ev.Floor)
			}
		}
		if carry > 0 {
			evs[cut].Carry = max(evs[cut].Carry, carry)
		}
		kept := evs[cut:]
		if len(kept) >= bindIntervalsKeep {
			// One life holds most of the file (touches and readings of a long-running daemon): drop
			// the oldest account events after its start. Calls up to the quiet period past the last
			// one dropped are no longer placed, and no sign-in observation at or before that time
			// proves a payer (accountPayer).
			di := lastDaemonEvent(kept)
			if di >= 0 {
				// Keep the start-of-life reading (the first out) and the first event that can end it:
				// they decide every key or free binding of the life. Drop the oldest of the rest.
				pin := map[int]bool{}
				for i := di + 1; i < len(kept); i++ {
					if kept[i].Acct == acctOut && !pin[-1] {
						pin[i], pin[-1] = true, true // the first out
					}
					if kept[i].Acct != acctOut {
						pin[i] = true // the first event that is not an out
						break
					}
				}
				n := len(kept) - bindIntervalsKeep/2
				carry, acarry := kept[di].Carry, kept[di].AcctCarry
				var rest []bindEvent
				for i, ev := range kept {
					if i > di && n > 0 && !pin[i] {
						n--
						carry = max(carry, ev.Carry)
						acarry = max(acarry, ev.AcctCarry, satAdd(ev.At, accountQuiet.Milliseconds()))
						if ev.Reg {
							if !unknownClock(ev) {
								carry = max(carry, ev.Floor)
							}
							acarry = max(acarry, satAdd(ev.Floor, accountQuiet.Milliseconds()))
						}
						continue
					}
					rest = append(rest, ev)
				}
				kept = rest
				kept[di].Carry, kept[di].AcctCarry = carry, acarry
			}
		}
		var sb strings.Builder
		for _, ev := range kept {
			b, _ := json.Marshal(ev)
			sb.Write(b)
			sb.WriteByte('\n')
		}
		sb.Write(line)
		tmp, err := os.CreateTemp(filepath.Dir(p), bindIntervalsFile+".tmp*")
		if err == nil {
			_, err = tmp.WriteString(sb.String())
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Rename(tmp.Name(), p)
			}
			if err != nil {
				_ = os.Remove(tmp.Name())
			}
		}
		if err != nil {
			fail(err)
		}
		return
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		_, err = f.Write(line)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fail(err)
	}
}

// spawnStamp is the wall time and uptime at one instant (the moment a daemon process was spawned),
// taken together so that the time the daemon then needs to answer does not read as a clock change.
type spawnStamp struct {
	at      time.Time
	boot    string
	up      int64
	stamped bool
}

func stampNow() spawnStamp {
	at := time.Now()
	boot, up, ok := clockStamp()
	return spawnStamp{at: at, boot: boot, up: up, stamped: ok}
}

// stampWindow is the longest the wall and uptime readings of one stamp may lie apart. A process
// held up between the two reads (not scheduled for seconds) would pair an old wall time with a new
// uptime and read as a clock that was set; such a sample says nothing, so it is left unstamped.
const stampWindow = 250 * time.Millisecond

// stampedEvent is an event at the given instant with the uptime of that instant. An instant that
// is just now (an observation made as the event is written) is re-read together with the uptime,
// so the two belong to one moment.
func stampedEvent(at time.Time) bindEvent {
	if d := time.Since(at); d >= 0 && d < time.Second {
		w1 := time.Now()
		boot, up, ok := clockStamp()
		if !ok || time.Since(w1) > stampWindow {
			return bindEvent{At: w1.UnixMilli()}
		}
		return bindEvent{At: w1.UnixMilli(), Boot: boot, Up: up}
	}
	boot, up, _ := clockStamp()
	// clockStamp reads now; an instant a moment ago (an observation that waited for the lock) is
	// close enough, since the slack is seconds.
	return bindEvent{At: at.UnixMilli(), Boot: boot, Up: up}
}

// noteDaemonStart records generation gen of the daemon, which this Agent started at `at` (when its
// process was spawned) with environment env, the one it keeps for its whole life. Called once the
// daemon answers and its process is known to be alive.
func noteDaemonStart(sp spawnStamp, env []string, gen int) {
	e := bindEvent{At: sp.at.UnixMilli(), Boot: sp.boot, Up: sp.up, Kind: evStart, Keyless: true, Gen: gen}
	for _, kv := range env { // a duplicate keeps its last value, as exec does
		if v, ok := strings.CutPrefix(kv, opencodeKeyEnv+"="); ok {
			e.Keyless = strings.TrimSpace(v) == ""
			e.Key = ""
			if !e.Keyless {
				e.Key = credbind.Key(opencodeKeyEnv, v)
			}
		}
	}
	appendBindEvent(e, nil)
	if oauthBusy() {
		// An account flow is under way across the restart: the new daemon may be signed in or out
		// by it, so its life starts touched.
		noteAccount(time.Now(), acctTouch, gen)
	}
}

// noteDaemonUnknown records a daemon whose environment this Agent did not set (an adopted one, or
// a start whose process cannot be shown to own the port): calls from here until the next start
// prove nothing.
func noteDaemonUnknown(gen int) {
	e := stampedEvent(time.Now())
	e.Kind, e.Gen = evUnknown, gen
	appendBindEvent(e, nil)
}

// touchCoalesce is how soon after a written touch another one is not written: the later one is
// covered by requiring the quiet period to be this much longer (accountQuiet).
const touchCoalesce = time.Minute

// accountQuiet is how long an in observation must lie behind every earlier account event of its
// life to prove the account was not changed by a flow that finished on its own (the user approves
// a sign-in on the Console; opencode saves the credential without any call from the Agent).
var accountQuiet = oauthFlowHold + touchCoalesce

// eventGap is how far b lies after a in milliseconds, by uptime when both are of one boot (it does
// not follow a clock that was set), by wall time otherwise.
func eventGap(a, b bindEvent) int64 {
	if a.Boot != "" && a.Boot == b.Boot && a.Up > 0 && b.Up > 0 {
		return b.Up - a.Up
	}
	return b.At - a.At
}

// unknownClock reports an account event whose clock stamp could not be taken. It says nothing
// about the clock having moved, so it concerns only the life it was written in (bindingAt's loop
// over that life), never the lives after it and never a trim's floor for the key and free bindings.
func unknownClock(e bindEvent) bool {
	return e.Kind == evAccount && e.Reg && e.Floor == math.MaxInt64
}

// movedClock reports an event that shows the clock moved (a stamp was taken and disagrees); a
// missing stamp is not that, and does not make an event worth keeping twice.
func movedClock(e bindEvent) bool { return e.Reg && e.Floor != math.MaxInt64 }

// satAdd is a+b, saturating at MaxInt64 (an unknown clock bound is stored as MaxInt64).
func satAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// tooClose reports whether b follows a within touchCoalesce on both the uptime and the wall clock:
// an event that went back on either is never folded into the earlier one, since the going back is
// itself what the later event has to record.
func tooClose(a, b bindEvent) bool {
	w := b.At - a.At
	d := eventGap(a, b)
	lim := touchCoalesce.Milliseconds()
	return d >= 0 && d <= lim && w >= 0 && w <= lim
}

// quietBehind reports whether every event of life before index i lies at least accountQuiet
// before it, other than readings of the same credential.
func quietBehind(life []bindEvent, i int) bool {
	for _, o := range life[:i] {
		if o.Acct == acctIn && o.ID == life[i].ID {
			continue
		}
		if eventGap(o, life[i]) < accountQuiet.Milliseconds() {
			return false
		}
	}
	return true
}

// noteAccount records, in the life of daemon generation gen, an observation of the Console account
// (acctOut / acctIn) or an Agent flow that can change it (acctTouch). See noteAccountID.
func noteAccount(at time.Time, acct string, gen int) { noteAccountID(at, acct, "", gen) }

// noteAccountID is noteAccount with the binding of the signed-in credential (acctIn only). An out
// observation counts only before any other account event (it proves the account was out from the
// start of the life up to then), once. After that every touch, and every in observation that
// names another credential than the last event, is kept: they bound the stretch in which one
// credential paid. A touch within touchCoalesce of the last written touch, and an in observation
// repeating the last event, add nothing and are not written. The event is written only when gen is
// the newest daemon event's generation, checked in the same step as the write.
func noteAccountID(at time.Time, acct, id string, gen int) {
	if acct != acctOut {
		noteSessionAccount(at) // the panes' proofs end here too (bind_sessions.go)
	}
	e := stampedEvent(at)
	e.Kind, e.Acct, e.ID, e.Gen = evAccount, acct, id, gen
	appendBindEvent(e, func(evs []bindEvent, e bindEvent) bool {
		di := lastDaemonEvent(evs)
		if di < 0 || evs[di].Kind != evStart || evs[di].Gen != gen {
			return false
		}
		hasOut, hasBad := false, false
		var last *bindEvent
		for i, o := range evs[di+1:] {
			if o.Acct == acctOut {
				hasOut = true
			} else {
				hasBad = true
			}
			last = &evs[di+1+i]
		}
		switch acct {
		case acctOut:
			// Before any other account event: the account was out from the start of the life. After
			// a sign-in observation: the credential is gone, which ends that credential's proof.
			// A reading that shows the clock moved is always kept: it is the only trace of it.
			return !hasBad && !hasOut || last != nil && last.Acct == acctIn || movedClock(e)
		case acctIn:
			if last == nil || last.Acct != acctIn || last.ID != id || movedClock(e) {
				return true // a reading that shows the clock going back is never redundant
			}
			// The same credential again adds nothing once the earlier reading is already behind a
			// full quiet period; until then it is kept (at most one a minute), as it is the one
			// that can prove the period over.
			return !quietBehind(evs[di+1:], len(evs[di+1:])-1) && !tooClose(*last, e)
		}
		return last == nil || last.Acct != acctTouch || movedClock(e) || !tooClose(*last, e)
	})
}

// bindingAt is the binding of one call on route, "" when the evidence does not prove a single
// payer. times are the instants (unix ms) the call was made: a multi-step run gives every step's
// start and end, and all of them must fall in one daemon's life. paid says the call reported a
// cost, which a keyless free call cannot have. gaps are the bounds of the lives an event could not
// be recorded for (noteGap): none of those lives is proven.
//
// The life is the newest daemon event strictly before the call, and nothing may share its
// millisecond or lie out of order (a clock that went back): then the interval is not told apart.
// The Console account must have been observed signed out before the call and not signed in, or
// touched by an Agent flow, at or before it; or, for a daemon with no key, observed signed in to
// one credential before the call with no flow near it (accountPayer).
func bindingAt(evs []bindEvent, gaps []int64, route string, paid bool, times ...int64) string {
	if route != routes.RouteOpencodeZen && route != routes.RouteOpencodeGo || len(times) == 0 {
		return ""
	}
	life := -2
	lo, hi := times[0], times[0]
	for _, ts := range times {
		lo, hi = min(lo, ts), max(hi, ts)
		si := lifeAt(evs, ts)
		if si < 0 || (life != -2 && si != life) {
			return ""
		}
		life = si
	}
	if evs[life].Kind != evStart || evs[life].Reg {
		return ""
	}
	for _, g := range gaps {
		if evs[life].At <= g {
			return ""
		}
	}
	for _, e := range evs[:life+1] {
		if e.Reg && !unknownClock(e) && lo <= e.Floor || lo <= e.Carry {
			return "" // possibly an earlier daemon's call, from before the clock was set
		}
	}
	out := false
	var late []bindEvent // the account events at or before the end of the call, in order
	prevAt, seenBad := evs[life].At, false
	for _, e := range evs[life+1:] {
		if e.Kind != evAccount {
			break
		}
		if e.Reg {
			// The clock was set around this event, so it cannot be placed by time. Without a stamp at
			// all (Floor saturated) the clock itself is not known to have moved: a call that ended
			// before the event before it cannot be after it either. A clock that moved is not
			// trusted for any call of the life.
			if e.Floor != math.MaxInt64 || hi >= prevAt {
				return ""
			}
			continue
		}
		prevAt = e.At
		isBad := e.Acct != acctOut
		switch {
		case !isBad && !seenBad && e.At < lo:
			out = true // the account was out from the start of the life
		case e.At <= hi:
			late = append(late, e) // a change, or the credential going away again
		}
		seenBad = seenBad || isBad
	}
	if len(late) > 0 {
		return accountPayer(evs[life], late, route, paid, lo)
	}
	if !out {
		return ""
	}
	if !evs[life].Keyless {
		return evs[life].Key
	}
	if route == routes.RouteOpencodeZen && !paid {
		return FreeBinding
	}
	return ""
}

// accountPayer binds a call to the Console account when that is the one payer proven: the daemon
// had no key in its environment, the last account event up to the end of the call is a sign-in
// observation of one credential made before the call, and nothing earlier in the life was recent
// enough for a flow to still be finishing on its own. A free zen call cannot be told from a paid one
// while the account is signed in (the account is not what paid for it), so only a call that
// reported a cost is bound on zen; opencode go has no free calls.
func accountPayer(life bindEvent, late []bindEvent, route string, paid bool, lo int64) string {
	in := late[len(late)-1]
	if !life.Keyless || in.Acct != acctIn || in.ID == "" || in.At >= lo || in.At <= life.AcctCarry {
		// (AcctCarry: a trim dropped events that may have been touches the quiet period counts from.)
		return ""
	}
	if route == routes.RouteOpencodeZen && !paid {
		return ""
	}
	if !quietBehind(late, len(late)-1) {
		return ""
	}
	return in.ID
}

// lifeAt is the index of the daemon event whose life contains a call at ts, -1 when the evidence
// cannot place it.
func lifeAt(evs []bindEvent, ts int64) int {
	si := -1
	for i, e := range evs {
		if e.Kind == evAccount {
			continue
		}
		if e.At == ts {
			return -1
		}
		if e.At < ts {
			si = i
		}
	}
	if si < 0 {
		return -1
	}
	for _, e := range evs[si+1:] {
		if e.Kind != evAccount && e.At < ts {
			return -1 // stamped out of order
		}
	}
	return si
}

// ledgerBinder returns the function that binds the calls of session m, nil when there is nothing
// to go on. Which process served a call is the session's own history (bind_sessions.go): the
// shared daemon's evidence applies only to the stretches the session was attached to it, so a
// session that moved from a Terminal pane to Managed does not take the daemon's evidence for the
// pane's calls. A session with its own route server is never bound from opencode.ai evidence.
func ledgerBinder(m session.Meta) bindFn {
	if bindOff.Load() {
		return nil
	}
	return sessionBinder(m, daemonBinder())
}

// daemonBinder binds a call by the shared daemon's evidence; nil when that is unreadable.
func daemonBinder() bindFn {
	evs, ok := readBindEvents()
	if !ok || len(evs) == 0 {
		return nil
	}
	gaps, ok := readGaps()
	if !ok {
		return nil
	}
	return func(route string, paid bool, times ...int64) string {
		return bindingAt(evs, gaps, route, paid, times...)
	}
}

// ownsListener reports whether process pid holds every socket listening on port: the daemon that
// answered on the port is the one this Agent spawned. A process that lost the race for the port
// holds none of them, and one that shares the port with another listener does not own it alone.
// "Cannot tell" is false.
func ownsListener(pid int, portStr string) bool {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return false
	}
	inodes := listenports.Inodes("/proc", port)
	if len(inodes) == 0 {
		return false
	}
	held := map[string]bool{}
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if l, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
			if v, ok := strings.CutPrefix(l, "socket:["); ok {
				held[strings.TrimSuffix(v, "]")] = true
			}
		}
	}
	for _, in := range inodes {
		if !held[in] {
			return false
		}
	}
	return true
}
