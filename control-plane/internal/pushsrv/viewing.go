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
	// seqs: membership id -> device id -> highest seq seen.
	seqs map[string]map[string]seqMark
}

type viewReport struct {
	session string
	at      time.Time
}

// seqMark is the highest seq a device has reported. It is kept apart from the on-screen entries so
// that remembering an order never takes a slot an on-screen device needs, and the reverse.
type seqMark struct {
	seq int64
	at  time.Time
}

// seqTTL is how long a device's highest seq is remembered. A report only has to be recognised as
// late while it can still be in flight, and every comparison checks this age itself, so the answer
// does not depend on when the last sweep ran.
const seqTTL = 5 * time.Minute

// maxSeqDevices bounds the seq marks one member can have on record; at the bound the oldest goes.
const maxSeqDevices = 64

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
				if now.Sub(r.at) > viewTTL {
					delete(ds, d)
				}
			}
			if len(ds) == 0 {
				delete(v.byMember, m)
			}
		}
		for m, ds := range v.seqs {
			for d, k := range ds {
				if now.Sub(k.at) > seqTTL {
					delete(ds, d)
				}
			}
			if len(ds) == 0 {
				delete(v.seqs, m)
			}
		}
	}
	if seq > 0 && !v.acceptSeq(member, device, seq, now) {
		return
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
		if session != "" && r.session == session && now.Sub(r.at) <= viewTTL {
			return true
		}
	}
	return false
}

// acceptSeq records seq as the device's highest and reports whether the report is current. Must be
// called with v.mu held.
func (v *viewing) acceptSeq(member, device string, seq int64, now time.Time) bool {
	ds := v.seqs[member]
	if k, ok := ds[device]; ok && now.Sub(k.at) <= seqTTL {
		if seq < k.seq {
			return false
		}
	}
	if ds == nil {
		if v.seqs == nil {
			v.seqs = map[string]map[string]seqMark{}
		}
		ds = map[string]seqMark{}
		v.seqs[member] = ds
	}
	if _, known := ds[device]; !known && len(ds) >= maxSeqDevices {
		oldest, oldAt := "", now
		for d, k := range ds {
			if now.Sub(k.at) > seqTTL {
				delete(ds, d)
			} else if oldest == "" || k.at.Before(oldAt) {
				oldest, oldAt = d, k.at
			}
		}
		if len(ds) >= maxSeqDevices && oldest != "" {
			delete(ds, oldest)
		}
	}
	ds[device] = seqMark{seq, now}
	return true
}
