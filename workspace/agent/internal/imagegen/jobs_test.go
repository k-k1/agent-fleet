package imagegen

// The queue's own rules (ADR 0081 decisions 2, 4, 8, 11, 12). What is driven here is the part
// that decides ORDER, what is refused, and what a cancel actually sends — the three things a
// live run cannot check cheaply, because each of them costs a GPU minute to observe once.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// withJobQueue installs a fresh queue for one test and stops its workers afterwards. The
// package's queue is a singleton by design — there is one per process — so without this a test
// would inherit whatever the previous one left pending.
func withJobQueue(t *testing.T) *jobQueue {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	old := jobs
	jobs = newJobQueue()
	q := jobs
	t.Cleanup(func() {
		q.Close()
		jobs = old
	})
	return q
}

// gateProvider is a fleet provider whose Generate can be held open, so a test can look at a
// queue while something is running rather than racing a call that has already returned.
type gateProvider struct {
	id string
	// begun receives one value per call, as it starts.
	begun chan string
	// release hands out one token per call: a Generate returns only once it has taken one.
	release chan struct{}
	// upstream is what the provider reports as the engine's own id for the request, which is the
	// only handle a cancel has.
	upstream string
	image    []byte

	mu        sync.Mutex
	cancelled []string
	seen      []Request
}

func newGateProvider(t *testing.T) *gateProvider {
	t.Helper()
	return &gateProvider{
		id: ProviderComfy, begun: make(chan string, 64), release: make(chan struct{}, 64),
		upstream: "engine-prompt-1", image: tinyPNG(t, 4, 4),
	}
}

func (p *gateProvider) ID() string                 { return p.id }
func (p *gateProvider) Ready(context.Context) bool { return true }
func (p *gateProvider) Caps(string) Caps           { return Caps{Ops: AllOps, Seed: true, Params: true} }
func (p *gateProvider) Cancel(_ context.Context, upstream string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelled = append(p.cancelled, upstream)
	return nil
}

func (p *gateProvider) Generate(_ context.Context, req Request) (Result, error) {
	p.mu.Lock()
	p.seen = append(p.seen, req)
	p.mu.Unlock()
	req.reportUpstream(p.upstream)
	req.reportPhase(PhaseRunning)
	p.begun <- req.Prompt
	<-p.release
	seed := int64(0)
	if req.Seed != nil {
		seed = *req.Seed
	}
	return Result{
		Provider: p.id, Model: req.Model,
		Images: []Image{{Bytes: p.image, MIME: "image/png", Width: 4, Height: 4, Seed: &seed}},
	}, nil
}

func (p *gateProvider) requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request{}, p.seen...)
}

func (p *gateProvider) cancels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.cancelled...)
}

// waitFor polls a condition rather than sleeping a fixed amount: the worker is a goroutine and
// the alternative is a test that is either slow or flaky, never neither.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stateOf(q *jobQueue, id string) JobState {
	for _, j := range q.List().Jobs {
		if j.ID == id {
			return JobState(j.State)
		}
	}
	return ""
}

func enqueue(t *testing.T, q *jobQueue, spec JobSpec) EnqueueResult {
	t.Helper()
	if spec.Request.Prompt == "" {
		spec.Request.Prompt = "a fox"
	}
	out, err := q.Enqueue(context.Background(), spec)
	if err != nil {
		t.Fatalf("Enqueue() = %v", err)
	}
	return out
}

// One worker per provider and one job at a time: the second job does not start until the first
// has finished, and the position it reports is the order it will run in.
func TestQueueRunsOneJobAtATimeInOrder(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	first := enqueue(t, q, JobSpec{Request: Request{Prompt: "first"}})
	waitFor(t, "the first job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	second := enqueue(t, q, JobSpec{Request: Request{Prompt: "second"}})

	if got := stateOf(q, second.Jobs[0].ID); got != JobQueued {
		t.Fatalf("the second job is %q while the first runs, want queued — the box serialises sampling anyway", got)
	}
	if len(p.begun) != 0 {
		t.Fatal("two jobs started at once: submitting ahead moves the queue somewhere this Agent cannot cancel from")
	}
	if second.Jobs[0].Position != 1 {
		t.Errorf("position = %d, want 1 (the first is running, not waiting)", second.Jobs[0].Position)
	}

	p.release <- struct{}{}
	waitFor(t, "the second job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "both jobs to finish", func() bool {
		return stateOf(q, first.Jobs[0].ID) == JobDone && stateOf(q, second.Jobs[0].ID) == JobDone
	})
	got := p.requests()
	if len(got) != 2 || got[0].Prompt != "first" || got[1].Prompt != "second" {
		t.Fatalf("ran %v, want the order they were enqueued in", got)
	}
}

