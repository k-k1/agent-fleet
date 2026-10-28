package main

// Kind-agnostic probes that hold a kind's declared agents.Caps up against what its real
// CLI does. The contract workflows already prove that a turn reaches the mirror; they say
// nothing about the capabilities a kind claims (agents.go asks that no cap be claimed
// unverified, and nothing checked the claims that were made).
//
// This file is the part that needs no CLI: which probes a Caps value obliges, and how an
// observation is classified against the expectation. The live observation lives behind the
// contract build tag (caps_probe_live_test.go) and is exercised only by the contract
// workflows; everything here is unit-tested with fakes.
//
// A disagreement in EITHER direction is DRIFT. "Declared, but it failed" is the obvious one.
// "Declared false, but it worked" is the other: a cap switched off from a static grep rather
// than from an observed failure leaves a working feature dark, and a probe that only ever
// confirmed `true` would never notice. A cap is declared false only after the probe has seen
// it fail.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

type capProbeID string

const (
	probeInterrupt     capProbeID = "interrupt"
	probeForkAt        capProbeID = "fork-at"
	probeModelOverride capProbeID = "model-override"
)

type probeVerdict string

const (
	verdictOK      probeVerdict = "ok"
	verdictDrift   probeVerdict = "DRIFT"
	verdictSkipped probeVerdict = "skipped"
	// verdictBroken means the probe could not observe anything (the turn never started,
	// the pane never came up). It is not drift: the declared cap is neither confirmed nor
	// contradicted, and it must not open a drift report.
	verdictBroken probeVerdict = "PROBE-ERROR"
)

// probeExpectation is what the declaration obliges one probe to observe.
type probeExpectation struct {
	ID capProbeID
	// Applies is false when the probe has nothing to test for this kind (no model list,
	// no Terminal route).
	Applies bool
	// Want is the declared behaviour: true = the capability must work.
	Want bool
	// Why says where Want came from, for the failure message.
	Why string
}

// probeObservation is one probe's raw result. Err means "could not observe" and wins over
// Worked.
type probeObservation struct {
	Worked bool
	Detail string
	Err    error
}

type probeResult struct {
	ID      capProbeID
	Verdict probeVerdict
	Want    bool
	Got     bool
	Why     string
	Detail  string
}

// capsDeclarationError reports a Caps value that contradicts itself. CanForkAt narrows
// CanFork (agents.go), so the pair is checked before any CLI is started.
func capsDeclarationError(c agents.Caps) error {
	if c.CanForkAt && !c.CanFork {
		return errors.New("Caps.CanForkAt is true without Caps.CanFork")
	}
	return nil
}

// expectedProbes derives the probes a kind owes from its Caps and its listed models.
// interrupt has no Caps flag: every kind with a Terminal route is expected to stop a turn
// on the keys its chat stop button sends (Escape, Ctrl+C for copilot and cursor).
func expectedProbes(c agents.Caps, models []agents.ModelChoice) []probeExpectation {
	if c.ManagedOnly {
		why := "ManagedOnly: no Terminal route to probe"
		return []probeExpectation{
			{ID: probeInterrupt, Why: why},
			{ID: probeForkAt, Why: why},
			{ID: probeModelOverride, Why: why},
		}
	}
	modelExp := probeExpectation{ID: probeModelOverride, Why: "the kind lists no model to start with"}
	if len(models) > 0 {
		modelExp = probeExpectation{ID: probeModelOverride, Applies: true, Want: true,
			Why: fmt.Sprintf("the kind lists %d model(s), so a listed one must be accepted at launch", len(models))}
	}
	return []probeExpectation{
		{ID: probeInterrupt, Applies: true, Want: true, Why: "every Terminal kind stops a turn on its chat stop button keys"},
		{ID: probeForkAt, Applies: true, Want: c.CanForkAt, Why: fmt.Sprintf("Caps.CanForkAt=%v", c.CanForkAt)},
		modelExp,
	}
}

