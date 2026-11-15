// Package routepolicy is the Agent's copy of the credential policy (ADR 0138 D12): the effective
// policy of this Workspace's member, as the Control Plane computes it, held on disk and kept
// fresh by a pull. It decides nothing about a create by itself; the enforcement points ask it.
//
// Two rules make it safe to judge by a copy:
//   - Only a strictly higher revision replaces the held policy. The CP gives every policy write
//     a revision greater than any before it, so a restart, an out-of-order answer, a replayed
//     one or a stale cache can never bring back a weaker rule.
//   - When the held policy restricts anything, or the deployment has a ceiling, and no pull has
//     succeeded for FreshnessLimit, new work is refused ("the policy cannot be confirmed") until
//     one does. A member nobody restricts is never blocked by an unreachable CP.
package routepolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Policy is the effective policy as the CP sends it (control-plane routepolicysrv.MemberPolicy).
// With Restricted false every route is allowed; with it true only the routes in Allowed are,
// and an id the CP did not list (one it does not know) is refused.
type Policy struct {
	Revision          int64    `json:"revision"`
	Restricted        bool     `json:"restricted"`
	CeilingRestricted bool     `json:"ceilingRestricted"`
	Allowed           []string `json:"allowed"`
}

// Allows reports whether the policy allows the route id.
func (p Policy) Allows(route string) bool { return !p.Restricted || slices.Contains(p.Allowed, route) }

// FreshnessLimit is how long a restricting policy is trusted without a successful pull: the route
// gateway's rule for its Settings pull (ADR 0128 note of 2026-11-05).
const FreshnessLimit = 15 * time.Minute

// PullInterval is the pull's period; with the CP reachable it is the longest a tightening takes
// to reach new work here (D12).
const PullInterval = 30 * time.Second

// ErrUnconfirmed is the refusal of new work while a restricting policy cannot be confirmed.
var ErrUnconfirmed = errors.New("the credential policy cannot be confirmed: the Control Plane has not answered for over 15 minutes, so new work is refused until it does")

// held is what is kept on disk: the policy and when a pull last succeeded.
type held struct {
	Policy Policy `json:"policy"`
	Held   bool   `json:"held"`
	// LastOK is when the CP last answered with this revision or a higher one (Unix seconds). It
	// is on disk so a restart during an outage does not restart the clock.
	LastOK int64 `json:"lastOk"`
}

// State is a snapshot for the Agent's own API and the enforcement points.
type State struct {
	Policy Policy
	// Held is false until a policy was ever received: there is then nothing to enforce.
	Held   bool
	LastOK time.Time
}

// Restricting reports whether the freshness rule applies: the held policy restricts anything or
// the deployment has a ceiling.
func (s State) Restricting() bool {
	return s.Held && (s.Policy.Restricted || s.Policy.CeilingRestricted)
}

// Confirmed reports whether new work may be judged by the held policy at now.
func (s State) Confirmed(now time.Time) bool {
	return !s.Restricting() || now.Sub(s.LastOK) <= FreshnessLimit
}

// Store holds the policy. The zero value is not usable; use Open.
type Store struct {
	mu   sync.Mutex
	path string
	h    held
	now  func() time.Time
}

// Path is where the Agent keeps its copy, in the durable config directory.
func Path() string { return filepath.Join(paths.AgentConfigDir(), "route-policy.json") }

// Open loads the copy at path. A missing file is "nothing held"; an unreadable or malformed one is
// kept as it is on disk and read as nothing held, logged, so the first answer replaces it.
func Open(path string) *Store {
	s := &Store{path: path, now: time.Now}
	b, err := os.ReadFile(path)
	if err == nil {
		if jerr := json.Unmarshal(b, &s.h); jerr != nil {
			log.Printf("route policy: %s does not parse (%v); holding nothing until the Control Plane answers", path, jerr)
			s.h = held{}
		}
	}
	return s
}

// State returns the current snapshot.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Policy: s.h.Policy, Held: s.h.Held, LastOK: time.Unix(s.h.LastOK, 0)}
}

// Accept takes an answer from the CP. A strictly higher revision (or the first answer ever)
// replaces the held policy and is persisted; an answer at the held revision or above confirms it
// (LastOK); a lower one is a replay or an out-of-order answer and changes nothing, freshness
// included. It reports whether the held policy changed.
func (s *Store) Accept(p Policy) (changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.h.Held && p.Revision < s.h.Policy.Revision {
		return false, nil
	}
	next := s.h
	if !s.h.Held || p.Revision > s.h.Policy.Revision {
		next.Policy, next.Held, changed = p, true, true
		if next.Policy.Allowed == nil {
			next.Policy.Allowed = []string{}
		}
	}
	next.LastOK = s.now().Unix()
	// Held in memory even when the write fails: the newer rule is the one to enforce now. The
	// failure is returned so the caller logs it; a restart before the next good write would
	// read the older copy.
	s.h = next
	return changed, s.save()
}

func (s *Store) save() error {
	b, err := json.Marshal(s.h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".af-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Gate is the enforcement points' question for new work: nil when it may be judged by the held
// policy now, ErrUnconfirmed when a restricting policy has gone unconfirmed past FreshnessLimit.
func (s *Store) Gate() error {
	if st := s.State(); !st.Confirmed(s.now()) {
		return ErrUnconfirmed
	}
	return nil
}

// Allows is the decision for a route id under the held policy (every route when nothing is held).
func (s *Store) Allows(route string) bool {
	st := s.State()
	return !st.Held || st.Policy.Allows(route)
}

// Describe is a one-line summary for the log.
func (st State) Describe() string {
	if !st.Held {
		return "none held"
	}
	return fmt.Sprintf("revision %d, restricted=%t, ceiling=%t, %d allowed", st.Policy.Revision, st.Policy.Restricted,
		st.Policy.CeilingRestricted, len(st.Policy.Allowed))
}
