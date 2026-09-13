package imagegen

// The job queue the Console's image-generation pane drives (ADR 0081 decision 2).
//
// ADR 0069 deferred jobs because a poll from a driver model costs a turn. A browser poll costs
// nothing but bytes, and the blocking route is unusable from a browser anyway: the Control
// Plane's REST relay is a buffered pass-through with no streaming and no heartbeat, so a call
// that sits through a cold start is cut at the ingress's 60 seconds. Enqueue and poll is the
// shape that survives that; the blocking POST /imagegen/generate stays exactly as it is, for the
// MCP tool whose caller cannot afford to poll.
//
// One worker per provider, and jobs run ONE AT A TIME. The box already serialises sampling;
// submitting ahead only moves the queue somewhere this Agent can neither see nor cancel from,
// and leaves two requests waiting on engine_waking at once. Serial is also what gives the queue
// position and the estimate a meaning.
//
// Memory is the store. Pending jobs are lost when the Agent restarts; finished ones survive
// through the sidecar on disk, which is the archive. The Agent restarts with the container, and
// the container's restart already discards more than a queue.
//
// 🔴 The bytes of GET /imagegen/jobs must be a function of the QUEUE's state alone. The Control
// Plane wraps every JSON GET in a weak ETag, so an unchanged poll costs a 304 and the pane's
// two-second poll costs nothing while nothing is happening — but only as long as nothing in the
// answer moves on its own. That is why a running job carries started_at and not a live
// elapsed_ms: the elapsed one is the browser's subtraction, and putting it here would make every
// single poll a full 200 for the life of the batch.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The queue's own limits. 200 and 500 are ADR 0081's stated guesses (unresolved 6): the shared
// host's memory is the constraint, and a finished job holds paths rather than bytes.
const (
	// imagegenQueueMax is how many jobs may WAIT. It exists so one member cannot park a day of
	// GPU on a box every other member shares, by accident — forty jobs is a quarter of an hour,
	// and a mistyped batch count is how two hundred happen.
	imagegenQueueMax = 200
	// imagegenFinishedMax is how many finished jobs stay in the list.
	imagegenFinishedMax = 500
	// imagegenTrialMax is how many trials may wait at once (ADR 0081 decision 11). A trial jumps
	// the queue, so without a cap the head of the queue becomes a queue of its own and the one
	// picture a trial promises turns back into a wait.
	imagegenTrialMax = 3
	// imagegenMaxPixels is the ceiling on one picture. A 2048² SDXL request on an l4 is an OOM
	// five minutes into a cold start, and the box's own 400 comes back bare — not even as
	// engine_waking — so refusing here is the only place the caller learns why.
	imagegenMaxPixels = 4 << 20
)

// JobState is where one job has got to. The four middle values come from the provider's own
// phase callbacks (Request.OnPhase); the rest are the queue's.
type JobState string

