// tts_control.go — the on-demand controller for the VOICEVOX engine (ADR 0070 P1).
//
// The engine is an ECS service that costs about $0.12 an hour while it runs and nothing
// while it does not, so it is started when somebody wants to be read to and stopped once
// the room goes quiet. Three parts, deliberately separated:
//
//   - engineDemand records *intent*: the characters of every synthesis request that would
//     have gone to the engine had it been up, never where the request actually went.
//     Counting the outcome flaps by construction — while the VOICEVOX engine starts, every
//     request is served by Polly, so demand would read as zero for exactly the two minutes
//     that matter and the controller would stop what it just started. For an inference
//     engine intent and request are the same thing (there is no Polly standing in), which
//     is why the unit of demand is the caller's to define.
//   - decideEngineAction is the whole judgement, as a pure function over one snapshot,
//     so the rules can be read and table-tested without AWS.
//   - engineController is the shell around it: it polls the service, applies the decision,
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

// The three values an engine's mode setting takes (ADR 0070 decision 7). The desired count
// is no longer the admin's intent — under on-demand it moves by itself — so the intent
// lives here and only here.
const (
	engineModeOff      = "off"      // routing stops; the engine is stopped and stays stopped
	engineModeOn       = "on"       // keep the engine running whatever the demand
	engineModeOnDemand = "ondemand" // start on demand, stop after the idle window
)

// engineSettings names the three rows one engine keeps in the settings store. Values rather
// than constants because there is more than one engine now and each keeps its own; the
// VOICEVOX names are frozen at what ADR 0070 shipped, so no deployment has to migrate a
// setting to gain this.
//
//   - mode:     off | on | ondemand — the admin's intent (the desired count is not).
//   - modeAt:   when mode was last written (unix seconds). The undo window of decision 5
//     debounces the stop that follows an explicit OFF, and the window has to survive the CP
//     restart that would otherwise cancel it.
//   - demandAt: the last demand timestamp (unix seconds). Held in memory only, a restarted
//     CP reads "no demand" and stops an engine out from under somebody who is using it.
type engineSettings struct{ mode, modeAt, demandAt string }

// ttsEngineSettings is the VOICEVOX engine's row names. tts.go writes two of them directly
// (the admin toggle), so they are named in one place rather than spelled out twice.
func ttsEngineSettings() engineSettings {
	return engineSettings{mode: ttsEngineSetting, modeAt: "tts_engine_at", demandAt: "tts_demand_at"}
}

// engineMode reads a stored setting as a mode. "" is the deployment that has never touched
// the toggle: a managed engine defaults to on-demand (deploying the engine's stack is the
// opt-in, and leaving a $90/month — or $918/month — engine running because nobody picked a
// value is the outcome these ADRs exist to avoid), an unmanaged one to on: its lifecycle
// belongs to whoever runs it, and all "on" means there is that routing is not switched off.
// Anything unrecognised is treated as on rather than off — a typo must not silence a
// feature.
func engineMode(v string, managed bool) string {
	switch v {
	case engineModeOff, engineModeOn, engineModeOnDemand:
		return v
	case "":
		if managed {
			return engineModeOnDemand
		}
	}
	return engineModeOn
}