// The cap judges the WHOLE batch. Admitting half of a request for forty pictures would leave the
// caller to work out which seventeen it got.
func TestQueueRefusesAWholeBatchWhenItWouldOverflow(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	enqueue(t, q, JobSpec{Jobs: imagegenQueueMax})
	waitFor(t, "one job to start", func() bool { return len(p.begun) > 0 })
	// One of them is running, so exactly one slot is free.
	before := q.List().Queued
	_, err := q.Enqueue(context.Background(), JobSpec{Request: Request{Prompt: "x"}, Jobs: 2})
	if err == nil || !strings.Contains(err.Error(), "waiting") {
		t.Fatalf("Enqueue() = %v, want a refusal naming the queue", err)
	}
	if got := q.List().Queued; got != before {
		t.Fatalf("queued = %d after the refusal, want %d — the batch must be admitted whole or not at all", got, before)
	}
	// The one that does fit is still admitted: the cap is about the batch, not about the queue
	// being closed.
	enqueue(t, q, JobSpec{Request: Request{Prompt: "x"}, Jobs: 1})
}

// A trial goes in front of every waiting job and behind the one already running, and there is a
// ceiling on how many may wait — or the head of the queue becomes a queue.
func TestTrialJumpsTheQueueAndIsCapped(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
	waitFor(t, "the batch to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	batch := enqueue(t, q, JobSpec{Request: Request{Prompt: "batch"}, Jobs: 3})
	trial := enqueue(t, q, JobSpec{Request: Request{Prompt: "trial"}, Trial: true})

	if trial.Jobs[0].Position != 1 {
		t.Errorf("trial position = %d, want 1 — behind a batch it arrives when the batch does", trial.Jobs[0].Position)
	}
	var batchPos int
	for _, j := range q.List().Jobs {
		if j.ID == batch.Jobs[0].ID {
			batchPos = j.Position
		}
	}
	if batchPos != 2 {
		t.Errorf("the first batch job is at %d, want 2 (behind the trial)", batchPos)
	}

	for i := 0; i < imagegenTrialMax-1; i++ {
		enqueue(t, q, JobSpec{Request: Request{Prompt: "trial"}, Trial: true})
	}
	_, err := q.Enqueue(context.Background(), JobSpec{Request: Request{Prompt: "trial"}, Trial: true})
	if err == nil || !strings.Contains(err.Error(), "waiting") {
		t.Fatalf("the %dth waiting trial = %v, want a refusal", imagegenTrialMax+1, err)
	}
}

// A trial samples at its family's trial step count, into its own folder, and keeps the form's
// own steps on the record so the sidecar says what the batch would run at.
func TestTrialReducesStepsAndWritesToTheTrialFolder(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)
	withStudio(t, p, StudioModel{ID: "sdxl-base-1.0", Family: string(ComfyFamilySDXL)})

	enqueue(t, q, JobSpec{
		Request: Request{Prompt: "a fox", Model: "sdxl-base-1.0", Params: &EngineParams{Steps: 40}},
		Trial:   true,
	})
	waitFor(t, "the trial to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "the trial to finish", func() bool { return len(q.List().Jobs[0].Files) > 0 })

	got := p.requests()
	if len(got) != 1 || got[0].Params == nil || got[0].Params.Steps != comfyTrialSteps[ComfyFamilySDXL] {
		t.Fatalf("trial ran at %+v, want steps = %d", got[0].Params, comfyTrialSteps[ComfyFamilySDXL])
	}
	job := q.List().Jobs[0]
	if job.FullSteps != 40 {
		t.Errorf("full_steps = %d, want the form's own 40 — the record has to say what the keeper would be made with", job.FullSteps)
	}
	if dir := filepath.Dir(job.Files[0].Path); dir != ConsoleTrialDir() {
		t.Errorf("trial wrote to %s, want %s (the one subtree the sweep still clears)", dir, ConsoleTrialDir())
	}
}

// full_steps turns the reduction off, for the case where the trial IS the picture.
func TestTrialWithFullStepsKeepsTheFormsSteps(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)
	withStudio(t, p, StudioModel{ID: "sdxl-base-1.0", Family: string(ComfyFamilySDXL)})

	enqueue(t, q, JobSpec{
		Request:   Request{Prompt: "a fox", Model: "sdxl-base-1.0", Params: &EngineParams{Steps: 40}},
		Trial:     true,
		FullSteps: true,
	})
	waitFor(t, "the trial to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	if got := p.requests(); got[0].Params.Steps != 40 {
		t.Fatalf("steps = %d, want the form's 40", got[0].Params.Steps)
	}
}

