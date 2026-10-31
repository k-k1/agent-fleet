package claude

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/limitobs"
)

// LimitReading is the claude quota as last captured from the statusLine, for the limit
// snapshot (ADR 0111 decision 6). It reads two local files and never runs the CLI: building
// the snapshot must not wait on anything.
func LimitReading() limitobs.Reading {
	r := limitobs.Reading{Identity: accountID()}
	c, _ := currentCapture()
	if c == nil {
		return r
	}
	for _, w := range []struct {
		unit string
		mins int
		cw   *capturedWindow
		at   time.Time
	}{{"5h", 5 * 60, c.FiveHour, c.windowObservedAt(true)}, {"7d", 7 * 24 * 60, c.SevenDay, c.windowObservedAt(false)}} {
		if w.cw == nil || w.cw.ResetsAt <= 0 {
			continue
		}
		r.Windows = append(r.Windows, limitobs.Window{
			Unit: w.unit, UsedPct: w.cw.UsedPercent, ResetsAt: time.Unix(w.cw.ResetsAt, 0).UTC(),
			WindowMins: w.mins, ObservedAt: w.at,
		})
	}
	return r
}

var (
	acctMu   sync.Mutex
	acctKey  string
	acctLast string
)

// accountID names the signed-in claude account from .claude.json's oauthAccount, cached on
// the file's stat. claude rewrites that file constantly and not always atomically, so a read
// that fails to parse keeps the last good answer: flipping to "no identity" for one read
// would look like a sign-in to another account and drop every reading.
func accountID() string {
	p := claudeJSONPath()
	st, err := os.Stat(p)
	if err != nil {
		// No file at all is signed out (or never onboarded), not a torn write.
		acctMu.Lock()
		defer acctMu.Unlock()
		acctKey, acctLast = "", ""
		return ""
	}
	key := p + "|" + st.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(st.Size(), 10)
	acctMu.Lock()
	defer acctMu.Unlock()
	if key == acctKey {
		return acctLast
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return acctLast
	}
	var f struct {
		OAuthAccount *struct {
			AccountUUID  string `json:"accountUuid"`
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &f) != nil {
		return acctLast
	}
	acctKey, acctLast = key, ""
	if a := f.OAuthAccount; a != nil {
		acctLast = a.AccountUUID
		if acctLast == "" {
			acctLast = a.EmailAddress
		}
	}
	return acctLast
}