const (
	JobQueued    JobState = "queued"
	JobWaking    JobState = "waking"
	JobUploading JobState = "uploading"
	JobRunning   JobState = "running"
	JobFetching  JobState = "fetching"
	JobDone      JobState = "done"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

func (s JobState) finished() bool { return s == JobDone || s == JobFailed || s == JobCancelled }

// Group states. A group is what one press of "enqueue N" submitted, and it is the unit all four
// of decision 12's verbs act on — because it is the unit the person thinks in.
const (
	GroupRunning   = "running"
	GroupPaused    = "paused"
	GroupDone      = "done"
	GroupCancelled = "cancelled"
)

// Seed policies (ADR 0081 decision 8).
const (
	// SeedRandom draws a fresh seed per job, which is what a caller who says nothing means.
	SeedRandom = "random"
	// SeedFixed gives every job of the group the SAME seed: the knob for "same picture, vary the
	// cfg", which is the whole reason a sweep is worth running.
	SeedFixed = "fixed"
	// SeedSequence is base + i.
	SeedSequence = "sequence"
)

func validSeedPolicy(p string) bool {
	return p == "" || p == SeedRandom || p == SeedFixed || p == SeedSequence
}

// comfyTrialSteps is the step count a trial run samples at, per family (ADR 0081 decision 11).
// They are guesses until the live run (unresolved 7): the number wanted is the smallest one at
// which the composition is still recognisable, which only a picture can answer.
//
// flux2-klein is already at its family recipe's 4 — a distilled 4-step model has no cheaper
// setting — so a trial there differs from the batch in nothing but where it sits in the queue.
var comfyTrialSteps = map[comfyFamily]int{
	ComfyFamilySDXL:       10,
	ComfyFamilySD35:       12,
	ComfyFamilyFlux1:      8,
	ComfyFamilyZImage:     4,
	ComfyFamilyFlux2Klein: 4,
}

// --- the records --------------------------------------------------------------------------

type jobRec struct {
	id      string
	group   string
	label   string
	trial   bool
	created time.Time

	provider string
	model    string
	family   string
	// dir is where the pictures land, absolute; outRel is the browse-root-relative form when the
	// caller named a folder of their own, for the answer to say what it wrote where.
	dir    string
	outRel string
	req    Request
	// fullSteps is what the BATCH would run at when this is a trial — the form's own steps, kept
	// so the sidecar records both what ran and what the keeper would be made with.
	fullSteps int

	state    JobState
	started  time.Time
	finished time.Time
	elapsed  time.Duration
	// wakingSince is when the current waking phase began, so the observed cold-start time can be
	// folded into the estimate the header shows (decision 10).
	wakingSince time.Time
	files       []StoredFile
	warnings    []string
	failure     string
	// upstream is the id the engine gave this request; a cancel has nothing to aim at without it.
	upstream  string
	cancelled bool
}

type groupRec struct {
	id       string
	label    string
	trial    bool
	created  time.Time
	total    int
	paused   bool
	pausedAt time.Time
	// aborted is decision 12's "cancel": the queued jobs are gone and the group is closed, but
	// the pictures already made stay and the row says "12 of 40 made".
	aborted bool
}

// --- the queue ------------------------------------------------------------------------------

type jobQueue struct {
	mu       sync.Mutex
	pending  []*jobRec
	running  map[string]*jobRec // by provider: one at a time, by design
	finished []*jobRec          // oldest first; trimmed from the front
	byID     map[string]*jobRec
	groups   map[string]*groupRec
	// groupOrder keeps the answer's group list stable: a map range would reorder it on every
	// poll and turn a 304 into a 200 for no reason.
	groupOrder []string
	paused     bool
	wake       map[string]chan struct{}
	// typical holds the exponential moving averages: per (provider, model, size bucket, steps)
	// for the estimate, and per (provider, model) for the catalogue's own typical_ms.
	typical map[string]float64
	wakeEMA float64
	seq     int64
	// closed ends the workers. Production never closes the queue — there is one, for the life of
	// the process — but a test that installs a fresh one would otherwise leave a goroutine per
	// provider blocked on a wake channel nobody will ever signal again.
	closed chan struct{}
}

var jobs = newJobQueue()

func newJobQueue() *jobQueue {
	return &jobQueue{
		running: map[string]*jobRec{},
		byID:    map[string]*jobRec{},
		groups:  map[string]*groupRec{},
		wake:    map[string]chan struct{}{},
		typical: map[string]float64{},
		closed:  make(chan struct{}),
	}
}

// Close stops this queue's workers. Test-only, and the reason is stated on the field.
func (q *jobQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-q.closed:
	default:
		close(q.closed)
	}
}

// jobsNow is the clock, a var so a test can pin it. Everything that reaches the wire goes
// through it, which is what makes "the same state emits the same bytes" testable at all.
var jobsNow = time.Now

// JobSpec is one enqueue request, already validated at the edge (jobs_http.go). It expands into
// Jobs jobs under one group.
type JobSpec struct {
	Provider string
	Request  Request
	Label    string
	OutDir   string
	// Jobs is decision 8's N: N jobs of one picture each, not one batch of N. A ComfyUI
	// batch_size above 1 is Request.Count and stays available as an advanced field.
	Jobs       int
	SeedPolicy string
	Trial      bool
	// FullSteps turns decision 11's step reduction off, for the case where the trial IS the
	// picture.
	FullSteps bool
}

// EnqueueResult is what POST /imagegen/jobs answers: the group, and every job with the position
// it was admitted at.
type EnqueueResult struct {
	Group string        `json:"group"`
	Jobs  []enqueuedJob `json:"jobs"`
}

type enqueuedJob struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
}

// errQueueFull and errTrialPending are the two refusals the edge turns into a 429. They are
// typed because the Console shows different words for them: one says "wait", the other says
// "look at the trial you already asked for".
var (
	errQueueFull    = errors.New("the image queue is full")
	errTrialPending = errors.New("too many trial runs are already waiting")
)

