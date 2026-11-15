package codex

import (
	"errors"
	"log"
	"os"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
)

// The shared app-server under a changing credential policy (ADR 0138 D12, "A security restart,
// not the ordinary one"). Every step here acts only while the held policy restricts something: a
// tenant that is not restricted never sees the daemon refused, interrupted or stopped by it.
//
//   - The daemon runs on the one login in codex's auth.json (codex ignores a provider key in its
//     environment), so what it carries is that login's route, read when this supervisor starts
//     it (credRoute). An adopted daemon has none: what it loaded cannot be read.
//   - Under a restriction the daemon is used only while it is this supervisor's own, the route it
//     was started on is allowed, and so is the route of the login codex would use now (the daemon
//     re-reads auth.json, on a token refresh among others). An adopted daemon is never used:
//     having no process handle the Agent cannot stop it either, so Managed codex is unavailable,
//     with the reason, until it is gone. A daemon is not started on a login that is forbidden or
//     whose route cannot be told.
//   - When a policy forbids the route the daemon runs on, it is stopped at once: no new turn is
//     admitted, every running managed turn is interrupted without the ordinary restart's drain
//     (the other sessions on the daemon too: one daemon carries one login), the process is
//     stopped, and the next Ensure starts one only on an allowed login.
//   - A policy that still allows that route forces nothing, a looser one included.

// ErrAppServerUnverified: under a restriction, the codex app-server is one this Agent did not
// start, is being stopped, or would run on a login the policy does not allow, so no turn is sent
// to it.
var ErrAppServerUnverified = errors.New("codex is unavailable under the credential policy")

// policyRestriction is the held policy when it restricts anything.
func policyRestriction() (routepolicy.Policy, bool) { return routepolicy.Hiding() }

// daemonLogin is the route a daemon started now would run on. A seam for tests.
var daemonLogin = func() string { return Login().Route }

// policyAdmitLocked is Ensure's check under a restriction: nil when nothing is restricted, or the
// daemon (if any) is this supervisor's own, on an allowed route, and codex's current login is on
// an allowed route too. A daemon on a route the policy no longer allows is stopped (a security
// restart, started here), and the caller retries. s.mu is held.
func (s *Supervisor) policyAdmitLocked(addr string) error {
	p, restricted := policyRestriction()
	if !restricted {
		return nil
	}
	if s.securing {
		return errors.Join(ErrAppServerUnverified, errors.New("its app-server is being stopped; try again shortly"))
	}
	if s.up && s.cmd == nil || !s.up && healthy(addr) {
		return errors.Join(ErrAppServerUnverified, errors.New("a codex app-server this Agent did not start is running at "+addr+
			", and it cannot be held to the policy; stop it, or restart the Workspace"))
	}
	login := daemonLogin()
	stale := s.up && !p.Allows(s.credRoute)
	switch {
	case !p.Allows(login): // an unresolved login ("") is never allowed
		if s.up {
			goSecurityRestart(s, "codex's current login is not on an allowed route")
		}
		why := "codex is signed in on " + login + ", which the policy does not allow"
		if login == "" {
			why = "the route of codex's current login cannot be told"
		}
		return errors.Join(ErrAppServerUnverified, errors.New(why))
	case stale:
		goSecurityRestart(s, "the daemon runs on a forbidden route")
		return errors.Join(ErrAppServerUnverified, errors.New("its app-server runs on a credential the policy no longer allows and is being stopped; try again shortly"))
	}
	return nil
}

// SecurityRestart stops the daemon at once under a tighter policy: admission closed, every running
// managed turn interrupted without a drain, the writer connection closed, the process stopped. An
// adopted daemon cannot be stopped: its turns are interrupted and it is let go, and Ensure keeps
// refusing it. Reports whether a process was stopped.
func (s *Supervisor) SecurityRestart(reason string) bool {
	s.mu.Lock()
	if !s.up && s.cmd == nil || s.securing {
		s.mu.Unlock()
		return false
	}
	cmd, cl := s.cmd, s.client
	s.securing = true
	// Left set, as stopIfIdle leaves it: the writer loss the close below causes is not a crash,
	// and retrying it would bring the daemon back. The next successful Ensure clears it.
	s.stopping = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.securing = false
		s.mu.Unlock()
	}()

	log.Printf("codex app-server: security restart (%s) — interrupting every managed turn", reason)
	interruptAllManaged()
	if cl != nil {
		cl.close()
	}
	s.mu.Lock()
	s.up, s.client = false, nil
	s.mu.Unlock()
	for _, h := range liveHandles() {
		h.runtimeLost()
	}
	if cmd == nil {
		return false
	}
	stopProcess(cmd, s.Addr())
	s.mu.Lock()
	if s.cmd == cmd {
		s.cmd = nil
	}
	s.mu.Unlock()
	_ = os.Unsetenv(appServerAddrEnv) // nothing usable lives at the address
	policyWork.Add(1)
	go func() {
		defer policyWork.Done()
		reconcileAll("security restart: " + reason)
	}()
	return true
}

// policyWork counts the security restarts goSecurityRestart starts and the reconciles they start.
// Tests wait on it so work that outlives the code that triggered it cannot read a package var
// (routepolicy.Active among them) after the test has restored it; production never waits.
var policyWork sync.WaitGroup

// goSecurityRestart runs SecurityRestart in the background, tracked by policyWork.
func goSecurityRestart(s *Supervisor, reason string) {
	policyWork.Add(1)
	go func() {
		defer policyWork.Done()
		s.SecurityRestart(reason)
	}()
}

// interruptAllManaged interrupts every managed turn running on the daemon at once, discarding
// their queues, and waits for the requests to be answered (each is bounded by its call timeout).
func interruptAllManaged() {
	var wg sync.WaitGroup
	for _, h := range busyManaged() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Printf("codex app-server: security restart — interrupting session %s", h.name)
			_ = h.interruptAll()
		}()
	}
	wg.Wait()
}

// onPolicyChange is the shared app-server's policy listener.
func (s *Supervisor) onPolicyChange(p routepolicy.Policy) {
	if !p.Restricted {
		return
	}
	s.mu.Lock()
	up, adopted, route := s.up, s.cmd == nil, s.credRoute
	s.mu.Unlock()
	if !up {
		return
	}
	switch {
	case adopted:
		s.SecurityRestart("the credential policy changed, and the daemon was not started by this Agent")
	case !p.Allows(route):
		s.SecurityRestart("the credential policy no longer allows " + route)
	case !p.Allows(daemonLogin()):
		s.SecurityRestart("the credential policy does not allow codex's current login")
	}
}

func init() { routepolicy.OnChange(func(p routepolicy.Policy) { supervisor.onPolicyChange(p) }) }
