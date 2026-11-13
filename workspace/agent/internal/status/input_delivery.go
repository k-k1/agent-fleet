package status

// A hook-less kind's input can fail to reach its CLI (delivery lost, a key the CLI swallows, an
// input that makes no user step). Its stored "working" would then stand until the next input,
// because a turn end only counts after the new input shows up in the kind's source. These two
// writes end that state on positive evidence and take it back when the input turns up late.

// PersistInputNotDelivered settles a working record to idle, flagged InputNotDelivered, only if it
// is still the record (wasRev) the caller judged. TurnEnd stays false and the turn mark moves to
// the new record, so a late input is still measured against the original baseline. It reports
// whether it wrote.
func PersistInputNotDelivered(sid, wasRev, mark string) bool {
	unlock, ok := lockSid(sid)
	defer unlock()
	if !ok {
		return false
	}
	st, has := Read(sid)
	if !has || st.State != "working" || st.Rev != wasRev || st.InputNotDelivered {
		return false
	}
	rev := persistLocked(sid, SessionStatus{State: "idle", InputNotDelivered: true})
	if mark != "" {
		_ = turnMarks.Write(sid, turnMark{Rev: rev, Mark: mark})
	}
	return true
}

// ReopenDelivered is the reverse: the input shows up after all, so the idle that
// PersistInputNotDelivered wrote (still the record wasRev) goes back to working, carrying the mark
// forward. The turn then ends like any other: a new input after the mark followed by an end.
func ReopenDelivered(sid, wasRev, mark string) bool {
	unlock, ok := lockSid(sid)
	defer unlock()
	if !ok {
		return false
	}
	st, has := Read(sid)
	if !has || st.State != "idle" || !st.InputNotDelivered || st.Rev != wasRev {
		return false
	}
	rev := persistLocked(sid, SessionStatus{State: "working"})
	RemoveCompletionKey(sid)
	if mark != "" {
		_ = turnMarks.Write(sid, turnMark{Rev: rev, Mark: mark})
	}
	return true
}

// InputNotDelivered reports whether the record currently says so.
func InputNotDelivered(sid string) bool {
	st, ok := Read(sid)
	return ok && st.State == "idle" && st.InputNotDelivered
}