// Enqueue admits the whole batch or none of it (ADR 0081 decision 2): the cap judges the request
// as submitted, so a caller never gets 17 of the 40 they asked for and has to work out which.
func (q *jobQueue) Enqueue(ctx context.Context, spec JobSpec) (EnqueueResult, error) {
	prov, err := fleetProviderFor(ctx, spec.Provider)
	if err != nil {
		return EnqueueResult{}, err
	}
	n := spec.Jobs
	if n <= 0 {
		n = 1
	}
	if spec.Trial {
		n = 1 // one picture is the point
	}

	model, family := resolveModelFamily(ctx, prov, spec.Request.Model)
	req := spec.Request
	req.Model = model
	fullSteps := 0
	if spec.Trial {
		req.Count = 1
		if !spec.FullSteps {
			if steps, ok := comfyTrialSteps[comfyFamily(family)]; ok {
				if req.Params != nil {
					fullSteps = req.Params.Steps
				}
				p := EngineParams{}
				if req.Params != nil {
					p = *req.Params
				}
				p.Steps = steps
				req.Params = &p
			}
		}
	}

	dir, outRel, err := jobOutputDir(spec)
	if err != nil {
		return EnqueueResult{}, err
	}

	seeds, err := seedsFor(spec.SeedPolicy, spec.Request.Seed, n)
	if err != nil {
		return EnqueueResult{}, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending)+n > imagegenQueueMax {
		return EnqueueResult{}, fmt.Errorf("%w: %d jobs are already waiting and the limit is %d",
			errQueueFull, len(q.pending), imagegenQueueMax)
	}
	if spec.Trial && q.pendingTrialsLocked() >= imagegenTrialMax {
		return EnqueueResult{}, fmt.Errorf("%w: %d are waiting and the limit is %d — look at those before asking for another",
			errTrialPending, q.pendingTrialsLocked(), imagegenTrialMax)
	}

	q.seq++
	g := &groupRec{
		id: fmt.Sprintf("g%d", q.seq), label: strings.TrimSpace(spec.Label),
		trial: spec.Trial, created: jobsNow(), total: n,
	}
	q.groups[g.id] = g
	q.groupOrder = append(q.groupOrder, g.id)

	out := EnqueueResult{Group: g.id}
	admitted := make([]*jobRec, 0, n)
	for i := 0; i < n; i++ {
		q.seq++
		one := req
		one.Seed = seeds[i]
		j := &jobRec{
			id: fmt.Sprintf("j%d", q.seq), group: g.id, label: g.label, trial: spec.Trial,
			created: jobsNow(), provider: prov.ID(), model: model, family: family,
			dir: dir, outRel: outRel, req: one, fullSteps: fullSteps, state: JobQueued,
		}
		q.byID[j.id] = j
		admitted = append(admitted, j)
	}
	if spec.Trial {
		// At the HEAD, after whatever is already running and before every queued job: behind a
		// batch a trial arrives when the batch does, and the batch was the thing it was meant to
		// decide.
		q.pending = append(admitted, q.pending...)
	} else {
		q.pending = append(q.pending, admitted...)
	}
	for _, j := range admitted {
		out.Jobs = append(out.Jobs, enqueuedJob{ID: j.id, Position: q.positionLocked(j.id)})
	}
	q.signalLocked(prov.ID())
	return out, nil
}

func (q *jobQueue) pendingTrialsLocked() int {
	n := 0
	for _, j := range q.pending {
		if j.trial {
			n++
		}
	}
	return n
}

func (q *jobQueue) positionLocked(id string) int {
	for i, j := range q.pending {
		if j.id == id {
			return i + 1
		}
	}
	return 0
}