// classifyProbe compares one observation with its expectation.
func classifyProbe(exp probeExpectation, obs probeObservation) probeResult {
	res := probeResult{ID: exp.ID, Want: exp.Want, Got: obs.Worked, Why: exp.Why, Detail: obs.Detail}
	switch {
	case !exp.Applies:
		res.Verdict = verdictSkipped
	case obs.Err != nil:
		res.Verdict = verdictBroken
		res.Detail = obs.Err.Error()
	case obs.Worked == exp.Want:
		res.Verdict = verdictOK
	default:
		res.Verdict = verdictDrift
	}
	return res
}

// String is the line the log carries. A DRIFT line says which way the declaration is wrong
// and what to do, because the person reading it is looking at an issue row, not the code.
func (r probeResult) String() string {
	head := fmt.Sprintf("%s %s: ", r.Verdict, r.ID)
	switch r.Verdict {
	case verdictDrift:
		if r.Want {
			return head + "expected to work (" + r.Why + ") but it did not: " + r.Detail
		}
		return head + "expected NOT to work (" + r.Why + ") but it did: " + r.Detail +
			" - the cap was switched off without an observed failure; enable it"
	case verdictSkipped:
		return head + r.Why
	default:
		return head + r.Detail
	}
}

// probeEnv is the live side: each method starts whatever it needs, observes, and cleans up.
type probeEnv interface {
	Interrupt() probeObservation
	ForkAt() probeObservation
	ModelOverride(model string) probeObservation
}

// pickProbeModel returns the model to start with: preferred when it is listed, otherwise
// the first listed one. "" when nothing is listed.
func pickProbeModel(models []agents.ModelChoice, preferred string) string {
	for _, m := range models {
		if preferred != "" && m.ID == preferred {
			return m.ID
		}
	}
	for _, m := range models {
		if m.ID != "" {
			return m.ID
		}
	}
	return ""
}

// runCapsProbes runs every probe the declaration obliges and classifies the outcomes.
func runCapsProbes(c agents.Caps, models []agents.ModelChoice, preferredModel string, env probeEnv) []probeResult {
	var out []probeResult
	for _, exp := range expectedProbes(c, models) {
		var obs probeObservation
		if exp.Applies {
			switch exp.ID {
			case probeInterrupt:
				obs = env.Interrupt()
			case probeForkAt:
				obs = env.ForkAt()
			case probeModelOverride:
				obs = env.ModelOverride(pickProbeModel(models, preferredModel))
			}
		}
		out = append(out, classifyProbe(exp, obs))
	}
	return out
}

// probeReporter is the slice of testing.TB that reporting needs.
type probeReporter interface {
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
}

// reportProbeResults logs every result and fails on DRIFT and PROBE-ERROR. A run in which
// no probe was actually observed fails too: all-skipped and "never ran" look alike.
func reportProbeResults(t probeReporter, results []probeResult) {
	observed := 0
	for _, r := range results {
		switch r.Verdict {
		case verdictOK:
			observed++
			t.Logf("%s", r)
		case verdictDrift:
			observed++
			t.Errorf("%s", r)
		case verdictBroken:
			t.Errorf("%s", r)
		default:
			t.Logf("%s", r)
		}
	}
	if observed == 0 {
		t.Errorf("%s: no capability probe was observed", verdictBroken)
	}
}

// observeForkAt reads the fork-at claim off a finished conversation: the SECOND user turn
// carries an AnchorID and the kind's resolver accepts it. The second, not the first: a fork
// cuts just before the anchored message, and before the very first message there is no
// exchange left to carry, so every kind rightly refuses that anchor (measured on copilot
// 1.0.103 to 1.0.105). resolver is nil for a kind that does not implement ForkAtResolver.
func observeForkAt(m session.Meta, turns []transcript.Turn, resolver agents.ForkAtResolver) probeObservation {
	var users []transcript.Turn
	for _, t := range turns {
		if t.Role == "user" {
			users = append(users, t)
		}
	}
	if len(users) < 2 {
		return probeObservation{Err: fmt.Errorf("the conversation has %d user turn(s); the probe needs two to fork before the second", len(users))}
	}
	anchor := users[1].AnchorID
	if anchor == "" {
		return probeObservation{Detail: "the second user turn has no AnchorID"}
	}
	if resolver == nil {
		return probeObservation{Detail: "the turn has an anchor but the agent implements no ForkAtResolver"}
	}
	if _, err := resolver.ResolveForkAt(m, agents.ForkPoint{Anchor: anchor}); err != nil {
		return probeObservation{Detail: "ResolveForkAt rejected the anchor: " + err.Error()}
	}
	return probeObservation{Worked: true, Detail: "second user turn anchored and resolvable"}
}

