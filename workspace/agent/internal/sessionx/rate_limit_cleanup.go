package sessionx

// Cancelling a usage-limit booking has to be able to fail visibly (ADR 0115 decision 11.5). The
// CP may be unreachable at the moment the Agent cancels, and an id forgotten then is a resume
// that still fires at the reset instant — for a moved source, into a session that must not wake.
// So an id whose delete the CP has not confirmed is kept here, apart from the episode (which
// closes for display regardless), and retried on every sweep. It is a file, not memory, because
// the reset can be days away and the Agent restarts in between.
//
// An id leaves only when the CP confirms the delete or answers that the schedule is gone. There
// is no expiry: a guess that the schedule must have fired by now is exactly what would leave a
// live one behind.
//
// A confirmed id whose session's episode still names it stays as Done until the episode lets go
// of it. That is a moving source whose commit recorded the id and then stopped: the sweep cancels
// the booking, and a rollback must still learn that the id it finds in the episode is dead, or it
// reopens a source whose resume nobody will book again.

import (
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rateLimitScheduleCleanup is one booking whose cancellation is not yet confirmed.
type rateLimitScheduleCleanup struct {
	Session string `json:"session"`
	Since   string `json:"since"` // first failed attempt (RFC3339)
	Tries   int    `json:"tries"`
	LastErr string `json:"lastErr,omitempty"`
	// Done: the CP confirmed; kept only while the session's episode still names the id.
	Done bool `json:"done,omitempty"`
	// KeepUntil (RFC3339): a confirmed id stays recorded until then even when the episode no
	// longer names it, so a fire of that booking already in flight is still refused after a
	// failed route switch rebooked the episode under a new id.
	KeepUntil string `json:"keepUntil,omitempty"`
}

// rateLimitCleanups holds every pending cancellation in one file, keyed by schedule id. One file
// rather than one per id: the id is the CP's, and nothing promises it is a safe file name.
var rateLimitCleanups = fstore.JSON[map[string]rateLimitScheduleCleanup](paths.AgentStateDir, "session-rate-limit-cleanup", ".json")

const rateLimitCleanupKey = "pending"

// rateLimitCleanupMu serializes the read-modify-write of the file, and keeps two sweeps from
// deleting the same id at once.
var rateLimitCleanupMu sync.Mutex

// cancelRateLimitSchedule cancels the booking id of session name. The id is recorded before the
// CP is called and dropped only once the CP confirms, so an Agent that stops at any point in
// between leaves it where the sweep retries it. An error means the record could not be written:
// the delete is still attempted, but a failure of that one is then lost, and a caller that is
// about to forget the id must not.
func cancelRateLimitSchedule(name, id string) error {
	if id == "" {
		return nil
	}
	if err := recordRateLimitCancel(name, id); err != nil {
		log.Printf("rate-limit: could not record the cancellation of %s (%s): %v", id, name, err)
		if derr := dropRateLimitSchedule(id); derr != nil {
			log.Printf("rate-limit: could not cancel the schedule %s of %s either: %v", id, name, derr)
		}
		return err
	}
	finishRateLimitCancel(id)
	return nil
}

// recordRateLimitCancel durably notes that id is to be cancelled.
func recordRateLimitCancel(name, id string) error {
	rateLimitCleanupMu.Lock()
	defer rateLimitCleanupMu.Unlock()
	all, _ := rateLimitCleanups.Read(rateLimitCleanupKey)
	if all == nil {
		all = map[string]rateLimitScheduleCleanup{}
	}
	if _, ok := all[id]; ok {
		return nil
	}
	all[id] = rateLimitScheduleCleanup{Session: name, Since: time.Now().Format(time.RFC3339)}
	return rateLimitCleanups.Write(rateLimitCleanupKey, all)
}

// finishRateLimitCancel deletes a recorded id at the CP and forgets it once confirmed.
func finishRateLimitCancel(id string) {
	rateLimitCleanupMu.Lock()
	defer rateLimitCleanupMu.Unlock()
	all, ok := rateLimitCleanups.Read(rateLimitCleanupKey)
	if !ok {
		return
	}
	c, ok := all[id]
	if !ok {
		return
	}
	settleRateLimitCancel(all, id, c)
	writeRateLimitCleanups(all)
}

// settleRateLimitCancel tries one delete and updates all in place.
func settleRateLimitCancel(all map[string]rateLimitScheduleCleanup, id string, c rateLimitScheduleCleanup) {
	if c.Done {
		forgetIfEpisodeLetGo(all, id, c)
		return
	}
	if err := dropRateLimitSchedule(id); err != nil {
		c.Tries++
		c.LastErr = err.Error()
		all[id] = c
		log.Printf("rate-limit: could not cancel the schedule %s of %s, keeping it to retry: %v", id, c.Session, err)
		return
	}
	if c.Tries > 0 {
		log.Printf("rate-limit: cancelled the schedule %s of %s after %d failed attempt(s)", id, c.Session, c.Tries)
	}
	c.Done, c.LastErr = true, ""
	all[id] = c
	forgetIfEpisodeLetGo(all, id, c)
}

// forgetIfEpisodeLetGo drops a confirmed id once its session's episode no longer names it.
func forgetIfEpisodeLetGo(all map[string]rateLimitScheduleCleanup, id string, c rateLimitScheduleCleanup) {
	if st, ok := RateLimitStates.Read(c.Session); ok && st.ScheduleID == id {
		return
	}
	if t, err := time.Parse(time.RFC3339, c.KeepUntil); err == nil && time.Now().Before(t) {
		return
	}
	delete(all, id)
}

// retainRateLimitCancel keeps id's cancellation on record for d beyond the episode naming it.
func retainRateLimitCancel(id string, d time.Duration) {
	rateLimitCleanupMu.Lock()
	defer rateLimitCleanupMu.Unlock()
	all, _ := rateLimitCleanups.Read(rateLimitCleanupKey)
	c, ok := all[id]
	if !ok {
		return
	}
	c.KeepUntil = time.Now().Add(d).Format(time.RFC3339)
	all[id] = c
	writeRateLimitCleanups(all)
}

func writeRateLimitCleanups(all map[string]rateLimitScheduleCleanup) {
	if len(all) == 0 {
		rateLimitCleanups.Remove(rateLimitCleanupKey)
		return
	}
	if err := rateLimitCleanups.Write(rateLimitCleanupKey, all); err != nil {
		log.Printf("rate-limit: could not update the pending cancellations: %v", err)
	}
}

// retryRateLimitScheduleCleanups retries every pending cancellation once.
func retryRateLimitScheduleCleanups() {
	rateLimitCleanupMu.Lock()
	defer rateLimitCleanupMu.Unlock()
	all, ok := rateLimitCleanups.Read(rateLimitCleanupKey)
	if !ok || len(all) == 0 {
		return
	}
	for id, c := range all {
		settleRateLimitCancel(all, id, c)
	}
	writeRateLimitCleanups(all)
}

// pendingRateLimitCleanups lists the ids still waiting for a confirmed cancellation.
func pendingRateLimitCleanups() map[string]rateLimitScheduleCleanup {
	rateLimitCleanupMu.Lock()
	defer rateLimitCleanupMu.Unlock()
	all, _ := rateLimitCleanups.Read(rateLimitCleanupKey)
	for id, c := range all {
		if c.Done {
			delete(all, id)
		}
	}
	return all
}

// rateLimitCancelRecorded reports whether id was ever recorded for cancellation and not yet
// forgotten — pending or confirmed. Either way the booking is not one to keep.
func rateLimitCancelRecorded(id string) bool {
	rateLimitCleanupMu.Lock()
	defer rateLimitCleanupMu.Unlock()
	all, _ := rateLimitCleanups.Read(rateLimitCleanupKey)
	_, ok := all[id]
	return ok
}