// signalLocked wakes this provider's worker, starting it the first time. The channel is buffered
// to one and the send is non-blocking, so a burst of forty enqueues costs one wake-up and a
// worker that is already busy does not miss the next one — it re-reads the queue when it
// finishes anyway.
func (q *jobQueue) signalLocked(provider string) {
	ch, ok := q.wake[provider]
	if !ok {
		ch = make(chan struct{}, 1)
		q.wake[provider] = ch
		go q.work(provider, ch)
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (q *jobQueue) work(provider string, wake <-chan struct{}) {
	for {
		j := q.take(provider)
		if j == nil {
			select {
			case <-wake:
			case <-q.closed:
				return
			}
			continue
		}
		q.run(j)
	}
}

// take hands the worker the next job it may run, or nil. It is where pause lives: a paused group
// is SKIPPED rather than blocking the queue, so another group — or a trial — runs while it
// waits. That is the reason to pause at all (decision 12).
func (q *jobQueue) take(provider string) *jobRec {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running[provider] != nil {
		return nil
	}
	for i, j := range q.pending {
		if j.provider != provider {
			continue
		}
		// A queue-wide pause still lets trials through: pausing the batch in order to try
		// something is the whole point.
		if q.paused && !j.trial {
			continue
		}
		if g := q.groups[j.group]; g != nil && (g.paused || g.aborted) {
			continue
		}
		q.pending = append(q.pending[:i], q.pending[i+1:]...)
		j.state = JobRunning
		j.started = jobsNow()
		q.running[provider] = j
		return j
	}
	return nil
}

func (q *jobQueue) run(j *jobRec) {
	prov := providerByID(j.provider)
	if prov == nil {
		q.finish(j, nil, nil, fmt.Errorf("provider %s is no longer available", j.provider))
		return
	}
	req := j.req
	req.OnPhase = func(p Phase) { q.setPhase(j, p) }
	req.OnUpstream = func(id string) { q.setUpstream(j, id) }

	started := jobsNow()
	res, err := prov.Generate(context.Background(), req)
	ok := err == nil && len(res.Images) > 0
	// The ledger row is written on every path, including the failed one: an attempt that spent
	// GPU time and produced nothing still consumed what the deployment pays for (ADR 0029 §3).
	// ref is empty — this path has no session, and naming one that does not exist would file a
	// member's own volume under a conversation nobody had.
	recordUsage(context.Background(), "", j.provider, res, ok, started)
	if err == nil && len(res.Images) == 0 {
		err = errors.New("the provider returned no image")
	}
	if err != nil {
		q.finish(j, nil, res.Warnings, err)
		return
	}

	if q.wasCancelled(j) {
		// Nothing is written at all. sdcpp has no cancel, so its job runs to the end and arrives
		// here with a perfectly good picture nobody asked for any more; storing it would put a
		// file in the gallery for a job the list says was cancelled, and the sidecar next to it
		// would be a record of a request that was withdrawn.
		q.finish(j, nil, res.Warnings, nil)
		return
	}
	props := q.propsFor(j, res)
	files, storeErr := storeImagesAt(j.dir, res.Images, &props)
	if storeErr != nil {
		// A storage failure is OURS, not the provider's — the picture exists and was made.
		q.finish(j, nil, res.Warnings, storeErr)
		return
	}
	warnings := append(append([]string{}, res.Warnings...), requestWarnings(req, res, prov.Caps(j.model))...)
	q.finish(j, files, warnings, nil)
}

// propsFor is the sidecar this job's pictures carry (ADR 0081 decision 3). It is the RESOLVED
// request — what actually ran, with the negative as composed and the params as merged — never
// the form's own values, because the whole point of the record is to answer "what made this
// picture" for somebody who no longer has the form.
func (q *jobQueue) propsFor(j *jobRec, res Result) ImageProps {
	q.mu.Lock()
	elapsed := jobsNow().Sub(j.started).Milliseconds()
	q.mu.Unlock()
	props := ImageProps{
		Provider: res.Provider, Model: res.Model, Family: j.family,
		Op: string(j.req.Op), Prompt: j.req.Prompt,
		Size: j.req.Size, Strength: j.req.Strength, Inputs: j.req.Inputs, Mask: j.req.Mask,
		Loras: j.req.Loras, Job: j.id, Group: j.group, Label: j.label, Trial: j.trial,
		ElapsedMS: elapsed, Warnings: res.Warnings, Agent: Build,
		CreatedAt: j.created.UTC().Format(time.RFC3339),
	}
	if props.Provider == "" {
		props.Provider = j.provider
	}
	if props.Model == "" {
		props.Model = j.model
	}
	if j.req.Params != nil {
		p := *j.req.Params
		props.Params = &p
	}
	if j.fullSteps > 0 {
		props.FullSteps = j.fullSteps
	}
	if n := composedNegativeOf(res); n != "" {
		props.Negative = n
	} else {
		props.Negative = j.req.NegativePrompt
	}
	return props
}

// composedNegativeOf is a seam for the day a provider reports the negative it actually sampled
// with. None does today — the composition happens inside comfy.Generate and is not on Result —
// so the sidecar records the caller's own, and the PNG chunk is where the composed one can be
// read back from. Stated here rather than left as a silent gap.
func composedNegativeOf(Result) string { return "" }

func (q *jobQueue) setPhase(j *jobRec, p Phase) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next := JobState(p)
	if j.state == next || j.state.finished() {
		return
	}
	if j.state == JobWaking && !j.wakingSince.IsZero() {
		// The box is up: fold how long it took into the estimate the header shows, so "the engine
		// starts on the first job; usually N minutes" is a measurement rather than a guess.
		q.observeWakeLocked(jobsNow().Sub(j.wakingSince))
		j.wakingSince = time.Time{}
	}
	if next == JobWaking {
		j.wakingSince = jobsNow()
	}
	j.state = next
}

// wasCancelled answers whether a cancel arrived while this job was in flight.
func (q *jobQueue) wasCancelled(j *jobRec) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return j.cancelled
}

func (q *jobQueue) setUpstream(j *jobRec, id string) {
	q.mu.Lock()
	j.upstream = id
	cancelled := j.cancelled
	q.mu.Unlock()
	if !cancelled {
		return
	}
	// A cancel arrived before the engine had named the request: there was nothing to aim at
	// then, and there is now.
	q.askProviderCancel(j.provider, id)
}

func (q *jobQueue) finish(j *jobRec, files []StoredFile, warnings []string, err error) {
	q.mu.Lock()
	j.finished = jobsNow()
	j.elapsed = j.finished.Sub(j.started)
	j.files = files
	j.warnings = warnings
	switch {
	case j.cancelled:
		// The result is discarded whatever it was. sdcpp has no cancel at all, so its job runs to
		// the end and lands here with a perfectly good picture that nobody asked for any more.
		j.state = JobCancelled
		j.files = nil
	case err != nil:
		j.state = JobFailed
		j.failure = err.Error()
	default:
		j.state = JobDone
		q.observeElapsedLocked(j)
	}
	delete(q.running, j.provider)
	q.finished = append(q.finished, j)
	q.trimFinishedLocked()
	q.signalLocked(j.provider)
	q.mu.Unlock()
}

