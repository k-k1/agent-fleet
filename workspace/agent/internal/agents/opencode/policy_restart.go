package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// The shared serve under a changing credential policy (ADR 0138 D12, "A security restart, not the
// ordinary one"). Every step here acts only while the held policy restricts something: a tenant
// that is not restricted never sees a daemon refused, aborted or restarted by it.
//
//   - Each daemon this supervisor starts carries a credential generation (credentialGen): the
//     pin and the keys it was given. An adopted daemon has none, since its environment cannot be read.
//   - Under a restriction a daemon is used only while its generation is the one the policy now
//     calls for. An adopted daemon is never used: it cannot be checked, and having no process
//     handle the Agent cannot stop it either, so opencode is unavailable, with the reason, until
//     it is gone.
//   - When a tighter policy is taken, the daemon is replaced at once: no new turn is admitted,
//     every running turn on it is aborted without the ordinary restart's drain (the other
//     sessions on the daemon are interrupted too, since one serve carries one set of keys), the
//     process is stopped, and the next Ensure starts one with the narrowed credentials.
//   - A looser policy forces nothing: the daemon keeps its narrower set until the member restarts
//     it (the pending-restart notice the Console already shows for a key change).

// openGen is the generation of a daemon started while nothing was restricted.
const openGen = "open"

// ErrServeUnverified: under a restriction, the opencode serve is one this Agent did not start or
// is being replaced, so no turn is sent to it.
var ErrServeUnverified = errors.New("opencode is unavailable under the credential policy")

// credentialGen is the credential generation a daemon started now carries: openGen while nothing
// is restricted, otherwise a digest of what the policy lets it hold: the providers of its pin, the
// stored and inherited provider keys it is given (by fingerprint, never the key), and whether the
// engine is allowed. It is computed from those sources, not from a daemon's environment, so the
// start and every later check agree; the engine's token, minted per call, is left out. It reads
// local state only: Ensure asks it under its lock.
func credentialGen() string { return credentialGenFrom(readView()) }