// ttsDemandIntent reports whether one synthesis request wanted the engine, judged from the
// member's configured preference rather than from what answered it (decision 3). An
// explicit "voicevox" counts whatever the language, because that request does reach the
// engine; "polly" never counts; everything else counts unless the text is English, which
// is what routing would have sent to Polly anyway.
func ttsDemandIntent(pref, lang, mode string) bool {
	if mode == engineModeOff {
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

// engineDemandBucket is the resolution the rolling window is kept at. Per-request samples
// would be unbounded; 10-second buckets hold the 5-minute window in 30 entries.
const engineDemandBucket = 10 * time.Second

// engineDemandWriteEvery throttles the persisted timestamp to one write a minute (decision 6).
// The value is only ever compared against an idle window measured in minutes, so a minute
// of staleness changes no decision, and a synthesis request must not pay a DB write.
const engineDemandWriteEvery = time.Minute

// engineDemand is the intent counter: a rolling character window for the start trigger and a
// last-wanted timestamp for the idle window. Safe for concurrent use — every synthesis
// request touches it.
type engineDemand struct {
	mu        sync.Mutex
	buckets   map[int64]int // unix second / bucket -> units of intent
	last      time.Time     // most recent intent seen by this process
	persisted time.Time     // last value written to the store
	window    time.Duration
	// key is the settings row the last-wanted mark is persisted under. Per engine, because
	// two engines sharing one row would each read the other's activity as their own.
	key      string
	settings store.SettingsStore // nil = nothing to persist to (tests, unmanaged engines)
	now      func() time.Time    // test seam
	// since is when THIS PROCESS started counting. The buckets are in memory and nothing
	// else, so a CP that was replaced two minutes ago honestly reports "0 requests in the
	// last 5 minutes" while somebody is mid-conversation with the engine. Only the mark is
	// persisted (decision 6), and widening the window would not help — the counts are simply
	// not there. Anything that displays units() has to display this next to it, or the panel
	// states a confident zero it has no basis for.
	since time.Time
}

func newEngineDemand(settings store.SettingsStore, key string, window time.Duration) *engineDemand {
	return &engineDemand{
		buckets: map[int64]int{}, window: window, key: key, settings: settings,
		now: time.Now, since: time.Now(),
	}
}

// record adds one request's units of demand to the window and refreshes the last-wanted
// mark. The unit is the caller's: characters for speech, one per request for inference.
// The store write is throttled and detached from the request's cancellation: a client that
// hangs up mid-sentence still wanted to be read to.
func (d *engineDemand) record(ctx context.Context, units int) {
	if d == nil || units <= 0 {
		return
	}
	now := d.now()
	d.mu.Lock()
	d.buckets[now.UnixNano()/int64(engineDemandBucket)] += units
	d.last = now
	cutoff := now.Add(-d.window).UnixNano() / int64(engineDemandBucket)
	for k := range d.buckets {
		if k < cutoff {
			delete(d.buckets, k)
		}
	}
	write := d.settings != nil && (d.persisted.IsZero() || now.Sub(d.persisted) >= engineDemandWriteEvery)
	if write {
		d.persisted = now
	}
	d.mu.Unlock()
	if write {
		if err := d.settings.SetSetting(context.WithoutCancel(ctx), d.key, strconv.FormatInt(now.Unix(), 10)); err != nil {
			log.Printf("engine: recording demand for %s failed: %v", d.key, err)
		}
	}
}

// countedFor is how much of the window this process can actually speak for: the whole window
// once it has been up that long, less than that right after a restart, and zero when there is
// no counter at all. A caller rendering units() must render this too — see `since`.
func (d *engineDemand) countedFor() time.Duration {
	if d == nil || d.since.IsZero() {
		return 0
	}
	if up := d.now().Sub(d.since); up < d.window {
		if up < 0 {
			return 0
		}
		return up
	}
	return d.window
}

// units is the demand inside the rolling window.
func (d *engineDemand) units() int {
	if d == nil {
		return 0
	}
	now := d.now()
	cutoff := now.Add(-d.window).UnixNano() / int64(engineDemandBucket)
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
func (d *engineDemand) lastAt(ctx context.Context) time.Time {
	if d == nil {
		return time.Time{}
	}
	d.mu.Lock()
	last := d.last
	d.mu.Unlock()
	if d.settings == nil {
		return last
	}
	v, _ := d.settings.GetSetting(ctx, d.key)
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
func (d *engineDemand) stamp(ctx context.Context) {
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
	if err := d.settings.SetSetting(ctx, d.key, strconv.FormatInt(now.Unix(), 10)); err != nil {
		log.Printf("engine: stamping demand for %s failed: %v", d.key, err)
	}
}

// --- the decision -------------------------------------------------------------

// What the controller may do to the desired count, and why. The reason is not decoration:
// it is what lands in the audit ledger next to a charge, and "deadline" is the one the
// shell has to treat differently (it is a failure, and failures cool down).
const (
	engineActionNone  = "none"
	engineActionStart = "start"
	engineActionStop  = "stop"

	engineReasonNoService  = "no_service"     // INACTIVE or missing: nothing to drive
	engineReasonOff        = "off"            // mode off, engine already stopped
	engineReasonOffGrace   = "off_grace"      // mode off inside the undo window
	engineReasonAdminOff   = "admin_off"      // mode off, undo window expired
	engineReasonAdminOn    = "admin_on"       // mode on, engine not running
	engineReasonOn         = "on"             // mode on, engine already running
	engineReasonCooldown   = "cooldown"       // a start failed recently
	engineReasonDemand     = "demand"         // the rolling window crossed the threshold
	engineReasonBelow      = "below_start"    // not enough intent to be worth the money
	engineReasonIdle       = "idle"           // nobody has wanted it for the idle window
	engineReasonInUse      = "in_use"         // somebody wanted it inside the idle window
	engineReasonDeadline   = "start_deadline" // desired 1 but never became running
	engineReasonNoIdleStop = "idle_disabled"  // the idle window is 0, so never stop
	engineReasonFirstPass  = "first_pass"     // no demand mark yet: stamp, judge nothing
	engineReasonDraining   = "draining"       // stopped, but the box has not gone yet
	engineReasonNoModel    = "no_model"       // the catalogue is empty: nothing to serve at any price
	engineReasonUnwarmed   = "unwarmed"       // RUNNING for longer than a start takes, and still not answering
)

// engineControlCfg is the controller's tuning, all of it from the environment.
type engineControlCfg struct {
	interval   time.Duration // how often the controller looks (0 = the controller does not run)
	window     time.Duration // the rolling demand window (decision 4)
	startUnits int           // units of intent inside the window that buy a start
	idle       time.Duration // stop after this long without intent; 0 = never stop
	deadline   time.Duration // a start that has not become running by now has failed
	cooldown   time.Duration // base wait after a failed start, doubling per consecutive failure
	offGrace   time.Duration // the undo window on an explicit OFF (decision 5)
}

// engineSnapshot is everything the decision is allowed to look at.
type engineSnapshot struct {
	state       string    // running | starting | draining | stopped | none
	desired     int32     // the service's desired count
	lastStart   time.Time // when the running deployment was created (DescribeServices)
	mode        string    // off | on | ondemand
	modeAt      time.Time // when the mode was last written; zero = long ago
	lastDemand  time.Time // most recent intent; zero = never stamped
	windowUnits int       // units of intent inside cfg.window
	failures    int       // consecutive failed starts
	lastFailure time.Time
	// noModels says the engine's catalogue holds nothing enabled (ADR 0072 decision 1(c)).
	// Stated in the negative so the zero value — every engine that has no catalogue at all,
	// VOICEVOX included — means "there is something to serve" and behaves exactly as before.
	noModels bool
	// warm is whether the engine has actually answered since it came up, and unwarmedSince is
	// the moment the clock on "it has not" started: the latest of the running deployment's
	// creation, this process's own start, and the last time the engine WAS warm.
	//
	// Both exist because of a failure measured on the dev deployment (ADR 0072's P0
	// measurements): a task that started before the Control Plane had published the active set
	// came up as the idle placeholder, reached RUNNING, and never warmed. `starting` is the
	// only state the start deadline covered, so nothing ever judged it — the box billed at
	// $1.26/hour and the panel said "preparing" for as long as anybody left it.
	//
	// The zero value means "do not judge", which is what every caller that does not track it
	// gets (the VOICEVOX controller, and every table case written before this).
	warm          bool
	unwarmedSince time.Time
}

// decideEngineAction is the whole of the controller's judgement (decisions 5 and 9).
//
// Invariants worth keeping when this is edited:
//   - the idle window is clamped to no less than the start deadline, or a window shorter
//     than a cold start stops the service while it is still starting and the next sentence
//     starts it again — a flap the intent rule cannot prevent;
//   - a stop for the mode being off waits out the undo window, because the UI lets somebody
//     press OFF and then ON again and the second press must not cost a 2 GB pull;
//   - with no demand mark stored, nothing is decided at all;
//   - `draining` (ADR 0071 decision 7) is a kind of stopped, NOT a kind of running. The
//     desired count is already 0, so there is nothing left to stop, and a start from here is
//     the cheap one — the box, its image layers and its model file are all still there.
//     Reading it as running instead would make the controller try to "stop" an engine that
//     is already stopping, once per tick, for the seven minutes AWS takes.
func decideEngineAction(now time.Time, s engineSnapshot, cfg engineControlCfg) (action, reason string) {
	if s.state == "" || s.state == "none" {
		return engineActionNone, engineReasonNoService
	}
	// The desired count, not the state: `draining` and `stopped` are both desired 0.
	up := s.desired >= 1

	if s.mode == engineModeOff {
		if !up {
			return engineActionNone, engineReasonOff
		}
		// The undo window is a debounce of the desired count, not a delay of the stop:
		// return to ON inside it and the count never moved, so the restart costs nothing.
		// It is a minute rather than the idle window's thirty because while the mode is
		// off routing sends everything to Polly anyway — an engine kept alive during the
		// grace is of no use to anybody, it is only cheaper to keep than to buy again.
		if cfg.offGrace > 0 && !s.modeAt.IsZero() && now.Sub(s.modeAt) < cfg.offGrace {
			return engineActionNone, engineReasonOffGrace
		}
		return engineActionStop, engineReasonAdminOff
	}

	// Nothing in the catalogue: there is no price at which this box is worth buying (ADR 0072
	// decision 1(c)). It is checked BELOW the off branch so an engine an administrator switched
	// off still audits as `admin_off` — the reason lands next to a charge and the two facts are
	// different — and ABOVE the mode branches because `on` otherwise starts the placeholder
	// container. That placeholder is the trap: it reaches RUNNING and never warms, and
	// `running && !warmed` is not a failure state, so the controller would re-examine it every
	// five seconds for ever while the deployment pays $1.26/hour for `sleep infinity`.
	//
	// Stopping (rather than merely not starting) is deliberate: an engine that was up when its
	// last model was disabled is the same waste, and the administrator who disabled it is the
	// one who asked for this.
	if s.noModels {
		if up {
			return engineActionStop, engineReasonNoModel
		}
		return engineActionNone, engineReasonNoModel
	}

	// RUNNING but never able to answer, for longer than a start is allowed to take. This is a
	// FAILED START that happens to have reached RUNNING first, and it is treated as one:
	// stopped, counted, and cooled down, so `mode=on` retries with a backoff instead of paying
	// for a wedged task for ever.
	//
	// The clock starts at the LATEST of the deployment's creation, this process's start and the
	// last time the engine was warm — never at "it is not warm right now". Judging on the
	// instant would stop a healthy engine over one failed probe, and judging from the
	// deployment alone would stop a healthy one on the first tick after a CP restart.
	if up && s.state == "running" && !s.warm && cfg.deadline > 0 &&
		!s.unwarmedSince.IsZero() && now.Sub(s.unwarmedSince) >= cfg.deadline {
		return engineActionStop, engineReasonUnwarmed
	}

	if s.mode == engineModeOn {
		if up {
			return engineActionNone, engineReasonOn
		}
		if engineCooldownLeft(now, s, cfg) > 0 {
			return engineActionNone, engineReasonCooldown
		}
		return engineActionStart, engineReasonAdminOn
	}

	// ondemand
	if up {
		// A service that cannot place its task sits at desired 1 forever and eventually
		// starts an engine nobody is waiting for. Give up instead, and let the cooldown
		// keep the next sentence from buying another 2 GB pull straight away.
		if s.state == "starting" && !s.lastStart.IsZero() && cfg.deadline > 0 && now.Sub(s.lastStart) >= cfg.deadline {
			return engineActionStop, engineReasonDeadline
		}
		if cfg.idle <= 0 {
			return engineActionNone, engineReasonNoIdleStop
		}
		if s.lastDemand.IsZero() {
			return engineActionNone, engineReasonFirstPass
		}
		// engineIdleWindow, not the raw cfg.idle: the admin panel counts down to the same
		// moment and the two must not drift (engine_uptime.go).
		if now.Sub(s.lastDemand) >= engineIdleWindow(cfg) {
			return engineActionStop, engineReasonIdle
		}
		return engineActionNone, engineReasonInUse
	}
	if engineCooldownLeft(now, s, cfg) > 0 {
		return engineActionNone, engineReasonCooldown
	}
	if cfg.startUnits > 0 && s.windowUnits >= cfg.startUnits {
		return engineActionStart, engineReasonDemand
	}
	if s.state == "draining" {
		// Not "below_start": the two look the same from the desired count and cost very
		// differently, and an operator reading the log needs to know a box was sitting there
		// unused when nobody asked for it.
		return engineActionNone, engineReasonDraining
	}
	return engineActionNone, engineReasonBelow
}

// engineCooldownMaxDoublings caps the doubling at 16x the base (four hours at the default 15
// minutes). A failure that has repeated five times is not going to be fixed by waiting
// longer, and an unbounded cooldown would hide a recovered service for a day.
const engineCooldownMaxDoublings = 4

// engineCooldownLeft is how much of the post-failure cooldown is still to run.
func engineCooldownLeft(now time.Time, s engineSnapshot, cfg engineControlCfg) time.Duration {
	if s.failures <= 0 || s.lastFailure.IsZero() || cfg.cooldown <= 0 {
		return 0
	}
	d := cfg.cooldown * time.Duration(int64(1)<<min(s.failures-1, engineCooldownMaxDoublings))
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

// engineAuditor is the audit ledger, narrowed to the one method the controller needs so a
// test can pass a recorder.
type engineAuditor interface {
	InsertAudit(context.Context, store.AuditLog) error
}

// engineController is the loop around decideEngineAction: read the service, decide, act,
// warm, audit. One per engine.
type engineController struct {
	eng  *engineECS
	keys engineSettings
	// warmup answers "has this engine actually served something since it came up". It is
	// separate from ECS RUNNING because RUNNING only says the container started: VOICEVOX
	// answers /version before any voice model is loaded, and llama-server binds its port
	// before the weights are in VRAM (the measured 267 seconds of a 527-second cold start).
	// nil = the engine has no such gap and is ready as soon as it is running.
	//
	// It is called on EVERY tick, warm or not — the point is also to notice the engine going
	// away — and is passed the current verdict so the expensive half (VOICEVOX's throwaway
	// synthesis) runs only on the way up.
	warmup func(ctx context.Context, alreadyWarm bool) bool
	// hasModels answers "is there anything this engine could serve" (ADR 0072). nil = the
	// engine has no catalogue — VOICEVOX, and any engine on a CP with no store — and is then
	// treated as having something, so nothing about those engines changes.
	hasModels func(ctx context.Context) bool
	// startGate is asked immediately before a start and can refuse it, returning the reason
	// (ADR 0074). It is not part of decideEngineAction because it is not a judgement over a
	// snapshot: it calls AWS — it re-applies the chosen instance class — and that function is
	// deliberately pure. nil = no gate, which is every engine without a GPU ladder.
	startGate func(ctx context.Context) (bool, string)
	demand    *engineDemand
	settings  store.SettingsStore
	audit     engineAuditor
	// uptime is where each tick's observation is recorded (engine_uptime.go). nil = not
	// recorded, which is what the VOICEVOX engine does: its panel has no heatmap, and an
	// INSERT every 30 seconds for a series nothing reads is a cost with no reader.
	uptime engineUptimeStore
	cfg    engineControlCfg
	now    func() time.Time // test seam

	mu          sync.Mutex
	warm        bool
	failures    int
	lastFailure time.Time
	prevState   string    // the service state at the previous tick, for spotting a replacement
	lastSample  time.Time // when this process last recorded an observation; zero = never
	// lastWarmAt is when this process last saw the engine answer, and watchSince is when it
	// started looking. Together they are what stops the "running but never warm" rule from
	// firing on a healthy engine right after a CP restart — see engineSnapshot.unwarmedSince.
	lastWarmAt time.Time
	watchSince time.Time
}

// ttsControlCfgFromEnv reads the tuning. The defaults are ADR 0070's: a 5-minute window,
// 2,000 characters (one answer read aloud, about three times the measured break-even
// against Polly neural), a 30-minute idle window, and a start deadline set from the
// measured cold start of 70-77 seconds with room for a slow pull.
func ttsControlCfgFromEnv() engineControlCfg {
	return engineControlCfg{
		interval:   time.Duration(runtime.EnvInt("AF_TTS_ECS_CONTROL_INTERVAL_SEC", 30)) * time.Second,
		window:     time.Duration(runtime.EnvInt("AF_TTS_ECS_WINDOW_SEC", 300)) * time.Second,
		startUnits: runtime.EnvInt("AF_TTS_ECS_START_CHARS", 2000),
		idle:       time.Duration(runtime.EnvInt("AF_TTS_ECS_IDLE_SEC", 1800)) * time.Second,
		deadline:   time.Duration(runtime.EnvInt("AF_TTS_ECS_START_DEADLINE_SEC", 300)) * time.Second,
		cooldown:   time.Duration(runtime.EnvInt("AF_TTS_ECS_FAIL_COOLDOWN_SEC", 900)) * time.Second,
		offGrace:   time.Duration(runtime.EnvInt("AF_TTS_ECS_OFF_GRACE_SEC", 60)) * time.Second,
	}
}

// newEngineController wires one engine's parts together. warmup may be nil.
func newEngineController(eng *engineECS, keys engineSettings, warmup func(context.Context, bool) bool,
	demand *engineDemand, settings store.SettingsStore, audit engineAuditor, cfg engineControlCfg) *engineController {
	return &engineController{
		eng: eng, keys: keys, warmup: warmup, demand: demand,
		settings: settings, audit: audit, cfg: cfg, now: time.Now,
		watchSince: time.Now(),
	}
}

// newTTSController is newEngineController for the VOICEVOX engine, plus the one wiring that
// must not be left to a caller: an engine this controller starts and stops is exactly the
// engine whose readiness has to wait for a warm-up, and a gate somebody forgot to attach
// fails silently — it just reads ready too early, once, per start.
//
// speakers may be nil. When it is not, the transition to warm is also where the character
// catalogue is captured (ADR 0070 decision 12): doing it only from the /speakers handler
// would mean a deployment where nobody opens the settings screen during the engine's half
// hour of life never stores one at all, and then the picker is empty for the rest of that
// deployment's life. The closure below runs the capture exactly where the ttsController's own
// maintainWarm used to — after a successful warm-up synthesis, i.e. once per start.
func newTTSController(eng *engineECS, vv *voicevoxProvider, demand *engineDemand, settings store.SettingsStore, audit engineAuditor, speakers *ttsSpeakerCache, cfg engineControlCfg) *engineController {
	c := newEngineController(eng, ttsEngineSettings(), func(ctx context.Context, alreadyWarm bool) bool {
		if !voicevoxReady(ctx, vv.base) {
			return false
		}
		if alreadyWarm {
			return true
		}
		if _, aerr := voicevoxSynthesize(ctx, vv.base, ttsWarmupText, "", 0, false); aerr != nil {
			log.Printf("tts: warm-up synthesis failed: %s", aerr.message)
			return false
		}
		if speakers != nil {
			if _, aerr := speakers.refresh(ctx, vv.base); aerr != nil {
				log.Printf("tts: refreshing the character catalogue failed: %s", aerr.message)
			}
		}
		return true
	}, demand, settings, audit, cfg)
	vv.warmGate = c.warmed
	return c
}

// warmed reports whether the engine has actually served something since it last came up.
// It is the gate behind voicevoxProvider.Ready and behind the /engine/* gateway's "is it up
// yet", so an engine that is RUNNING but has not loaded its model still reads as not ready.
func (c *engineController) warmed() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.warm
}

func (c *engineController) setWarm(v bool) {
	c.mu.Lock()
	if v {
		// Stamped on every warm observation, not only on the transition: it is the moment the
		// "has not answered for too long" clock restarts from.
		c.lastWarmAt = c.now()
	}
	if c.warm != v {
		c.warm = v
		if v {
			log.Printf("%s: warmed up (ready)", c.eng.logKey())
		}
	}
	c.mu.Unlock()
}

// noteAdminAction clears the failure streak when a person takes over. A cooldown is there
// to stop an automatic retry loop, never to refuse an admin who pressed the button.
func (c *engineController) noteAdminAction() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.failures, c.lastFailure = 0, time.Time{}
	c.mu.Unlock()
}

// run drives one tick per interval until ctx ends. While the engine is starting, or running
// but not yet warm, it looks more often: those are the seconds somebody spends waiting on a
// worse answer (Polly reading, or a held-open request), and the interval is otherwise sized
// for a quiet deployment.
func (c *engineController) run(ctx context.Context) {
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

const engineControlBusyInterval = 5 * time.Second

// tick performs one round and returns how long to wait before the next one.
func (c *engineController) tick(ctx context.Context) time.Duration {
	view, err := c.eng.view(ctx)
	if err != nil {
		log.Printf("%s: describing the engine service failed: %v", c.eng.logKey(), err)
		return c.cfg.interval
	}
	now := c.now()
	c.noteReplacement(ctx, view)
	// The RAW state, not engineDisplayState's: the heatmap records what the hardware did,
	// and the display state is a statement about the button that was just pressed.
	// Deliberately above the first-pass return below — that tick observed the engine just as
	// well as any other, it merely has nothing to decide.
	c.recordUptime(ctx, now, view.state)
	mode := engineMode(c.setting(ctx, c.keys.mode), true)
	lastDemand := c.demand.lastAt(ctx)
	if lastDemand.IsZero() {
		// First pass with nothing stored: stamp and judge nothing. Anything else stops an
		// engine somebody may be listening to, on the strength of a value that was never
		// written down.
		c.demand.stamp(ctx)
		return c.cfg.interval
	}
	// Outside the lock: this reads the catalogue (a cached database query), and c.mu guards
	// only the failure counters and the warm flag.
	noModels := c.hasModels != nil && !c.hasModels(ctx)
	c.mu.Lock()
	snap := engineSnapshot{
		state: view.state, desired: view.desired, lastStart: view.lastStart,
		mode: mode, modeAt: c.settingTime(ctx, c.keys.modeAt),
		lastDemand: lastDemand, windowUnits: c.demand.units(),
		failures: c.failures, lastFailure: c.lastFailure,
		noModels: noModels,
		warm:     c.warm,
	}
	snap.unwarmedSince = latestTime(view.lastStart, c.watchSince, c.lastWarmAt)
	c.mu.Unlock()

	action, reason := decideEngineAction(now, snap, c.cfg)
	// The gate is consulted after the decision, never inside it: what it does — waiting for a
	// box of the previous instance class to leave, and re-applying the class — is an act with
	// AWS in it, and decideEngineAction is a pure function over one snapshot (ADR 0074).
	if action == engineActionStart && c.startGate != nil {
		if ok, why := c.startGate(ctx); !ok {
			log.Printf("%s: holding the start back (%s after %s)", c.eng.logKey(), why, reason)
			// Come back soon: the thing being waited for is a box going away, which takes
			// minutes, and the person who pressed the button is watching.
			return engineControlBusyInterval
		}
	}
	switch action {
	case engineActionStart:
		c.apply(ctx, true, reason, "")
	case engineActionStop:
		detail := ""
		// Both of these ARE failed starts, and both have to cool down: without a cooldown
		// `mode=on` restarts the same wedged task on the next tick, for ever.
		if reason == engineReasonDeadline || reason == engineReasonUnwarmed {
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
	if action != engineActionStop {
		c.maintainWarm(ctx, view.state)
	}
	switch {
	case reason == engineReasonOffGrace:
		// Come back when the undo window closes rather than at the next quiet tick, or an
		// OFF that nobody undid keeps paying for up to a whole interval past its grace.
		if left := snap.modeAt.Add(c.cfg.offGrace).Sub(now); left > 0 && left < c.cfg.interval {
			return left
		}
	case view.state == "starting", view.state == "running" && !c.warmed():
		// The seconds somebody spends waiting for the engine they asked for: look often.
		return engineControlBusyInterval
	}
	return c.cfg.interval
}

// latestTime is the most recent of the times given, ignoring the zero ones.
func latestTime(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if !t.IsZero() && t.After(out) {
			out = t
		}
	}
	return out
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
func (c *engineController) noteReplacement(ctx context.Context, view engineServiceView) {
	c.mu.Lock()
	prev := c.prevState
	c.prevState = view.state
	c.mu.Unlock()
	if prev != "running" || view.state != "starting" || view.desired < 1 {
		return
	}
	detail := strings.TrimSpace(view.rollout + " " + strings.Join(view.events, " | "))
	log.Printf("%s: the engine task was replaced without being asked: %s", c.eng.logKey(), detail)
	if c.audit == nil {
		return
	}
	_ = c.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: c.actorID(),
		Action: c.auditAction("replaced"), Target: "restart", Detail: detail, At: store.NowTS(),
	})
}

// apply moves the desired count and records why. Every automatic movement is audited:
// this is the only place a charge for a Fargate task can be explained afterwards.
func (c *engineController) apply(ctx context.Context, on bool, reason, detail string) {
	target := "stop"
	if on {
		target = "start"
	}
	if err := c.eng.setEnabled(ctx, on); err != nil {
		log.Printf("%s: %s failed (%s): %v", c.eng.logKey(), target, reason, err)
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
	log.Printf("%s: %s (%s) %s", c.eng.logKey(), target, reason, detail)
	if c.audit == nil {
		return
	}
	_ = c.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: c.actorID(),
		Action: c.auditAction("auto"), Target: target, Detail: strings.TrimSpace(reason + " " + detail),
		At: store.NowTS(),
	})
}

// The ledger's names. VOICEVOX keeps the exact strings ADR 0070 shipped — an audit trail
// whose action name changes under a reader is worse than an inconsistent one — and every
// engine added since is `engine.<key>.<what>`.
func (c *engineController) actorID() string {
	if c.eng == nil || c.eng.key == "" || c.eng.key == "tts" {
		return "tts-controller"
	}
	return "engine-" + c.eng.key + "-controller"
}

func (c *engineController) auditAction(what string) string {
	if c.eng == nil || c.eng.key == "" || c.eng.key == "tts" {
		return "tts.engine." + what
	}
	return "engine." + c.eng.key + "." + what
}

// maintainWarm keeps the readiness gate honest (ADR 0070 decision 16). ECS RUNNING says the
// container started, nothing more; a task ECS replaced under us (the VOICEVOX engine is
// OOM-killed by a single oversized request — measured, and its container health check stays
// HEALTHY throughout) comes back cold, so the gate drops the moment the engine stops
// answering.
//
// An engine with no warmup hook is warm as soon as it is running, which is the honest
// answer for one whose readiness endpoint does not lie.
func (c *engineController) maintainWarm(ctx context.Context, state string) {
	if state != "running" {
		c.setWarm(false)
		return
	}
	if c.warmup == nil {
		c.setWarm(true)
		return
	}
	c.setWarm(c.warmup(ctx, c.warmed()))
}

func (c *engineController) setting(ctx context.Context, key string) string {
	if c.settings == nil {
		return ""
	}
	v, _ := c.settings.GetSetting(ctx, key)
	return v
}

// settingTime reads a unix-second setting. Unreadable or unset is the zero time, which
// every caller reads as "long ago" — the safe direction for the undo window, since a
// missing mark means the stop is not held up.
func (c *engineController) settingTime(ctx context.Context, key string) time.Time {
	secs, err := strconv.ParseInt(strings.TrimSpace(c.setting(ctx, key)), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}
