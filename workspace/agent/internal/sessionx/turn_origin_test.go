package sessionx

// Every place that builds a TurnInput sets its origin (ADR 0105 decision 1). The stop rules
// read nothing else to tell the member's own input from a peer's, an operator's or a
// schedule's, so an origin missing at one entry point is a stop that discards or keeps the
// wrong input, with nothing on screen to show why.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// originFakeHandle records every input it is given, through any of the three send verbs.
type originFakeHandle struct {
	agents.ThreadHandle
	got chan agents.TurnInput
}

func (h *originFakeHandle) Send(in agents.TurnInput) error  { h.got <- in; return nil }
func (h *originFakeHandle) Steer(in agents.TurnInput) error { h.got <- in; return nil }
func (h *originFakeHandle) SendQueued(in agents.TurnInput) (bool, error) {
	h.got <- in
	return false, nil
}

type originFakeDriver struct {
	agents.Driver
	h *originFakeHandle
}

func (d *originFakeDriver) Resume(session.Meta) (agents.ThreadHandle, error) { return d.h, nil }

// useOriginFake installs the fake as codex's driver and writes a Managed codex session.
func useOriginFake(t *testing.T, name string) (*originFakeHandle, session.Meta) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	h := &originFakeHandle{got: make(chan agents.TurnInput, 4)}
	prev, had := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = &originFakeDriver{h: h}
	t.Cleanup(func() {
		if had {
			managedDrivers[session.KindCodex] = prev
			return
		}
		delete(managedDrivers, session.KindCodex)
	})
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	return h, m
}

func (h *originFakeHandle) next(t *testing.T) agents.TurnInput {
	t.Helper()
	select {
	case in := <-h.got:
		return in
	default:
		t.Fatal("nothing reached the driver")
		return agents.TurnInput{}
	}
}

