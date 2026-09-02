package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNotificationStoreMembershipSeenAndDedup(t *testing.T) {
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(context.Background())
	id, _ := st.UpsertIdentity(context.Background(), "notice@example.com", "notice", "")
	m, _ := st.EnsureMembership(context.Background(), id.ID, tenant.ID, "member")
	n := Notification{EventID: "evt-1", MembershipID: m.ID, Kind: "answer-ready", TargetType: "session", TargetID: "s1", Payload: "{}", CreatedAt: nowTS()}
	if err := st.InsertNotification(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNotification(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListNotifications(context.Background(), m.ID, "", 50)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if count, _ := st.CountUnseenNotifications(context.Background(), m.ID, ""); count != 1 {
		t.Fatalf("unseen=%d", count)
	}
	if err := st.MarkNotificationsSeen(context.Background(), m.ID, []string{"evt-1"}, nowTS()); err != nil {
		t.Fatal(err)
	}
	if count, _ := st.CountUnseenNotifications(context.Background(), m.ID, ""); count != 0 {
		t.Fatalf("unseen after mark=%d", count)
	}
}

func TestUsageNotificationStateRoundTrip(t *testing.T) {
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(context.Background())
	id, _ := st.UpsertIdentity(context.Background(), "usage@example.com", "usage", "")
	m, _ := st.EnsureMembership(context.Background(), id.ID, tenant.ID, "member")
	want := UsageNotificationState{MembershipID: m.ID, Source: "claude", WindowKey: "5h", ResetsAt: "2026-07-13T12:00:00Z", Armed: 1}
	if err := st.PutUsageNotificationState(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetUsageNotificationState(context.Background(), m.ID, "claude", "5h")
	if err != nil || !ok || got != want {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestUsageObservationCreatesOneResetNotification(t *testing.T) {
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(context.Background())
	id, _ := st.UpsertIdentity(context.Background(), "reset@example.com", "reset", "")
	m, _ := st.EnsureMembership(context.Background(), id.ID, tenant.ID, "member")
	a := notificationAPI{store: st}
	post := func(body string) {
		r := httptest.NewRequest("POST", "/api/notifications/usage-observations", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		a.observeUsage(w, r, Identity{}, MembershipView{MembershipID: m.ID})
		if w.Code != 204 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	post(`{"source":"claude","windows":[{"windowKey":"5h","percent":95,"resetsAt":"2026-07-13T12:00:00Z"}]}`)
	post(`{"source":"claude","windows":[{"windowKey":"5h","percent":2,"resetsAt":"2026-07-13T17:00:00Z"}]}`)
	post(`{"source":"claude","windows":[{"windowKey":"5h","percent":2,"resetsAt":"2026-07-13T17:00:00Z"}]}`)
	rows, err := st.ListNotifications(context.Background(), m.ID, "", 50)
	if err != nil || len(rows) != 1 || rows[0].Kind != "usage-reset" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
