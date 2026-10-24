package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A gateway resends a batch after a failed POST. Events with an id are stored and counted
// once; the own proxy's id-less events keep their old at-least-once behaviour.
func TestEgressIngestStoresGatewayEventsOnce(t *testing.T) {
	ctx := context.Background()
	st, _, mgr := reaperLifecycleFixture(t)
	eg := newEgressAPI(mgr, "tok", "", &egressAuditDedup{}, nil)
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC).UnixMilli()
	body := `{"events":[` +
		`{"id":"gw:100","at":` + strconv.FormatInt(at, 10) + `,"source":"10.0.1.5","dest":"140.82.112.3","host":"GitHub.com","allowed":true,"count":1},` +
		`{"id":"gw:200","at":` + strconv.FormatInt(at, 10) + `,"source":"10.0.1.5","dest":"203.0.113.9","allowed":false},` +
		`{"id":"gw:300","host":"no-source.example","allowed":true,"count":1}]}`
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/internal/egress", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		eg.ingest(w, r)
		if w.Code != 204 {
			t.Fatalf("post %d: want 204, got %d", i, w.Code)
		}
	}
	stats, _ := st.ListEgress(ctx, "2026-10-24", 10)
	if len(stats) != 1 || stats[0].Host != "github.com" || stats[0].Allowed != 1 {
		t.Errorf("a resent batch must be counted once, under the event's day: %+v", stats)
	}
	got, err := st.ListAttributedEgressEvents(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The nameless connection is kept (by address); the one without a source is refused.
	if len(got) != 2 {
		t.Fatalf("want the two events with a source and a time, got %+v", got)
	}
	for _, e := range got {
		if e.ID == "gw:300" {
			t.Errorf("an event with no source address cannot be attributed and must not be stored")
		}
		if e.ID == "gw:200" && (e.Host != "" || e.Count != 1 || e.Allowed) {
			t.Errorf("nameless event stored wrong: %+v", e)
		}
	}
}

func TestEgressAttributionOf(t *testing.T) {
	infra := parseEgressInfraCIDRs("10.0.0.0/24, 10.9.9.9 not-a-cidr")
	if len(infra) != 2 {
		t.Fatalf("want two infra ranges (one prefix, one bare address), got %v", infra)
	}
	one := []store.WorkspaceAddress{{MembershipID: "m1"}}
	two := []store.WorkspaceAddress{{MembershipID: "m1"}, {MembershipID: "m2"}}
	for _, c := range []struct {
		source  string
		holders []store.WorkspaceAddress
		want    string
	}{
		{"10.0.1.5", one, egressAttributed},
		{"10.0.1.5", two, egressAmbiguous},
		{"10.0.1.5", nil, egressUnattributed},
		{"10.0.0.7", nil, egressInfrastructure},
		{"10.9.9.9", nil, egressInfrastructure},
		// A task ENI inside the slot subnet: the row is what tells it apart.
		{"10.0.0.7", one, egressAttributed},
	} {
		e := store.AttributedEgressEvent{EgressEvent: store.EgressEvent{Source: c.source}, Holders: c.holders}
		if got := egressAttributionOf(e, infra); got != c.want {
			t.Errorf("%s with %d holders: got %s, want %s", c.source, len(c.holders), got, c.want)
		}
	}
}

type addressStub struct {
	hibernateStub
	held []runtime.HeldAddress
	err  error
}

func (r *addressStub) HeldAddresses(context.Context) ([]runtime.HeldAddress, error) {
	return r.held, r.err
}

// The recorder end to end: a running workspace's address is opened, a failed read leaves it
// open, a vanished holder closes it, and a stopped workspace with nothing open is not asked.
func TestAddressRecorderKeepsIntervals(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	rt := &addressStub{}
	// Recent enough for the events endpoint's default window.
	base := time.Now().Add(-time.Minute).UnixMilli()
	mgr.rtFactory = stubFactory{rt: rt}
	clock := time.UnixMilli(base + 10_000)
	ar := &addressRecorder{mgr: mgr, now: func() time.Time { return clock }}
	eg := newEgressAPI(mgr, "tok", "", nil, nil)

	rt.held = []runtime.HeldAddress{{Address: "10.0.1.5", Ref: "task/1", From: time.UnixMilli(base + 1_000)}}
	ar.sweep(ctx)
	if open, _ := st.HasOpenWorkspaceAddress(ctx, ws.MembershipID); !open {
		t.Fatal("a reported address must be recorded open")
	}

	rt.held, rt.err = nil, errors.New("throttled")
	clock = time.UnixMilli(base + 20_000)
	ar.sweep(ctx)
	if open, _ := st.HasOpenWorkspaceAddress(ctx, ws.MembershipID); !open {
		t.Fatal("a failed read must not close the interval")
	}

	rt.err = nil
	clock = time.UnixMilli(base + 30_000)
	ar.sweep(ctx)
	if open, _ := st.HasOpenWorkspaceAddress(ctx, ws.MembershipID); open {
		t.Fatal("a holder no longer reported must be closed")
	}

	for _, e := range []store.EgressEvent{
		{ID: "in", At: base + 25_000, Source: "10.0.1.5", Count: 1},
		{ID: "after", At: base + 31_000, Source: "10.0.1.5", Count: 1},
	} {
		if _, err := st.RecordEgressEvent(ctx, "d", e, base+40_000); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	eg.events(w, httptest.NewRequest("GET", "/api/admin/egress/events", nil), store.Identity{})
	var resp egressEventsWire
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	attr := map[string]egressEventWire{}
	for _, e := range resp.Events {
		attr[e.ID] = e
	}
	if e := attr["in"]; e.Attribution != egressAttributed || len(e.Holders) != 1 || e.Holders[0].MembershipID != ws.MembershipID {
		t.Errorf("event inside the interval: %+v", e)
	}
	if e := attr["after"]; e.Attribution != egressUnattributed {
		t.Errorf("event after the close: %+v", e)
	}

	// Stopped with nothing open: the runtime is not asked at all.
	if err := st.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	rt.held = []runtime.HeldAddress{{Address: "10.0.1.6", Ref: "task/2", From: time.UnixMilli(base + 35_000)}}
	ar.sweep(ctx)
	if open, _ := st.HasOpenWorkspaceAddress(ctx, ws.MembershipID); open {
		t.Error("a stopped workspace with no open address was polled")
	}
}