// trimFinishedLocked keeps the finished list bounded. A job that falls off the end leaves byID
// too — its sidecar on disk is the archive, and holding the record forever would make the list
// the thing that grows without bound on a shared host.
func (q *jobQueue) trimFinishedLocked() {
	for len(q.finished) > imagegenFinishedMax {
		delete(q.byID, q.finished[0].id)
		q.finished = q.finished[1:]
	}
}

// --- the estimate ---------------------------------------------------------------------------

// emaAlpha weights the newest measurement. 0.3 moves with a checkpoint switch or a box class
// change within a few pictures while a single cold outlier does not throw the number.
const emaAlpha = 0.3

func (q *jobQueue) observeElapsedLocked(j *jobRec) {
	ms := float64(j.elapsed.Milliseconds())
	if ms <= 0 {
		return
	}
	// Two keys. The fine one is what an ETA is computed from: a trial at 10 steps and a batch at
	// 30 are not the same picture, and averaging them together would teach the batch that it is
	// three times faster than it is (ADR 0081 decision 11). The coarse one is what the
	// catalogue's own typical_ms reports, where "about this long for this model" is the question.
	q.emaLocked(typicalKeyFine(j.provider, j.model, j.req.Size, stepsOf(j.req)), ms)
	q.emaLocked(typicalKeyModel(j.provider, j.model), ms)
}

func (q *jobQueue) emaLocked(key string, ms float64) {
	if cur, ok := q.typical[key]; ok && cur > 0 {
		q.typical[key] = cur*(1-emaAlpha) + ms*emaAlpha
		return
	}
	q.typical[key] = ms
}

func (q *jobQueue) observeWakeLocked(d time.Duration) {
	if d <= 0 {
		return
	}
	if q.wakeEMA > 0 {
		q.wakeEMA = q.wakeEMA*(1-emaAlpha) + float64(d.Milliseconds())*emaAlpha
		return
	}
	q.wakeEMA = float64(d.Milliseconds())
}

func typicalKeyFine(provider, model, size string, steps int) string {
	return "f:" + provider + "|" + model + "|" + sizeBucket(size) + "|" + strconv.Itoa(steps)
}

func typicalKeyModel(provider, model string) string { return "m:" + provider + "|" + model }

