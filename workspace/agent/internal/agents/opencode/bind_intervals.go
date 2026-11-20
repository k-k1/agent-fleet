package opencode

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
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

const bindIntervalsFile = "opencode-binding-intervals.jsonl"

// bindIntervalsKeep bounds the file: older evidence only matters to calls older than the
// daemon starts it holds, and those calls stay unbound rather than guessed.
const bindIntervalsKeep = 256

const (
	evStart   = "start"   // a daemon this Agent started
	evUnknown = "unknown" // a daemon whose environment is not known (adopted)
	evAccount = "account" // the Console account as read from the daemon
)

// bindEvent is one line of the file. It holds bindings and states only, never a key.
type bindEvent struct {
	At   int64  `json:"at"` // unix milliseconds
	Kind string `json:"kind"`
	// Key is the binding of OPENCODE_API_KEY in the started daemon's environment ("" when the
	// key could not be bound); Keyless says the environment held none. Only start events.
	Key     string `json:"key,omitempty"`
	Keyless bool   `json:"keyless,omitempty"`
	// Acct is "in" or "out" for an account observation.
	Acct string `json:"acct,omitempty"`
}

var bindIntervalsMu sync.Mutex

func bindIntervalsPath() string {
	return filepath.Join(paths.AgentConfigDir(), bindIntervalsFile)
}

// readBindEvents returns the recorded events in file order; an unreadable or missing file is no
// evidence, and a line that does not parse is skipped.
func readBindEvents() []bindEvent {
	f, err := os.Open(bindIntervalsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []bindEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e bindEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Kind != "" {
			out = append(out, e)
		}
	}
	return out
}

func appendBindEvent(e bindEvent) {
	bindIntervalsMu.Lock()
	defer bindIntervalsMu.Unlock()
	evs := append(readBindEvents(), e)
	if len(evs) > bindIntervalsKeep {
		evs = evs[len(evs)-bindIntervalsKeep:]
	}
	var sb strings.Builder
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	p := bindIntervalsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		log.Printf("opencode: binding intervals: %v", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o600); err != nil {
		log.Printf("opencode: binding intervals: %v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("opencode: binding intervals: %v", err)
		_ = os.Remove(tmp)
	}
}

// noteDaemonStart records a daemon this Agent started at `at` with environment env, the one it
// will keep for its whole life.
func noteDaemonStart(at time.Time, env []string) {
	e := bindEvent{At: at.UnixMilli(), Kind: evStart, Keyless: true}
	for _, kv := range env { // a duplicate keeps its last value, as exec does
		if v, ok := strings.CutPrefix(kv, opencodeKeyEnv+"="); ok {
			e.Keyless = strings.TrimSpace(v) == ""
			e.Key = ""
			if !e.Keyless {
				e.Key = credbind.Key(opencodeKeyEnv, v)
			}
		}
	}
	appendBindEvent(e)
}

// noteDaemonUnknown records a daemon whose environment this Agent did not set (an adopted one):
// calls from here until the next start prove nothing.
func noteDaemonUnknown(at time.Time) {
	appendBindEvent(bindEvent{At: at.UnixMilli(), Kind: evUnknown})
}

// noteAccountObs records the Console account as just read from the daemon. A reading equal to the
// previous one of the same daemon adds nothing.
func noteAccountObs(at time.Time, connected bool) {
	acct := "out"
	if connected {
		acct = "in"
	}
	evs := readBindEvents()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != evAccount {
			break
		}
		if evs[i].Acct == acct {
			return
		}
		break
	}
	appendBindEvent(bindEvent{At: at.UnixMilli(), Kind: evAccount, Acct: acct})
}

// bindingAt is the binding of one call on route at ts (unix ms), "" when the evidence does not
// prove a single payer. The call belongs to the newest daemon start at or before ts; a later
// unknown event, or no start at all, proves nothing. The Console account must be observed signed
// out on both sides of the call within that daemon's life (an observation after the call counts:
// it bounds how long the account could have been signed in), and never signed in.
func bindingAt(evs []bindEvent, route string, ts int64) string {
	if route != routes.RouteOpencodeZen && route != routes.RouteOpencodeGo {
		return ""
	}
	si := -1
	for i, e := range evs {
		if e.Kind != evAccount && e.At <= ts {
			si = i
		}
	}
	if si < 0 || evs[si].Kind != evStart {
		return ""
	}
	before, after := "", ""
	for _, e := range evs[si+1:] {
		if e.Kind != evAccount {
			break
		}
		if e.At <= ts {
			before = e.Acct
		} else if after == "" {
			after = e.Acct
		}
	}
	if (before == "" && after == "") || before == "in" || after == "in" {
		return ""
	}
	if !evs[si].Keyless {
		return evs[si].Key
	}
	if route == routes.RouteOpencodeZen {
		return FreeBinding
	}
	return ""
}

// ledgerBinder returns the function that binds the calls of session m, nil when m's calls did not
// run on the shared daemon this evidence describes: a session with its own route server, or a
// Terminal session on a private server, has its own environment.
func ledgerBinder(m session.Meta) func(route string, ms int64) string {
	shared := m.DriverKind() == session.DriverManaged && m.Route == nil || sharedSlot(m)
	if !shared {
		return nil
	}
	evs := readBindEvents()
	if len(evs) == 0 {
		return nil
	}
	return func(route string, ms int64) string { return bindingAt(evs, route, ms) }
}
