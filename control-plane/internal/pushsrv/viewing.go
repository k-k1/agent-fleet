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

// A report with an empty session is a tombstone: it is kept (rather than deleted) when it carries a
// seq, so a heartbeat that was sent before the clear but arrives after it is recognised as late.
type viewReport struct {
	session string
	at      time.Time
	seq     int64
}

// tombstoneTTL is how long a clear's seq is remembered. Longer than viewTTL: the late heartbeat it
// guards against can only matter while a lost-order report is still in flight, but a generous
// bound costs one small map entry per device.
const tombstoneTTL = 5 * time.Minute

func (r viewReport) expired(now time.Time) bool {
	if r.session == "" {
		return now.Sub(r.at) > tombstoneTTL
	}
	return now.Sub(r.at) > viewTTL
}

// maxViewDevices bounds the devices one member can have on record, so a client that sends a new
// device id every time cannot grow the map or make each report scan an ever longer list.
const maxViewDevices = 16

// sweepEvery is how often set walks every member to forget the expired ones, which is what
// reclaims a member who stopped reporting.
const sweepEvery = time.Minute

// set records one report. seq is the device's own counter, rising with every report it makes; 0
// means the Console does not send one (older Console), which keeps the arrival-order behaviour. A
// report whose seq is below the last one kept for the device is dropped, so a heartbeat that
// overtakes the clear sent after it cannot bring the suppression back.
func (v *viewing) set(member, device, session string, seq int64, now time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if now.Sub(v.lastSweep) >= sweepEvery {
		v.lastSweep = now
		for m, ds := range v.byMember {
			for d, r := range ds {
				if r.expired(now) {
					delete(ds, d)
				}
			}
			if len(ds) == 0 {
				delete(v.byMember, m)
			}
		}
	}
	devs := v.byMember[member]
	if prev, ok := devs[device]; ok && seq > 0 && prev.seq > 0 && seq < prev.seq {
		return
	}
	if session == "" && seq == 0 {
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
			if r.expired(now) {
				delete(devs, d)
			} else if oldest == "" || r.at.Before(oldAt) {
				oldest, oldAt = d, r.at
			}
		}
		if len(devs) >= maxViewDevices && oldest != "" {
			delete(devs, oldest)
		}
	}
	devs[device] = viewReport{session, now, seq}
}

// on reports whether any device of the member has shown session within viewTTL.
func (v *viewing) on(member, session string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range v.byMember[member] {
		if session != "" && r.session == session && now.Sub(r.at) <= viewTTL {
			return true
		}
	}
	return false
}
