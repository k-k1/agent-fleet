// tts_control.go — the on-demand controller for the VOICEVOX engine (ADR 0070 P1).
//
// The engine is an ECS service that costs about $0.12 an hour while it runs and nothing
// while it does not, so it is started when somebody wants to be read to and stopped once
// the room goes quiet. Three parts, deliberately separated:
//
//   - ttsDemand records *intent*: the characters of every synthesis request that would
//     have gone to the engine had it been up, never where the request actually went.
//     Counting the outcome flaps by construction — while the engine starts, every request
//     is served by Polly, so demand would read as zero for exactly the two minutes that
//     matter and the controller would stop what it just started.
//   - decideEngineAction is the whole judgement, as a pure function over one snapshot,
//     so the rules can be read and table-tested without AWS.
//   - ttsController is the shell around it: it polls the service, applies the decision,
//     warms a fresh engine before anyone is told it is ready, and audits every automatic
//     start and stop — a bill nobody can explain is a bill nobody trusts.
package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The three values of the tts_engine setting (ADR 0070 decision 7). The desired count is
// no longer the admin's intent — under on-demand it moves by itself — so the intent lives
// here and only here.
const (
	ttsModeOff      = "off"      // routing stops; the engine is stopped and stays stopped
	ttsModeOn       = "on"       // keep the engine running whatever the demand
	ttsModeOnDemand = "ondemand" // start on demand, stop after the idle window
)

// ttsDemandSetting carries the last demand timestamp (unix seconds) across a CP restart.
// In memory only, a restarted CP reads "no demand" and stops the engine out from under
// somebody who is listening.
const ttsDemandSetting = "tts_demand_at"

// ttsModeAtSetting is when tts_engine was last written (unix seconds). It exists for the
// undo window of decision 5: the stop that follows an explicit OFF is debounced, and the
// window has to survive the CP restart that would otherwise cancel it.
const ttsModeAtSetting = "tts_engine_at"

// ttsEngineMode reads the stored setting as a mode. "" is the deployment that has never
// touched the toggle: a managed engine defaults to on-demand (deploying 50-tts is the
// opt-in, and leaving a $90/month engine running because nobody picked a value is the
// outcome this ADR exists to avoid), an unmanaged one to on — its lifecycle belongs to
// whoever runs it, and all "on" means there is that routing is not switched off.
// Anything unrecognised is treated as on rather than off: a typo must not silence speech.
func ttsEngineMode(v string, managed bool) string {
	switch v {
	case ttsModeOff, ttsModeOn, ttsModeOnDemand:
		return v
	case "":
		if managed {
			return ttsModeOnDemand
		}
	}
	return ttsModeOn
}

// ttsDemandIntent reports whether one synthesis request wanted the engine, judged from the
// member's configured preference rather than from what answered it (decision 3). An
// explicit "voicevox" counts whatever the language, because that request does reach the
// engine; "polly" never counts; everything else counts unless the text is English, which
// is what routing would have sent to Polly anyway.
func ttsDemandIntent(pref, lang, mode string) bool {
	if mode == ttsModeOff {
		return false // routing is off: nothing would have reached the engine
	}
	switch pref {
	case "polly":
		return false
	case "voicevox":
		return true
	}
	return lang != "en"
}

// --- demand -------------------------------------------------------------------

// ttsDemandBucket is the resolution the rolling window is kept at. Per-request samples
// would be unbounded; 10-second buckets hold the 5-minute window in 30 entries.
const ttsDemandBucket = 10 * time.Second

// ttsDemandWriteEvery throttles the persisted timestamp to one write a minute (decision 6).
// The value is only ever compared against an idle window measured in minutes, so a minute
// of staleness changes no decision, and a synthesis request must not pay a DB write.
const ttsDemandWriteEvery = time.Minute

// ttsDemand is the intent counter: a rolling character window for the start trigger and a
// last-wanted timestamp for the idle window. Safe for concurrent use — every synthesis
// request touches it.
type ttsDemand struct {
	mu        sync.Mutex
	buckets   map[int64]int // unix second / bucket -> characters of intent
	last      time.Time     // most recent intent seen by this process
	persisted time.Time     // last value written to the store
	window    time.Duration
	settings  store.SettingsStore // nil = nothing to persist to (tests, unmanaged engines)
	now       func() time.Time    // test seam
}

