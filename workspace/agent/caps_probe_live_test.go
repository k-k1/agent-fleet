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
// long turn that is interrupted, and three one-line turns (up to five when the model probe meets explicit refusals).

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
		started = turnWorking(e.spec, meta, tn)
	}
	if !started {
		return observeInterrupt(false, false, false, false)
	}
	time.Sleep(capsProbeStreamFor)
	// Working right now, not merely at some point before: a turn that ended or errored on its
	// own is idle without any help from Escape.
	workingAtEscape := turnWorking(e.spec, meta, tn)
	if !workingAtEscape {
		return observeInterrupt(true, false, false, false)
	}
	// The keys the chat stop button sends for this kind (copilot ignores a plain Escape).
	keys, gap := sessionx.TUIInterruptKeys(e.spec.kind)
	for i, k := range keys {
		if out, err := tmuxx.Cmd("send-keys", "-t", tn, k).CombinedOutput(); err != nil {
			return probeObservation{Err: fmt.Errorf("send %s: %v: %s", k, err, out)}
		}
		if i < len(keys)-1 {
			time.Sleep(gap)
		}
	}
	idle := false
	for deadline := time.Now().Add(capsProbeInterruptWait); !idle && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		idle = e.spec.agent.WireLive(meta, true).State == "idle"
	}
	td, _ := e.spec.agent.Transcript(meta)
	return observeInterrupt(true, true, idle, mirrorHasTurn(td.Turns, "assistant", done))
}

// oneLineTurns starts a session (with model, when given), runs n one-line turns to completion
// one after another and returns the transcript of the last. whileLive, when set, runs with
// the session still up: a CLI's own store can be written late, or on exit, and the pane is
// gone once this returns.
func (e *liveProbeEnv) oneLineTurns(suffix, model string, n int, whileLive func(agents.TranscriptData, session.Meta)) (agents.TranscriptData, session.Meta, error) {
	t := e.t
	t.Helper()
	meta, tn, stop := startTUIContractSession(t, e.spec, suffix, model)
	defer stop()
	var td agents.TranscriptData
	for i := 1; i <= n; i++ {
		marker := e.marker(fmt.Sprintf("CAPS_PROBE_OK_%d", i))
		e.typeAndSubmit(tn, meta, "Reply with exactly: "+marker, marker)
		done := false
		for deadline := time.Now().Add(tuiContractTurnWait); !done && time.Now().Before(deadline); {
			var ok bool
			td, ok = e.spec.agent.Transcript(meta)
			done = ok && mirrorHasTurn(td.Turns, "user", marker) && mirrorHasTurn(td.Turns, "assistant", marker) &&
				e.spec.agent.WireLive(meta, true).State == "idle"
			if !done {
				time.Sleep(500 * time.Millisecond)
			}
		}
		if !done {
			return agents.TranscriptData{}, meta, fmt.Errorf("one-line turn %d/%d did not complete\npane:\n%s", i, n, tmuxx.CapturePane(tn))
		}
	}
	if whileLive != nil {
		whileLive(td, meta)
	}
	return td, meta, nil
}

func (e *liveProbeEnv) ForkAt() probeObservation {
	// Two exchanges: a fork cuts before the anchored message, so the first message can
	// never be the anchor (observeForkAt).
	td, meta, err := e.oneLineTurns("forkat", "", 2, nil)
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
	var rec modelRecord
	td, _, err := e.oneLineTurns("model", model, 1, func(_ agents.TranscriptData, meta session.Meta) {
		// Read the CLI's own record with the request cleared, so a store that only echoes the
		// meta cannot answer for itself (see observeModelOverride). A kind that exposes the raw
		// stored id is preferred: RecallSettings maps through the catalog and may answer "".
		asked := meta
		asked.Model = ""
		read := func() string { return "" }
		if raw, ok := e.spec.agent.(interface{ RecordedModel(session.Meta) string }); ok {
			rec.Have, read = true, func() string { return raw.RecordedModel(asked) }
		} else if recaller, ok := e.spec.agent.(agents.SettingsRecaller); ok {
			rec.Have, read = true, func() string { return recaller.RecallSettings(asked).Model }
		}
		rec.Model = waitRecordedModel(read, recordedModelWait, recordedModelEvery, time.Now, time.Sleep)
		if why, ok := e.spec.agent.(interface{ RecordedModelMissing(session.Meta) string }); ok && rec.Model == "" {
			rec.Missing = why.RecordedModelMissing(asked)
		}
		if r, ok := e.spec.agent.(interface {
			ModelRefusal(session.Meta, string) string
		}); ok {
			rec.Refusal = r.ModelRefusal(asked, model)
		}
	})
	if err != nil {
		return probeObservation{Err: err}
	}
	return observeModelOverride(td.Turns, rec, model)
}
