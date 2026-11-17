// Package usagehistory is the pure half of the per-member usage history (ADR 0139 decision 3):
// what the Control Plane accepts from an Agent's minute windows, and the shape it serves back.
// The parts that need the manager (launch records, pulls, the sampler, the HTTP handlers) are
// in package main's usage_history.go.
package usagehistory

import (
	"math"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/usagehist"
)

const (
	// RetentionDays is how long day rows and closed launches are kept (decision 3).
	RetentionDays = 90
	// LaunchEnv is mempressure.LaunchIDEnv on the Agent side.
	LaunchEnv = "AF_LAUNCH_ID"
	// OverrideAdopted marks a launch the sampler found running with no record of its start
	// (a CP crash between Start and its record, or a workspace that predates this ledger). Its
	// box is the member's current setting, which may differ from what it really runs on, and it
	// carries no launch id the Agent knows, so it has running minutes and no measured ones.
	OverrideAdopted = "adopted"
	// maxFuture is how far past now a window's timestamp may be. The windows come from a process
	// the member controls, so a figure from the future is dropped, not folded.
	maxFuture = 5 * time.Minute
)

// Window is the Agent's mempressure.Window.
type Window struct {
	T           int64    `json:"t"`
	LID         string   `json:"lid"`
	CPUPct      *float64 `json:"cpu_pct"`
	WorkingSet  *uint64  `json:"ws"`
	Some10Max   *float64 `json:"some10_max"`
	Full60      *float64 `json:"full60"`
	RefaultRate *float64 `json:"refault_rate"`
}

type Page struct {
	Windows []Window `json:"windows"`
	Cursor  int64    `json:"cursor"`
	More    bool     `json:"more"`
}

// DayWire is one member / day / box row of the usage history (ADR 0139 decision 3).
//
// Every figure that can be unmeasured is a pointer without omitempty, so it reads as an explicit
// null: a client must be able to tell "not measured" from 0, and a missing key looks like a
// field this CP version does not have. Each figure is only meaningful against the measured-
// minutes counter of its own signal, which is why they ride together.
type DayWire struct {
	Day string `json:"day"`
	// Tenant, UserKey and Email label the member on the admin views; the member's own view
	// leaves them out.
	Tenant  string `json:"tenant,omitempty"`
	UserKey string `json:"user_key,omitempty"`
	Email   string `json:"email,omitempty"`

	InstanceType string `json:"instance_type"`
	Class        string `json:"class,omitempty"`
	// Override names what chose this box ("" = the member's own setting).
	Override string `json:"override,omitempty"`
	// VCPU and BurstBaselinePct are what the rung declared, null when it declared nothing.
	VCPU             *int `json:"vcpu"`
	BurstBaselinePct *int `json:"burst_baseline_pct"`

	// RunningMinutes is the launch records' running time, never the Agent's samples.
	RunningMinutes float64 `json:"running_minutes"`
	// MeasuredMinutes counts, per signal, the running minutes the Agent actually measured.
	MeasuredMinutes Measured `json:"measured_minutes"`

	// CPU is percent of ONE core. CPUBox is percent of the whole box, null unless the rung
	// declares its vCPU: dividing by a guess would be a made-up number.
	CPUP50    *float64 `json:"cpu_p50"`
	CPUP95    *float64 `json:"cpu_p95"`
	CPUBoxP50 *float64 `json:"cpu_box_p50"`
	CPUBoxP95 *float64 `json:"cpu_box_p95"`

	WorkingSetP95 *uint64 `json:"working_set_p95"`
	WorkingSetMax *uint64 `json:"working_set_max"`

	PSIFullAvg60Max *float64 `json:"psi_full_avg60_max"`
	PSIWarnMinutes  *int     `json:"psi_warn_minutes"`
	RefaultP95      *float64 `json:"refault_p95"`
	// AboveBaselineMinutes is null unless the rung declares both its vCPU and its baseline.
	AboveBaselineMinutes *int `json:"above_baseline_minutes"`
}

type Measured struct {
	CPU        int `json:"cpu"`
	WorkingSet int `json:"working_set"`
	PSI        int `json:"psi"`
	Refault    int `json:"refault"`
}

