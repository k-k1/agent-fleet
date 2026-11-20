package opencode

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
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
// proves exactly one payer; any doubt gives no binding.
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
	// Gen is the supervisor generation a daemon event describes, and the one an account event was
	// read from or touched on: an account event counts only for the life of that generation.
	Gen int `json:"gen,omitempty"`
	// Boot and Up stamp the event with the kernel's boot id and uptime in milliseconds, which do not
	// follow the wall clock. Between two events of one boot, wall time and uptime advance together;
	// when they do not, the wall clock was set and the events cannot be ordered by time (Reg).
	Boot string `json:"boot,omitempty"`
	Up   int64  `json:"up,omitempty"`
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

const bindGapsFile = "opencode-binding-intervals.gaps"

// gapsMem holds the gaps of this process, for when the gaps file cannot be written either.
var gapsMem []int64

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
func appendBindEvent(e bindEvent, check func(evs []bindEvent) bool) {
	bindIntervalsMu.Lock()
	defer bindIntervalsMu.Unlock()
	evs, ok := readBindEvents()
	if !ok {
		log.Printf("opencode: binding intervals unreadable; bindings stay unproven")
		gapsMem = append(gapsMem, e.At)
		noteGapFile(e.At)
		return
	}
	if check != nil && !check(evs) {
		return
	}
	boot, up, stamped := clockStamp()
	e.Boot, e.Up = boot, up
	if !stamped {
		e.Reg = true
	}
	if n := len(evs); n > 0 && stamped {
		prev := evs[n-1]
		if prev.Boot == boot && prev.Up > 0 {
			if d := (e.At - prev.At) - (e.Up - prev.Up); d > clockSlack || d < -clockSlack {
				e.Reg = true
			}
		}
	}
	if e.Kind != evAccount {
		for _, o := range evs { // an event stamped after e shows the clock going back too
			if o.At >= e.At {
				e.Reg = true
			}
		}
	}
	line, _ := json.Marshal(e)
	line = append(line, '\n')
	p := bindIntervalsPath()
	fail := func(err error) {
		log.Printf("opencode: binding intervals: %v", err)
		gapsMem = append(gapsMem, e.At)
		noteGapFile(e.At)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		fail(err)
		return
	}
	if len(evs) >= bindIntervalsKeep {
		cut := min(len(evs)-bindIntervalsKeep/2, lastDaemonEvent(evs))
		var sb strings.Builder
		for _, ev := range evs[cut:] {
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

// noteGapFile writes a gap to the gaps file only (the in-memory copy is already kept).
func noteGapFile(at int64) {
	f, err := os.OpenFile(filepath.Join(paths.AgentConfigDir(), bindGapsFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(strconv.FormatInt(at, 10) + "\n")
	_ = f.Close()
}

// noteDaemonStart records generation gen of the daemon, which this Agent started at `at` (when its
// process was spawned) with environment env, the one it keeps for its whole life. Called once the
// daemon answers and its process is known to be alive.
func noteDaemonStart(at time.Time, env []string, gen int) {
	e := bindEvent{At: at.UnixMilli(), Kind: evStart, Keyless: true, Gen: gen}
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
func noteDaemonUnknown(at time.Time, gen int) {
	appendBindEvent(bindEvent{At: at.UnixMilli(), Kind: evUnknown, Gen: gen}, nil)
}

// noteAccount records, in the life of daemon generation gen, an observation of the Console account
// (acctOut / acctIn) or an Agent flow that can change it (acctTouch). Only the first of each
// matters: a life is bound only until its first sign-in or touch, and an out observation proves
// the account was out from the start of the life up to then. The event is written only when gen is
// the newest daemon event's generation, checked in the same step as the write.
func noteAccount(at time.Time, acct string, gen int) {
	appendBindEvent(bindEvent{At: at.UnixMilli(), Kind: evAccount, Acct: acct, Gen: gen}, func(evs []bindEvent) bool {
		di := lastDaemonEvent(evs)
		if di < 0 || evs[di].Kind != evStart || evs[di].Gen != gen {
			return false
		}
		hasOut, hasBad := false, false
		for _, e := range evs[di+1:] {
			if e.Acct == acctOut {
				hasOut = true
			} else {
				hasBad = true
			}
		}
		return !hasBad && !(acct == acctOut && hasOut)
	})
}

// bindingAt is the binding of one call on route, "" when the evidence does not prove a single
// payer. times are the instants (unix ms) the call was made: a multi-step run gives every step's
// start and end, and all of them must fall in one daemon's life. paid says the call reported a
// cost, which a keyless free call cannot have. gaps are the instants an event could not be
// recorded: a life that began before one is not proven for calls after it.
//
// The life is the newest daemon event strictly before the call, and nothing may share its
// millisecond or lie out of order (a clock that went back): then the interval is not told apart.
// The Console account must have been observed signed out before the call and not signed in, or
// touched by an Agent flow, at or before it.
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
		if g >= evs[life].At && g <= hi {
			return ""
		}
	}
	out := false
	for _, e := range evs[life+1:] {
		if e.Kind != evAccount {
			break
		}
		if e.Reg && e.At <= hi {
			return "" // the clock was set inside this life
		}
		switch {
		case e.Acct == acctOut && e.At < lo:
			out = true
		case e.Acct != acctOut && e.At <= hi:
			return ""
		}
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

// ledgerBinder returns the function that binds the calls of session m, nil when m's calls did not
// run on the shared daemon this evidence describes: a session with its own route server, a
// Terminal session, or evidence that cannot be read.
func ledgerBinder(m session.Meta) bindFn {
	if m.DriverKind() != session.DriverManaged || m.Route != nil {
		return nil
	}
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

// procRunning reports whether pid is a live process that has not exited (a zombie, which a failed
// start leaves until it is reaped, is not).
func procRunning(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// "pid (comm) S ...": comm may hold spaces and parentheses, so cut at the last ')'.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	return b[i+2] != 'Z' && b[i+2] != 'X'
}
