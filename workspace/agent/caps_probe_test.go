package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	modelFn           func(string) probeObservation
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
	if f.modelFn != nil {
		return f.modelFn(m)
	}
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

	t.Run("first listed model refused, second accepted", func(t *testing.T) {
		env := &fakeProbeEnv{interrupt: ok, forkAt: ok, modelFn: func(m string) probeObservation {
			if m == "m1" {
				return probeObservation{Detail: "refused", Refused: true}
			}
			return probeObservation{Worked: true}
		}}
		rep := &recReporter{}
		reportProbeResults(rep, runCapsProbes(agents.Caps{CanFork: true, CanForkAt: true}, models, "", env))
		if len(rep.errs) != 0 {
			t.Errorf("unexpected failures: %v", rep.errs)
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

func TestModelOverrideTriesPastAnAccountRefusal(t *testing.T) {
	// refusedBy: the CLI said it would not launch these. mismatchOn: recorded another model
	// with no refusal on record.
	fake := func(refused, mismatchOn []string) func(string) probeObservation {
		in := func(l []string, m string) bool {
			for _, x := range l {
				if x == m {
					return true
				}
			}
			return false
		}
		return func(m string) probeObservation {
			switch {
			case in(refused, m):
				return probeObservation{Detail: fmt.Sprintf("started with %q but the CLI recorded auto", m), Refused: true}
			case in(mismatchOn, m):
				return probeObservation{Detail: fmt.Sprintf("started with %q but the CLI recorded other", m)}
			}
			return probeObservation{Worked: true, Detail: "the CLI recorded " + m}
		}
	}
	ms := []agents.ModelChoice{{ID: "gated"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	cands := probeModelCandidates(ms, "")
	if got := strings.Join(cands, ","); got != "gated,b,c" {
		t.Fatalf("candidates = %s, want gated,b,c", got)
	}
	if obs := observeModelOverrideTries(cands, fake([]string{"gated"}, nil)); !obs.Worked || !strings.Contains(obs.Detail, "gated") {
		t.Errorf("an explicit refusal must move on to the next model, got %+v", obs)
	}
	// A silent substitution is DRIFT at once, even though the next candidate would match.
	calls := 0
	f := fake(nil, []string{"gated"})
	obs := observeModelOverrideTries(cands, func(m string) probeObservation { calls++; return f(m) })
	if obs.Worked || obs.Err != nil || calls != 1 {
		t.Errorf("mismatch without a refusal must be DRIFT after one try: calls=%d obs=%+v", calls, obs)
	}
	// Every candidate refused is a probe error naming each refusal, never DRIFT.
	if obs := observeModelOverrideTries(cands, fake([]string{"gated", "b", "c"}, nil)); obs.Worked || obs.Err == nil ||
		!strings.Contains(obs.Err.Error(), `"gated"`) || !strings.Contains(obs.Err.Error(), `"c"`) {
		t.Errorf("all candidates refused must be a probe error naming them, got %+v", obs)
	}
	// A probe error is not retried past.
	calls = 0
	obs = observeModelOverrideTries(cands, func(string) probeObservation { calls++; return probeObservation{Err: errors.New("pane died")} })
	if obs.Err == nil || calls != 1 {
		t.Errorf("probe error: calls=%d obs=%+v", calls, obs)
	}
	if obs := observeModelOverrideTries(nil, fake(nil, nil)); obs.Err == nil {
		t.Errorf("no candidate must be a probe error, got %+v", obs)
	}
}

func TestObserveModelOverrideCarriesRefusal(t *testing.T) {
	turns := []transcript.Turn{{Role: "assistant", Model: "x"}}
	msg := `Model "x" from --model flag is not available. Using "auto" instead.`
	if o := observeModelOverride(turns, modelRecord{Have: true, Model: "auto", Refusal: msg}, "x"); o.Worked || !o.Refused || !strings.Contains(o.Detail, msg) {
		t.Errorf("mismatch + CLI refusal = %+v, want Refused carrying the CLI message", o)
	}
	if o := observeModelOverride(turns, modelRecord{Have: true, Model: "auto"}, "x"); o.Worked || o.Refused {
		t.Errorf("bare mismatch = %+v, must not be Refused", o)
	}
}

func TestObserveModelOverrideEmptyRecordSaysWhy(t *testing.T) {
	turns := []transcript.Turn{{Role: "assistant", Model: "x"}}
	o := observeModelOverride(turns, modelRecord{Have: true, Missing: "store.db meta has no lastUsedModel; keys: mode"}, "x")
	if o.Err == nil || !strings.Contains(o.Err.Error(), "keys: mode") {
		t.Errorf("an empty record must be a probe error that says why, got %+v", o)
	}
}

// The probe must still tell the two outcomes apart end to end: a CLI that quietly runs another
// model (flag ignored, nothing refused) is DRIFT; one that said it would not launch any of the
// listed models is PROBE-ERROR, with its own words in the line.
func TestModelOverrideIgnoredFlagIsDriftRefusedIsProbeError(t *testing.T) {
	models := []agents.ModelChoice{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	turns := []transcript.Turn{{Role: "assistant", Model: "stamped"}}
	run := func(rec modelRecord) probeResult {
		env := &fakeProbeEnv{modelFn: func(m string) probeObservation { return observeModelOverride(turns, rec, m) }}
		for _, r := range runCapsProbes(agents.Caps{}, models, "", env) {
			if r.ID == probeModelOverride {
				return r
			}
		}
		t.Fatal("no model-override result")
		return probeResult{}
	}
	if r := run(modelRecord{Have: true, Model: "auto"}); r.Verdict != verdictDrift {
		t.Errorf("flag ignored without a refusal = %s, want DRIFT: %s", r.Verdict, r)
	}
	msg := `Model "a" from --model flag is not available. Using "auto" instead.`
	r := run(modelRecord{Have: true, Model: "auto", Refusal: msg})
	if r.Verdict != verdictBroken || !strings.Contains(r.String(), "not available") {
		t.Errorf("every model refused = %s, want PROBE-ERROR with the CLI message: %s", r.Verdict, r)
	}
}

func TestWaitRecordedModel(t *testing.T) {
	sleeps := 0
	sleep := func(time.Duration) { sleeps++ }
	reads := 0
	late := func() string {
		reads++
		if reads == 4 {
			return "composer-2.5"
		}
		return ""
	}
	if got := waitRecordedModel(late, 10*time.Second, time.Second, sleep); got != "composer-2.5" || sleeps != 3 {
		t.Errorf("late write: got %q after %d sleeps, want composer-2.5 after 3", got, sleeps)
	}
	sleeps = 0
	if got := waitRecordedModel(func() string { return "" }, 5*time.Second, time.Second, sleep); got != "" || sleeps != 5 {
		t.Errorf("never written: got %q after %d sleeps, want an empty answer after the bounded 5", got, sleeps)
	}
	// A non-empty record is final even when it is the wrong one.
	sleeps = 0
	if got := waitRecordedModel(func() string { return "auto" }, 5*time.Second, time.Second, sleep); got != "auto" || sleeps != 0 {
		t.Errorf("a recorded model must not be waited on: got %q after %d sleeps", got, sleeps)
	}
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

type fakeResolver struct {
	err error
	// rejectFirst models every real resolver: the anchor of the first user turn leaves no
	// exchange before it, so it is refused.
	rejectFirst string
}

func (f fakeResolver) ResolveForkAt(_ session.Meta, at agents.ForkPoint) (string, error) {
	if f.rejectFirst != "" && at.Anchor == f.rejectFirst {
		return "", errors.New("no exchange before this message")
	}
	return "x", f.err
}

func TestObserveForkAt(t *testing.T) {
	two := []transcript.Turn{{Role: "user", AnchorID: "u1"}, {Role: "assistant"}, {Role: "user", AnchorID: "u2"}, {Role: "assistant"}}
	one := []transcript.Turn{{Role: "user", AnchorID: "u1"}, {Role: "assistant"}}
	bare := []transcript.Turn{{Role: "user", AnchorID: "u1"}, {Role: "user"}, {Role: "assistant", AnchorID: "ignored"}}
	m := session.Meta{}
	if o := observeForkAt(m, two, fakeResolver{rejectFirst: "u1"}); !o.Worked {
		t.Errorf("the second anchor must be the one tried: %+v", o)
	}
	if o := observeForkAt(m, one, fakeResolver{}); o.Err == nil || o.Worked {
		t.Errorf("a one-exchange conversation cannot say anything: %+v", o)
	}
	if o := observeForkAt(m, bare, fakeResolver{}); o.Worked {
		t.Errorf("a user turn without anchor must not pass: %+v", o)
	}
	if o := observeForkAt(m, two, nil); o.Worked {
		t.Errorf("no ForkAtResolver must not pass: %+v", o)
	}
	if o := observeForkAt(m, two, fakeResolver{err: errors.New("bad")}); o.Worked || o.Err != nil {
		t.Errorf("a rejecting resolver is DRIFT, not a probe error: %+v", o)
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
		o := observeModelOverride(c.turns, modelRecord{Have: c.have, Model: c.recalled}, c.want)
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
