package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func expOf(t *testing.T, exps []probeExpectation, id capProbeID) probeExpectation {
	t.Helper()
	for _, e := range exps {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no expectation for %s", id)
	return probeExpectation{}
}

func TestExpectedProbesFollowCaps(t *testing.T) {
	models := []agents.ModelChoice{{ID: "m1"}}
	withFork := expectedProbes(agents.Caps{CanFork: true, CanForkAt: true}, models)
	if e := expOf(t, withFork, probeForkAt); !e.Applies || !e.Want {
		t.Errorf("CanForkAt kind: fork-at = %+v, want applies+want", e)
	}
	noFork := expectedProbes(agents.Caps{CanTranscript: true}, models)
	if e := expOf(t, noFork, probeForkAt); !e.Applies || e.Want {
		t.Errorf("kind without CanForkAt: fork-at = %+v, want applies, want=false", e)
	}
	if e := expOf(t, noFork, probeInterrupt); !e.Applies || !e.Want {
		t.Errorf("interrupt = %+v, want applies+want for every Terminal kind", e)
	}
	if e := expOf(t, noFork, probeModelOverride); !e.Applies || !e.Want {
		t.Errorf("model-override with a model list = %+v, want applies+want", e)
	}
	if e := expOf(t, expectedProbes(agents.Caps{}, nil), probeModelOverride); e.Applies {
		t.Errorf("model-override without a model list applies: %+v", e)
	}
	for _, e := range expectedProbes(agents.Caps{ManagedOnly: true}, models) {
		if e.Applies {
			t.Errorf("ManagedOnly kind: %s applies, but there is no Terminal route", e.ID)
		}
	}
}

func TestCapsDeclarationError(t *testing.T) {
	if capsDeclarationError(agents.Caps{CanForkAt: true}) == nil {
		t.Error("CanForkAt without CanFork must be rejected")
	}
	if err := capsDeclarationError(agents.Caps{CanFork: true, CanForkAt: true}); err != nil {
		t.Errorf("coherent Caps rejected: %v", err)
	}
}

func TestClassifyProbe(t *testing.T) {
	boom := errors.New("composer never appeared")
	cases := []struct {
		name string
		exp  probeExpectation
		obs  probeObservation
		want probeVerdict
	}{
		{"declared and works", probeExpectation{Applies: true, Want: true}, probeObservation{Worked: true}, verdictOK},
		{"declared false and fails", probeExpectation{Applies: true}, probeObservation{}, verdictOK},
		{"declared but fails", probeExpectation{Applies: true, Want: true}, probeObservation{}, verdictDrift},
		{"declared false but works", probeExpectation{Applies: true}, probeObservation{Worked: true}, verdictDrift},
		{"cannot observe is not drift", probeExpectation{Applies: true, Want: true}, probeObservation{Err: boom}, verdictBroken},
		{"error wins over Worked", probeExpectation{Applies: true}, probeObservation{Worked: true, Err: boom}, verdictBroken},
		{"not applicable", probeExpectation{}, probeObservation{Worked: true}, verdictSkipped},
	}
	for _, c := range cases {
		if got := classifyProbe(c.exp, c.obs).Verdict; got != c.want {
			t.Errorf("%s: verdict = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDriftMessageNamesTheDirection(t *testing.T) {
	down := classifyProbe(probeExpectation{ID: probeForkAt, Applies: true, Want: true, Why: "Caps.CanForkAt=true"},
		probeObservation{Detail: "no anchor"}).String()
	if !strings.HasPrefix(down, "DRIFT fork-at:") || !strings.Contains(down, "but it did not") {
		t.Errorf("declared-but-fails line = %q", down)
	}
	up := classifyProbe(probeExpectation{ID: probeForkAt, Applies: true, Why: "Caps.CanForkAt=false"},
		probeObservation{Worked: true, Detail: "anchored"}).String()
	if !strings.HasPrefix(up, "DRIFT fork-at:") || !strings.Contains(up, "enable it") {
		t.Errorf("false-but-works line = %q", up)
	}
}

type fakeProbeEnv struct {
	interrupt, forkAt probeObservation
	model             probeObservation
	modelAsked        string
	called            []capProbeID
}

func (f *fakeProbeEnv) Interrupt() probeObservation {
	f.called = append(f.called, probeInterrupt)
	return f.interrupt
}
func (f *fakeProbeEnv) ForkAt() probeObservation {
	f.called = append(f.called, probeForkAt)
	return f.forkAt
}
func (f *fakeProbeEnv) ModelOverride(m string) probeObservation {
	f.called = append(f.called, probeModelOverride)
	f.modelAsked = m
	return f.model
}

type recReporter struct{ logs, errs []string }

func (r *recReporter) Logf(f string, a ...any)   { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }
func (r *recReporter) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }

func TestRunCapsProbesEndToEndWithFakes(t *testing.T) {
	ok := probeObservation{Worked: true}
	models := []agents.ModelChoice{{ID: "m1"}, {ID: "m2"}}

	t.Run("all agree", func(t *testing.T) {
		env := &fakeProbeEnv{interrupt: ok, forkAt: ok, model: ok}
		res := runCapsProbes(agents.Caps{CanFork: true, CanForkAt: true}, models, "m2", env)
		rep := &recReporter{}
		reportProbeResults(rep, res)
		if len(rep.errs) != 0 {
			t.Errorf("unexpected failures: %v", rep.errs)
		}
		if env.modelAsked != "m2" {
			t.Errorf("model probe started with %q, want the preferred listed m2", env.modelAsked)
		}
	})

	t.Run("fork-at claimed but absent is DRIFT", func(t *testing.T) {
		env := &fakeProbeEnv{interrupt: ok, forkAt: probeObservation{Detail: "no anchor"}, model: ok}
		rep := &recReporter{}
		reportProbeResults(rep, runCapsProbes(agents.Caps{CanFork: true, CanForkAt: true}, models, "", env))
		if len(rep.errs) != 1 || !strings.HasPrefix(rep.errs[0], "DRIFT fork-at") {
			t.Errorf("errs = %v, want one DRIFT fork-at", rep.errs)
		}
	})

	t.Run("fork-at declared false but working is DRIFT", func(t *testing.T) {
		env := &fakeProbeEnv{interrupt: ok, forkAt: ok, model: ok}
		rep := &recReporter{}
		reportProbeResults(rep, runCapsProbes(agents.Caps{CanTranscript: true}, models, "", env))
		if len(rep.errs) != 1 || !strings.Contains(rep.errs[0], "enable it") {
			t.Errorf("errs = %v, want one stale-false DRIFT", rep.errs)
		}
	})

	t.Run("no model list skips the model probe", func(t *testing.T) {
		env := &fakeProbeEnv{interrupt: ok}
		rep := &recReporter{}
		reportProbeResults(rep, runCapsProbes(agents.Caps{}, nil, "", env))
		for _, id := range env.called {
			if id == probeModelOverride {
				t.Error("model probe ran without a listed model")
			}
		}
		if len(rep.errs) != 0 {
			t.Errorf("errs = %v", rep.errs)
		}
	})

	t.Run("nothing observed fails", func(t *testing.T) {
		rep := &recReporter{}
		reportProbeResults(rep, runCapsProbes(agents.Caps{ManagedOnly: true}, models, "", &fakeProbeEnv{}))
		if len(rep.errs) != 1 || !strings.Contains(rep.errs[0], "no capability probe was observed") {
			t.Errorf("errs = %v, want the nothing-observed failure", rep.errs)
		}
	})

	t.Run("probe error is not DRIFT", func(t *testing.T) {
		env := &fakeProbeEnv{interrupt: probeObservation{Err: errors.New("pane died")}, forkAt: probeObservation{}, model: ok}
		rep := &recReporter{}
		reportProbeResults(rep, runCapsProbes(agents.Caps{}, models, "", env))
		if len(rep.errs) != 1 || !strings.HasPrefix(rep.errs[0], "PROBE-ERROR interrupt") {
			t.Errorf("errs = %v, want one PROBE-ERROR interrupt", rep.errs)
		}
	})
}

func TestPickProbeModel(t *testing.T) {
	ms := []agents.ModelChoice{{ID: ""}, {ID: "a"}, {ID: "b"}}
	for _, c := range []struct{ pref, want string }{{"", "a"}, {"b", "b"}, {"zzz", "a"}} {
		if got := pickProbeModel(ms, c.pref); got != c.want {
			t.Errorf("pickProbeModel(pref=%q) = %q, want %q", c.pref, got, c.want)
		}
	}
	if got := pickProbeModel(nil, "x"); got != "" {
		t.Errorf("empty list = %q", got)
	}
}

type fakeResolver struct{ err error }

func (f fakeResolver) ResolveForkAt(session.Meta, agents.ForkPoint) (string, error) {
	return "x", f.err
}

func TestObserveForkAt(t *testing.T) {
	anchored := []transcript.Turn{{Role: "user", AnchorID: "u1"}, {Role: "assistant"}}
	bare := []transcript.Turn{{Role: "user"}, {Role: "assistant", AnchorID: "ignored"}}
	m := session.Meta{}
	if o := observeForkAt(m, anchored, fakeResolver{}); !o.Worked {
		t.Errorf("anchored+resolvable: %+v", o)
	}
	if o := observeForkAt(m, bare, fakeResolver{}); o.Worked {
		t.Errorf("a user turn without anchor must not pass: %+v", o)
	}
	if o := observeForkAt(m, anchored, nil); o.Worked {
		t.Errorf("no ForkAtResolver must not pass: %+v", o)
	}
	if o := observeForkAt(m, anchored, fakeResolver{err: errors.New("bad")}); o.Worked {
		t.Errorf("rejecting resolver must not pass: %+v", o)
	}
}

func TestObserveModelOverride(t *testing.T) {
	stamped := []transcript.Turn{{Role: "user"}, {Role: "assistant", Model: "want"}}
	rec := func(m string) string { return m }
	for _, c := range []struct {
		name     string
		turns    []transcript.Turn
		recalled string
		have     bool
		want     string
		worked   bool
		inconcl  bool
	}{
		{"store agrees", stamped, rec("claude-x"), true, "claude-x", true, false},
		{"case and provider prefix", stamped, rec("Anthropic/Claude-X"), true, "claude-x", true, false},
		{"store keeps the base id", stamped, rec("gemini-x"), true, "gemini-x-high", true, false},
		{"store disagrees although the transcript echoes the request", stamped, rec("other"), true, "want", false, false},
		{"recorded base of a different model than requested", stamped, rec("gpt-5"), true, "gpt-5-mini", false, false},
		{"base plus a non-parameter word", stamped, rec("gemini-x"), true, "gemini-x-pro", false, false},
		{"base plus two parameter words", stamped, rec("claude-x"), true, "claude-x-thinking-high", true, false},
		{"prefix of a different model", stamped, rec("gpt-5-mini"), true, "gpt-5", false, false},
		{"transcript alone is no evidence", stamped, "", true, "want", false, true},
		{"agent has no recorder", stamped, "", false, "want", false, true},
		{"no assistant model at all", []transcript.Turn{{Role: "user", Model: "want"}, {Role: "assistant"}}, rec("want"), true, "want", false, true},
	} {
		o := observeModelOverride(c.turns, c.recalled, c.have, c.want)
		if o.Worked != c.worked || (o.Err != nil) != c.inconcl {
			t.Errorf("%s: Worked=%v Err=%v, want Worked=%v inconclusive=%v", c.name, o.Worked, o.Err, c.worked, c.inconcl)
		}
	}
}

func TestObserveInterrupt(t *testing.T) {
	cases := []struct {
		name                                        string
		started, workingAtEscape, idle, reachedMark bool
		worked, inconclusive                        bool
	}{
		{"idle after Escape, turn cut short", true, true, true, false, true, false},
		{"still busy after Escape", true, true, false, false, false, false},
		{"never started", false, false, true, false, false, true},
		{"already idle at Escape (finished or errored alone)", true, false, true, false, false, true},
		{"Escape ignored, turn ran to completion", true, true, true, true, false, true},
	}
	for _, c := range cases {
		o := observeInterrupt(c.started, c.workingAtEscape, c.idle, c.reachedMark)
		if o.Worked != c.worked || (o.Err != nil) != c.inconclusive {
			t.Errorf("%s: Worked=%v Err=%v, want Worked=%v inconclusive=%v", c.name, o.Worked, o.Err, c.worked, c.inconclusive)
		}
	}
}
