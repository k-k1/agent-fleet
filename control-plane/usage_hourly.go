package main

import (
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Uptime heatmap API (docs/log/83). Returns one-hour buckets of occupancy, enough to draw
// a grid of 24 hours (vertical) by date (horizontal).
//
// This is not money. Real spend (Cost Explorer) is only available per day, so an hourly
// amount could only be "seconds x a unit price somebody typed in once" — an estimate, which
// is exactly what ADR 0048 decision 2 rejects. The heatmap explains a day on the invoice
// (how many sessions ran at what hour); it does not price it. Hence it lives under "uptime"
// rather than "cloud cost".
//
// The response shape is identical at all three entry points (own, tenant aggregate, member
// detail), for the same reason /api/cost/me and the admin side share one shape and one
// component: two shapes drift silently from the day only one of them is fixed.

// usageHourWindow widens the requested date range to UTC hour-bucket boundaries.
//
// The arguments are dates (YYYY-MM-DD, UTC) and the result is [from T00, to T23]. Shifting
// to local time is the renderer's job, so a client must request one day more on each side
// than it means to show (at +09:00, UTC 15:00 lands in the next day's cell). Nothing shifts
// here because only the browser holds the clock: buckets cut on a timezone the server
// guessed match nobody's clock.
func usageHourWindow(r *http.Request, now time.Time) (fromDay, toDay, fromHour, toHour string, aerr *apiError) {
	// Default is 14 days. Not usageRange's 30: 30 columns x 24 rows overlaps the date labels
	// into illegibility (measured on the cloud-cost bar chart, docs/log/67).
	fromDay = now.AddDate(0, 0, -(usageHourlyDefaultDays - 1)).Format(usageDayFmt)
	toDay = now.Format(usageDayFmt)
	for _, p := range []struct {
		name string
		dst  *string
	}{{"from", &fromDay}, {"to", &toDay}} {
		v := r.URL.Query().Get(p.name)
		if v == "" {
			continue
		}
		if _, err := time.Parse(usageDayFmt, v); err != nil {
			return "", "", "", "", &apiError{http.StatusBadRequest, "bad_request", p.name + " must be YYYY-MM-DD"}
		}
		*p.dst = v
	}
	if fromDay > toDay {
		return "", "", "", "", &apiError{http.StatusBadRequest, "bad_request", "from must not be after to"}
	}
	return fromDay, toDay, fromDay + "T00", toDay + "T23", nil
}

// usageHourPoint is one hour of one member. Zero values are dropped by omitempty — this is
// not meant to be a dense array of 720 cells, and padding idle hours with seven numbers
// each only fattens the response.
type usageHourPoint struct {
	Hour string `json:"hour"` // YYYY-MM-DDTHH (UTC)
	store.UsageHourCounters
}

// usageHourMember is one member's series. Only hours with activity get a row (an idle hour
// has none); telling those apart from "not observed" is Observed's job below.
type usageHourMember struct {
	Tenant  string           `json:"tenant"`
	UserKey string           `json:"user_key"`
	Email   string           `json:"email"`
	Hours   []usageHourPoint `json:"hours"`
}

// usageHourlyResponse is the response of all three entry points.
//
// Observed is the crux of this API. A cell is three-valued (idle / running / unobserved),
// and without a separate record of when the sampler was alive, a day the CP was down and a
// day nobody worked come out the same grey. An hour with no row in Members means "idle" if
// it appears in Observed and "unknown" if it does not.
//
// Observed carries points, not a list of timestamps, because samples is the cell's
// denominator: hard-coding an hour as 3600 seconds always washes out the hour still in
// progress, and any hour the CP died partway through. Unless the ratio is "of the time we
// watched, how much was running", the colour shows gaps in observation rather than uptime.
type usageHourlyResponse struct {
	From         string            `json:"from"`
	To           string            `json:"to"`
	IntervalSecs int               `json:"interval_secs"`
	Observed     []usageHourPoint  `json:"observed"`
	Members      []usageHourMember `json:"members"`
}

// buildUsageHourly folds store rows into the response. Pure — no sampler and no HTTP
// concerns belong here, so a test can aim at this alone.
//
// A row with membership_id=="" is the sampler's heartbeat, not a member. Mixing it into
// Members puts a nameless member in every tenant's list.
func buildUsageHourly(rows []store.UsageHourRow, from, to string, intervalSecs int) usageHourlyResponse {
	out := usageHourlyResponse{
		From: from, To: to, IntervalSecs: intervalSecs,
		Observed: []usageHourPoint{}, Members: []usageHourMember{},
	}
	idx := map[string]int{}
	for _, r := range rows {
		if r.MembershipID == "" {
			out.Observed = append(out.Observed,
				usageHourPoint{Hour: r.Hour, UsageHourCounters: store.UsageHourCounters{Samples: r.Samples}})
			continue
		}
		// Uptime of a reserved tenant (af-golden and the like) is not a person's uptime.
		// Hiding them from the list is pointless if they still surface in the heatmap
		// (the same call as usage.go).
		if isSystemTenantSlug(r.TenantSlug) {
			continue
		}
		i, ok := idx[r.MembershipID]
		if !ok {
			i = len(out.Members)
			idx[r.MembershipID] = i
			out.Members = append(out.Members, usageHourMember{
				Tenant: r.TenantSlug, UserKey: r.UserKey, Email: r.Email,
			})
		}
		out.Members[i].Hours = append(out.Members[i].Hours,
			usageHourPoint{Hour: r.Hour, UsageHourCounters: r.UsageHourCounters})
	}
	return out
}

// usageSampleInterval is how many seconds one sample stands for. A cell's "what fraction of
// the hour was running" is running_secs / (samples x interval), so the response carries this
// for the client to build the denominator. An operator can change it through
// AF_USAGE_SAMPLE_INTERVAL, so never copy it into the Console as a constant.
func (a adminAPI) usageSampleInterval() int {
	return int(a.mgr.usageInterval.Seconds())
}

// usageHourlyFor is the part the three entry points share. tenantID=="" means every tenant;
// membershipID!="" narrows to one person.
func (a adminAPI) usageHourlyFor(w http.ResponseWriter, r *http.Request, tenantID, membershipID string) {
	fromDay, toDay, fromHour, toHour, aerr := usageHourWindow(r, time.Now().UTC())
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.mgr.store.ListUsageHourly(r.Context(), tenantID, fromHour, toHour)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if membershipID != "" {
		kept := rows[:0:0]
		for _, row := range rows {
			// Heartbeat rows belong to nobody, so keep them — even a single-person view
			// has to leave unobserved hours blank in the same way.
			if row.MembershipID == "" || row.MembershipID == membershipID {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	writeJSON(w, http.StatusOK, buildUsageHourly(rows, fromDay, toDay, a.usageSampleInterval()))
}

// myUsageHourly (GET /api/usage/me/hourly?from=&to=) — the caller's own uptime only.
//
// Never return a value from which someone else's total can be recovered by subtraction (the
// same discipline as /api/cost/me): only the caller's rows and the heartbeat go out, and no
// deployment-wide total appears anywhere.
func (a adminAPI) myUsageHourly(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	// Deliberately not scoped by tenant. Rows from destroyed workspaces are still the
	// caller's own, and filtering them shows the caller a confidently empty screen (the
	// same shape as docs/log/67 §67.15).
	a.usageHourlyFor(w, r, "", mv.MembershipID)
}

// adminUsageHourly (GET /api/admin/usage/hourly?tenant=&from=&to=) — the admin's aggregate
// heatmap.
//
// Returns per-member series rather than the aggregate itself; the client sums them. The
// hover breakdown then comes out of the same data (no second API call), so "total disagrees
// with breakdown" cannot arise.
func (a adminAPI) adminUsageHourly(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	a.usageHourlyFor(w, r, tenantID, "")
}

// memberUsageHourly (GET /api/admin/tenants/{slug}/members/{key}/usage-hourly) — the single
// pane on member detail. It sits on the surface holding the force-stop and disk-quota
// buttons for the same reason cost does: "ran all weekend" is itself the case for pressing
// the button next to it.
//
// As with cost, look it up by membership alone. tenantAdminFor + resolveMember have already
// proved the affiliation, and scoping by tenant as well drops destroyed workspaces.
func (a adminAPI) memberUsageHourly(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, _, _, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.usageHourlyFor(w, r, "", mem.ID)
}

// usageHourlyDefaultDays is the default window. 30 days x 24 rows is too wide and crushes
// the date labels (measured on the cloud-cost bar chart), so the heatmap defaults to 14.
const usageHourlyDefaultDays = 14
