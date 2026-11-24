package pushsrv

import (
	"sync"
	"time"
)

// viewTTL is how long one on-screen report keeps suppressing pushes. Consoles report about every
// 20 s, so this tolerates a missed beat; a longer value would silence a phone for a session the
// member already left.
const viewTTL = 45 * time.Second

// viewing is the in-memory "which session is on screen where" map. It is per Control Plane
// process: with several replicas a report only suppresses pushes that this replica sends, and a
// restart forgets it. Both fail toward sending a push. The zero value is ready to use, so a Service
// built as a literal (tests, or a later constructor) cannot panic on its first report or delivery.
type viewing struct {
	mu        sync.Mutex
	lastSweep time.Time
	// byMember: membership id -> device id -> report.
	byMember map[string]map[string]viewReport
}

type viewReport struct {
	session string
	at      time.Time
}

// maxViewDevices bounds the devices one member can have on record, so a client that sends a new
// device id every time cannot grow the map or make each report scan an ever longer list.
const maxViewDevices = 16

// sweepEvery is how often set walks every member to forget the expired ones, which is what
// reclaims a member who stopped reporting.
const sweepEvery = time.Minute

func (v *viewing) set(member, device, session string, now time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if now.Sub(v.lastSweep) >= sweepEvery {
		v.lastSweep = now
		for m, ds := range v.byMember {
			for d, r := range ds {
				if now.Sub(r.at) > viewTTL {
					delete(ds, d)
				}
			}
			if len(ds) == 0 {
				delete(v.byMember, m)
			}
		}
	}
	devs := v.byMember[member]
	if session == "" {
		delete(devs, device)
		if len(devs) == 0 {
			delete(v.byMember, member)
		}
		return
	}
	if devs == nil {
		if v.byMember == nil {
			v.byMember = map[string]map[string]viewReport{}
		}
		devs = map[string]viewReport{}
		v.byMember[member] = devs
	}
	if _, known := devs[device]; !known && len(devs) >= maxViewDevices {
		// Make room by dropping this member's expired entries, then the oldest.
		oldest, oldAt := "", now
		for d, r := range devs {
			if now.Sub(r.at) > viewTTL {
				delete(devs, d)
			} else if oldest == "" || r.at.Before(oldAt) {
				oldest, oldAt = d, r.at
			}
		}
		if len(devs) >= maxViewDevices && oldest != "" {
			delete(devs, oldest)
		}
	}
	devs[device] = viewReport{session, now}
}

// on reports whether any device of the member has shown session within viewTTL.
func (v *viewing) on(member, session string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range v.byMember[member] {
		if r.session == session && now.Sub(r.at) <= viewTTL {
			return true
		}
	}
	return false
}
