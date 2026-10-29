package sessionx

// createSession is being cut into named stages (issue #2930, ADR 0124 Phase 4a). These tests pin
// what the cut must not move: every early return's status, headers and body, byte for byte, and
// the ordering rules the handler's comments spell out. They drive the handler itself, so they
// hold whatever the stages end up being called.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var updateCreateGolden = flag.Bool("update-create-golden", false, "rewrite testdata/create_refusals.golden")

const createGoldenPath = "testdata/create_refusals.golden"

// createProbe is one isolated create endpoint. Nothing in it launches: the tmux launch is a seam
// that fails, so the last early return (tmux_failed) is reachable and no pane is ever started.
type createProbe struct {
	t    *testing.T
	home string
	repo string // a git repository with branches main (checked out) and feature
}

func newCreateProbe(t *testing.T) *createProbe {
	t.Helper()
	isolateAgentState(t)
	useCreateOutcomeDir(t)
	prevLaunch, prevDeliver, prevAlive := launchTmuxFn, deliverInitialPromptFn, sessionAliveFn
	prevStart, prevLcpp := KindStartRefusal, LcppLiveModels
	launchTmuxFn = func(session.Meta, bool) error { return errors.New("probe: launch refused") }
	deliverInitialPromptFn = func(string, string) {}
	sessionAliveFn = func(session.Meta) bool { return false }
	KindStartRefusal, LcppLiveModels = nil, nil
	t.Cleanup(func() {
		launchTmuxFn, deliverInitialPromptFn, sessionAliveFn = prevLaunch, prevDeliver, prevAlive
		KindStartRefusal, LcppLiveModels = prevStart, prevLcpp
	})
	p := &createProbe{t: t, home: homeDir()}
	p.repo = filepath.Join(p.home, "repos", "app")
	gitInit(t, p.repo)
	p.meta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	p.meta(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: p.repo,
		Origin: session.OriginSession, OriginSession: "parent1"})
	return p
}

func (p *createProbe) meta(m session.Meta) {
	if m.CreatedAt == "" {
		m.CreatedAt = "2026-10-29T10:00:00+09:00"
	}
	session.WriteMeta(m)
}

// post sends one create through the real handler. name, when set, is the booked name a move's
// in-process create carries (CreateReq.name), which no HTTP body can set.
func (p *createProbe) post(body map[string]any, name string) *httptest.ResponseRecorder {
	p.t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(b))
	if name == "" {
		HandleCreateSession(rec, r)
		return rec
	}
	var req CreateReq
	if err := json.Unmarshal(b, &req); err != nil {
		p.t.Fatal(err)
	}
	req.name = name
	createSession(rec, r, req)
	return rec
}

// sideEffects is what a refused create must leave as it found it.
type sideEffects struct {
	metas     []string
	repos     []string
	worktrees string
	inflight  string
}

func (p *createProbe) sideEffects() sideEffects {
	p.t.Helper()
	var s sideEffects
	for _, m := range session.ListMetas() {
		s.metas = append(s.metas, m.Name)
	}
	sort.Strings(s.metas)
	ents, _ := os.ReadDir(filepath.Join(p.home, "repos"))
	for _, e := range ents {
		s.repos = append(s.repos, e.Name())
	}
	out, err := exec.Command("git", "-C", p.repo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		p.t.Fatalf("git worktree list: %v: %s", err, out)
	}
	s.worktrees = string(out)
	spawnInflight.mu.Lock()
	s.inflight = fmt.Sprint(spawnInflight.n)
	spawnInflight.mu.Unlock()
	return s
}

func (s sideEffects) diff(o sideEffects) string {
	var d []string
	if fmt.Sprint(s.metas) != fmt.Sprint(o.metas) {
		d = append(d, fmt.Sprintf("metas %v -> %v", s.metas, o.metas))
	}
	if fmt.Sprint(s.repos) != fmt.Sprint(o.repos) {
		d = append(d, fmt.Sprintf("repos %v -> %v", s.repos, o.repos))
	}
	if s.worktrees != o.worktrees {
		d = append(d, fmt.Sprintf("worktrees %q -> %q", s.worktrees, o.worktrees))
	}
	if s.inflight != o.inflight {
		d = append(d, fmt.Sprintf("spawn slots %s -> %s", s.inflight, o.inflight))
	}
	return strings.Join(d, "; ")
}

