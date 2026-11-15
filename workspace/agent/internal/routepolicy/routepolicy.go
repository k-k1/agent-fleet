// Package routepolicy is the Agent's copy of the credential policy (ADR 0138 D12): the effective
// policy of this Workspace's member, as the Control Plane computes it, held on disk and kept
// fresh by a pull. It decides nothing about a create by itself; the enforcement points ask it.
//
// The rules that make it safe to judge by a copy:
//   - Only a strictly higher revision replaces the held policy. The CP gives every policy write
//     a revision greater than any before it, so a restart, an out-of-order answer, a replayed
//     one or a stale cache can never bring back a weaker rule.
//   - Freshness comes only from the CP answering now: a live pull, or the start environment the
//     CP issued, at the time it says it issued it. An old start environment read again by a
//     restarted Agent confirms nothing.
//   - When the held policy restricts anything, or the deployment has a ceiling, and nothing has
//     confirmed it for FreshnessLimit, new work is refused ("the policy cannot be confirmed")
//     until something does. A member nobody restricts is never blocked by an unreachable CP.
//   - The copy on disk is written file-synced then renamed then directory-synced, behind a floor
//     record written first: the revision being taken and whether it restricts. A restart that
//     finds the copy older than the floor, or unreadable, under a restricting floor, holds a
//     placeholder that allows nothing until the CP answers, never the older or missing copy.
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

func (p Policy) restricting() bool { return p.Restricted || p.CeilingRestricted }

// FreshnessLimit is how long a restricting policy is trusted without a confirmation: the route
// gateway's rule for its Settings pull (ADR 0128 note of 2026-11-05).
const FreshnessLimit = 15 * time.Minute

// ErrUnconfirmed is the refusal of new work while a restricting policy cannot be confirmed.
var ErrUnconfirmed = errors.New("the credential policy cannot be confirmed: the Control Plane has not answered for over 15 minutes, so new work is refused until it does")

// held is what is kept on disk: the policy and when the CP last confirmed it.
type held struct {
	Policy Policy `json:"policy"`
	Held   bool   `json:"held"`
	// LastOK is when the CP last confirmed this revision or a higher one (Unix seconds). It is
	// on disk so a restart during an outage does not restart the clock.
	LastOK int64 `json:"lastOk"`
	// Placeholder marks a policy that stands in for one the Agent cannot read (a lost copy under
	// a restricting floor, a start the CP could not compute): it allows nothing, and any answer
	// at its revision or above replaces it.
	Placeholder bool `json:"placeholder,omitempty"`
}

// floor is the record written before a copy: the revision being taken and whether it restricts.
type floor struct {
	Revision    int64 `json:"revision"`
	Restricting bool  `json:"restricting"`
}

// placeholder allows nothing and restricts, unconfirmed.
func placeholder(rev int64) held {
	return held{Policy: Policy{Revision: rev, Restricted: true, CeilingRestricted: true, Allowed: []string{}}, Held: true, Placeholder: true}
}

// State is a snapshot for the Agent's own API and the enforcement points.
type State struct {
	Policy Policy
	// Held is false until a policy was ever received: there is then nothing to enforce.
	Held   bool
	LastOK time.Time
	// Placeholder: the held policy stands in for one that could not be read; it allows nothing.
	Placeholder bool
}

// Restricting reports whether the freshness rule applies: the held policy restricts anything or
// the deployment has a ceiling.
func (s State) Restricting() bool { return s.Held && s.Policy.restricting() }

// Confirmed reports whether new work may be judged by the held policy at now.
func (s State) Confirmed(now time.Time) bool {
	return !s.Restricting() || now.Sub(s.LastOK) <= FreshnessLimit
}

// Store holds the policy. Use Open.
type Store struct {
	mu   sync.Mutex
	path string
	h    held
	now  func() time.Time
}

// Path is where the Agent keeps its copy, in the durable config directory.
func Path() string { return filepath.Join(paths.AgentConfigDir(), "route-policy.json") }

func floorPath(path string) string { return path + ".floor" }

