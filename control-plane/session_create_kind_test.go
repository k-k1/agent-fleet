package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The spellings a caller actually gets wrong (#2297). The Agent registers a kind under one
// spelling and used to fold anything else to claude.
var unregisteredCreateKinds = []string{"Codex", " codex", "kiro-cli", "llamacpp"}

// POST /api/sessions (the Console's launch) refuses an unregistered kind before the Agent is
// asked — and before a cold Workspace would be started for it — and names the accepted kinds.
// A registered kind and the empty kind (claude) are forwarded untouched.
func TestSessionCreateRefusesUnregisteredKind(t *testing.T) {
	f := newHandoffLife(t)
	rc := f.recipientWorkspace()
	for _, kind := range unregisteredCreateKinds {
		rec := rc.create(t, map[string]any{"kind": kind, "dir": "/repos/app"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"bad_kind"`) ||
			!strings.Contains(rec.Body.String(), "accepted kinds") {
			t.Errorf("kind %q: %d %s, want 400 bad_kind naming the accepted kinds", kind, rec.Code, rec.Body.String())
		}
	}
	if len(rc.bodies) != 0 {
		t.Fatalf("a refused kind reached the Agent: %v", rc.bodies)
	}
	for _, kind := range []string{"codex", "claude", ""} {
		rec := rc.create(t, map[string]any{"kind": kind, "dir": "/repos/app"})
		if rec.Code != http.StatusCreated {
			t.Errorf("kind %q: %d %s, want the create forwarded", kind, rec.Code, rec.Body.String())
		}
	}
	if len(rc.bodies) != 3 {
		t.Errorf("Agent saw %d creates, want 3", len(rc.bodies))
	}
}

// A schedule names the kind every fire creates, so saving one with an unregistered kind is
// refused (it would fail at fire time) — and a fire of a row that predates the check is refused
// before the Workspace is touched instead of launching claude. An assistant schedule ignores
// agent_kind and an empty kind stays claude.
func TestScheduleRefusesUnregisteredKind(t *testing.T) {
	api, _, mv := newSchedAPITest(t)
	for _, kind := range unregisteredCreateKinds {
		b := `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","agent_kind":"` + kind + `"}`
		rec := doJSON(api.create, mv, "POST", b, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_kind") {
			t.Errorf("create agent_kind %q -> %d %s, want 400 bad_kind", kind, rec.Code, rec.Body.String())
		}
	}
	for _, kind := range []string{"codex", "claude", ""} {
		b := `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","agent_kind":"` + kind + `"}`
		if rec := doJSON(api.create, mv, "POST", b, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Errorf("create agent_kind %q -> %d %s, want it accepted", kind, rec.Code, rec.Body.String())
		}
	}
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	for _, kind := range unregisteredCreateKinds {
		up := doJSON(api.update, mv, "PATCH", `{"agent_kind":"`+kind+`"}`, dto.ID)
		if up.Code != http.StatusBadRequest || !strings.Contains(up.Body.String(), "bad_kind") {
			t.Errorf("update agent_kind %q -> %d %s, want 400 bad_kind", kind, up.Code, up.Body.String())
		}
	}
	if up := doJSON(api.update, mv, "PATCH", `{"agent_kind":"codex"}`, dto.ID); up.Code != http.StatusOK {
		t.Errorf("update agent_kind codex -> %d %s", up.Code, up.Body.String())
	}
	// An assistant schedule may hold any agent_kind, but leaving assistant mode makes it live.
	as := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","session_mode":"assistant","agent_kind":"Codex"}`, "")
	var adto scheduleDTO
	_ = json.Unmarshal(as.Body.Bytes(), &adto)
	for _, mode := range []string{"new", "reuse"} {
		up := doJSON(api.update, mv, "PATCH", `{"session_mode":"`+mode+`"}`, adto.ID)
		if up.Code != http.StatusBadRequest || !strings.Contains(up.Body.String(), "bad_kind") {
			t.Errorf("assistant -> %s keeping agent_kind Codex -> %d %s, want 400 bad_kind", mode, up.Code, up.Body.String())
		}
	}
	if up := doJSON(api.update, mv, "PATCH", `{"prompt":"y"}`, adto.ID); up.Code != http.StatusOK {
		t.Errorf("editing an assistant schedule's prompt -> %d %s", up.Code, up.Body.String())
	}
	if up := doJSON(api.update, mv, "PATCH", `{"session_mode":"new","agent_kind":"codex"}`, adto.ID); up.Code != http.StatusOK {
		t.Errorf("assistant -> new with a valid kind -> %d %s", up.Code, up.Body.String())
	}
	// agent_kind is ignored by an assistant fire, so it is not validated there.
	if rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","session_mode":"assistant","agent_kind":"Codex"}`, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Errorf("assistant schedule with an ignored agent_kind -> %d %s", rec.Code, rec.Body.String())
	}

	for _, kind := range unregisteredCreateKinds {
		// A zero wakeFirer would panic on the first store call: the refusal has to come first.
		_, _, err := (&wakeFirer{}).fire(t.Context(), store.Schedule{ID: "s", SessionMode: "new", AgentKind: kind}, time.Now())
		if err == nil || !strings.Contains(err.Error(), "accepted kinds") {
			t.Errorf("fire agent_kind %q = %v, want a refusal naming the accepted kinds", kind, err)
		}
	}
}
