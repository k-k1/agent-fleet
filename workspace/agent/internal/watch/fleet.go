package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The fleet source of ADR 0133 §9 ("Agent Fleet itself"): events of another session of the same
// Workspace, so a parent waiting on a child (a reviewer, an implementer) is told when it is done
// instead of keeping a timer as a backstop.
//
// ref is a session name. The subject is "fleet:session/<name>" and every type below is about that
// one session. The engine knows no session: the Agent supplies Fleet, which answers from the
// state the session list shows, so this file holds no second idle classifier.
//
// Versions (ADR 0133 §5):
//   - fleet.session.idle: the session's last turn end. One turn end is one notice, and a restart
//     of the Agent re-reads the same time, so it is never told twice.
//   - fleet.session.stopped: a count of the stops this source has seen, kept in the subject's
//     cursor; a session that is resumed and stopped again is a new version.
//   - fleet.quota.reopened: the usage-limit episode that ended, so each episode is told once.

// Types of the fleet source.
const (
	FleetKind          = "fleet"
	FleetIdle          = "fleet.session.idle"
	FleetStopped       = "fleet.session.stopped"
	FleetQuotaReopened = "fleet.quota.reopened"

	fleetSubjectPrefix = "fleet:session/"
	// fleetMinPoll bounds how often one session is read: the read is the session list's own
	// (it can capture a pane), so it is not free, and a notice needs no better than seconds.
	fleetMinPoll = 10 * time.Second
	fleetPoll    = 15 * time.Second
)

// FleetObservation is one session as the session list reports it.
type FleetObservation struct {
	// Alive is false for a stopped session.
	Alive bool
	// Idle is the list's idle classification: waiting for input and nothing else (a question,
	// a plan, a permission or a limit wait is not idle).
	Idle bool
	// LastTurnEndAt is the list's last turn end (RFC 3339); "" when the session has had none.
	LastTurnEndAt string
	// Limit is the open usage-limit episode's identity ("" when none is open).
	Limit string
}

// Fleet is what the fleet source asks of the Agent.
type Fleet interface {
	// Visible says whether caller may watch name: the sessions its own list_child_sessions or
	// list_peer_sessions show. caller is never "" and never equal to name.
	Visible(caller, name string) bool
	// Observe reads one session. ok is false when it no longer exists or is archived.
	Observe(name string) (obs FleetObservation, ok bool)
}

// FleetSource is the source of Agent Fleet's own events.
type FleetSource struct {
	Fleet Fleet
}

func (*FleetSource) Kind() string { return FleetKind }
func (*FleetSource) Types() []string {
	return []string{FleetIdle, FleetStopped, FleetQuotaReopened}
}
func (*FleetSource) MinPollInterval() time.Duration { return fleetMinPoll }

// fleetCursor is what one subject keeps between reads.
type fleetCursor struct {
	Stops   int    `json:"stops,omitempty"`   // stops seen so far
	Stopped bool   `json:"stopped,omitempty"` // the last read found it stopped
	Limit   string `json:"limit,omitempty"`   // the open episode seen last, until its end is told
}

// Resolve checks the reference names a session the caller may see. A session that is idle now
// is recorded as already told for its current turn end (Resolved.Told): that turn end is history,
// and waiting on a session that was just messaged must not end at once on the turn before the
// message.
func (f *FleetSource) Resolve(ctx context.Context, ref, _ string) (Resolved, error) {
	name := strings.TrimSpace(ref)
	if name == "" {
		return Resolved{}, &ErrRefused{"ref is required: the name of a session (from list_child_sessions or list_peer_sessions)"}
	}
	caller := SubscriberFrom(ctx)
	if caller == "" {
		return Resolved{}, &ErrRefused{"cannot tell which session is asking"}
	}
	if name == caller || name == OwnerFrom(ctx) {
		return Resolved{}, &ErrRefused{"a session cannot watch itself; name a child or a peer"}
	}
	// The same refusal for "no such session" and "not yours to see": the answer must not tell a
	// session which names exist outside its own list.
	refused := &ErrRefused{"no session " + quoteShort(name) + " that you can see; use a name from list_child_sessions or list_peer_sessions"}
	if !TokenOK(fleetSubjectPrefix+name) || !f.Fleet.Visible(caller, name) {
		return Resolved{}, refused
	}
	obs, ok := f.Fleet.Observe(name)
	if !ok {
		return Resolved{}, refused
	}
	res := Resolved{Subject: SubjectID(fleetSubjectPrefix + name)}
	if obs.Alive && obs.Idle && obs.LastTurnEndAt != "" {
		res.Told = map[string]string{FleetIdle: obs.LastTurnEndAt}
	}
	return res, nil
}

// Poll reports the conditions of one session that hold now.
func (f *FleetSource) Poll(_ context.Context, subject SubjectID, raw json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	name, ok := strings.CutPrefix(string(subject), fleetSubjectPrefix)
	if !ok {
		return nil, raw, Hint{}, fmt.Errorf("not a fleet subject: %q", subject)
	}
	var cur fleetCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	obs, ok := f.Fleet.Observe(name)
	if !ok {
		return nil, raw, Hint{}, &ErrGone{"session " + name + " no longer exists"}
	}
	var evs []Event
	add := func(typ, version, summary string) {
		evs = append(evs, Event{Source: FleetKind, Subject: subject, Type: typ, Version: version, Summary: summary, At: time.Now().UTC()})
	}
	if obs.Alive {
		cur.Stopped = false
		if obs.Idle && obs.LastTurnEndAt != "" {
			add(FleetIdle, obs.LastTurnEndAt, "Session "+name+" ended its turn and is idle (turn end "+obs.LastTurnEndAt+")")
		}
	} else {
		if !cur.Stopped {
			cur.Stops++
			cur.Stopped = true
		}
		add(FleetStopped, strconv.Itoa(cur.Stops), "Session "+name+" is stopped")
	}
	switch {
	case obs.Limit != "":
		cur.Limit = obs.Limit
	case cur.Limit != "":
		// Told once from the pending queue; the cursor forgets it so a later watch is not told
		// about an episode that ended before it was made.
		add(FleetQuotaReopened, cur.Limit, "Session "+name+" can work again: its usage limit has lifted")
		cur.Limit = ""
	}
	out, _ := json.Marshal(cur)
	return evs, out, Hint{After: fleetPoll}, nil
}