// render is a response as the golden file holds it. The isolated home differs per run, so it
// is spelled $HOME; everything else is verbatim. Headers come from Result(), the snapshot taken
// at WriteHeader: rec.Header() stays writable afterwards and would show a header set too late
// to reach a real client.
func (p *createProbe) render(name string, rec *httptest.ResponseRecorder) string {
	res := rec.Result()
	var b strings.Builder
	fmt.Fprintf(&b, "== %s\nstatus %d\n", name, res.StatusCode)
	keys := make([]string, 0, len(res.Header))
	for k := range res.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range res.Header[k] {
			fmt.Fprintf(&b, "header %s: %s\n", k, strings.ReplaceAll(v, p.home, "$HOME"))
		}
	}
	body, _ := io.ReadAll(res.Body)
	fmt.Fprintf(&b, "body %q\n", strings.ReplaceAll(string(body), p.home, "$HOME"))
	return b.String()
}

// createRefusalCase is one early return of createSession.
type createRefusalCase struct {
	name   string
	setup  func(p *createProbe) // optional: plant state the refusal answers from
	body   func(p *createProbe) map[string]any
	booked string // CreateReq.name (a move's in-process create)
	code   string // the error code the body must carry; "" for a replay
}

// spawn is a create raised by session parent (ADR 0073).
func spawn(parent string, extra map[string]any) map[string]any {
	b := map[string]any{"kind": "claude", "origin": "session", "origin_session": parent}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func reserveDurable(p *createProbe, key string) {
	if err := createLedger.reserve(key); err != nil {
		p.t.Fatal(err)
	}
}

func createRefusalCases() []createRefusalCase {
	inRepo := func(extra map[string]any) func(p *createProbe) map[string]any {
		return func(p *createProbe) map[string]any {
			b := map[string]any{"kind": "claude", "dir": p.repo}
			for k, v := range extra {
				b[k] = v
			}
			return b
		}
	}
	spawnInRepo := func(parent string, extra map[string]any) func(p *createProbe) map[string]any {
		return func(p *createProbe) map[string]any {
			b := spawn(parent, map[string]any{"dir": p.repo})
			for k, v := range extra {
				b[k] = v
			}
			return b
		}
	}
	done := func(key string) func(p *createProbe) {
		return func(p *createProbe) {
			if _, dup := createLedger.begin(key, false); dup {
				p.t.Fatal("begin")
			}
			createLedger.complete(key, []byte(`{"name":"already"}`))
		}
	}
	return []createRefusalCase{
		// Stage 1: kind validation.
		{name: "bad_kind", body: inRepo(map[string]any{"kind": "Codex"}), code: "bad_kind"},
		{name: "bad_kind_under_done_key", setup: done("k-done"),
			body: inRepo(map[string]any{"kind": "Codex", "idempotency_key": "k-done"}), code: "bad_kind"},
		{name: "bad_kind_under_reserved_durable_key", setup: func(p *createProbe) { reserveDurable(p, "k-dur") },
			body: inRepo(map[string]any{"kind": "Codex", "idempotency_key": "k-dur", "idempotency_durable": true}), code: "bad_kind"},

		// Stage 2: the idempotency claim.
		{name: "replay_done", setup: done("k-done"), body: inRepo(map[string]any{"idempotency_key": "k-done"})},
		{name: "in_progress", setup: func(p *createProbe) { createLedger.begin("k-run", false) },
			body: inRepo(map[string]any{"idempotency_key": "k-run"}), code: "create_in_progress"},
		{name: "abandoned", setup: func(p *createProbe) { reserveDurable(p, "k-ab"); createLedger.abandon("k-ab") },
			body: inRepo(map[string]any{"idempotency_key": "k-ab", "idempotency_durable": true}), code: "create_abandoned"},
		{name: "refused_durable", setup: func(p *createProbe) { createLedger.begin("k-ref", true); createLedger.fail("k-ref") },
			body: inRepo(map[string]any{"idempotency_key": "k-ref", "idempotency_durable": true}), code: "create_refused"},
		{name: "outcome_unknown", setup: func(p *createProbe) {
			if err := writeCreateOutcome("k-unk", createOutcomeRecord{State: "bogus"}); err != nil {
				p.t.Fatal(err)
			}
		}, body: inRepo(map[string]any{"idempotency_key": "k-unk", "idempotency_durable": true}), code: "create_outcome_unknown"},
		// A key derived from report_to, not sent: the same claim.
		{name: "in_progress_fingerprint", setup: func(p *createProbe) {
			createLedger.begin(createIdempotencyKey(&CreateReq{Kind: "claude", Dir: p.repo, ReportTo: "conv1"}), false)
		}, body: inRepo(map[string]any{"report_to": "conv1"}), code: "create_in_progress"},

		// Stage 3: refusals that answer from changing state, after the claim.
		{name: "start_refused", setup: func(p *createProbe) { refuseKind(p.t, "codex") },
			body: inRepo(map[string]any{"kind": "codex"}), code: "cli_install_no_space"},
		{name: "start_refused_durable", setup: func(p *createProbe) { refuseKind(p.t, "codex"); reserveDurable(p, "k-sd") },
			body: inRepo(map[string]any{"kind": "codex", "idempotency_key": "k-sd", "idempotency_durable": true}), code: "cli_install_no_space"},
		{name: "start_refused_retry_of_done", setup: func(p *createProbe) { refuseKind(p.t, "codex"); done("k-done")(p) },
			body: inRepo(map[string]any{"kind": "codex", "idempotency_key": "k-done"})},
		{name: "spawn_depth", body: spawnInRepo("kid", nil), code: "spawn_depth"},
		{name: "spawn_depth_retry_of_done", setup: done("k-done"),
			body: spawnInRepo("kid", map[string]any{"idempotency_key": "k-done"})},
		{name: "spawn_depth_retry_in_progress", setup: func(p *createProbe) { createLedger.begin("k-run", false) },
			body: spawnInRepo("kid", map[string]any{"idempotency_key": "k-run"}), code: "create_in_progress"},
		{name: "spawn_unknown_parent", body: spawnInRepo("ghost", nil), code: "spawn_unknown_parent"},
		{name: "spawn_kind_refused", body: spawnInRepo("parent1", map[string]any{"kind": "shell"}), code: "spawn_kind_refused"},
		{name: "spawn_budget", setup: func(p *createProbe) {
			p.meta(session.Meta{Name: "full", Kind: session.KindClaude, Origin: session.OriginUser})
			for i := 0; i < session.SpawnChildLimit(); i++ {
				p.meta(session.Meta{Name: fmt.Sprintf("fullkid%d", i), Kind: session.KindClaude, Dir: p.repo,
					Origin: session.OriginSession, OriginSession: "full"})
			}
		}, body: spawnInRepo("full", nil), code: "spawn_budget"},

		// Stage 4: request and driver validation.
		{name: "bad_title", body: inRepo(map[string]any{"title": "a\x01b"}), code: "bad_title"},
		{name: "bad_mode", body: inRepo(map[string]any{"mode": "fast"}), code: "bad_mode"},
		{name: "bad_spend_cap", body: inRepo(map[string]any{"spend_cap_usd": -1}), code: "bad_spend_cap"},
		{name: "permission_choice_unsupported", body: inRepo(map[string]any{"kind": "shell", "skip_permissions": false}),
			code: "permission_choice_unsupported"},
		{name: "driver_unsupported", body: inRepo(map[string]any{"driver": "managed"}), code: "driver_unsupported"},
		{name: "bad_driver", body: inRepo(map[string]any{"driver": "warp"}), code: "bad_driver"},

		// The order between and inside stages 3 and 4: with two refusals due, the earlier one
		// answers. A spawn refused in stage 4 has already reserved its slot, and the side-effect
		// check sees it handed back.
		{name: "order_start_before_spawn", setup: func(p *createProbe) { refuseKind(p.t, "codex") },
			body: spawnInRepo("kid", map[string]any{"kind": "codex"}), code: "cli_install_no_space"},
		{name: "order_spawn_depth_before_kind", body: spawnInRepo("kid", map[string]any{"kind": "shell"}), code: "spawn_depth"},
		{name: "order_spawn_before_title", body: spawnInRepo("kid", map[string]any{"title": "a\x01b"}), code: "spawn_depth"},
		{name: "order_title_before_mode", body: inRepo(map[string]any{"title": "a\x01b", "mode": "fast"}), code: "bad_title"},
		{name: "order_mode_before_spend_cap", body: inRepo(map[string]any{"mode": "fast", "spend_cap_usd": -1}), code: "bad_mode"},
		{name: "order_spend_cap_before_permission", body: inRepo(map[string]any{"kind": "shell", "spend_cap_usd": -1,
			"skip_permissions": false}), code: "bad_spend_cap"},
		{name: "order_permission_before_driver", body: inRepo(map[string]any{"kind": "shell", "skip_permissions": false,
			"driver": "warp"}), code: "permission_choice_unsupported"},
		{name: "order_driver_before_model", body: inRepo(map[string]any{"kind": "lcpp", "driver": "warp"}), code: "bad_driver"},
		{name: "spawn_slot_released_on_bad_title", body: spawnInRepo("parent1", map[string]any{"title": "a\x01b"}), code: "bad_title"},
		{name: "spawn_slot_released_on_bad_driver", body: spawnInRepo("parent1", map[string]any{"driver": "warp"}), code: "bad_driver"},

		// Stage 5: model checks.
		{name: "lcpp_disabled", setup: func(p *createProbe) { writeLcppEnabledPref(p.t, false) },
			body: inRepo(map[string]any{"kind": "lcpp", "model": "m1"}), code: "lcpp_disabled"},
		{name: "lcpp_no_model", body: inRepo(map[string]any{"kind": "lcpp"}), code: "bad_model"},
		{name: "model_hidden", setup: func(p *createProbe) { writeHiddenModelsPref(p.t, `{"claude":["opus"]}`) },
			body: inRepo(map[string]any{"model": "opus"}), code: "model_hidden"},
		{name: "lcpp_model_not_in_catalog", setup: func(p *createProbe) {
			LcppLiveModels = func(context.Context) []agents.ModelChoice {
				return []agents.ModelChoice{{ID: "qwen3-8b", Label: "qwen3-8b"}}
			}
		}, body: inRepo(map[string]any{"kind": "lcpp", "model": "llama-zz"}), code: "bad_model"},

		// Stage 6: working copy and subdir.
		{name: "worktree_without_dir", body: inRepo(map[string]any{"dir": "", "worktree": true}), code: "bad_dir"},
		{name: "worktree_branch_in_use", setup: func(p *createProbe) {
			runIntegrationGit(p.t, p.repo, "worktree", "add", filepath.Join(p.home, "repos", "app@feature"), "feature")
		}, body: inRepo(map[string]any{"worktree": true, "use_existing": true, "branch": "feature"}),
			code: "sessionx-stub:ErrCodeBranchInUse"}, // the package tests' gitx stub spells the code
		{name: "worktree_branch_exists", body: inRepo(map[string]any{"worktree": true, "new_branch": "feature"}),
			code: "branch_exists"},
		{name: "clone_failed", body: inRepo(map[string]any{"dir": "", "remote_url": "-x"}), code: "clone_failed"},
		{name: "dir_missing", body: func(p *createProbe) map[string]any {
			return map[string]any{"kind": "claude", "dir": filepath.Join(p.home, "absent")}
		}, code: "bad_dir"},
		{name: "spawn_working_copy_busy", setup: func(p *createProbe) {
			p.meta(session.Meta{Name: "busy", Kind: session.KindShell, Dir: p.repo, Origin: session.OriginUser})
			sessionAliveFn = func(m session.Meta) bool { return m.Name == "busy" }
		}, body: spawnInRepo("parent1", nil), code: "spawn_working_copy_busy"},
		// An empty dir is home by the time the check runs.
		{name: "spawn_working_copy_busy_home", setup: func(p *createProbe) {
			p.meta(session.Meta{Name: "athome", Kind: session.KindShell, Dir: p.home, Origin: session.OriginUser})
			sessionAliveFn = func(m session.Meta) bool { return m.Name == "athome" }
		}, body: func(p *createProbe) map[string]any { return spawn("parent1", nil) }, code: "spawn_working_copy_busy"},
		{name: "subdir_outside", body: inRepo(map[string]any{"subdir": "../x"}), code: "bad_subdir"},
		{name: "subdir_missing", body: inRepo(map[string]any{"subdir": "nope"}), code: "bad_subdir"},

		// Stage 7: identity and title.
		{name: "name_taken", booked: "kid", body: inRepo(nil), code: "name_taken"},
		{name: "ssm_without_target", body: inRepo(map[string]any{"kind": "ssm"}), code: "bad_ssm"},
		{name: "studio_from_spawn", body: spawnInRepo("parent1", map[string]any{"studio": "st1"}), code: "bad_studio"},
		{name: "studio_bad_id", body: inRepo(map[string]any{"studio": "a/b"}), code: "bad_studio"},

		// Stages 8-9: prompt arming, publish and launch.
		{name: "launch_failed", body: inRepo(map[string]any{"initial_prompt": "hi"}), code: "tmux_failed"},
		{name: "studio_unbindable", body: inRepo(map[string]any{"studio": "0123abcd-0000-4000-8000-00000000abcd"}), code: "studio_unavailable"},
		{name: "managed_launch_failed", setup: func(p *createProbe) {
			useLockingDriver(p.t, session.KindCodex, &lockingDriver{resumeErr: errors.New("probe: runtime down")})
		}, body: inRepo(map[string]any{"kind": "codex", "driver": "managed", "initial_prompt": "hi"})},
		{name: "launch_failed_durable", setup: func(p *createProbe) { reserveDurable(p, "k-lf") },
			body: inRepo(map[string]any{"idempotency_key": "k-lf", "idempotency_durable": true}), code: "tmux_failed"},
	}
}

// writeHiddenModelsPref writes ui-prefs hiddenModels; call it after isolateAgentState.
func writeHiddenModelsPref(t *testing.T, hidden string) {
	t.Helper()
	path := filepath.Join(homeDir(), ".config", "agent-fleet", "ui-prefs.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hiddenModels":`+hidden+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every early return of createSession answers byte-identically to the golden file, which was
// recorded from the handler before it was cut into stages, and leaves no meta, clone, worktree
// or spawn slot behind. A response that moves is a behaviour change, not a refactor.
func TestCreateSessionEarlyReturnsAreByteIdentical(t *testing.T) {
	var got strings.Builder
	for _, tc := range createRefusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			if tc.setup != nil {
				tc.setup(p)
			}
			before := p.sideEffects()
			rec := p.post(tc.body(p), tc.booked)
			if tc.code != "" && !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
				t.Errorf("= %d %s, want code %s", rec.Code, rec.Body, tc.code)
			}
			if d := before.diff(p.sideEffects()); d != "" {
				t.Errorf("a refused create left side effects: %s", d)
			}
			got.WriteString(p.render(tc.name, rec))
		})
	}
	if *updateCreateGolden {
		if err := os.WriteFile(createGoldenPath, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(createGoldenPath)
	if err != nil {
		t.Fatalf("%v (record it with -update-create-golden on the unchanged handler)", err)
	}
	if got.String() != string(want) {
		t.Errorf("responses differ from %s:\n%s", createGoldenPath, lineDiff(string(want), got.String()))
	}
}

// lineDiff names the first differing lines, enough to find the case.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var out []string
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			out = append(out, fmt.Sprintf("line %d\n  want %s\n  got  %s", i+1, a, b))
			if len(out) == 5 {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// A refused kind is answered before the idempotency claim: a durable key reserved for the
// create is still only reserved afterwards (a claim would have turned it refused for good), and
// a key whose create already finished does not replay that session for a request it never made.
func TestCreateRefusedKindLeavesNoLedgerEntry(t *testing.T) {
	p := newCreateProbe(t)
	reserveDurable(p, "k-dur")
	durable := map[string]any{"kind": "Codex", "dir": p.repo, "idempotency_key": "k-dur", "idempotency_durable": true}
	if rec := p.post(durable, ""); rec.Code != http.StatusBadRequest || rec.Result().Header.Get(createOutcomeHeader) != "" {
		t.Fatalf("refused kind = %d %v, want 400 with no outcome header", rec.Code, rec.Result().Header)
	}
	if e, ok := createLedger.lookup("k-dur"); !ok || e.state != createReserved {
		t.Fatalf("the reserved key is %+v (found=%v) after a refused kind, want still reserved", e, ok)
	}
	// Control: a registered kind refused further down does claim the key, and the probe sees it.
	durable["kind"], durable["dir"] = "claude", filepath.Join(p.home, "absent")
	if rec := p.post(durable, ""); rec.Code != http.StatusBadRequest || rec.Result().Header.Get(createOutcomeHeader) != "refused" {
		t.Fatalf("control = %d %v, want 400 refused through the outcome writer", rec.Code, rec.Result().Header)
	}
	if e, _ := createLedger.lookup("k-dur"); e.state != createRefused {
		t.Fatalf("control: the key is %+v, want refused", e)
	}
}

// The spawn refusals run after the claim: the retry of a create that already made its child is
// replayed, not refused, and one still in flight is create_in_progress.
func TestSpawnRefusalsAnswerAfterTheClaim(t *testing.T) {
	p := newCreateProbe(t)
	if _, dup := createLedger.begin("k-done", false); dup {
		t.Fatal("begin")
	}
	createLedger.complete("k-done", []byte(`{"name":"already"}`))
	createLedger.begin("k-run", false)
	for key, want := range map[string]int{"k-done": http.StatusOK, "k-run": http.StatusConflict, "": http.StatusConflict} {
		body := spawn("kid", map[string]any{"dir": p.repo})
		if key != "" {
			body["idempotency_key"] = key
		}
		rec := p.post(body, "")
		refused := strings.Contains(rec.Body.String(), "spawn_depth")
		if rec.Code != want || refused != (key == "") {
			t.Errorf("key %q = %d %s, want %d (spawn_depth only without a claim to answer)", key, rec.Code, rec.Body, want)
		}
	}
}

// Driver, model and spawn validation all answer before the create's first side effect: a
// worktree launch that is refused leaves no worktree and no branch. The control shows the same
// launch, valid, does create one — so "nothing changed" is a probe that can see a change.
func TestCreateValidationRunsBeforeAnySideEffect(t *testing.T) {
	wt := func(extra map[string]any) func(p *createProbe) map[string]any {
		return func(p *createProbe) map[string]any {
			b := map[string]any{"kind": "claude", "dir": p.repo, "worktree": true, "new_branch": "probe-wt"}
			for k, v := range extra {
				b[k] = v
			}
			return b
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(p *createProbe)
		body  func(p *createProbe) map[string]any
		code  string
	}{
		{"spawn_depth", nil, wt(map[string]any{"origin": "session", "origin_session": "kid"}), "spawn_depth"},
		{"start_refused", func(p *createProbe) { refuseKind(p.t, "codex") }, wt(map[string]any{"kind": "codex"}), "cli_install_no_space"},
		{"bad_driver", nil, wt(map[string]any{"driver": "warp"}), "bad_driver"},
		{"driver_unsupported", nil, wt(map[string]any{"driver": "managed"}), "driver_unsupported"},
		{"lcpp_no_model", nil, wt(map[string]any{"kind": "lcpp"}), "bad_model"},
		{"model_hidden", func(p *createProbe) { writeHiddenModelsPref(p.t, `{"claude":["opus"]}`) },
			wt(map[string]any{"model": "opus"}), "model_hidden"},
		{"lcpp_model_not_in_catalog", func(p *createProbe) {
			LcppLiveModels = func(context.Context) []agents.ModelChoice { return []agents.ModelChoice{{ID: "qwen3-8b"}} }
		}, wt(map[string]any{"kind": "lcpp", "model": "llama-zz"}), "bad_model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			if tc.setup != nil {
				tc.setup(p)
			}
			before := p.sideEffects()
			rec := p.post(tc.body(p), "")
			if !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
				t.Fatalf("= %d %s, want %s", rec.Code, rec.Body, tc.code)
			}
			if d := before.diff(p.sideEffects()); d != "" {
				t.Errorf("refused after a side effect: %s", d)
			}
		})
	}
	t.Run("control", func(t *testing.T) {
		p := newCreateProbe(t)
		before := p.sideEffects()
		rec := p.post(wt(nil)(p), "")
		if !strings.Contains(rec.Body.String(), `"tmux_failed"`) {
			t.Fatalf("= %d %s, want the probe's launch failure", rec.Code, rec.Body)
		}
		if d := before.diff(p.sideEffects()); !strings.Contains(d, "worktrees") {
			t.Fatalf("a valid worktree launch left no worktree (%q): the probe cannot see a side effect", d)
		}
	})
}

// The working-copy check compares the RESOLVED dir: an empty dir is home and a relative one is
// joined onto home before it runs, so both find the session already working there.
func TestSpawnWorkingCopyCheckUsesTheResolvedDir(t *testing.T) {
	for _, dir := range []string{"", "repos/app"} {
		p := newCreateProbe(t)
		target := p.home
		if dir != "" {
			target = filepath.Join(p.home, dir)
		}
		p.meta(session.Meta{Name: "busy", Kind: session.KindShell, Dir: target, Origin: session.OriginUser})
		sessionAliveFn = func(m session.Meta) bool { return m.Name == "busy" }
		rec := p.post(spawn("parent1", map[string]any{"dir": dir}), "")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "spawn_working_copy_busy") {
			t.Errorf("dir %q = %d %s, want 409 spawn_working_copy_busy", dir, rec.Code, rec.Body)
		}
	}
}
