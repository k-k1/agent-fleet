package opencode

// Reading an opencode 2 conversation without its daemon.
//
// v2 keeps a conversation in session_v2 / session_message and never writes the v1 tables, and the
// state readers (v2state.go) go through the daemon's API. The daemon folds up when nothing depends
// on it, and a stopped session is not a dependent, so a stopped session's history would read as
// empty although every message is still in opencode.db. When the daemon cannot be reached the
// transcript is read from the store, read-only, and run through the same v2Turns as the API's
// answer: session_message.data is the API's message without its id and type, which are columns.
//
// The same store reader serves a --standalone Terminal session, whose private server af cannot
// reach (v2TargetFor). This leans on a private schema, which is why it is not the default where a
// daemon can answer. Anything the store cannot answer (the form list, the inbox, the running set)
// degrades to "nothing" because those only exist on a running server.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var errV2Offline = errors.New("opencode api: reading from the store, no daemon")

// daemonGone: err is a failure to reach the daemon, or a connection cut while its answer was being
// read, and not an answer from it. A refusal (statusError) and a body that arrives whole and does
// not parse (an HTML page, a truncated JSON document, a changed contract) are answers: the store
// would show an older conversation than the one the daemon holds, or hide that its contract moved.
func daemonGone(err error) bool {
	if err == nil {
		return false
	}
	var ue *url.Error
	var ne net.Error
	var be bodyReadError
	return errors.Is(err, errV2Down) || errors.As(err, &ue) || errors.As(err, &ne) || errors.As(err, &be)
}

// v2Kid is a subagent's conversation: when it was created (messages older than that are copies)
// and what it said.
type v2Kid struct {
	Born int64
	Msgs []v2Msg
}

// v2Conv is a conversation read as one unit, the target it came from, and its subagents'.
type v2Conv struct {
	T    apiTarget
	Info *v2SessionInfo
	Msgs []v2Msg
	Kids []v2Kid
}

// readConv reads info's messages and its subagents' through t. Only an unreachable daemon is an
// error; any other failure leaves what was gathered, like readSession.
func (t apiTarget) readConv(info *v2SessionInfo) (v2Conv, error) {
	c := v2Conv{T: t, Info: info}
	var err error
	if c.Msgs, err = t.messages(info.ID); daemonGone(err) {
		return c, err
	}
	for _, child := range v2Children(c.Msgs) {
		var k v2Kid
		ci, err := t.sessionInfo(child)
		if daemonGone(err) {
			return c, err
		}
		if err == nil {
			k.Born = ci.Time.Created
		}
		cm, err := t.messages(child)
		if daemonGone(err) {
			return c, err
		}
		if err != nil && len(cm) == 0 {
			continue
		}
		k.Msgs = cm
		c.Kids = append(c.Kids, k)
	}
	return c, nil
}

// conversation resolves the slot's conversation and reads it, subagents included, through the
// daemon. When the daemon cannot be reached at any step, everything is read again, whole, from the
// store: a partial answer from the API is never joined to the store's. The conversation carries the
// target every later read of it has to use.
func (t apiTarget) conversation(m session.Meta) (v2Conv, error) {
	info, err := t.v2Session(m)
	if err == nil && info != nil {
		var c v2Conv
		if c, err = t.readConv(info); !daemonGone(err) {
			return c, nil
		}
	}
	if !daemonGone(err) {
		return v2Conv{T: t, Info: info}, err
	}
	off := apiTarget{offline: true}
	info, err = off.v2Session(m)
	if err != nil || info == nil {
		return v2Conv{T: off, Info: info}, err
	}
	c, _ := off.readConv(info)
	return c, nil
}

// openV2Store opens the database the v2 opencode of this workspace writes.
func openV2Store() (*sql.DB, error) {
	db, ok := openRODB(dbPath())
	if !ok {
		return nil, errors.New("opencode store: not found")
	}
	return db, nil
}

const storeSessionCols = `SELECT id, COALESCE(parent_id, ''), COALESCE(agent, ''), COALESCE(model, ''),
	COALESCE(directory, ''), time_created, time_updated FROM session_v2`

func scanStoreSession(row interface{ Scan(...any) error }) (*v2SessionInfo, error) {
	var s v2SessionInfo
	var model string
	if err := row.Scan(&s.ID, &s.ParentID, &s.Agent, &model, &s.Location.Directory, &s.Time.Created, &s.Time.Updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(model), &s.Model) // best-effort: only the model's name is shown
	return &s, nil
}

// storeSessionByID is one conversation's record; the not-found answer is the API's 404.
func storeSessionByID(id string) (*v2SessionInfo, error) {
	db, err := openV2Store()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	s, err := scanStoreSession(db.QueryRow(storeSessionCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, statusError(http.StatusNotFound)
	}
	return s, err
}

// storeSession is v2Session on the store: the id recorded for the slot, else the newest root
// conversation in its directory that the slot can have opened. nil when there is none.
func storeSession(m session.Meta) (*v2SessionInfo, error) {
	if id := sids.Read(session.UUID(m.Dir, m.Name)); id != "" {
		switch s, err := storeSessionByID(id); {
		case err == nil:
			return s, nil
		case !errors.Is(err, statusError(http.StatusNotFound)):
			return nil, err
		}
	}
	db, err := openV2Store()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	s, err := scanStoreSession(db.QueryRow(storeSessionCols+
		` WHERE directory = ? AND (parent_id IS NULL OR parent_id = '') AND time_created >= ?
		 ORDER BY time_updated DESC, id DESC LIMIT 1`, m.CWD(), metaCreatedMs(m)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// storeMessages reads a whole conversation, oldest first.
func storeMessages(ses string) ([]v2Msg, error) {
	db, err := openV2Store()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, type, data FROM session_message WHERE session_id = ? ORDER BY seq`, ses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []v2Msg
	for rows.Next() {
		var id, typ, data string
		if err := rows.Scan(&id, &typ, &data); err != nil {
			return out, err
		}
		var msg v2Msg
		if json.Unmarshal([]byte(data), &msg) != nil {
			continue // a payload this reader does not parse is skipped, not a reason to show nothing
		}
		msg.ID, msg.Type = id, typ
		out = append(out, msg)
	}
	return out, rows.Err()
}
