package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #5070: show_in_sessions defaults to false, is stored, read back, patched, and an unrelated
// patch leaves it alone.
func TestScheduleShowInSessionsRoundTrip(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}

	var dto scheduleDTO
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x"}`, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusCreated || dto.ShowInSessions {
		t.Fatalf("default: code=%d show_in_sessions=%v", rec.Code, dto.ShowInSessions)
	}

	rec = doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","show_in_sessions":true}`, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusCreated || !dto.ShowInSessions {
		t.Fatalf("create: code=%d show_in_sessions=%v", rec.Code, dto.ShowInSessions)
	}
	got, _, err := st.GetSchedule(ctx, dto.ID)
	if err != nil || !got.ShowInSessions {
		t.Fatalf("stored show_in_sessions=%v err=%v", got.ShowInSessions, err)
	}

	rec = doJSON(api.update, mv, "PATCH", `{"spec_label":"morning"}`, dto.ID)
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || !dto.ShowInSessions {
		t.Fatalf("unrelated patch: code=%d show_in_sessions=%v", rec.Code, dto.ShowInSessions)
	}
	rec = doJSON(api.update, mv, "PATCH", `{"show_in_sessions":false}`, dto.ID)
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	got, _, _ = st.GetSchedule(ctx, dto.ID)
	if rec.Code != http.StatusOK || dto.ShowInSessions || got.ShowInSessions {
		t.Fatalf("patch off: code=%d dto=%v stored=%v", rec.Code, dto.ShowInSessions, got.ShowInSessions)
	}
}