func TestInputSetsOriginFromTheBadge(t *testing.T) {
	cases := []struct {
		name string
		body string
		want agents.Origin
	}{
		{"unmarked", `{"prompt":"my own words"}`, agents.Origin{Kind: agents.OriginMember}},
		{"peer", `{"prompt":"PR is ready","peer_from":"origin_src","peer_intent":"request"}`,
			agents.Origin{Kind: agents.OriginPeer, From: "origin_src"}},
		{"operator", `{"prompt":"do the thing","report_to":"chan-1"}`, agents.Origin{Kind: agents.OriginOperator}},
		{"schedule", `{"prompt":"nightly","source":"schedule"}`, agents.Origin{Kind: agents.OriginSchedule}},
		{"schedule-manual", `{"prompt":"run now","source":"schedule-manual","report_to":"chan-1"}`,
			agents.Origin{Kind: agents.OriginScheduleManual}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := useOriginFake(t, "origin_dst")
			session.WriteMeta(session.Meta{Name: "origin_src", Dir: t.TempDir(), Kind: session.KindClaude})
			rec := postInput(t, "origin_dst", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := h.next(t).Origin; got != tc.want {
				t.Errorf("origin = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTurnStartAndSteerAreMemberInput(t *testing.T) {
	for _, op := range []string{"start", "steer"} {
		t.Run(op, func(t *testing.T) {
			h, _ := useOriginFake(t, "origin_turn")
			req := httptest.NewRequest(http.MethodPost, "/sessions/origin_turn/turn",
				strings.NewReader(`{"op":"`+op+`","prompt":"no, do X instead"}`))
			req.SetPathValue("name", "origin_turn")
			rec := httptest.NewRecorder()
			HandleSessionTurn(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := h.next(t).Origin; got != (agents.Origin{Kind: agents.OriginMember}) {
				t.Errorf("origin = %+v, want member", got)
			}
		})
	}
}

// injectSessionPrompt is shared by the chat bridge and the auto-resumes, so the origin is the
// caller's to give and must reach the driver unchanged.
func TestInjectSessionPromptPassesTheOriginThrough(t *testing.T) {
	h, _ := useOriginFake(t, "origin_inject")
	for _, want := range []agents.Origin{{Kind: agents.OriginDiscord}, {Kind: agents.OriginAutoResume}} {
		if err := injectSessionPrompt("origin_inject", "hello", want); err != nil {
			t.Fatal(err)
		}
		if got := h.next(t).Origin; got != want {
			t.Errorf("origin = %+v, want %+v", got, want)
		}
	}
}

// The bridge maps its source onto the origin with turnOrigin; both bridges are member input.
func TestBridgeSourcesAreMemberInput(t *testing.T) {
	for _, src := range []string{TurnSourceDiscord, TurnSourceSlack} {
		if o := turnOrigin(src, ""); !o.IsMember() || o.Kind != src {
			t.Errorf("turnOrigin(%q) = %+v, want member input of kind %q", src, o, src)
		}
	}
}

func TestSendManagedPromptIsMemberInput(t *testing.T) {
	h, m := useOriginFake(t, "origin_carried")
	if err := sendManagedPrompt(m, "the answer I picked"); err != nil {
		t.Fatal(err)
	}
	if got := h.next(t).Origin; got != (agents.Origin{Kind: agents.OriginMember}) {
		t.Errorf("origin = %+v, want member", got)
	}
}

// createTurnOrigin follows noteCreateOrigin's branches in order: report_to first, then a
// schedule tag, then a spawn parent, and the ordinary launch last.
func TestCreateTurnOrigin(t *testing.T) {
	cases := []struct {
		name   string
		req    CreateReq
		parent string
		want   agents.Origin
	}{
		{"console launch", CreateReq{}, "", agents.Origin{Kind: agents.OriginMember}},
		{"operator", CreateReq{ReportTo: "chan-1"}, "", agents.Origin{Kind: agents.OriginOperator}},
		{"reported schedule", CreateReq{ReportTo: "chan-1", Source: "schedule"}, "", agents.Origin{Kind: agents.OriginSchedule}},
		{"quiet manual run", CreateReq{Source: "schedule-manual"}, "", agents.Origin{Kind: agents.OriginScheduleManual}},
		{"spawn", CreateReq{}, "parent1", agents.Origin{Kind: agents.OriginSpawn, From: "parent1"}},
		{"reported spawn", CreateReq{ReportTo: "chan-1"}, "parent1", agents.Origin{Kind: agents.OriginOperator}},
	}
	for _, tc := range cases {
		req := tc.req
		if got := createTurnOrigin(&req, tc.parent); got != tc.want {
			t.Errorf("%s: origin = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// The origin kinds and the mirror's badge sources are one vocabulary; a respelling on either
// side makes the Console name the same input two ways.
func TestTurnOriginMatchesBadgeVocabulary(t *testing.T) {
	pairs := map[string]string{
		turnSourcePeer:           agents.OriginPeer,
		TurnSourceSpawn:          agents.OriginSpawn,
		TurnSourceOperator:       agents.OriginOperator,
		TurnSourceSchedule:       agents.OriginSchedule,
		TurnSourceScheduleManual: agents.OriginScheduleManual,
		TurnSourceDiscord:        agents.OriginDiscord,
		TurnSourceSlack:          agents.OriginSlack,
		TurnSourceAutoResume:     agents.OriginAutoResume,
	}
	for badge, kind := range pairs {
		if badge != kind {
			t.Errorf("badge %q is origin %q", badge, kind)
		}
		if got := turnOrigin(badge, "").Kind; got != kind {
			t.Errorf("turnOrigin(%q).Kind = %q", badge, got)
		}
	}
	if got := turnOrigin("", "").Kind; got != agents.OriginMember {
		t.Errorf("an unmarked injection is %q, want member", got)
	}
	if (agents.Origin{}).IsMember() {
		t.Error("an unset origin counts as member input: a forgotten constructor would end a stop episode")
	}
}
