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
// restart forgets it. Both fail toward sending a push.
type viewing struct {
	mu sync.Mutex
	// byMember: membership id -> device id -> report.
	byMember map[string]map[string]viewReport
}

type viewReport struct {
	session string
	at      time.Time
}

func newViewing() *viewing { return &viewing{byMember: map[string]map[string]viewReport{}} }

func (v *viewing) set(member, device, session string, now time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	devs := v.byMember[member]
	if session == "" {
		delete(devs, device)
		if len(devs) == 0 {
			delete(v.byMember, member)
		}
		return
	}
	if devs == nil {
		devs = map[string]viewReport{}
		v.byMember[member] = devs
	}
	devs[device] = viewReport{session, now}
	// Drop what has expired so the map cannot grow with abandoned devices.
	for d, r := range devs {
		if now.Sub(r.at) > viewTTL {
			delete(devs, d)
		}
	}
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