func newTTSDemand(settings store.SettingsStore, window time.Duration) *ttsDemand {
	return &ttsDemand{buckets: map[int64]int{}, window: window, settings: settings, now: time.Now}
}

// record adds one request's characters to the window and refreshes the last-wanted mark.
// The store write is throttled and detached from the request's cancellation: a client that
// hangs up mid-sentence still wanted to be read to.
func (d *ttsDemand) record(ctx context.Context, chars int) {
	if d == nil || chars <= 0 {
		return
	}
	now := d.now()
	d.mu.Lock()
	d.buckets[now.UnixNano()/int64(ttsDemandBucket)] += chars
	d.last = now
	cutoff := now.Add(-d.window).UnixNano() / int64(ttsDemandBucket)
	for k := range d.buckets {
		if k < cutoff {
			delete(d.buckets, k)
		}
	}
	write := d.settings != nil && (d.persisted.IsZero() || now.Sub(d.persisted) >= ttsDemandWriteEvery)
	if write {
		d.persisted = now
	}
	d.mu.Unlock()
	if write {
		if err := d.settings.SetSetting(context.WithoutCancel(ctx), ttsDemandSetting, strconv.FormatInt(now.Unix(), 10)); err != nil {
			log.Printf("tts: recording demand failed: %v", err)
		}
	}
}

// chars is the characters of intent inside the rolling window.
func (d *ttsDemand) chars() int {
	if d == nil {
		return 0
	}
	now := d.now()
	cutoff := now.Add(-d.window).UnixNano() / int64(ttsDemandBucket)
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for k, n := range d.buckets {
		if k >= cutoff {
			total += n
		}
	}
	return total
}

// lastAt is the most recent intent this process has seen, merged with the stored value.
// The merge is what makes two CP replicas safe during a deployment: neither can stop the
// engine on the strength of its own short memory. A zero return means nobody has ever
// stamped it, which the decision function treats as "stamp only, judge nothing".
func (d *ttsDemand) lastAt(ctx context.Context) time.Time {
	if d == nil {
		return time.Time{}
	}
	d.mu.Lock()
	last := d.last
	d.mu.Unlock()
	if d.settings == nil {
		return last
	}
	v, _ := d.settings.GetSetting(ctx, ttsDemandSetting)
	if secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && secs > 0 {
		if stored := time.Unix(secs, 0); stored.After(last) {
			return stored
		}
	}
	return last
}

// stamp writes the current time as demand without counting characters. Used on the
// controller's first pass when nothing is stored yet: it must not judge on that pass, and
// leaving the mark unset would make every later pass the first one.
func (d *ttsDemand) stamp(ctx context.Context) {
	if d == nil {
		return
	}
	now := d.now()
	d.mu.Lock()
	if now.After(d.last) {
		d.last = now
	}
	d.persisted = now
	d.mu.Unlock()
	if d.settings == nil {
		return // nothing to persist to; the in-memory mark is all there is
	}
	if err := d.settings.SetSetting(ctx, ttsDemandSetting, strconv.FormatInt(now.Unix(), 10)); err != nil {
		log.Printf("tts: stamping demand failed: %v", err)
	}
}

// --- the decision -------------------------------------------------------------