// typicalFor is what the member-facing catalogue reports: how long a picture usually takes on
// this (provider, model), or on the provider as a whole when model is empty. 0 means nothing has
// finished yet, which the status must report as absent rather than as "instant".
func (q *jobQueue) typicalFor(provider, model string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if model != "" {
		return int64(q.typical[typicalKeyModel(provider, model)])
	}
	// The provider's own number is the mean of what its models have been measured at: a single
	// model's average would be a different claim depending on which one was asked for last.
	var sum float64
	var n int
	prefix := "m:" + provider + "|"
	for key, v := range q.typical {
		if strings.HasPrefix(key, prefix) && v > 0 {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return int64(sum / float64(n))
}

func (q *jobQueue) observedWakeMS() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return int64(q.wakeEMA)
}

// sizeBucket groups sizes by area rather than by exact dimensions: 1024x1024 and 1152x896 cost
// nearly the same and would otherwise be two averages that each learn half as fast.
func sizeBucket(size string) string {
	w, h, ok := parseSize(size)
	if !ok {
		return "auto"
	}
	switch px := w * h; {
	case px <= 512*512:
		return "s"
	case px <= 1024*1024:
		return "m"
	case px <= 1536*1536:
		return "l"
	default:
		return "xl"
	}
}

func stepsOf(req Request) int {
	if req.Params != nil {
		return req.Params.Steps
	}
	return 0
}

// --- cancel ---------------------------------------------------------------------------------

var errNoSuchJob = errors.New("no such job")

// Cancel takes one job back (ADR 0081 decision 2). A queued job is simply removed; a running one
// is asked of the provider's optional Canceller, and the result is discarded either way.
func (q *jobQueue) Cancel(id string) error {
	q.mu.Lock()
	j, ok := q.byID[id]
	if !ok {
		q.mu.Unlock()
		return errNoSuchJob
	}
	if j.state.finished() {
		q.mu.Unlock()
		return nil // already over; cancelling it again is not an error worth reporting
	}
	if j.state == JobQueued {
		for i, p := range q.pending {
			if p.id == id {
				q.pending = append(q.pending[:i], q.pending[i+1:]...)
				break
			}
		}
		j.state = JobCancelled
		j.finished = jobsNow()
		q.finished = append(q.finished, j)
		q.trimFinishedLocked()
		q.mu.Unlock()
		return nil
	}
	j.cancelled = true
	provider, upstream := j.provider, j.upstream
	q.mu.Unlock()
	if upstream == "" {
		// The engine has not named the request yet. setUpstream will ask the moment it does,
		// which is the only way to cancel a job that is still waking a box.
		return nil
	}
	return q.askProviderCancel(provider, upstream)
}

// askProviderCancel is where the OPTIONAL capability is asked for. A provider without one (sdcpp)
// is not an error: its job runs out and its result is discarded, which is what decision 2 says
// happens and is better than reporting a failure the caller cannot act on.
func (q *jobQueue) askProviderCancel(provider, upstream string) error {
	p := providerByID(provider)
	c, ok := p.(Canceller)
	if !ok {
		return nil
	}
	return c.Cancel(context.Background(), upstream)
}

// --- group and queue operations ---------------------------------------------------------------

var errNoSuchGroup = errors.New("no such group")

// GroupOp is decision 12's four verbs, all at the JOB boundary. ComfyUI has no pause and an
// interrupted sampling cannot be resumed — only restarted from step 0 with the same seed, which
// is a repeat and not a resume — so the running picture always finishes except under skip and
// cancel, where finishing it is exactly what was not wanted.
func (q *jobQueue) GroupOp(id, op string) error {
	q.mu.Lock()
	g, ok := q.groups[id]
	if !ok {
		q.mu.Unlock()
		return errNoSuchGroup
	}
	var toCancel []*jobRec
	switch op {
	case "pause":
		g.paused, g.pausedAt = true, jobsNow()
	case "resume":
		g.paused, g.pausedAt = false, time.Time{}
	case "skip":
		// The picture being made now is not worth finishing; the next one starts. The skipped job
		// is cancelled, not retried.
		for _, j := range q.running {
			if j.group == id {
				j.cancelled = true
				toCancel = append(toCancel, j)
			}
		}
	case "cancel":
		g.aborted = true
		var keep []*jobRec
		for _, j := range q.pending {
			if j.group != id {
				keep = append(keep, j)
				continue
			}
			j.state = JobCancelled
			j.finished = jobsNow()
			q.finished = append(q.finished, j)
			q.trimFinishedLocked()
		}
		q.pending = keep
		for _, j := range q.running {
			if j.group == id {
				j.cancelled = true
				toCancel = append(toCancel, j)
			}
		}
	default:
		q.mu.Unlock()
		return fmt.Errorf("unknown group operation %q", op)
	}
	providers := map[string]bool{}
	for _, j := range q.pending {
		providers[j.provider] = true
	}
	for _, j := range toCancel {
		providers[j.provider] = true
	}
	for p := range providers {
		q.signalLocked(p)
	}
	asks := make([][2]string, 0, len(toCancel))
	for _, j := range toCancel {
		if j.upstream != "" {
			asks = append(asks, [2]string{j.provider, j.upstream})
		}
	}
	q.mu.Unlock()
	for _, a := range asks {
		_ = q.askProviderCancel(a[0], a[1])
	}
	return nil
}

// QueueOp pauses or resumes every group at once. Trials still run while the queue is paused —
// that is the reason to pause it.
func (q *jobQueue) QueueOp(op string) error {
	q.mu.Lock()
	switch op {
	case "pause":
		q.paused = true
	case "resume":
		q.paused = false
	default:
		q.mu.Unlock()
		return fmt.Errorf("unknown queue operation %q", op)
	}
	providers := map[string]bool{}
	for _, j := range q.pending {
		providers[j.provider] = true
	}
	for p := range providers {
		q.signalLocked(p)
	}
	q.mu.Unlock()
	return nil
}

// --- resolution helpers -------------------------------------------------------------------

// fleetProviderFor picks what will make the picture. Only the FLEET's own providers are offered
// here (ADR 0081 decision 1): the CLI-driven ones are agents by construction, own none of these
// knobs, and burn a member's plan quota — which is what the agents.image_generation opt-in
// exists to gate, and why that opt-in does not gate this path at all.
func fleetProviderFor(ctx context.Context, pref string) (Provider, error) {
	pref = strings.TrimSpace(pref)
	byID := map[string]Provider{}
	for _, p := range Providers() {
		byID[p.ID()] = p
	}
	if pref != "" && pref != "auto" {
		p, ok := byID[pref]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, pref)
		}
		if !providerIsFleet(pref) {
			return nil, fmt.Errorf("%w: %s is driven by a CLI on a member's own plan, and this route offers"+
				" only the engines this deployment runs itself", ErrNoProvider, pref)
		}
		if !p.Ready(ctx) {
			return nil, fmt.Errorf("%w: %s is not available in this deployment", ErrNoProvider, pref)
		}
		return p, nil
	}
	for _, id := range effectiveOrder() {
		p, ok := byID[id]
		if ok && providerIsFleet(id) && p.Ready(ctx) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: this deployment runs no image engine of its own", ErrNoProvider)
}