// Sanitize converts the Agent's windows, dropping each figure that cannot be a
// measurement. The Agent runs next to member code, so this is input: a negative, non-finite or
// absurd figure becomes "not measured", never a number in someone's history.
func Sanitize(in []Window, now time.Time) []store.UsageWindow {
	ok := func(p *float64, max float64) *float64 {
		if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > max {
			return nil
		}
		return p
	}
	out := make([]store.UsageWindow, 0, len(in))
	limit := now.Add(maxFuture).Unix()
	for _, w := range in {
		if w.LID == "" || len(w.LID) > 64 || w.T <= 0 || w.T > limit {
			continue
		}
		sw := store.UsageWindow{
			T: w.T, LID: w.LID,
			CPUPct:      ok(w.CPUPct, 1e5),
			Some10Max:   ok(w.Some10Max, 100),
			Full60:      ok(w.Full60, 100),
			RefaultRate: ok(w.RefaultRate, 1e9),
		}
		if w.WorkingSet != nil && *w.WorkingSet < 1<<50 {
			sw.WorkingSet = w.WorkingSet
		}
		out = append(out, sw)
	}
	return out
}

func intPtrIf(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// quantile reads q from h, never above the exact max when one is known: the buckets give the
// width of the answer, the max is what the data actually did.
func quantile(h usagehist.Hist, spec usagehist.Spec, q float64, max *float64) *float64 {
	v, ok := h.Quantile(spec, q)
	if !ok {
		return nil
	}
	if max != nil && v > *max {
		v = *max
	}
	return &v
}

// Response is the body of every usage-history endpoint.
type Response struct {
	From          string    `json:"from"`
	To            string    `json:"to"`
	RetentionDays int       `json:"retention_days"`
	Days          []DayWire `json:"days"`
}

// DayOf builds the wire row from a stored one. labelled adds the member's identity.
func DayOf(r store.UsageDayLabelled, labelled bool) DayWire {
	w := DayWire{
		Day: r.Day, InstanceType: r.InstanceType, Class: r.ClassID, Override: r.Override,
		VCPU: intPtrIf(r.VCPU), BurstBaselinePct: intPtrIf(r.BaselinePct),
		RunningMinutes: round1(float64(r.RunningSecs) / 60),
		MeasuredMinutes: Measured{
			CPU: r.CPUMeasuredMin, WorkingSet: r.WSMeasuredMin, PSI: r.PSIMeasuredMin, Refault: r.RefaultMeasuredMin,
		},
	}
	if labelled {
		w.Tenant, w.UserKey, w.Email = r.TenantSlug, r.UserKey, r.Email
	}
	w.CPUP50 = quantile(r.CPUHist, usagehist.CPU, 0.5, r.CPUMax)
	w.CPUP95 = quantile(r.CPUHist, usagehist.CPU, 0.95, r.CPUMax)
	if r.VCPU > 0 {
		div := func(p *float64) *float64 {
			if p == nil {
				return nil
			}
			v := *p / float64(r.VCPU)
			return &v
		}
		w.CPUBoxP50, w.CPUBoxP95 = div(w.CPUP50), div(w.CPUP95)
	}
	if r.WSMeasuredMin > 0 {
		w.WorkingSetMax = r.WSMax
		var max *float64
		if r.WSMax != nil {
			m := float64(*r.WSMax)
			max = &m
		}
		if p := quantile(r.WSHist, usagehist.WorkingSet, 0.95, max); p != nil {
			v := uint64(*p)
			w.WorkingSetP95 = &v
		}
	}
	if r.PSIMeasuredMin > 0 {
		w.PSIFullAvg60Max = r.PSIFullMax
		warn := r.PSIWarnMin
		w.PSIWarnMinutes = &warn
	}
	w.RefaultP95 = quantile(r.RefaultHist, usagehist.Refault, 0.95, nil)
	if r.VCPU > 0 && r.BaselinePct > 0 && r.CPUMeasuredMin > 0 {
		w.AboveBaselineMinutes = r.BurstMin
	}
	return w
}
