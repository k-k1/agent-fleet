package sessionx

// The idempotency ledger for create_session. It de-duplicates POST /sessions by idempotency
// key, killing the "the client timed out but the backend did create the session" race - an LLM
// reads that as a failure, retries, and ends up with a second independent session (the double
// launch in docs/log/36). Same idea as the ClientMessageID MsgLedger (agents.MsgLedger,
// docs/log/27 §4/§9.5), in a create-only lightweight form.
//
// No persistence needed: a duplicate retry arrives seconds later, in the same process. A TTL
// ring keeps it from growing.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// createLedgerTTL is one record's lifetime. It must comfortably exceed the slowest launch
// (worktree fetch plus CLI boot), or a create that timed out and was retried (or simply ran
// concurrently) never converges onto the first request and a duplicate session appears. Short
// enough, though, that deliberately launching the same thing again later is not blocked.
const createLedgerTTL = 3 * time.Minute

type createLedgerState int

const (
	createInflight createLedgerState = iota // being created (the first request is doing the work)
	createDone                              // finished (body holds the wireSession JSON)
)

type createLedgerEntry struct {
	state createLedgerState
	body  []byte // the wireSession JSON replayed for idempotency (when state==createDone)
	at    time.Time
}

// createSessionLedger is the in-memory ledger de-duplicating POST /sessions by idempotency key.
type createSessionLedger struct {
	mu sync.Mutex
	m  map[string]*createLedgerEntry
}

var createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}}

// begin claims the right to create under key. It returns (nil, false) when the caller owns
// this create and should carry on. When a record already exists it returns a snapshot of it
// and true, so the caller can replay a done one and reject an inflight one as "being created".
func (l *createSessionLedger) begin(key string) (createLedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()
	if e := l.m[key]; e != nil {
		return *e, true
	}
	l.m[key] = &createLedgerEntry{state: createInflight, at: time.Now()}
	return createLedgerEntry{}, false
}

// complete moves an inflight record to done and stores the body used for replays.
func (l *createSessionLedger) complete(key string, body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		e.state = createDone
		e.body = body
		e.at = time.Now()
	}
}

// fail drops a record that ended while still inflight, letting a genuine retry through. done
// records are kept for replay.
func (l *createSessionLedger) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil && e.state == createInflight {
		delete(l.m, key)
	}
}

// lookup returns a snapshot of the current record (for GET /sessions/idempotency/{key}).
func (l *createSessionLedger) lookup(key string) (createLedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()
	if e := l.m[key]; e != nil {
		return *e, true
	}
	return createLedgerEntry{}, false
}

func (l *createSessionLedger) gcLocked() {
	cutoff := time.Now().Add(-createLedgerTTL)
	for k, e := range l.m {
		if e.at.Before(cutoff) {
			delete(l.m, k)
		}
	}
}

// createIdempotencyKey decides the de-duplication key for a create request.
//   - An explicit key sent by the client (create_session over stdio MCP) is used as is. The
//     tool derives it deterministically from the conversation id plus the arguments, so an LLM
//     retrying with the same arguments reproduces the key and a timed-out retry converges onto
//     the first session.
//   - Without an explicit key it falls back to an intent fingerprint scoped by report_to (the
//     originating conversation), so a client that sends no key (CP MCP) cannot double-create on
//     a retry either. With no conversation scope (an interactive launch from the Console)
//     nothing is de-duplicated - a person is free to launch the same thing twice on purpose.
func createIdempotencyKey(r *CreateReq) string {
	if k := strings.TrimSpace(r.IdempotencyKey); k != "" {
		return k
	}
	if strings.TrimSpace(r.ReportTo) == "" {
		return ""
	}
	h := sha256.New()
	for _, f := range []string{
		r.ReportTo, r.Dir, r.Subdir, r.Kind, r.Model, r.Effort, r.Mode, r.Driver,
		r.InitialPrompt, r.Branch, r.NewBranch, r.RemoteURL, r.RepoName, r.Folder, r.Title,
		strconv.FormatBool(r.Worktree), strconv.FormatBool(r.UseExisting),
	} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return "fp_" + hex.EncodeToString(h.Sum(nil))
}
