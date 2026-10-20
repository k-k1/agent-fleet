//go:build contract

package main

// The live half of the capability probes (caps_probe.go holds the logic and its unit tests).
// It is reached through each kind's own TUI contract test with AF_CAPS_PROBES=only, so the
// kind's auth and HOME isolation are reused and the contract workflow can run it as a step of
// its own. Nothing here runs in the unit suite: it needs the real CLI, and the unit suite
// proves only the classification.
//
// Each probe starts its own session through the production BuildLaunch and observes through
// the production reads (WireLive / Transcript), like the mirror contract. Costs per kind: one
// long turn that is interrupted, and two one-line turns.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	// capsProbesEnv = "only" makes runTUIMirrorContract run the probes instead of the mirror
	// contract.
	capsProbesEnv = "AF_CAPS_PROBES"
	// How long a turn may take to show "working", and to go idle once interrupted.
	capsProbeWorkingWait   = 60 * time.Second
	capsProbeInterruptWait = 60 * time.Second
	// Let the long turn stream for a moment, so Escape lands mid-turn and not on the submit.
	capsProbeStreamFor = 3 * time.Second
)

func runTUICapsProbes(t *testing.T, spec tuiMirrorContractSpec) {
	t.Helper()
	caps := sessionx.AgentOf(spec.kind).Caps()
	if caps != spec.agent.Caps() {
		t.Fatalf("the registry's Caps for %s differ from the agent under test: %+v vs %+v", spec.kind, caps, spec.agent.Caps())
	}
	if err := capsDeclarationError(caps); err != nil {
		t.Fatalf("%s: %v", spec.kind, err)
	}
	var models []agents.ModelChoice
	if spec.models != nil {
		models = spec.models()
	}
	preferred := os.Getenv("E2E_" + strings.ToUpper(spec.kind) + "_PROBE_MODEL")
	reportProbeResults(t, runCapsProbes(caps, models, preferred, &liveProbeEnv{t: t, spec: spec}))
}

type liveProbeEnv struct {
	t    *testing.T
	spec tuiMirrorContractSpec
}

func (e *liveProbeEnv) marker(what string) string {
	return "AF_" + strings.ToUpper(e.spec.kind) + "_" + what
}

func (e *liveProbeEnv) typeAndSubmit(tn string, meta session.Meta, prompt, marker string) bool {
	e.t.Helper()
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "-l", prompt).CombinedOutput(); err != nil {
		e.t.Fatalf("send prompt: %v: %s", err, out)
	}
	return submitPrompt(e.t, e.spec, meta, tn, marker)
}

func (e *liveProbeEnv) Interrupt() probeObservation {
	t := e.t
	t.Helper()
	meta, tn, stop := startTUIContractSession(t, e.spec, "interrupt", "")
	defer stop()

	done := e.marker("LONG_DONE")
	started := e.typeAndSubmit(tn, meta,
		"Count from 1 to 300, one number per line, adding a short sentence of commentary to each. Then finish with exactly: "+done, done)
	for deadline := time.Now().Add(capsProbeWorkingWait); !started && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		started = e.spec.agent.WireLive(meta, true).State == "working"
	}
	if !started {
		return observeInterrupt(false, false, false, false)
	}
	time.Sleep(capsProbeStreamFor)
	// Working right now, not merely at some point before: a turn that ended or errored on its
	// own is idle without any help from Escape.
	workingAtEscape := e.spec.agent.WireLive(meta, true).State == "working"
	if !workingAtEscape {
		return observeInterrupt(true, false, false, false)
	}
	// One Escape, the same keys the chat stop button sends (sessionx handleSessionTurn).
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Escape").CombinedOutput(); err != nil {
		return probeObservation{Err: fmt.Errorf("send Escape: %v: %s", err, out)}
	}
	idle := false
	for deadline := time.Now().Add(capsProbeInterruptWait); !idle && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		idle = e.spec.agent.WireLive(meta, true).State == "idle"
	}
	td, _ := e.spec.agent.Transcript(meta)
	return observeInterrupt(true, true, idle, mirrorHasTurn(td.Turns, "assistant", done))
}

// oneLineTurn starts a session (with model, when given), runs a one-line turn to completion
// and returns its transcript.
func (e *liveProbeEnv) oneLineTurn(suffix, model string) (agents.TranscriptData, session.Meta, error) {
	t := e.t
	t.Helper()
	meta, tn, stop := startTUIContractSession(t, e.spec, suffix, model)
	defer stop()
	marker := e.marker("CAPS_PROBE_OK")
	e.typeAndSubmit(tn, meta, "Reply with exactly: "+marker, marker)
	for deadline := time.Now().Add(tuiContractTurnWait); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		td, ok := e.spec.agent.Transcript(meta)
		if ok && mirrorHasTurn(td.Turns, "user", marker) && mirrorHasTurn(td.Turns, "assistant", marker) &&
			e.spec.agent.WireLive(meta, true).State == "idle" {
			return td, meta, nil
		}
	}
	return agents.TranscriptData{}, meta, errors.New("the one-line turn did not complete\npane:\n" + tmuxx.CapturePane(tn))
}

func (e *liveProbeEnv) ForkAt() probeObservation {
	td, meta, err := e.oneLineTurn("forkat", "")
	if err != nil {
		return probeObservation{Err: err}
	}
	resolver, _ := e.spec.agent.(agents.ForkAtResolver)
	return observeForkAt(meta, td.Turns, resolver)
}

func (e *liveProbeEnv) ModelOverride(model string) probeObservation {
	if model == "" {
		return probeObservation{Err: errors.New("no model id to start with")}
	}
	td, meta, err := e.oneLineTurn("model", model)
	if err != nil {
		return probeObservation{Err: err}
	}
	// Read the CLI's own record with the request cleared, so a store that only echoes the
	// meta cannot answer for itself (see observeModelOverride). A kind that exposes the raw
	// stored id is preferred: RecallSettings maps through the catalog and may answer "".
	asked := meta
	asked.Model = ""
	var recorded string
	haveRecorder := false
	if raw, ok := e.spec.agent.(interface{ RecordedModel(session.Meta) string }); ok {
		recorded, haveRecorder = raw.RecordedModel(asked), true
	} else if recaller, ok := e.spec.agent.(agents.SettingsRecaller); ok {
		recorded, haveRecorder = recaller.RecallSettings(asked).Model, true
	}
	return observeModelOverride(td.Turns, recorded, haveRecorder, model)
}