// What the controller may do to the desired count, and why. The reason is not decoration:
// it is what lands in the audit ledger next to a charge, and "deadline" is the one the
// shell has to treat differently (it is a failure, and failures cool down).
const (
	ttsActionNone  = "none"
	ttsActionStart = "start"
	ttsActionStop  = "stop"

	ttsReasonNoService  = "no_service"     // INACTIVE or missing: nothing to drive
	ttsReasonOff        = "off"            // mode off, engine already stopped
	ttsReasonOffGrace   = "off_grace"      // mode off inside the undo window
	ttsReasonAdminOff   = "admin_off"      // mode off, undo window expired
	ttsReasonAdminOn    = "admin_on"       // mode on, engine not running
	ttsReasonOn         = "on"             // mode on, engine already running
	ttsReasonCooldown   = "cooldown"       // a start failed recently
	ttsReasonDemand     = "demand"         // the rolling window crossed the threshold
	ttsReasonBelow      = "below_start"    // not enough intent to be worth the money
	ttsReasonIdle       = "idle"           // nobody has wanted it for the idle window
	ttsReasonInUse      = "in_use"         // somebody wanted it inside the idle window
	ttsReasonDeadline   = "start_deadline" // desired 1 but never became running
	ttsReasonNoIdleStop = "idle_disabled"  // AF_TTS_ECS_IDLE_SEC=0, never stop
	ttsReasonFirstPass  = "first_pass"     // no demand mark yet: stamp, judge nothing
)

// ttsControlCfg is the controller's tuning, all of it from the environment.
type ttsControlCfg struct {
	interval   time.Duration // how often the controller looks (AF_TTS_ECS_CONTROL_INTERVAL, 0 = off)
	window     time.Duration // the rolling demand window (decision 4)
	startChars int           // characters of intent inside the window that buy a start
	idle       time.Duration // stop after this long without intent; 0 = never stop
	deadline   time.Duration // a start that has not become running by now has failed
	cooldown   time.Duration // base wait after a failed start, doubling per consecutive failure
	offGrace   time.Duration // the undo window on an explicit OFF (decision 5)
}

// ttsEngineSnapshot is everything the decision is allowed to look at.
type ttsEngineSnapshot struct {
	state       string    // running | starting | stopped | none
	desired     int32     // the service's desired count
	lastStart   time.Time // when the running deployment was created (DescribeServices)
	mode        string    // off | on | ondemand
	modeAt      time.Time // when the mode was last written; zero = long ago
	lastDemand  time.Time // most recent intent; zero = never stamped
	windowChars int       // characters of intent inside cfg.window
	failures    int       // consecutive failed starts
	lastFailure time.Time
}

// decideEngineAction is the whole of the controller's judgement (decisions 5 and 9).
//
// Invariants worth keeping when this is edited:
//   - the idle window is clamped to no less than the start deadline, or a window shorter
//     than a cold start stops the service while it is still starting and the next sentence
//     starts it again — a flap the intent rule cannot prevent;
//   - a stop for the mode being off waits out the undo window, because the UI lets somebody
//     press OFF and then ON again and the second press must not cost a 2 GB pull;
//   - with no demand mark stored, nothing is decided at all.
func decideEngineAction(now time.Time, s ttsEngineSnapshot, cfg ttsControlCfg) (action, reason string) {
	if s.state == "" || s.state == "none" {
		return ttsActionNone, ttsReasonNoService
	}
	up := s.desired >= 1

	if s.mode == ttsModeOff {
		if !up {
			return ttsActionNone, ttsReasonOff
		}
		// The undo window is a debounce of the desired count, not a delay of the stop:
		// return to ON inside it and the count never moved, so the restart costs nothing.
		// It is a minute rather than the idle window's thirty because while the mode is
		// off routing sends everything to Polly anyway — an engine kept alive during the
		// grace is of no use to anybody, it is only cheaper to keep than to buy again.
		if cfg.offGrace > 0 && !s.modeAt.IsZero() && now.Sub(s.modeAt) < cfg.offGrace {
			return ttsActionNone, ttsReasonOffGrace
		}
		return ttsActionStop, ttsReasonAdminOff
	}

	if s.mode == ttsModeOn {
		if up {
			return ttsActionNone, ttsReasonOn
		}
		if ttsCooldownLeft(now, s, cfg) > 0 {
			return ttsActionNone, ttsReasonCooldown
		}
		return ttsActionStart, ttsReasonAdminOn
	}

	// ondemand
	if up {
		// A service that cannot place its task sits at desired 1 forever and eventually
		// starts an engine nobody is waiting for. Give up instead, and let the cooldown
		// keep the next sentence from buying another 2 GB pull straight away.
		if s.state == "starting" && !s.lastStart.IsZero() && cfg.deadline > 0 && now.Sub(s.lastStart) >= cfg.deadline {
			return ttsActionStop, ttsReasonDeadline
		}
		if cfg.idle <= 0 {
			return ttsActionNone, ttsReasonNoIdleStop
		}
		if s.lastDemand.IsZero() {
			return ttsActionNone, ttsReasonFirstPass
		}
		idle := cfg.idle
		if idle < cfg.deadline {
			idle = cfg.deadline
		}
		if now.Sub(s.lastDemand) >= idle {
			return ttsActionStop, ttsReasonIdle
		}
		return ttsActionNone, ttsReasonInUse
	}
	if ttsCooldownLeft(now, s, cfg) > 0 {
		return ttsActionNone, ttsReasonCooldown
	}
	if cfg.startChars > 0 && s.windowChars >= cfg.startChars {
		return ttsActionStart, ttsReasonDemand
	}
	return ttsActionNone, ttsReasonBelow
}

