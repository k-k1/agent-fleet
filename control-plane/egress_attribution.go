package main

import (
	"log"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Egress attribution (ADR 0122, addendum 2026-10-24): which workspace made a connection the
// gateway saw. The event keeps the raw source address. This file decides, at read time,
// what that address meant at the event's time, from the rows the address recorder keeps
// (egress_address_recorder.go).
//
// Read time rather than ingest time because a holder can be recorded after its first
// connection: a task replaced by ECS on its own makes connections before the next
// recorder pass sees it.

// Attribution states. Nothing is dropped and nothing is guessed: an address two rows claim
// at once is shown with both, and an address no row claims is shown as such.
const (
	egressAttributed     = "attributed"
	egressAmbiguous      = "ambiguous"
	egressUnattributed   = "unattributed"
	egressInfrastructure = "infrastructure"
)

// parseEgressInfraCIDRs reads AF_EGRESS_INFRA_CIDRS: the ranges the stack's own machines
// send from (nodes, ecs-ec2 slot instances, the CP), comma- or space-separated. An entry
// that does not parse is logged and skipped, so a typo narrows the class instead of
// stopping the CP.
func parseEgressInfraCIDRs(v string) []netip.Prefix {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			if a, aerr := netip.ParseAddr(f); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				log.Printf("egress: ignoring AF_EGRESS_INFRA_CIDRS entry %q: %v", f, err)
				continue
			}
		}
		out = append(out, p.Masked())
	}
	return out
}

// egressAttributionOf classifies one event. A workspace row wins over the infrastructure
// ranges: an ecs-ec2 task ENI sits in the same subnet as its slot instance, and only the
// row can tell the two apart.
func egressAttributionOf(e store.AttributedEgressEvent, infra []netip.Prefix) string {
	switch len(e.Holders) {
	case 0:
	case 1:
		return egressAttributed
	default:
		return egressAmbiguous
	}
	if a, err := netip.ParseAddr(e.Source); err == nil {
		for _, p := range infra {
			if p.Contains(a) {
				return egressInfrastructure
			}
		}
	}
	return egressUnattributed
}

// egressEventHolderWire is one address row an event's source matched.
type egressEventHolderWire struct {
	MembershipID string `json:"membership_id"`
	WorkspaceID  string `json:"workspace_id"`
	Ref          string `json:"ref"`
	From         int64  `json:"from"`
	Until        int64  `json:"until"`
}

// egressEventWire is one raw event with its attribution. Times are unix milliseconds.
type egressEventWire struct {
	ID          string                  `json:"id"`
	At          int64                   `json:"at"`
	Source      string                  `json:"source"`
	Dest        string                  `json:"dest"`
	Host        string                  `json:"host"`
	Allowed     bool                    `json:"allowed"`
	Count       int                     `json:"count"`
	Attribution string                  `json:"attribution"`
	Holders     []egressEventHolderWire `json:"holders"`
}

type egressEventsWire struct {
	Events  []egressEventWire `json:"events"`
	Minutes int               `json:"minutes"`
}

// events (GET /api/admin/egress/events?minutes=N&limit=M) serves the newest raw events with
// their attribution. Deployment-wide, so super_admin only, like stats.
func (a egressAPI) events(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	minutes, limit := 60, 200
	if v, err := strconv.Atoi(r.URL.Query().Get("minutes")); err == nil && v > 0 && v <= 7*24*60 {
		minutes = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	rows, err := a.store.ListAttributedEgressEvents(r.Context(), since, limit)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := egressEventsWire{Events: make([]egressEventWire, 0, len(rows)), Minutes: minutes}
	for _, e := range rows {
		ev := egressEventWire{
			ID: e.ID, At: e.At, Source: e.Source, Dest: e.Dest, Host: e.Host,
			Allowed: e.Allowed, Count: e.Count,
			Attribution: egressAttributionOf(e, a.infra),
			Holders:     make([]egressEventHolderWire, 0, len(e.Holders)),
		}
		for _, h := range e.Holders {
			ev.Holders = append(ev.Holders, egressEventHolderWire{
				MembershipID: h.MembershipID, WorkspaceID: h.WorkspaceID, Ref: h.Ref, From: h.From, Until: h.Until,
			})
		}
		out.Events = append(out.Events, ev)
	}
	writeJSON(w, http.StatusOK, out)
}