func credentialGenFrom(v policyView) string {
	p, restricted := v.p, v.restricted
	if !restricted {
		return openGen
	}
	keys, pref, acct, custom := v.keys, v.pref, v.acct, v.custom
	engines := v.engines
	parts := []string{"pin:" + strings.Join(serveProviders(p, keys, pref, acct, engines, custom), ","),
		"custom:" + customPinDigest(p, engines, custom),
		"engine:" + strconv.FormatBool(p.Allows(routes.RouteAFEngineLLM))}
	fp := func(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:6]) }
	for name, v := range keys {
		if v != "" && keyAllowed(p, name) {
			parts = append(parts, "key:"+name+":"+fp(v))
		}
	}
	for _, name := range providerKeyEnvs() {
		if v := os.Getenv(name); v != "" && keyAllowed(p, name) {
			parts = append(parts, "env:"+name+":"+fp(v))
		}
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// policyAdmitLocked is Ensure's check under a restriction: nil when nothing is restricted, or the
// daemon (if any) is this supervisor's own and of the wanted generation. A daemon of another
// generation is not reused: a security restart is started, and the caller retries. s.mu is held.
func (s *Supervisor) policyAdmitLocked(addr string) error {
	if _, restricted := policyRestriction(); !restricted {
		return nil
	}
	switch {
	case s.securing:
		return errors.Join(ErrServeUnverified, errors.New("its daemon is being replaced with the narrowed credentials; try again shortly"))
	case s.up && s.cmd == nil, !s.up && healthy(addr):
		return errors.Join(ErrServeUnverified, errors.New("an opencode serve this Agent did not start is running at "+addr+
			", and it cannot be held to the policy; stop it, or restart the Workspace"))
	case s.up && s.credGen != credentialGen():
		goSecurityRestart(s, "credential generation is stale")
		return errors.Join(ErrServeUnverified, errors.New("its daemon carries credentials the policy no longer allows and is being replaced; try again shortly"))
	}
	return nil
}

// SecurityRestart replaces the daemon at once under a tighter policy: admission closed, every
// running turn aborted without a drain, the process stopped. An adopted daemon cannot be stopped:
// it is left marked, and Ensure keeps refusing it. Reports whether a process was stopped.
func (s *Supervisor) SecurityRestart(reason string) bool {
	s.mu.Lock()
	if !s.up || s.securing {
		s.mu.Unlock()
		return false
	}
	addr := serveAddr()
	cmd, exited := s.cmd, s.exited
	s.securing = true
	s.stopping = true
	s.askedStop = cmd
	s.mu.Unlock()
	// securing is cleared in the same critical section that records the daemon gone, never after
	// the reconcile is started: the reconcile's Ensure would otherwise be refused by this
	// restart's own flag and, for a managed session, leave it stopped until its next resume. The
	// flag stays up for every other caller for as long as the process is being stopped. A deferred
	// clear would be a second, late write that could wipe the flag of a later restart.
	cleared := false
	defer func() {
		if !cleared {
			s.mu.Lock()
			s.securing = false
			s.mu.Unlock()
		}
	}()

	log.Printf("opencode serve: security restart (%s) — aborting every turn", reason)
	recordLifecycle("security-restart", s.Generation(), reason)
	abortAll(addr)
	if cmd == nil {
		s.mu.Lock()
		s.stopping = false
		s.securing = false
		cleared = true
		s.mu.Unlock()
		return false
	}
	stopProcess(cmd, exited, addr)
	// One critical section with finishTeardown's own: clearing securing before up/cmd are reset
	// would let an Ensure see the stale daemon and start a second security restart on it.
	s.mu.Lock()
	s.securing = false
	cleared = true
	s.finishTeardownLocked(cmd)
	s.mu.Unlock()
	startReconcile("security restart: " + reason)
	return true
}

// startReconcile starts the reconcile that follows a security restart; a var so a test can run
// it synchronously and see the supervisor's state at the instant it starts.
var startReconcile = goReconcile

// goSecurityRestart runs SecurityRestart in the background, tracked by reconcileInflight like the
// reconcile it starts, so a test that triggered it can wait for both.
func goSecurityRestart(s *Supervisor, reason string) {
	reconcileInflight.Add(1)
	go func() {
		defer reconcileInflight.Done()
		s.SecurityRestart(reason)
	}()
}

// abortAll aborts every turn running on the daemon, managed and Terminal, without waiting.
func abortAll(addr string) {
	for _, h := range busyManaged(addr) {
		h.mu.Lock()
		h.abortAsked = true // ours: runTurn reports it as an abort it asked for
		h.mu.Unlock()
		abortSession(addr, h.sessionID())
	}
	for _, ses := range busyTerminals(addr) {
		abortSession(addr, ses)
	}
}

// onPolicyChange is the shared serve's policy listener.
func (s *Supervisor) onPolicyChange(p routepolicy.Policy) {
	s.mu.Lock()
	up, gen := s.up, s.credGen
	s.mu.Unlock()
	if !up {
		return
	}
	if !p.Restricted {
		if gen != openGen {
			NotePendingRestart("the credential policy no longer restricts this Workspace")
		}
		return
	}
	if gen != credentialGen() {
		s.SecurityRestart("the credential policy changed")
	}
}

func init() { routepolicy.OnChange(func(p routepolicy.Policy) { supervisor.onPolicyChange(p) }) }

// servePinMatches refuses a session whose launch was judged on view while the shared serve runs
// on another custom pin: a connection updated between the judgement and Ensure would send a
// call the policy approved on URL A to URL B. Only the custom pin is compared, since any other
// generation difference restarts the serve with no recorded endpoint to contradict. An adopted
// daemon (credGen "") has an unknown environment and is not judged here.
func (s *Supervisor) servePinMatches(v policyView) error {
	if !v.restricted || customPinDigest(v.p, v.engines, v.custom) == "" {
		return nil
	}
	s.mu.Lock()
	up, gen := s.up, s.credGen
	s.mu.Unlock()
	if up && gen != "" && gen != credentialGenFrom(v) {
		return ErrServePolicyMoved
	}
	return nil
}
