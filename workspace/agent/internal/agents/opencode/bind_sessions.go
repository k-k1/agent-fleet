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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Per-session process history for the ledger bindings (ADR 0138 D5b, part of bind_intervals.go).
// The shared daemon's evidence describes the calls of the daemon, and only a session attached to it
// ran its calls there. Which process served a session changes over its life: a Terminal pane of its
// own (--standalone), a pane attached to the shared daemon, the Managed runtime on the daemon, a
// private route server. A session that switched from Terminal to Managed must not have the old
// pane's calls bound from the daemon's evidence, so each launch of a session's process is recorded
// here and a call is bound by the process that was serving the session at the time.
//
// A call with no recorded launch before it (a session older than this file) is not bound.
// What a pane can pay with is fixed by its launch environment, as the daemon's is by its start; the
// Console account is the other payer and is observed through the Agent's own flows only (a sign-in
// typed into a pane by hand is not seen, the limit bind_intervals.go states for the daemon).

const bindSessionsFile = "opencode-session-launches.jsonl"
const bindSessionsGapsFile = "opencode-session-launches.gaps"

// bindSessionsKeep bounds the file; the oldest lines go first.
const bindSessionsKeep = 1024

const (
	procPane   = "pane"   // a Terminal pane with a private server (--standalone)
	procShared = "shared" // the shared daemon: the Managed runtime or a pane attached to it
	procRoute  = "route"  // the session's private route server
	evSessAcct = "sacct"  // an Agent flow that can change the account, or a sign-in observed
)

// sessEvent is one line: a launch of a session's process, or an account event. It holds bindings
// only, never a key.
type sessEvent struct {
	At      int64  `json:"at"` // unix milliseconds
	Kind    string `json:"kind"`
	Session string `json:"session,omitempty"`
	// Key is the binding of OPENCODE_API_KEY in a pane's environment ("" when it could not be
	// bound); Keyless says the environment held none. Panes only.
	Key     string `json:"key,omitempty"`
	Keyless bool   `json:"keyless,omitempty"`
	// Out says the Console account was observed signed out when the pane launched and no Agent
	// flow touched it since that observation.
	Out   bool   `json:"out,omitempty"`
	Boot  string `json:"boot,omitempty"`
	Up    int64  `json:"up,omitempty"`
	Reg   bool   `json:"reg,omitempty"`   // the wall clock was set around this event
	Floor int64  `json:"floor,omitempty"` // on Reg: the latest time the earlier clock can have shown
	Carry int64  `json:"carry,omitempty"` // the largest Floor of the Reg events a trim dropped
}

var (
	bindSessionsMu sync.Mutex
	sessGapsMem    []int64
	sessOff        atomic.Bool
)

func bindSessionsPath() string { return filepath.Join(paths.AgentConfigDir(), bindSessionsFile) }