// ttsCooldownMaxDoublings caps the doubling at 16x the base (four hours at the default 15
// minutes). A failure that has repeated five times is not going to be fixed by waiting
// longer, and an unbounded cooldown would hide a recovered service for a day.
const ttsCooldownMaxDoublings = 4

// ttsCooldownLeft is how much of the post-failure cooldown is still to run.
func ttsCooldownLeft(now time.Time, s ttsEngineSnapshot, cfg ttsControlCfg) time.Duration {
	if s.failures <= 0 || s.lastFailure.IsZero() || cfg.cooldown <= 0 {
		return 0
	}
	d := cfg.cooldown * time.Duration(int64(1)<<min(s.failures-1, ttsCooldownMaxDoublings))
	if left := s.lastFailure.Add(d).Sub(now); left > 0 {
		return left
	}
	return 0
}

// --- the shell ----------------------------------------------------------------

// ttsWarmupText is the warm-up utterance (decision 16). /version answers 200 before any
// voice model is loaded — measured: the engine boots in under a second without
// --load_all_models and then pays 650 ms on the first audio_query — so the engine is not
// called ready until one synthesis has actually come back. Short, and never heard by
// anyone: the audio is discarded.
const ttsWarmupText = "こんにちは。"

// ttsAuditor is the audit ledger, narrowed to the one method the controller needs so a
// test can pass a recorder.
type ttsAuditor interface {
	InsertAudit(context.Context, store.AuditLog) error
}

// ttsController is the loop around decideEngineAction: read the service, decide, act,
// warm, audit.
type ttsController struct {
	eng      *ttsEngineECS
	vv       *voicevoxProvider
	demand   *ttsDemand
	settings store.SettingsStore
	audit    ttsAuditor
	cfg      ttsControlCfg
	now      func() time.Time // test seam

	mu          sync.Mutex
	warm        bool
	failures    int
	lastFailure time.Time
	prevState   string // the service state at the previous tick, for spotting a replacement
}

// ttsControlCfgFromEnv reads the tuning. The defaults are ADR 0070's: a 5-minute window,
// 2,000 characters (one answer read aloud, about three times the measured break-even
// against Polly neural), a 30-minute idle window, and a start deadline set from the
// measured cold start of 70-77 seconds with room for a slow pull.
func ttsControlCfgFromEnv() ttsControlCfg {
	return ttsControlCfg{
		interval:   time.Duration(runtime.EnvInt("AF_TTS_ECS_CONTROL_INTERVAL_SEC", 30)) * time.Second,
		window:     time.Duration(runtime.EnvInt("AF_TTS_ECS_WINDOW_SEC", 300)) * time.Second,
		startChars: runtime.EnvInt("AF_TTS_ECS_START_CHARS", 2000),
		idle:       time.Duration(runtime.EnvInt("AF_TTS_ECS_IDLE_SEC", 1800)) * time.Second,
		deadline:   time.Duration(runtime.EnvInt("AF_TTS_ECS_START_DEADLINE_SEC", 300)) * time.Second,
		cooldown:   time.Duration(runtime.EnvInt("AF_TTS_ECS_FAIL_COOLDOWN_SEC", 900)) * time.Second,
		offGrace:   time.Duration(runtime.EnvInt("AF_TTS_ECS_OFF_GRACE_SEC", 60)) * time.Second,
	}
}