// modelMatches reports whether the model a transcript names is the one asked for. Kinds
// differ on a provider prefix ("anthropic/x" vs "x") and on case, never on more; a looser
// substring match would let "gpt-5" pass for "gpt-5-mini".
func modelMatches(reported, want string) bool {
	r, w := strings.ToLower(strings.TrimSpace(reported)), strings.ToLower(strings.TrimSpace(want))
	if r == "" || w == "" {
		return false
	}
	return r == w || strings.HasSuffix(r, "/"+w) || strings.HasSuffix(w, "/"+r)
}

// cursorParamTokens are the parameter words a cursor catalog id appends to the base id its
// store keeps ("gemini-x" -> "gemini-x-high"). Anything else after the base is a different
// model, not a parameter ("gpt-5" is not "gpt-5-mini").
var cursorParamTokens = map[string]bool{
	"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
	"thinking": true, "fast": true, "none": true,
}

// recordedModelMatches compares the model the CLI's own store recorded with the one asked
// for: equal, or the recorded value is the base of a requested id whose remainder is only
// parameter words.
func recordedModelMatches(recorded, want string) bool {
	if modelMatches(recorded, want) {
		return true
	}
	r, w := strings.ToLower(strings.TrimSpace(recorded)), strings.ToLower(strings.TrimSpace(want))
	if r == "" || !strings.HasPrefix(w, r+"-") {
		return false
	}
	for _, tok := range strings.Split(strings.TrimPrefix(w, r+"-"), "-") {
		if !cursorParamTokens[tok] {
			return false
		}
	}
	return true
}

// observeModelOverride checks that a session started with `want` ended up on it. The
// evidence is recorded: the model the CLI itself kept (read with an empty meta.Model).
// The transcript is NOT evidence: cursor and kiro stamp meta.Model onto every assistant
// turn, so the probe's own request would come back and match even when the CLI ignored the
// flag. No independent record is PROBE-ERROR, never a pass.
func observeModelOverride(turns []transcript.Turn, recorded string, haveRecorder bool, want string) probeObservation {
	reported := false
	for _, t := range turns {
		if t.Role == "assistant" && t.Model != "" {
			reported = true
			break
		}
	}
	switch {
	case !reported:
		return probeObservation{Err: fmt.Errorf("started with %q but no assistant turn reports a model", want)}
	case !haveRecorder:
		return probeObservation{Err: errors.New("the agent records no settings of its own to check the model against")}
	case recorded == "":
		return probeObservation{Err: errors.New("the CLI's own store recorded no model for the session")}
	case recordedModelMatches(recorded, want):
		return probeObservation{Worked: true, Detail: "the CLI recorded " + recorded}
	default:
		return probeObservation{Detail: fmt.Sprintf("started with %q but the CLI recorded %s", want, recorded)}
	}
}

// observeInterrupt turns what the live probe watched into an observation. Only a turn that
// was seen running, was still running when Escape went in, and did not reach its completion
// marker afterwards can say anything: a CLI that ignores Escape and finishes the turn by
// itself must not read as interrupted. Those cases are PROBE-ERROR, not DRIFT.
func observeInterrupt(startedWorking, workingAtEscape, idleAfter, reachedMarker bool) probeObservation {
	switch {
	case !startedWorking:
		return probeObservation{Err: errors.New("the long turn never reached the working state")}
	case !workingAtEscape:
		return probeObservation{Err: errors.New("the turn was no longer working when the stop keys were sent; nothing to interrupt")}
	case reachedMarker:
		return probeObservation{Err: errors.New("the turn ran to its completion marker; the stop keys either did nothing or came too late")}
	case idleAfter:
		return probeObservation{Worked: true, Detail: "returned to idle after the stop keys without completing the turn"}
	default:
		return probeObservation{Detail: "still not idle after the stop keys"}
	}
}
