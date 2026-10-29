package sessionx

// The successful creates, byte for byte: the response, the meta as it lands on disk, and the
// launch task as it reaches the CLI or the managed driver. createSession's last stages build the
// meta and arm the prompt, and those are pinned only here, since no refusal shows them.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const createSuccessGoldenPath = "testdata/create_success.golden"

// promptProbeHandle is a managed thread that records the turn it is sent.
type promptProbeHandle struct {
	agents.ThreadHandle
	sent    *[]agents.TurnInput
	sendErr error
}

func (h promptProbeHandle) Send(in agents.TurnInput) error {
	*h.sent = append(*h.sent, in)
	return h.sendErr
}

// promptProbeDriver opens promptProbeHandles.
type promptProbeDriver struct {
	agents.Driver
	h promptProbeHandle
}

func (d promptProbeDriver) Resume(session.Meta) (agents.ThreadHandle, error) { return d.h, nil }

type createSuccessCase struct {
	name    string
	setup   func(p *createProbe)
	body    func(p *createProbe) map[string]any
	booked  string
	managed string // the kind whose managed driver is replaced by the probe
	sendErr error  // the managed probe's Send answer
}

// The parts of a created session that move with the clock or the temp dir, and their stand-ins.
var successVolatile = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`), "$$TIME"},
	{regexp.MustCompile(` @\d{4}-\d{4}`), " @$$STAMP"},           // the default title's MMDD-HHMM
	{regexp.MustCompile(`\d{2}/\d{2} \d{2}:\d{2}`), "$$STARTED"}, // startedLabel
	{regexp.MustCompile(`wc_[0-9a-f]{32}`), "wc_$$ID"},           // hashes the dir
	{regexp.MustCompile(`fp_[0-9a-f]{64}`), "fp_$$KEY"},          // fingerprints the dir
}

func createSuccessCases() []createSuccessCase {
	in := func(extra map[string]any) func(p *createProbe) map[string]any {
		return func(p *createProbe) map[string]any {
			b := map[string]any{"kind": "claude", "dir": p.repo}
			for k, v := range extra {
				b[k] = v
			}
			return b
		}
	}
	return []createSuccessCase{
		{name: "tui_plain", body: in(nil)},
		{name: "tui_full_request", body: in(map[string]any{"initial_prompt": "do the thing", "title": "My task",
			"color": "teal", "model": "opus", "effort": "high", "mode": "plan", "skip_permissions": true})},
		{name: "tui_stop_after_turn_and_spend_cap", body: in(map[string]any{"initial_prompt": "go",
			"stop_after_turn": true, "spend_cap_usd": 2.345})},
		{name: "tui_report_to", body: in(map[string]any{"initial_prompt": "report back", "report_to": "conv1"})},
		{name: "tui_spawn", body: func(p *createProbe) map[string]any {
			return spawn("parent1", map[string]any{"dir": p.repo, "worktree": false, "initial_prompt": "child task"})
		}},
		{name: "tui_schedule", body: in(map[string]any{"initial_prompt": "nightly", "source": "schedule",
			"schedule_id": "s1", "schedule_slot": "2026-10-29T03:00"})},
		{name: "tui_ssm", body: func(p *createProbe) map[string]any {
			return map[string]any{"kind": "ssm", "title": "box", "ssm_target": "i-0123", "ssm_profile": "dev",
				"sso_region": "ap-northeast-1"}
		}},
		{name: "tui_shell_label", body: in(map[string]any{"kind": "shell", "title": "build"})},
		{name: "tui_subdir", setup: func(p *createProbe) {
			if err := os.MkdirAll(filepath.Join(p.repo, "sub"), 0o755); err != nil {
				p.t.Fatal(err)
			}
		}, body: in(map[string]any{"subdir": "sub"})},
		{name: "tui_worktree", body: in(map[string]any{"worktree": true, "new_branch": "wt-ok", "initial_prompt": "in a tree"})},
		{name: "tui_studio", setup: func(p *createProbe) { stubStudio(p, nil, nil) },
			body: in(map[string]any{"studio": probeStudio})},
		{name: "tui_durable_key", setup: func(p *createProbe) { reserveDurable(p, "k-ok") },
			body: in(map[string]any{"idempotency_key": "k-ok", "idempotency_durable": true})},
		{name: "move_target", booked: "mv1", body: in(map[string]any{"initial_prompt": "moved"})},
		{name: "managed_prompt_delivered", managed: "codex",
			body: in(map[string]any{"kind": "codex", "driver": "managed", "initial_prompt": "managed task"})},
		{name: "managed_prompt_failed", managed: "codex", sendErr: errors.New("probe: send"),
			body: in(map[string]any{"kind": "codex", "driver": "managed", "initial_prompt": "managed task"})},
		{name: "managed_report_to", managed: "codex",
			body: in(map[string]any{"kind": "codex", "driver": "managed", "initial_prompt": "report back", "report_to": "conv1"})},
		{name: "managed_only_default_driver", managed: "lcpp", body: in(map[string]any{"kind": "lcpp", "model": "qwen3-8b"})},
	}
}

// renderSuccess is one successful create as the golden holds it. The session name is random and
// the clock moves, so both are spelled out of the way; everything else is verbatim.
func (p *createProbe) renderSuccess(name string, out string) string {
	var meta session.Meta
	for _, m := range session.ListMetas() {
		if m.Name != "parent1" && m.Name != "kid" {
			meta = m
		}
	}
	norm := func(s string) string {
		if meta.Name != "" {
			s = strings.ReplaceAll(s, meta.Name, "$NAME")
		}
		s = strings.ReplaceAll(s, p.home, "$HOME")
		for _, v := range successVolatile {
			s = v.re.ReplaceAllString(s, v.with)
		}
		return s
	}
	mb, _ := json.Marshal(meta)
	return fmt.Sprintf("== %s\n%smeta %s\n", name, norm(out), norm(string(mb)))
}

func TestCreateSessionSuccessIsByteIdentical(t *testing.T) {
	var got strings.Builder
	for _, tc := range createSuccessCases() {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			launchTmuxFn = func(session.Meta, bool) error { return nil }
			delivered := make(chan string, 1)
			deliverInitialPromptFn = func(_, prompt string) { delivered <- prompt }
			var sent []agents.TurnInput
			if tc.managed != "" {
				prev, had := managedDrivers[tc.managed]
				managedDrivers[tc.managed] = promptProbeDriver{h: promptProbeHandle{sent: &sent, sendErr: tc.sendErr}}
				t.Cleanup(func() {
					if had {
						managedDrivers[tc.managed] = prev
					} else {
						delete(managedDrivers, tc.managed)
					}
				})
			}
			if tc.setup != nil {
				tc.setup(p)
			}
			body := tc.body(p)
			rec := p.post(body, tc.booked)
			if rec.Code >= 300 {
				t.Fatalf("= %d %s, want a created session", rec.Code, rec.Body)
			}
			var b strings.Builder
			res := rec.Result()
			fmt.Fprintf(&b, "status %d\n", res.StatusCode)
			for _, k := range []string{"Content-Type", createKeyHeader, createOutcomeHeader} {
				if v := res.Header.Get(k); v != "" {
					fmt.Fprintf(&b, "header %s: %s\n", k, v)
				}
			}
			fmt.Fprintf(&b, "body %q\n", rec.Body.String())
			// The tui path delivers asynchronously; the managed one sends before answering.
			if body["initial_prompt"] != nil && tc.managed == "" {
				select {
				case prompt := <-delivered:
					fmt.Fprintf(&b, "delivered %q\n", prompt)
				case <-time.After(5 * time.Second):
					t.Errorf("the launch task was never delivered")
				}
			}
			for _, in := range sent {
				fmt.Fprintf(&b, "sent %q origin=%+v instr=%q schedule=%+v\n", in.Prompt, in.Origin, in.Instr, in.Schedule)
			}
			if tc.managed == "" {
				// The slot of a spawn is handed over to the meta, never left held.
				if inflight := p.sideEffects().inflight; inflight != "map[]" {
					t.Errorf("spawn slots after a created session: %s", inflight)
				}
			}
			got.WriteString(p.renderSuccess(tc.name, b.String()))
		})
	}
	if *updateCreateGolden {
		if err := os.WriteFile(createSuccessGoldenPath, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(createSuccessGoldenPath)
	if err != nil {
		t.Fatalf("%v (record it with -update-create-golden on the unchanged handler)", err)
	}
	if got.String() != string(want) {
		t.Errorf("created sessions differ from %s:\n%s", createSuccessGoldenPath, lineDiff(string(want), got.String()))
	}
}

// eventDriver logs Resume and Send into the same event list as the tui seams, and fails Resume
// when told to.
type eventDriver struct {
	agents.Driver
	log       func(string)
	resumeErr error
}

func (d eventDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	d.log("launch")
	if d.resumeErr != nil {
		return nil, d.resumeErr
	}
	return eventHandle{log: d.log}, nil
}

type eventHandle struct {
	agents.ThreadHandle
	log func(string)
}

func (h eventHandle) Send(agents.TurnInput) error { h.log("deliver"); return nil }

// The last stages in order, on both launch paths: the studio is bound before the launch, the
// meta is published after it and before the launch task is delivered, and a failed launch hands
// the studio back.
func TestCreateBindsLaunchesPublishesThenDelivers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		fail    bool
		want    string
	}{
		{"tui", false, false, "bind,launch(no meta),publish,deliver(meta)"},
		{"tui launch fails", false, true, "bind,launch(no meta),unbind"},
		{"managed", true, false, "bind,launch(no meta),publish,deliver(meta)"},
		{"managed launch fails", true, true, "bind,launch(no meta),unbind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			var events []string
			var name string
			metaState := func() string {
				if _, ok := session.ReadMeta(name); ok {
					return "(meta)"
				}
				return "(no meta)"
			}
			log := func(e string) {
				if e == "launch" || e == "deliver" {
					e += metaState()
				}
				events = append(events, e)
			}
			prevBind := imagegen.BindStudioSession
			imagegen.BindStudioSession = func(studio, sess, previous string) (string, error) {
				if sess != "" {
					name = sess
					log("bind")
				} else {
					log("unbind")
				}
				return "", nil
			}
			t.Cleanup(func() { imagegen.BindStudioSession = prevBind })
			var launchErr error
			if tc.fail {
				launchErr = errors.New("probe: launch")
			}
			launchTmuxFn = func(session.Meta, bool) error { log("launch"); return launchErr }
			done := make(chan struct{})
			deliverInitialPromptFn = func(string, string) { log("deliver"); close(done) }
			prevMeta := writeMoveTargetMeta
			t.Cleanup(func() { writeMoveTargetMeta = prevMeta })
			body := map[string]any{"kind": "claude", "dir": p.repo, "studio": probeStudio, "initial_prompt": "go"}
			if tc.managed {
				body["kind"], body["driver"] = "codex", "managed"
				prev, had := managedDrivers["codex"]
				managedDrivers["codex"] = eventDriver{log: log, resumeErr: launchErr}
				t.Cleanup(func() {
					if had {
						managedDrivers["codex"] = prev
					} else {
						delete(managedDrivers, "codex")
					}
				})
			}
			// The publish is observed through the booked-name path, the one write that has a seam.
			booked := "evt1"
			writeMoveTargetMeta = func(m session.Meta) error { log("publish"); return session.WriteMetaChecked(m) }
			p.post(body, booked)
			if !tc.fail && !tc.managed {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("the launch task was never delivered")
				}
			}
			if got := strings.Join(events, ","); got != tc.want {
				t.Errorf("events %s, want %s", got, tc.want)
			}
		})
	}
}