func newTTSController(eng *ttsEngineECS, vv *voicevoxProvider, demand *ttsDemand, settings store.SettingsStore, audit ttsAuditor, cfg ttsControlCfg) *ttsController {
	c := &ttsController{eng: eng, vv: vv, demand: demand, settings: settings, audit: audit, cfg: cfg, now: time.Now}
	// Wired here rather than by the caller: an engine this controller starts and stops is
	// exactly the engine whose readiness has to wait for a warm-up, and a gate somebody
	// forgot to attach fails silently — it just reads ready too early, once, per start.
	vv.warmGate = c.warmed
	return c
}

// warmed reports whether the engine has answered a real synthesis since it last came up.
// It is the gate behind voicevoxProvider.Ready, so an engine that is RUNNING but has not
// loaded a model still reads as not ready and Polly keeps reading.
func (c *ttsController) warmed() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.warm
}

func (c *ttsController) setWarm(v bool) {
	c.mu.Lock()
	if c.warm != v {
		c.warm = v
		if v {
			log.Print("tts: engine warmed up (ready)")
		}
	}
	c.mu.Unlock()
}

// noteAdminAction clears the failure streak when a person takes over. A cooldown is there
// to stop an automatic retry loop, never to refuse an admin who pressed the button.
func (c *ttsController) noteAdminAction() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.failures, c.lastFailure = 0, time.Time{}
	c.mu.Unlock()
}

// run drives one tick per interval until ctx ends. While the engine is starting, or
// running but not yet warm, it looks more often: those are the seconds a listener spends
// hearing Polly, and the interval is otherwise sized for a quiet deployment.
func (c *ttsController) run(ctx context.Context) {
	if c.cfg.interval <= 0 {
		return
	}
	for {
		next := c.tick(ctx)
		t := time.NewTimer(next)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

const ttsControlBusyInterval = 5 * time.Second

// tick performs one round and returns how long to wait before the next one.
func (c *ttsController) tick(ctx context.Context) time.Duration {
	view, err := c.eng.view(ctx)
	if err != nil {
		log.Printf("tts: describing the engine service failed: %v", err)
		return c.cfg.interval
	}
	now := c.now()
	c.noteReplacement(ctx, view)
	mode := ttsEngineMode(c.setting(ctx, ttsEngineSetting), true)
	lastDemand := c.demand.lastAt(ctx)
	if lastDemand.IsZero() {
		// First pass with nothing stored: stamp and judge nothing. Anything else stops an
		// engine somebody may be listening to, on the strength of a value that was never
		// written down.
		c.demand.stamp(ctx)
		return c.cfg.interval
	}
	c.mu.Lock()
	snap := ttsEngineSnapshot{
		state: view.state, desired: view.desired, lastStart: view.lastStart,
		mode: mode, modeAt: c.settingTime(ctx, ttsModeAtSetting),
		lastDemand: lastDemand, windowChars: c.demand.chars(),
		failures: c.failures, lastFailure: c.lastFailure,
	}
	c.mu.Unlock()

	action, reason := decideEngineAction(now, snap, c.cfg)
	switch action {
	case ttsActionStart:
		c.apply(ctx, true, reason, "")
	case ttsActionStop:
		detail := ""
		if reason == ttsReasonDeadline {
			// The real reason a start failed ("no container instances met the placement
			// constraints", a pull failure) is only ever written into the service events.
			detail = strings.Join(view.events, " | ")
			c.mu.Lock()
			c.failures, c.lastFailure = c.failures+1, now
			c.mu.Unlock()
		}
		c.apply(ctx, false, reason, detail)
	}

	// Never re-warm what was just stopped: the view still says "running" for a moment
	// after the desired count reaches 0, and a warm gate left open there routes a listener
	// to a task that is draining — which surfaces as a 502 the client silently skips.
	if action != ttsActionStop {
		c.maintainWarm(ctx, view.state)
	}
	switch {
	case reason == ttsReasonOffGrace:
		// Come back when the undo window closes rather than at the next quiet tick, or an
		// OFF that nobody undid keeps paying for up to a whole interval past its grace.
		if left := snap.modeAt.Add(c.cfg.offGrace).Sub(now); left > 0 && left < c.cfg.interval {
			return left
		}
	case view.state == "starting", view.state == "running" && !c.warmed():
		// The seconds a listener spends hearing Polly instead of Zundamon: look often.
		return ttsControlBusyInterval
	}
	return c.cfg.interval
}

// noteReplacement records an engine that went away without being asked to. The controller
// only ever leaves `running` by writing desired 0, which shows up as `stopped`; a service
// that goes from running back to starting while desired is still 1 lost its task to
// something else — the OOM kill P0 measured, a health-check replacement, a Fargate
// interruption. Telling that apart from the controller's own starts and stops is what keeps
// the ledger readable: this is not a charge anybody decided to make.
//
// The transition is judged from this process's previous observation, so a CP that restarted
// in between says nothing rather than inventing an event.
func (c *ttsController) noteReplacement(ctx context.Context, view ttsServiceView) {
	c.mu.Lock()
	prev := c.prevState
	c.prevState = view.state
	c.mu.Unlock()
	if prev != "running" || view.state != "starting" || view.desired < 1 {
		return
	}
	detail := strings.TrimSpace(view.rollout + " " + strings.Join(view.events, " | "))
	log.Printf("tts: the engine task was replaced without being asked: %s", detail)
	if c.audit == nil {
		return
	}
	_ = c.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "tts-controller",
		Action: "tts.engine.replaced", Target: "restart", Detail: detail, At: store.NowTS(),
	})
}