// A queued job is simply removed, and nothing is asked of the engine — there is nothing there to
// ask about.
func TestCancelQueuedJobRemovesItWithoutTouchingTheEngine(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
	waitFor(t, "the first job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	waiting := enqueue(t, q, JobSpec{Request: Request{Prompt: "waiting"}})

	if err := q.Cancel(waiting.Jobs[0].ID); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if got := stateOf(q, waiting.Jobs[0].ID); got != JobCancelled {
		t.Fatalf("state = %q, want cancelled", got)
	}
	if got := q.List().Queued; got != 0 {
		t.Fatalf("queued = %d, want 0", got)
	}
	if got := p.cancels(); len(got) != 0 {
		t.Fatalf("asked the engine to cancel %v — a job it never received is not the engine's to take back", got)
	}
}

// A running job is taken back through the provider's own Canceller, WITH the id the engine gave
// it, and whatever it then produces is discarded.
func TestCancelRunningJobAsksTheProviderWithTheEngineId(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	out := enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
	waitFor(t, "the job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun

	if err := q.Cancel(out.Jobs[0].ID); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if got := p.cancels(); len(got) != 1 || got[0] != p.upstream {
		t.Fatalf("cancelled %v, want exactly the engine's own id %q — a cancel with no id kills another workspace's picture", got, p.upstream)
	}
	p.release <- struct{}{}
	waitFor(t, "the cancelled job to settle", func() bool {
		return stateOf(q, out.Jobs[0].ID) == JobCancelled
	})
	for _, j := range q.List().Jobs {
		if j.ID == out.Jobs[0].ID && len(j.Files) != 0 {
			t.Fatal("a cancelled job kept its files: the result of a cancelled generation is discarded")
		}
	}
}

// A paused group is skipped rather than blocking the queue: another group — or a trial — runs
// while it waits, which is the whole reason to pause.
func TestPausedGroupIsSkippedAndOthersKeepRunning(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	blocking := enqueue(t, q, JobSpec{Request: Request{Prompt: "blocking"}})
	waitFor(t, "the queue to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	slow := enqueue(t, q, JobSpec{Request: Request{Prompt: "slow"}, Jobs: 2})
	other := enqueue(t, q, JobSpec{Request: Request{Prompt: "other"}})

	if err := q.GroupOp(slow.Group, "pause"); err != nil {
		t.Fatalf("pause = %v", err)
	}
	p.release <- struct{}{} // the blocking job finishes
	waitFor(t, "the unpaused group to start", func() bool { return len(p.begun) > 0 })
	if got := <-p.begun; got != "other" {
		t.Fatalf("ran %q next, want the unpaused group's job", got)
	}
	_ = blocking
	if got := groupStateOf(q, slow.Group); got != GroupPaused {
		t.Fatalf("group state = %q, want paused", got)
	}
	if err := q.GroupOp(slow.Group, "resume"); err != nil {
		t.Fatalf("resume = %v", err)
	}
	if got := groupStateOf(q, slow.Group); got != GroupRunning {
		t.Fatalf("group state after resume = %q", got)
	}
	_ = other
}

// Abort removes every queued job of the group and interrupts the running one; the pictures
// already made stay, and the group says so.
func TestGroupCancelKeepsWhatWasAlreadyMade(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	out := enqueue(t, q, JobSpec{Request: Request{Prompt: "batch"}, Jobs: 3})
	waitFor(t, "the batch to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	p.release <- struct{}{} // one picture is made
	waitFor(t, "the second to start", func() bool { return len(p.begun) > 0 })
	<-p.begun

	if err := q.GroupOp(out.Group, "cancel"); err != nil {
		t.Fatalf("cancel = %v", err)
	}
	p.release <- struct{}{}
	// The GROUP is cancelled the moment the verb is accepted; the running job settles later, and
	// it is the job that decides whether anything else is still being written.
	waitFor(t, "the interrupted job to settle", func() bool {
		return groupStateOf(q, out.Group) == GroupCancelled && q.List().Groups[0].Running == ""
	})
	var g groupWire
	for _, got := range q.List().Groups {
		if got.ID == out.Group {
			g = got
		}
	}
	if g.Done != 1 || g.Total != 3 {
		t.Fatalf("group = %+v, want 1 of 3 made", g)
	}
	if got := p.cancels(); len(got) != 1 {
		t.Fatalf("cancelled %v, want the running job taken back once", got)
	}
}

func groupStateOf(q *jobQueue, id string) string {
	for _, g := range q.List().Groups {
		if g.ID == id {
			return g.State
		}
	}
	return ""
}

// ADR 0094 decision 2, root-fixed per sfiowgj review (2026-09-20): the queue's own finish() has
// to ask Caps about res.Model (what actually ran), not j.model (resolveModelFamily's ENQUEUE-time
// guess) — j.model is always resolved to a concrete id even for a request naming none, so asking
// it here was ALREADY per-model before decision 11 ever existed, and would keep being per-model
// even where it guessed the wrong row (a family that does not offer the requested op, remapped
// away from inside Generate itself).
// The queue's own version of sfiowgj's 🟡A finding: `resolveModelFamily` always resolves
// `req.Model` to a concrete id before `Generate` ever runs (jobs.go's Enqueue), so this route's
// Caps lookup was ALREADY per-model even before ADR 0094 decision 11 existed — which is exactly
// why decision 2's caller-negative branch, once it lived in comfyNegativeIgnoredWarning too,
// fired ALONGSIDE this one on every request through the pane: two warnings for the one dropped
// negative_prompt. Removing that branch (and asking Caps about res.Model, the row that actually
// ran, rather than j.model, the enqueue-time guess) leaves exactly one.
func TestQueueWarnsExactlyOnceAboutADroppedNegativePrompt(t *testing.T) {
	q := withJobQueue(t)
	// negConn() minus its catalogue-level negatives, so the ONLY possible warning is about the
	// CALLER's own negative_prompt — isolating exactly the duplicate this test guards against.
	conn := negConn()
	conn.Negatives, conn.NegativeAlways = nil, ""
	p, _ := comfyStub(t, conn, nil)
	withStubProvider(t, p)

	out := enqueue(t, q, JobSpec{Request: Request{
		Op: OpGenerate, Prompt: "a fox", Model: "klein-4b", NegativePrompt: "watermark",
	}})
	id := out.Jobs[0].ID
	waitFor(t, "the job to finish", func() bool { return stateOf(q, id) == "done" || stateOf(q, id) == "failed" })
	if got := stateOf(q, id); got != "done" {
		t.Fatalf("state = %s, want done", got)
	}
	var warnings []string
	for _, j := range q.List().Jobs {
		if j.ID == id {
			warnings = j.Warnings
		}
	}
	count := 0
	for _, w := range warnings {
		if strings.Contains(w, "negative") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("warnings = %v, want exactly ONE warning about the dropped negative prompt, got %d", warnings, count)
	}
}

// The sidecar is the resolved request, and it is what makes a picture reproducible after the
// form that made it is gone.
func TestSidecarRecordsTheResolvedRequest(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)
	withStudio(t, p, StudioModel{ID: "sdxl-base-1.0", Family: string(ComfyFamilySDXL)})
	Build = "test-build"

	seed := int64(4242)
	enqueue(t, q, JobSpec{
		Label: "poster run",
		Request: Request{
			Prompt: "a fox", NegativePrompt: "blurry", Model: "sdxl-base-1.0",
			Size: "1024x1024", Seed: &seed,
			Params: &EngineParams{Steps: 30, CFG: 6, Sampler: "euler", Scheduler: "karras"},
			Loras:  []LoraRef{{Name: "lineart", Weight: 0.8}},
		},
	})
	waitFor(t, "the job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "the job to finish", func() bool { return len(q.List().Jobs[0].Files) > 0 })

	file := q.List().Jobs[0].Files[0]
	b, err := os.ReadFile(file.Path + ".json")
	if err != nil {
		t.Fatalf("no sidecar next to %s: %v", file.Path, err)
	}
	var got ImageProps
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("the sidecar is not JSON: %v (%s)", err, b)
	}
	if got.Prompt != "a fox" || got.Negative != "blurry" || got.Model != "sdxl-base-1.0" {
		t.Errorf("sidecar request = %+v", got)
	}
	if got.Family != string(ComfyFamilySDXL) {
		t.Errorf("family = %q, want the resolved one", got.Family)
	}
	if got.Seed == nil || *got.Seed != seed {
		t.Errorf("seed = %v, want the one this picture actually used", got.Seed)
	}
	if got.Params == nil || got.Params.Steps != 30 || got.Params.Sampler != "euler" {
		t.Errorf("params = %+v, want what ran", got.Params)
	}
	if len(got.Loras) != 1 || got.Loras[0].Name != "lineart" || got.Loras[0].Weight != 0.8 {
		t.Errorf("loras = %+v", got.Loras)
	}
	if got.Label != "poster run" || got.Job == "" || got.Group == "" {
		t.Errorf("the queue's own fields are missing: %+v", got)
	}
	if got.Agent != "test-build" {
		t.Errorf("agent = %q, want the build that wrote the graph", got.Agent)
	}
	if got.Size != "1024x1024" && got.Size != "4x4" {
		t.Errorf("size = %q", got.Size)
	}
}

// The seed of each picture rides on the answer. "It was random and I cannot get it back" is the
// complaint the whole field exists to answer.
func TestSeedRidesOnTheStoredFile(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	seed := int64(7)
	enqueue(t, q, JobSpec{Request: Request{Prompt: "a fox", Seed: &seed}})
	waitFor(t, "the job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "the job to finish", func() bool { return len(q.List().Jobs[0].Files) > 0 })

	file := q.List().Jobs[0].Files[0]
	if file.Seed == nil || *file.Seed != seed {
		t.Fatalf("files[0].seed = %v, want %d", file.Seed, seed)
	}
}

// Each policy answers the question it exists for: fixed is "same picture, vary the cfg",
// sequence is "one axis at a time", random leaves it to the provider.
func TestSeedPolicies(t *testing.T) {
	base := int64(100)
	t.Run("fixed", func(t *testing.T) {
		got, err := seedsFor(SeedFixed, &base, 3)
		if err != nil {
			t.Fatal(err)
		}
		for i, s := range got {
			if s == nil || *s != base {
				t.Fatalf("job %d got %v, want every job the same seed", i, s)
			}
		}
	})
	t.Run("sequence", func(t *testing.T) {
		got, err := seedsFor(SeedSequence, &base, 3)
		if err != nil {
			t.Fatal(err)
		}
		for i, s := range got {
			if s == nil || *s != base+int64(i) {
				t.Fatalf("job %d got %v, want %d", i, s, base+int64(i))
			}
		}
	})
	t.Run("random leaves it to the provider", func(t *testing.T) {
		got, err := seedsFor(SeedRandom, nil, 2)
		if err != nil {
			t.Fatal(err)
		}
		for i, s := range got {
			if s != nil {
				t.Fatalf("job %d pinned %d — random means the route draws one", i, *s)
			}
		}
	})
	t.Run("fixed with no seed draws one and holds it", func(t *testing.T) {
		got, err := seedsFor(SeedFixed, nil, 3)
		if err != nil {
			t.Fatal(err)
		}
		if got[0] == nil || *got[0] != *got[1] || *got[1] != *got[2] {
			t.Fatalf("got %v, want one drawn seed on every job", got)
		}
	})
}

// A typed value fails LOUDLY. The catalogue overlay is lenient on purpose — an administrator's
// old row may name a sampler newer than this binary — and a member's form is the opposite case.
func TestValidationRefusesATypedValueByName(t *testing.T) {
	cases := []struct {
		name string
		body jobRequest
		code string
		says string
	}{
		{"unknown sampler", jobRequest{Prompt: "x", Params: &EngineParams{Sampler: "not_a_sampler"}},
			"bad_params", "not_a_sampler"},
		{"unknown scheduler", jobRequest{Prompt: "x", Params: &EngineParams{Scheduler: "not_a_scheduler"}},
			"bad_params", "not_a_scheduler"},
		{"steps over the ceiling", jobRequest{Prompt: "x", Params: &EngineParams{Steps: 400}},
			"bad_params", "steps"},
		{"cfg over the ceiling", jobRequest{Prompt: "x", Params: &EngineParams{CFG: 99}},
			"bad_params", "cfg"},
		{"a side that is not a multiple of 8", jobRequest{Prompt: "x", Size: "1023x1024"},
			"bad_params", "multiple"},
		{"over the pixel ceiling", jobRequest{Prompt: "x", Size: "4096x4096"},
			"bad_params", "pixels"},
		{"a batch bigger than the graph allows", jobRequest{Prompt: "x", Count: 9},
			"bad_count", "batch size"},
		{"an unknown seed policy", jobRequest{Prompt: "x", SeedPolicy: "spiral"},
			"bad_seed_policy", "spiral"},
		{"no prompt", jobRequest{}, "bad_prompt", "prompt"},
		{"an unknown op", jobRequest{Prompt: "x", Op: "colourise"}, "bad_op", "colourise"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, code, msg := c.body.spec()
			if code != c.code {
				t.Fatalf("code = %q, want %q (%s)", code, c.code, msg)
			}
			if !strings.Contains(msg, c.says) {
				t.Errorf("message %q does not name %q — a refusal the caller cannot act on is a 500 with better manners", msg, c.says)
			}
		})
	}
}

// A sampler name the Agent DOES know passes, so the test above is not just proving that
// everything is refused.
func TestValidationAdmitsAKnownSampler(t *testing.T) {
	body := jobRequest{Prompt: "x", Size: "1024x1024",
		Params: &EngineParams{Sampler: "dpmpp_2m", Scheduler: "karras", Steps: 30, CFG: 7}}
	if _, code, msg := body.spec(); code != "" {
		t.Fatalf("refused a valid request: %s %s", code, msg)
	}
}

// ADR 0094 decision 2/4's edge refusal, on the queue's own route — the P0 completion condition
// (2) requires this on BOTH routes, not only the blocking one (HandleGenerate's TestGenerate*
// tests cover that side).
func TestSpecRefusesStrengthAndSizeAgainstQwenImageEdit(t *testing.T) {
	p, _ := comfyStub(t, qwenEditConn(), nil)
	withStubProvider(t, p)

	s := 0.3
	if _, code, msg := (jobRequest{Prompt: "x", Op: "edit", Model: "qwen-edit-row", Strength: &s}).spec(); code != "bad_strength_family" {
		t.Fatalf("code = %q (%s), want bad_strength_family", code, msg)
	}
	if _, code, msg := (jobRequest{Prompt: "x", Op: "edit", Model: "qwen-edit-row", Size: "1024x1024"}).spec(); code != "bad_size_family" {
		t.Fatalf("code = %q (%s), want bad_size_family", code, msg)
	}
	// The positive control: the same two fields against the OTHER row on the same engine must
	// not be refused for this reason.
	if _, code, msg := (jobRequest{Prompt: "x", Op: "edit", Model: "sdxl-base-1.0", Strength: &s}).spec(); code != "" {
		t.Fatalf("sdxl was refused: %s %s", code, msg)
	}
	// No model and no provider named: nothing here can be resolved to a family, so neither
	// refusal fires — that gap is comfyStrengthIgnoredWarning's, not this one's.
	if _, code, msg := (jobRequest{Prompt: "x", Op: "edit", Strength: &s}).spec(); code != "" {
		t.Fatalf("an unresolved request was refused: %s %s", code, msg)
	}
}

// 🔴 The same state has to produce the same bytes, or the Control Plane's ETag never matches and
// the pane's two-second poll costs a full response a second for the life of a batch.
func TestJobsAnswerIsByteStableForTheSameState(t *testing.T) {
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)

	enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
	waitFor(t, "the job to start", func() bool { return len(p.begun) > 0 })
	<-p.begun
	enqueue(t, q, JobSpec{Request: Request{Prompt: "waiting"}, Jobs: 3})

	first, err := json.Marshal(q.List())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // a live elapsed_ms would move in here
	second, err := json.Marshal(q.List())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("two polls of an unchanged queue differ:\n%s\n%s", first, second)
	}
	// And it does change when the state does — otherwise the test above would pass on a handler
	// that answered a constant.
	p.release <- struct{}{}
	waitFor(t, "the answer to move", func() bool {
		b, _ := json.Marshal(q.List())
		return string(b) != string(first)
	})
}

// The estimate is a moving average of what actually finished, keyed by steps as well as model
// and size — otherwise trials would teach the average that batches are fast.
func TestTypicalIsAMovingAverageKeyedByStepsToo(t *testing.T) {
	q := withJobQueue(t)
	slow := &jobRec{provider: ProviderComfy, model: "m", elapsed: 10 * time.Second,
		req: Request{Size: "1024x1024", Params: &EngineParams{Steps: 30}}}
	fast := &jobRec{provider: ProviderComfy, model: "m", elapsed: 2 * time.Second,
		req: Request{Size: "1024x1024", Params: &EngineParams{Steps: 10}}}

	q.mu.Lock()
	q.observeElapsedLocked(slow)
	q.observeElapsedLocked(slow)
	q.observeElapsedLocked(fast)
	batchKey := typicalKeyFine(ProviderComfy, "m", "1024x1024", 30)
	trialKey := typicalKeyFine(ProviderComfy, "m", "1024x1024", 10)
	batch, trial := q.typical[batchKey], q.typical[trialKey]
	q.mu.Unlock()

	if batch != 10000 {
		t.Errorf("the batch estimate is %v ms, want the measured 10000 — nothing else was measured at 30 steps", batch)
	}
	if trial != 2000 {
		t.Errorf("the trial estimate is %v ms, want 2000 — a trial must not teach the batch that it is fast", trial)
	}
	// The coarse key, which is what the catalogue reports, DOES see both.
	if got := q.typicalFor(ProviderComfy, "m"); got <= 2000 || got >= 10000 {
		t.Errorf("typical_ms = %d, want a moving average of both", got)
	}
}

// The wake is measured too, so "the engine starts on the first job; usually N minutes" is a
// measurement rather than a guess.
func TestWakeTimeIsObservedWhenThePhaseLeavesWaking(t *testing.T) {
	q := withJobQueue(t)
	j := &jobRec{state: JobRunning}
	q.setPhase(j, PhaseWaking)
	q.mu.Lock()
	j.wakingSince = j.wakingSince.Add(-90 * time.Second)
	q.mu.Unlock()
	q.setPhase(j, PhaseRunning)

	if got := q.observedWakeMS(); got < 89_000 || got > 95_000 {
		t.Fatalf("wake_ms = %d, want about 90000", got)
	}
}

// Only the deployment's OWN engines. The CLI-driven routes are agents by construction and burn a
// member's personal plan, which is exactly what this pane exists to avoid.
func TestQueueRefusesACLIDrivenProvider(t *testing.T) {
	withJobQueue(t)
	withStubProvider(t, stubProvider{id: ProviderAgy, caps: &Caps{Ops: AllOps}})
	if _, err := fleetProviderFor(context.Background(), ProviderAgy); err == nil {
		t.Fatal("agy was admitted: this route offers only the engines the deployment runs itself")
	}
	if _, err := fleetProviderFor(context.Background(), ""); err == nil {
		t.Fatal("auto found a provider where the only one is CLI-driven")
	}
}

// out_dir goes through the Agent's own path gate, and a folder outside it is refused rather than
// created.
func TestOutDirGoesThroughTheBrowseGate(t *testing.T) {
	withJobQueue(t)
	root := t.TempDir()
	oldWrite := BrowseWritablePath
	BrowseWritablePath = func(p string) (string, string, bool) {
		if strings.Contains(p, "..") || filepath.IsAbs(p) {
			return "", "", false
		}
		return filepath.Join(root, p), p, true
	}
	t.Cleanup(func() { BrowseWritablePath = oldWrite })

	dir, rel, err := jobOutputDir(JobSpec{OutDir: "pictures/posters"})
	if err != nil {
		t.Fatalf("a folder inside the root was refused: %v", err)
	}
	if dir != filepath.Join(root, "pictures/posters") || rel != "pictures/posters" {
		t.Fatalf("dir = %q rel = %q", dir, rel)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the folder was not created on first use: %v", err)
	}
	if _, _, err := jobOutputDir(JobSpec{OutDir: "../escape"}); err == nil {
		t.Error("a path above the browse root was admitted")
	}
	// A trial ignores out_dir entirely: its folder is the one the sweep still clears.
	if got, _, _ := jobOutputDir(JobSpec{OutDir: "pictures", Trial: true}); got != ConsoleTrialDir() {
		t.Errorf("a trial wrote to %q, want %q", got, ConsoleTrialDir())
	}
}

// --- the widened status ----------------------------------------------------------------------

// studioProvider is a gateProvider that also answers the member-facing catalogue.
type studioProvider struct {
	*gateProvider
	studio Studio
}

func (p studioProvider) Studio(context.Context) (Studio, bool) { return p.studio, true }

// withStudio re-registers the provider wrapped in a studio answer, so the queue can resolve a
// model's family the way it does against the real comfy provider.
func withStudio(t *testing.T, p *gateProvider, models ...StudioModel) {
	t.Helper()
	withStubProvider(t, studioProvider{gateProvider: p, studio: Studio{Models: models}})
}

func TestStatusReportsTheMemberFacingCatalogue(t *testing.T) {
	withJobQueue(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldEnabled := Enabled
	Enabled = func() bool { return true }
	t.Cleanup(func() { Enabled = oldEnabled })

	conn := sdxlConn()
	conn.Descriptions = map[string]string{"sdxl-base-1.0": "photoreal"}
	conn.Negatives = map[string]string{"sdxl-base-1.0": "watermark"}
	conn.NegativeAlways = "gore"
	conn.Params = map[string]EngineParams{"sdxl-base-1.0": {Steps: 35}}
	conn.Licenses = map[string]EngineLicense{"sdxl-base-1.0": {Name: "CreativeML", URL: "https://x/l", Source: "https://x/s"}}
	conn.Loras = []EngineLora{{ID: "lineart", File: "lineart.safetensors", BaseModel: "sdxl",
		TrainedWords: []string{"lineart style"}, Weight: 0.7}}
	// A row the engine could not run: no declared family, so no template.
	conn.Models = append(conn.Models, "mystery")

	p, _ := comfyStub(t, conn, nil)
	withStubProvider(t, p)

	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status", nil))
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers = %+v", got.Providers)
	}
	st := got.Providers[0]
	if len(st.Models) != 1 || st.Models[0].ID != "sdxl-base-1.0" {
		t.Fatalf("models = %+v, want the undeclared-family row withheld — the form would offer a button that cannot work", st.Models)
	}
	m := st.Models[0]
	if m.Family != "sdxl" {
		t.Errorf("family = %q", m.Family)
	}
	if m.Params == nil || m.Params.Steps != 35 || m.Params.Sampler != "dpmpp_2m" {
		t.Errorf("params = %+v, want the row over the family recipe (35 steps, the family's sampler)", m.Params)
	}
	if m.Negative != "watermark" || st.NegativeAlways != "gore" {
		t.Errorf("negatives = %q / %q", m.Negative, st.NegativeAlways)
	}
	if strings.Join(m.Knobs, ",") != "steps,cfg,sampler,scheduler,negative,strength" {
		t.Errorf("knobs = %v, want what the sdxl template reads (ADR 0094 decision 12 adds strength)", m.Knobs)
	}
	if m.LicenseName != "CreativeML" || m.LicenseURL != "https://x/l" || m.SourceURL != "https://x/s" {
		t.Errorf("licence = %+v", m)
	}
	if len(m.Sizes) == 0 {
		t.Error("sizes are missing")
	}
	if len(st.Samplers) == 0 || len(st.Schedulers) == 0 {
		t.Error("the sampler and scheduler allow-lists are missing: the form would offer a name this Agent refuses")
	}
	if st.LoraWeightMax != comfyMaxLoraWeight {
		t.Errorf("lora_weight_max = %v", st.LoraWeightMax)
	}
	if len(st.Loras) != 1 || len(st.Loras[0].TrainedWords) != 1 || st.Loras[0].Weight != 0.7 {
		t.Errorf("loras = %+v, want the trigger words and the declared strength", st.Loras)
	}
}

// The knob table is the graphs'. A family that reads no cfg must not report one, or the form
// greys the wrong field out.
func TestFamilyKnobsMatchTheTemplates(t *testing.T) {
	// `strength` (ADR 0094 decision 12) is on every family here — all of them read
	// Request.Strength on an edit — and absent only from qwen-image-edit-2509, which fixes its
	// denoise at 1 by construction (comfyFamilyStrength).
	want := map[comfyFamily]string{
		ComfyFamilySDXL:              "steps,cfg,sampler,scheduler,negative,strength",
		ComfyFamilySD35:              "steps,cfg,sampler,scheduler,negative,strength",
		ComfyFamilyFlux1:             "steps,sampler,scheduler,strength",
		ComfyFamilyFlux2Klein:        "steps,sampler,strength",
		ComfyFamilyZImage:            "steps,cfg,sampler,scheduler,strength",
		ComfyFamilyQwenImageEdit2509: "steps,cfg,sampler,scheduler,negative",
	}
	for family, expect := range want {
		if got := strings.Join(comfyFamilyKnobs(family), ","); got != expect {
			t.Errorf("%s knobs = %s, want %s", family, got, expect)
		}
	}
}

// The recipes the status reports are the ones the templates render with — one declaration, so
// the form's placeholder cannot be a number no picture was ever made at.
func TestEveryFamilyHasARecipe(t *testing.T) {
	for _, f := range comfyFamilies {
		r, ok := comfyFamilyRecipes[f]
		if !ok || r.Steps == 0 || r.Sampler == "" {
			t.Errorf("%s has no usable recipe: %+v", f, r)
		}
	}
}