// Open loads the copy at path, behind its floor. Nothing on disk is nothing held. A copy that is
// unreadable, malformed or older than its floor is held as it is safe to: a placeholder that
// allows nothing when the floor restricts, nothing when it does not.
func Open(path string) *Store {
	s := &Store{path: path, now: time.Now}
	var f floor
	haveFloor := false
	if b, err := os.ReadFile(floorPath(path)); err == nil {
		if json.Unmarshal(b, &f) == nil {
			haveFloor = true
		} else {
			// A floor that cannot be read could have said "restricting".
			f, haveFloor = floor{Restricting: true}, true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		f, haveFloor = floor{Restricting: true}, true
	}
	h, copyErr := readCopy(path)
	switch {
	case copyErr == nil && (!haveFloor || h.Policy.Revision >= f.Revision):
		s.h = h
	case haveFloor && f.Restricting:
		log.Printf("route policy: the copy at %s cannot be trusted (%v) behind a restricting floor at revision %d; allowing nothing until the Control Plane answers",
			path, copyErrOr(copyErr, "older than the floor"), f.Revision)
		s.h = placeholder(f.Revision)
	default:
		if copyErr != nil && !errors.Is(copyErr, os.ErrNotExist) {
			log.Printf("route policy: the copy at %s cannot be read (%v) and nothing restricted it; holding nothing until the Control Plane answers", path, copyErr)
		}
	}
	return s
}

func copyErrOr(err error, alt string) string {
	if err != nil {
		return err.Error()
	}
	return alt
}

// readCopy reads and validates the stored copy.
func readCopy(path string) (held, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return held{}, err
	}
	var wire struct {
		Policy      json.RawMessage `json:"policy"`
		Held        *bool           `json:"held"`
		LastOK      *int64          `json:"lastOk"`
		Placeholder bool            `json:"placeholder"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return held{}, err
	}
	if wire.Held == nil || wire.LastOK == nil || !*wire.Held {
		return held{}, errors.New("the copy holds no policy")
	}
	p, err := parse(wire.Policy)
	if err != nil {
		return held{}, err
	}
	return held{Policy: p, Held: true, LastOK: *wire.LastOK, Placeholder: wire.Placeholder}, nil
}

// State returns the current snapshot.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Policy: s.h.Policy, Held: s.h.Held, LastOK: time.Unix(s.h.LastOK, 0), Placeholder: s.h.Placeholder}
}

// Accept takes a live answer from the CP (a pull). A strictly higher revision, the first answer
// ever, or any answer at or above a placeholder's revision replaces the held policy; an answer at
// the held revision or above confirms it; a lower one is a replay or an out-of-order answer and
// changes nothing, its freshness included. It reports whether the held policy changed.
func (s *Store) Accept(p Policy) (changed bool, err error) { return s.take(p, s.now().Unix()) }

// Seed takes the start environment's policy, issued by the CP at issuedAt. It replaces like
// Accept, but confirms only up to issuedAt: the environment is read again by every restart of
// the Agent in the same container, and an old one must not stand in for the CP answering now.
func (s *Store) Seed(p Policy, issuedAt int64) (changed bool, err error) { return s.take(p, issuedAt) }

// SeedUnknown holds a placeholder when nothing is held: the CP said at start that it could not
// compute the policy while a ceiling restricts this deployment.
func (s *Store) SeedUnknown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.h.Held {
		s.h = placeholder(0)
	}
}

func (s *Store) take(p Policy, confirmedAt int64) (bool, error) {
	if p.Allowed == nil {
		p.Allowed = []string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.h
	replace := !cur.Held || p.Revision > cur.Policy.Revision || cur.Placeholder && p.Revision >= cur.Policy.Revision
	if !replace && p.Revision < cur.Policy.Revision {
		return false, nil
	}
	if !replace && confirmedAt <= cur.LastOK {
		return false, nil // nothing new: the same revision, confirmed no later than already
	}
	next := cur
	if replace {
		next = held{Policy: p, Held: true, LastOK: cur.LastOK}
	}
	if confirmedAt > next.LastOK {
		next.LastOK = confirmedAt
	}
	// Held in memory whatever the disk does: the newer rule is the one to enforce now. The floor
	// goes first, so a copy that then fails to land is caught at the next start.
	s.h = next
	if replace {
		if err := writeJSON(floorPath(s.path), floor{Revision: p.Revision, Restricting: p.restricting()}); err != nil {
			return true, fmt.Errorf("recording the policy floor: %w", err)
		}
	}
	if err := writeJSON(s.path, next); err != nil {
		return replace, fmt.Errorf("recording the policy: %w", err)
	}
	return replace, nil
}

// Durability seams: a test injects failures through these.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	syncDir  = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	rename = os.Rename
)

// writeJSON replaces path with v: a temporary file written and synced, renamed, then the directory
// synced, so after a crash the path holds either the old bytes or the new ones.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if werr == nil {
		werr = syncFile(tmp)
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return werr
	}
	return syncDir(dir)
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
	switch {
	case !st.Held:
		return "none held"
	case st.Placeholder:
		return fmt.Sprintf("a placeholder at revision %d that allows nothing", st.Policy.Revision)
	}
	return fmt.Sprintf("revision %d, restricted=%t, ceiling=%t, %d allowed", st.Policy.Revision, st.Policy.Restricted,
		st.Policy.CeilingRestricted, len(st.Policy.Allowed))
}