// apply moves the desired count and records why. Every automatic movement is audited:
// this is the only place a charge for a Fargate task can be explained afterwards.
func (c *ttsController) apply(ctx context.Context, on bool, reason, detail string) {
	target := "stop"
	if on {
		target = "start"
	}
	if err := c.eng.setEnabled(ctx, on); err != nil {
		log.Printf("tts: %s failed (%s): %v", target, reason, err)
		// Count an UpdateService failure as a failed start too: without a cooldown the
		// next tick repeats it, and a permission or quota error repeats forever.
		if on {
			c.mu.Lock()
			c.failures, c.lastFailure = c.failures+1, c.now()
			c.mu.Unlock()
		}
		return
	}
	if on {
		c.mu.Lock()
		c.failures, c.lastFailure = 0, time.Time{}
		c.mu.Unlock()
	} else {
		c.setWarm(false)
	}
	log.Printf("tts: engine %s (%s) %s", target, reason, detail)
	if c.audit == nil {
		return
	}
	_ = c.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "tts-controller",
		Action: "tts.engine.auto", Target: target, Detail: strings.TrimSpace(reason + " " + detail),
		At: store.NowTS(),
	})
}

// maintainWarm keeps the readiness gate honest (decision 16). ECS RUNNING says the
// container started, nothing more; a task ECS replaced under us (the engine is OOM-killed
// by a single oversized request — measured, and its container health check stays HEALTHY
// throughout) comes back cold, so the gate drops the moment /version stops answering.
func (c *ttsController) maintainWarm(ctx context.Context, state string) {
	if state != "running" {
		c.setWarm(false)
		return
	}
	if !voicevoxReady(ctx, c.vv.base) {
		c.setWarm(false)
		return
	}
	if c.warmed() {
		return
	}
	if _, aerr := voicevoxSynthesize(ctx, c.vv.base, ttsWarmupText, "", 0, false); aerr != nil {
		log.Printf("tts: warm-up synthesis failed: %s", aerr.message)
		return
	}
	c.setWarm(true)
}

func (c *ttsController) setting(ctx context.Context, key string) string {
	if c.settings == nil {
		return ""
	}
	v, _ := c.settings.GetSetting(ctx, key)
	return v
}

// settingTime reads a unix-second setting. Unreadable or unset is the zero time, which
// every caller reads as "long ago" — the safe direction for the undo window, since a
// missing mark means the stop is not held up.
func (c *ttsController) settingTime(ctx context.Context, key string) time.Time {
	secs, err := strconv.ParseInt(strings.TrimSpace(c.setting(ctx, key)), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}
