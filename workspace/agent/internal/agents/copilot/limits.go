package copilot

import (
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/limitobs"
)

// The limit snapshot reads copies published here rather than taking tokenMu or usageMu: Token
// holds tokenMu across a gh run of up to 10 s and HandleUsage holds usageMu across the HTTP
// fetch, and the reaper's serial sweep waits on GET /sessions (ADR 0111 decision 6).
var (
	// tokenPublished is the token Token last read; nil until gh was first asked.
	tokenPublished atomic.Pointer[string]
	usagePublished atomic.Pointer[usagePub]
)

// usagePub is one successful chip fetch, with the token it was made with.
type usagePub struct {
	res   *usageResult
	at    time.Time
	token string
}

// LimitReading is the copilot credit pools for the limit snapshot, as last fetched by the usage
// chip. Nothing is fetched and no lock is waited on. A pool's used share is
// 100 - percent_remaining; unlimited pools have no quota to report and are left out.
//
// The account is named by the current gh token (the snapshot only ever sees an HMAC of it). The
// cached pools are reported only while that token is the one they were fetched with, so a gh
// login to another account reports the new epoch with no units, which clears the old pools on
// the CP. Token is refreshed by every chip poll and every copilot launch.
func LimitReading() limitobs.Reading {
	tp := tokenPublished.Load()
	if tp == nil {
		return limitobs.Reading{NoIdentity: true}
	}
	tok := *tp
	r := limitobs.Reading{}
	if tok != "" {
		r.Identity = identityOf(tok)
	}
	p := usagePublished.Load()
	if tok != "" && p == nil {
		// Nothing fetched since the Agent started: the reading stored before the restart, if it
		// is this account's.
		if res, at := storedUsage(tok); res != nil {
			p = &usagePub{res: res, at: at, token: tok}
		}
	}
	if tok == "" || p == nil || p.token != tok {
		return r
	}
	var reset time.Time
	if t, err := time.Parse(time.RFC3339, p.res.ResetsAt); err == nil {
		reset = t.UTC()
	}
	for _, q := range p.res.Quotas {
		if q.Unlimited {
			continue
		}
		r.Windows = append(r.Windows, limitobs.Window{
			Unit: q.ID, UsedPct: 100 - q.RemainingPct, ResetsAt: reset, ObservedAt: p.at.UTC(),
		})
	}
	return r
}