func providerByID(id string) Provider {
	for _, p := range Providers() {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// resolveModelFamily answers what will actually run, so the job list and the sidecar name a
// checkpoint rather than repeating the caller's empty string.
func resolveModelFamily(ctx context.Context, p Provider, model string) (string, string) {
	model = strings.TrimSpace(model)
	s, ok := studioOf(ctx, p)
	if !ok {
		return model, ""
	}
	for _, m := range s.Models {
		if m.ID == model {
			return m.ID, m.Family
		}
	}
	if model != "" {
		return model, "" // named something the catalogue does not list; the provider refuses it by name
	}
	for _, m := range s.Models {
		if m.Warm {
			return m.ID, m.Family
		}
	}
	if len(s.Models) > 0 {
		return s.Models[0].ID, s.Models[0].Family
	}
	return "", ""
}

// jobOutputDir decides where this job's pictures land (ADR 0081 decision 3).
func jobOutputDir(spec JobSpec) (dir, rel string, err error) {
	if spec.Trial {
		// Trials always go to their own subfolder, whatever out_dir says: they are disposable by
		// definition (the batch remakes the keeper at full steps) and that folder is the one
		// subtree the sweep still clears.
		return ConsoleTrialDir(), "", nil
	}
	full, rel, err := resolveOutDir(spec.OutDir)
	if err != nil {
		return "", "", err
	}
	if full == "" {
		return ConsoleDir(), "", nil
	}
	return full, rel, nil
}

// seedsFor turns the policy into one seed per job (ADR 0081 decision 8). A nil entry means "the
// provider draws one", which is what random means and is the only way a route with no seed of
// its own stays honest.
func seedsFor(policy string, base *int64, n int) ([]*int64, error) {
	out := make([]*int64, n)
	switch policy {
	case "", SeedRandom:
		if base != nil {
			// A seed was pinned AND the policy left at its default: the pinned one wins, because
			// typing a seed is a louder statement than not changing a dropdown.
			for i := range out {
				s := *base
				out[i] = &s
			}
		}
		return out, nil
	case SeedFixed:
		s := int64(0)
		if base != nil {
			s = *base
		} else {
			drawn, err := comfyRandomSeed()
			if err != nil {
				return nil, err
			}
			s = drawn
		}
		for i := range out {
			v := s
			out[i] = &v
		}
		return out, nil
	case SeedSequence:
		s := int64(0)
		if base != nil {
			s = *base
		} else {
			drawn, err := comfyRandomSeed()
			if err != nil {
				return nil, err
			}
			s = drawn
		}
		for i := range out {
			v := s + int64(i)
			out[i] = &v
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown seed policy %q", policy)
}

// --- the answer -------------------------------------------------------------------------------

type jobWire struct {
	ID    string `json:"id"`
	Group string `json:"group,omitempty"`
	Label string `json:"label,omitempty"`
	State string `json:"state"`
	// Position is 1-based and rides only while the job is queued.
	Position int    `json:"position,omitempty"`
	Trial    bool   `json:"trial,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Family   string `json:"family,omitempty"`
	Op       string `json:"op"`
	Prompt   string `json:"prompt"`
	Negative string `json:"negative,omitempty"`
	// Seed is what was PINNED, nil when the provider drew one — the seed each picture actually
	// came out at rides on files[].seed, because a batch of four is four seeds.
	Seed     *int64        `json:"seed,omitempty"`
	Size     string        `json:"size,omitempty"`
	Count    int           `json:"count,omitempty"`
	Params   *EngineParams `json:"params,omitempty"`
	Loras    []LoraRef     `json:"loras,omitempty"`
	Strength *float64      `json:"strength,omitempty"`
	Inputs   []string      `json:"inputs,omitempty"`
	// FullSteps is what the batch would run at, on a trial whose steps were reduced.
	FullSteps int    `json:"full_steps,omitempty"`
	OutDir    string `json:"out_dir,omitempty"`
	CreatedAt string `json:"created_at"`
	StartedAt string `json:"started_at,omitempty"`
	// FinishedAt and ElapsedMS ride only on a FINISHED job. A live elapsed_ms would move on
	// every poll and cost the pane a full response per second for the life of the batch; the
	// browser subtracts started_at instead. See this file's header.
	FinishedAt string       `json:"finished_at,omitempty"`
	ElapsedMS  int64        `json:"elapsed_ms,omitempty"`
	TypicalMS  int64        `json:"typical_ms,omitempty"`
	Files      []StoredFile `json:"files,omitempty"`
	Warnings   []string     `json:"warnings,omitempty"`
	Error      string       `json:"error,omitempty"`
}

type groupWire struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	State string `json:"state"`
	Total int    `json:"total"`
	Done  int    `json:"done"`
	// Failed and Cancelled are separate counts: "12 of 40 made" and "12 of 40, 3 refused" are
	// different things to have happened.
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled,omitempty"`
	Running   string `json:"running,omitempty"`
	Trial     bool   `json:"trial,omitempty"`
	// ETAMS is remaining × typical_ms for this (model, size, steps), plus the observed wake when
	// nothing is running yet and the box may be cold.
	ETAMS    int64  `json:"eta_ms,omitempty"`
	PausedAt string `json:"paused_at,omitempty"`
}

type jobsResponse struct {
	Jobs   []jobWire   `json:"jobs"`
	Groups []groupWire `json:"groups"`
	// Paused is the queue-wide pause, not a group's.
	Paused       bool `json:"paused,omitempty"`
	Queued       int  `json:"queued"`
	QueueMax     int  `json:"queue_max"`
	TrialPending int  `json:"trial_pending"`
	TrialMax     int  `json:"trial_max"`
	// WakeMS is the last observed cold start, as an EMA. 0 means nothing has been measured yet,
	// which is a different statement from "the engine starts instantly".
	WakeMS int64 `json:"wake_ms,omitempty"`
}

// List is the whole answer. The order is running first, then the queue in the order it will run,
// then the finished newest-first — the reading order of the pane, and fixed, so that two polls
// of an unchanged queue produce identical bytes.
func (q *jobQueue) List() jobsResponse {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := jobsResponse{
		Paused: q.paused, Queued: len(q.pending),
		QueueMax: imagegenQueueMax, TrialPending: q.pendingTrialsLocked(), TrialMax: imagegenTrialMax,
		WakeMS: int64(q.wakeEMA),
		Jobs:   []jobWire{},
		Groups: []groupWire{},
	}
	runningIDs := make([]string, 0, len(q.running))
	for id := range q.running {
		runningIDs = append(runningIDs, id)
	}
	sort.Strings(runningIDs) // provider ids: a map range would reorder the answer every poll
	for _, id := range runningIDs {
		out.Jobs = append(out.Jobs, q.wireOf(q.running[id], 0))
	}
	for i, j := range q.pending {
		out.Jobs = append(out.Jobs, q.wireOf(j, i+1))
	}
	for i := len(q.finished) - 1; i >= 0; i-- {
		out.Jobs = append(out.Jobs, q.wireOf(q.finished[i], 0))
	}
	for i := len(q.groupOrder) - 1; i >= 0; i-- {
		g := q.groups[q.groupOrder[i]]
		if g == nil {
			continue
		}
		out.Groups = append(out.Groups, q.groupWireLocked(g))
	}
	return out
}

func (q *jobQueue) wireOf(j *jobRec, position int) jobWire {
	w := jobWire{
		ID: j.id, Group: j.group, Label: j.label, State: string(j.state), Position: position,
		Trial: j.trial, Provider: j.provider, Model: j.model, Family: j.family,
		Op: string(j.req.Op), Prompt: j.req.Prompt, Negative: j.req.NegativePrompt,
		Seed: j.req.Seed, Size: j.req.Size, Count: j.req.Count, Params: j.req.Params,
		Loras: j.req.Loras, Strength: j.req.Strength, Inputs: j.req.Inputs,
		FullSteps: j.fullSteps, OutDir: j.outRel,
		CreatedAt: j.created.UTC().Format(time.RFC3339),
		Files:     j.files, Warnings: j.warnings, Error: j.failure,
		TypicalMS: int64(q.typical[typicalKeyFine(j.provider, j.model, j.req.Size, stepsOf(j.req))]),
	}
	if !j.started.IsZero() {
		w.StartedAt = j.started.UTC().Format(time.RFC3339)
	}
	if j.state.finished() && !j.finished.IsZero() {
		w.FinishedAt = j.finished.UTC().Format(time.RFC3339)
		w.ElapsedMS = j.elapsed.Milliseconds()
	}
	return w
}

func (q *jobQueue) groupWireLocked(g *groupRec) groupWire {
	w := groupWire{ID: g.id, Label: g.label, Total: g.total, Trial: g.trial, State: GroupRunning}
	var remaining int
	var sample *jobRec
	for _, j := range q.byID {
		if j.group != g.id {
			continue
		}
		switch j.state {
		case JobDone:
			w.Done++
		case JobFailed:
			w.Failed++
		case JobCancelled:
			w.Cancelled++
		default:
			remaining++
		}
	}
	// The job the estimate is keyed to comes from the ORDERED pending list, never from a map
	// range: two reads of an unchanged queue must produce the same eta_ms or the ETag never
	// matches and the pane's poll costs a full response a second.
	for _, j := range q.pending {
		if j.group == g.id {
			sample = j
			break
		}
	}
	for _, j := range q.running {
		if j.group == g.id {
			w.Running = j.id
			sample = j
		}
	}
	switch {
	case g.aborted:
		w.State = GroupCancelled
	case remaining == 0:
		w.State = GroupDone
	case g.paused:
		w.State = GroupPaused
		w.PausedAt = g.pausedAt.UTC().Format(time.RFC3339)
	}
	if remaining > 0 && sample != nil {
		per := q.typical[typicalKeyFine(sample.provider, sample.model, sample.req.Size, stepsOf(sample.req))]
		if per > 0 {
			w.ETAMS = int64(per) * int64(remaining)
			if w.Running == "" {
				// Nothing is running: the box may have gone to sleep, and the first job of the
				// resumed batch pays the wake before it pays the sampling. Leaving it out is what
				// makes "about 9 minutes left" turn into fifteen without explanation.
				w.ETAMS += int64(q.wakeEMA)
			}
		}
	}
	return w
}
