package codex

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/limitobs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// LimitReading is the codex quota for the limit snapshot (ADR 0111 decision 6): per window,
// the newest real observation among the app-server push, the newest rollout and the account
// view's cache. It never fetches — the account view is reported only as far as the chip or
// the usage-limit watch last fetched it.
//
// Windows are reported as recorded, never rolled past their reset: adjustWindow's 0% is the
// chip's guess, and the snapshot carries observations only.
func LimitReading() limitobs.Reading {
	r := limitobs.Reading{Identity: accountIdentity()}
	var readings []usage
	if u, ok := observedUsage(); ok {
		readings = append(readings, u)
	}
	if u := rolloutUsage(); u.OK {
		readings = append(readings, u)
	}
	if auth, ok := readCodexAuth(); ok {
		if u, ok := peekAccountUsage(auth.Tokens.AccountID); ok && u.OK {
			readings = append(readings, u)
		}
	}
	if len(readings) == 0 {
		// Every live source is empty (rollouts cleaned out, nothing pushed or fetched since the
		// Agent started): the reading usagestore kept for this account.
		if u := withStored(usage{}); u.OK {
			readings = append(readings, u)
		}
	}
	for _, slot := range []struct {
		unit string
		pick func(usage) *usageWindow
	}{
		{"5h", func(u usage) *usageWindow { return u.FiveHour }},
		{"7d", func(u usage) *usageWindow { return u.SevenDay }},
	} {
		var best limitobs.Window
		for _, u := range readings {
			w := slot.pick(u)
			if w == nil || u.ObservedAt.IsZero() || w.raw.ResetsAt <= 0 || !u.ObservedAt.After(best.ObservedAt) {
				continue
			}
			best = limitobs.Window{
				Unit: slot.unit, UsedPct: w.raw.UsedPercent, ResetsAt: time.Unix(w.raw.ResetsAt, 0).UTC(),
				WindowMins: w.raw.WindowMinutes, ObservedAt: u.ObservedAt.UTC(),
			}
		}
		if best.Unit != "" {
			r.Windows = append(r.Windows, best)
		}
	}
	return r
}

var (
	identityMu   sync.Mutex
	identityLast string
)

// accountIdentity names the codex login from auth.json without exec'ing the CLI: the ChatGPT
// account id, else the id_token's e-mail. "" when signed out or in API-key mode. A file that
// exists but does not parse keeps the last answer: one torn read must not look like a sign-in
// to another account, which would drop every reading.
func accountIdentity() string {
	identityMu.Lock()
	defer identityMu.Unlock()
	b, err := os.ReadFile(filepath.Join(paths.HomeDir(), ".codex", "auth.json"))
	if errors.Is(err, fs.ErrNotExist) {
		identityLast = ""
		return ""
	}
	if err != nil {
		return identityLast
	}
	var a struct {
		Tokens struct {
			AccountID string `json:"account_id"`
			IDToken   string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &a) != nil {
		return identityLast
	}
	identityLast = a.Tokens.AccountID
	if identityLast == "" {
		identityLast, _ = idTokenInfo(a.Tokens.IDToken)
	}
	return identityLast
}