func readSessEvents() (evs []sessEvent, ok bool) {
	f, err := os.Open(bindSessionsPath())
	if os.IsNotExist(err) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e sessEvent
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

// noteSessGap records that an event could not be written: a process launched after bound may be
// missing, so no period that began at or before bound is proven.
func noteSessGap(bound int64) {
	sessGapsMem = append(sessGapsMem, bound)
	f, err := os.OpenFile(filepath.Join(paths.AgentConfigDir(), bindSessionsGapsFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		_, err = f.WriteString(strconv.FormatInt(bound, 10) + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("opencode: session launch gap not persisted, session binding is off until restart: %v", err)
		sessOff.Store(true)
	}
}

func readSessGaps() (gaps []int64, ok bool) {
	bindSessionsMu.Lock()
	gaps = append(gaps, sessGapsMem...)
	bindSessionsMu.Unlock()
	b, err := os.ReadFile(filepath.Join(paths.AgentConfigDir(), bindSessionsGapsFile))
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

// appendSessEvent adds one line under the lock, with check (may refuse after seeing the file;
// false = not written, not a failure). A failure leaves a gap.
func appendSessEvent(e sessEvent, check func(evs []sessEvent, e *sessEvent) bool) {
	bindSessionsMu.Lock()
	defer bindSessionsMu.Unlock()
	evs, ok := readSessEvents()
	if !ok {
		log.Printf("opencode: session launch evidence unreadable; session bindings stay unproven")
		noteSessGap(max(e.At, time.Now().UnixMilli()))
		return
	}
	if check != nil && !check(evs, &e) {
		return
	}
	if e.Boot == "" {
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
	for _, o := range evs { // an event stamped at or after e shows the clock going back
		if o.At >= e.At {
			e.Reg = true
			e.Floor = max(e.Floor, o.At)
		}
	}
	line, _ := json.Marshal(e)
	line = append(line, '\n')
	p := bindSessionsPath()
	fail := func(err error) {
		log.Printf("opencode: session launch evidence: %v", err)
		bound := e.At
		for _, o := range evs {
			bound = max(bound, o.At)
		}
		noteSessGap(bound)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		fail(err)
		return
	}
	if len(evs) >= bindSessionsKeep {
		cut := len(evs) - bindSessionsKeep/2
		carry := int64(0)
		for _, ev := range evs[:cut] {
			carry = max(carry, ev.Carry)
			if ev.Reg {
				carry = max(carry, ev.Floor)
			}
		}
		if carry > 0 {
			evs[cut].Carry = max(evs[cut].Carry, carry)
		}
		var sb strings.Builder
		for _, ev := range evs[cut:] {
			b, _ := json.Marshal(ev)
			sb.Write(b)
			sb.WriteByte('\n')
		}
		sb.Write(line)
		tmp, err := os.CreateTemp(filepath.Dir(p), bindSessionsFile+".tmp*")
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

// noteSessionAccount records an Agent flow that can change the Console account (or a sign-in
// observed). Written only when the newest event is not already such an event: the first one after
// a launch is the one that ends a pane's proof, and nothing launches in between.
func noteSessionAccount(at time.Time) {
	e := stampedSessEvent(at)
	e.Kind = evSessAcct
	appendSessEvent(e, func(evs []sessEvent, _ *sessEvent) bool {
		return len(evs) == 0 || evs[len(evs)-1].Kind != evSessAcct || evs[len(evs)-1].Reg
	})
}

func stampedSessEvent(at time.Time) sessEvent {
	boot, up, _ := clockStamp()
	return sessEvent{At: at.UnixMilli(), Boot: boot, Up: up}
}

// noteSessionProcess records that session name's calls are served from now on by a process of
// kind proc. env is the pane's environment (panes only). The shared daemon and a route server
// are recorded once per stretch: they are one process for many launches of the session, and a
// daemon's own life is the daemon evidence's business.
func noteSessionProcess(name, proc string, env []string) {
	st, readAt := oauthReading()
	recordSessionProcess(time.Now(), name, proc, env, st, readAt)
}

// recordSessionProcess is noteSessionProcess at a given instant and account view (st, read at
// readAt).
func recordSessionProcess(at time.Time, name, proc string, env []string, st oauthState, readAt time.Time) {
	e := stampedSessEvent(at)
	e.Kind, e.Session = proc, name
	if proc == procPane {
		e.Keyless = true
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, opencodeKeyEnv+"="); ok {
				e.Keyless = strings.TrimSpace(v) == ""
				e.Key = ""
				if !e.Keyless {
					e.Key = credbind.Key(opencodeKeyEnv, v)
				}
			}
		}
	}
	appendSessEvent(e, func(evs []sessEvent, e *sessEvent) bool {
		if proc != procPane {
			for i := len(evs) - 1; i >= 0; i-- {
				if evs[i].Session == name && evs[i].Kind != evSessAcct {
					return evs[i].Kind != proc
				}
			}
			return true
		}
		// The account counts as out only when it was observed out, and no Agent flow touched it
		// between that observation and this launch.
		out := st.known && !st.connected && !readAt.IsZero()
		for _, o := range evs {
			if o.Kind == evSessAcct && (o.Reg || o.At >= readAt.UnixMilli()) {
				out = false
			}
		}
		e.Out = out
		return true
	})
}

// sessionBinder returns the function that binds the calls of session m, nil when there is nothing
// to go on. shared is the shared daemon's binder (nil when its evidence is unusable).
func sessionBinder(m session.Meta, shared bindFn) bindFn {
	if sessOff.Load() {
		return nil
	}
	evs, ok := readSessEvents()
	if !ok || len(evs) == 0 {
		return nil
	}
	gaps, ok := readSessGaps()
	if !ok {
		return nil
	}
	return func(route string, paid bool, times ...int64) string {
		if len(times) == 0 {
			return ""
		}
		idx := -2
		lo, hi := times[0], times[0]
		for _, ts := range times {
			lo, hi = min(lo, ts), max(hi, ts)
			i := processAt(evs, m.Name, ts)
			if i < 0 || (idx != -2 && i != idx) {
				return ""
			}
			idx = i
		}
		ev := evs[idx]
		for _, g := range gaps {
			if ev.At <= g {
				return ""
			}
		}
		for _, e := range evs[:idx+1] {
			if e.Reg && lo <= e.Floor || lo <= e.Carry {
				return "" // possibly an earlier process's call, from before the clock was set
			}
		}
		switch ev.Kind {
		case procShared:
			if shared == nil {
				return ""
			}
			return shared(route, paid, times...)
		case procPane:
			return paneBinding(evs, idx, route, paid, lo, hi)
		}
		return "" // a private route server has no opencode.ai payer; its calls are never bound here
	}
}

// processAt is the index of the launch event of session name whose process served a call at ts,
// -1 when the evidence cannot place it (none before, the same instant, or stamped out of order).
func processAt(evs []sessEvent, name string, ts int64) int {
	si := -1
	for i, e := range evs {
		if e.Session != name || e.Kind == evSessAcct {
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
		if e.Session == name && e.Kind != evSessAcct && e.At < ts {
			return -1
		}
	}
	return si
}

// paneBinding binds a call of the pane process launched at evs[idx]: its environment fixed the key,
// and the account must have been out at launch and not touched or signed in since.
func paneBinding(evs []sessEvent, idx int, route string, paid bool, lo, hi int64) string {
	if route != routes.RouteOpencodeZen && route != routes.RouteOpencodeGo {
		return ""
	}
	ev := evs[idx]
	if !ev.Out || ev.Reg {
		return ""
	}
	for i, e := range evs {
		if e.Kind != evSessAcct {
			continue
		}
		if i > idx && e.Reg || e.At >= ev.At && e.At <= hi {
			return ""
		}
	}
	if !ev.Keyless {
		return ev.Key
	}
	if route == routes.RouteOpencodeZen && !paid {
		return FreeBinding
	}
	return ""
}
