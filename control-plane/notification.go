package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const notificationRetentionDays = 7

type notificationTargetDTO struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
}

type notificationDTO struct {
	Seq         int64                 `json:"seq"`
	ID          string                `json:"id"`
	Kind        string                `json:"kind"`
	Target      notificationTargetDTO `json:"target"`
	DisplayName string                `json:"displayName"`
	// Payload is always a JSON object (boundedPayload): the Console reads its fields directly.
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"createdAt"`
	Seen      bool            `json:"seen"`
}

type notificationAPI struct {
	memberAuth
	store store.NotificationStore
	// limits receives the browser's observations for the limit board (ADR 0111 decision 6).
	// nil leaves the board out, which is what the notification-only tests construct.
	limits interface {
		store.LimitBoardStore
		GetMembershipByID(ctx context.Context, membershipID string) (store.MembershipView, bool, error)
	}
	now func() time.Time
}

func newNotificationAPI(m *manager) notificationAPI {
	return notificationAPI{memberAuth: memberAuth{m}, store: m.store, limits: m.store, now: time.Now}
}

func notificationCutoff() string {
	return time.Now().UTC().Add(-notificationRetentionDays * 24 * time.Hour).Format(time.RFC3339)
}

func notificationToDTO(n store.Notification) notificationDTO {
	return notificationDTO{Seq: n.Seq, ID: n.EventID, Kind: n.Kind,
		Target:      notificationTargetDTO{Type: n.TargetType, ID: n.TargetID, Kind: n.TargetKind},
		DisplayName: n.DisplayName, Payload: json.RawMessage(boundedPayload([]byte(n.Payload))),
		CreatedAt: n.CreatedAt, Seen: n.SeenAt != ""}
}

// The bounds on the payload one notification keeps. The payload is kept as raw JSON and never
// expanded into a map: a response of many small values decodes to several times its byte size.
// A healthy payload is a few KiB (the Agent caps its own at maxPayloadBytes in package notice,
// half of this, so nothing it sends today is cut here).
const (
	maxNotificationPayload      = 256 << 10
	maxNotificationPayloadDepth = 32
	// maxNotificationPayloadValue bounds each member shrinkPayload keeps of an oversized payload.
	maxNotificationPayloadValue = 8 << 10
)

// omittedPayload replaces a payload that is not a JSON object. The rest of the notification is
// still stored and acked: dropping the whole entry would leave it queued at the Agent, where it
// holds back every entry after it.
const omittedPayload = `{"payloadOmitted":true}`

// payloadTruncatedKey marks a payload shrinkPayload cut; the Agent uses the same key.
const payloadTruncatedKey = "payloadTruncated"

// boundedPayload returns raw when it is a JSON object within maxNotificationPayload and
// maxNotificationPayloadDepth, "{}" for an empty or null payload, the shrunk object for one past
// a bound, and omittedPayload for anything else. The result is always a valid UTF-8 JSON object,
// which notificationDTO relies on: an invalid json.RawMessage fails the marshal of the whole
// list, and Postgres refuses invalid UTF-8 in a TEXT column, which would refuse the entry on
// every drain.
func boundedPayload(raw []byte) string {
	t := bytes.Trim(raw, " \t\r\n") // JSON's whitespace only
	if len(t) == 0 || string(t) == "null" {
		return "{}"
	}
	if t[0] != '{' {
		return omittedPayload
	}
	// json.Valid accepts invalid UTF-8 inside a string, and RawMessage keeps it. A map decode
	// replaced each invalid byte with U+FFFD; repairUTF8 does the same.
	if !utf8.Valid(t) {
		t = repairUTF8(t)
	}
	if !json.Valid(t) {
		return omittedPayload
	}
	if len(t) <= maxNotificationPayload && jsonDepthWithin(t, maxNotificationPayloadDepth) {
		return string(t)
	}
	return string(shrinkPayload(t, maxNotificationPayload, maxNotificationPayloadValue, maxNotificationPayloadDepth))
}

// shrinkPayload keeps what fits of a valid JSON object past a bound, in order: each top-level
// member whose value encodes within maxValue (and nests within maxDepth), a long string cut to
// an encoding of maxValue bytes, and a long "questions" list reduced to each question's text, which Web Push
// shows (pushsrv.questionText). What the Console navigates by and the short reasons it words
// (a schedule's status beside a long spec) survive this way. The result carries
// payloadTruncatedKey and stays within max. Each member is decoded raw, so nothing is expanded.
func shrinkPayload(obj []byte, max, maxValue, maxDepth int) []byte {
	const mark = `"` + payloadTruncatedKey + `":true}`
	dec := json.NewDecoder(bytes.NewReader(obj))
	out := []byte{'{'}
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return []byte(omittedPayload)
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			break
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			break
		}
		k, _ := kt.(string)
		if k == payloadTruncatedKey || len(k) > 256 {
			continue
		}
		var keep []byte
		switch {
		case len(v) <= maxValue && jsonDepthWithin(v, maxDepth-1):
			keep = v
		case v[0] == '"':
			var s string
			if json.Unmarshal(v, &s) == nil {
				keep = fitString(s, maxValue)
			}
		case k == "questions":
			keep = questionHeads(v, maxValue)
		}
		if keep == nil {
			continue
		}
		kb, _ := json.Marshal(k)
		if len(out)+len(kb)+len(keep)+2+len(mark) > max {
			continue
		}
		out = append(append(append(append(out, kb...), ':'), keep...), ',')
	}
	return append(out, mark...)
}

// questionHeads reduces a questions list to the first questions' text (leading space trimmed,
// as Web Push trims it) and header, encoded within maxValue, or nil when it does not start as a
// list of objects. It reads the list element by element and stops after maxQuestions, so a list
// of millions of small elements is never decoded.
func questionHeads(v []byte, maxValue int) []byte {
	const maxQuestions = 4
	dec := json.NewDecoder(bytes.NewReader(v))
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return nil
	}
	out := []byte{'['}
	for n := 0; n < maxQuestions && dec.More(); n++ {
		// Raw fields, so a field of the wrong type costs only that field, as it does in
		// pushsrv.questionText, which reads the question alone.
		var q struct {
			Question json.RawMessage `json:"question"`
			Header   json.RawMessage `json:"header"`
		}
		if dec.Decode(&q) != nil {
			return nil
		}
		var question, header string
		_ = json.Unmarshal(q.Question, &question)
		_ = json.Unmarshal(q.Header, &header)
		if n > 0 {
			out = append(out, ',')
		}
		out = append(append(out, `{"question":`...), fitString(strings.TrimSpace(question), maxValue/maxQuestions-192)...)
		if header != "" {
			out = append(append(out, `,"header":`...), fitString(header, 128)...)
		}
		out = append(out, '}')
	}
	if len(out) == 1 {
		return nil
	}
	return append(out, ']')
}

// fitString is the longest head of s, cut on a rune boundary, whose encoding as a JSON string
// fits n bytes. The bound is on the encoding, not the text: an escaped character such as '<'
// takes six bytes. The encoded length only grows with the head, so a binary search finds it.
func fitString(s string, n int) []byte {
	s = headBytes(s, n)
	if b, _ := json.Marshal(s); len(b) <= n {
		return b
	}
	lo, hi := 0, len(s) // the head of lo bytes fits, the head of hi does not
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if b, _ := json.Marshal(headBytes(s, mid)); len(b) <= n {
			lo = mid
		} else {
			hi = mid
		}
	}
	b, _ := json.Marshal(headBytes(s, lo))
	return b
}

// repairUTF8 replaces each byte that is not part of valid UTF-8 with U+FFFD, one per byte, which
// is what decoding the JSON into a Go string does.
func repairUTF8(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/8)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			out = append(out, "\uFFFD"...)
		} else {
			out = append(out, b[:size]...)
		}
		b = b[size:]
	}
	return out
}

// headBytes cuts s to at most n bytes without splitting a UTF-8 sequence.
func headBytes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// jsonDepthWithin reports whether no object or array in b nests deeper than max. It only counts
// brackets outside strings, so it is meaningful for valid JSON alone; callers check validity.
func jsonDepthWithin(b []byte, max int) bool {
	depth, inStr, esc := 0, false, false
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
			if depth > max {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}

type agentNotification struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SessionName string `json:"sessionName"`
	SessionKind string `json:"sessionKind"`
	// Empty = "session" (see drainAgentOutbox): the Agent only learned to send
	// workspace-level events later, and old Agents must keep their meaning.
	TargetType  string `json:"targetType"`
	DisplayName string `json:"displayName"`
	CreatedAt   string `json:"createdAt"`
	// Raw, not a map: any JSON value decodes, so a payload of the wrong type no longer fails the
	// entry, and boundedPayload sees the bytes before anything is expanded.
	Payload json.RawMessage `json:"payload"`
}

func (a notificationAPI) drainAgent(ctx context.Context, res *resolved) string {
	if res.rt.State(ctx) != "running" {
		return "offline"
	}
	return drainAgentOutbox(ctx, a.store, res.rt, res.mv.MembershipID)
}

// agentEventIDPrefix namespaces the event ids a workspace's Agent sends. Every id the Control
// Plane generates itself (schedules, usage, deadlines, hand-offs) is built from other prefixes,
// so an id chosen inside a workspace can never equal one of them.
const agentEventIDPrefix = "agent:"

// agentNotificationRow turns one outbox entry into the row stored for membershipID. The stored
// id carries agentEventIDPrefix and the row is unique per (membership, id), so the same id sent
// twice by one Agent is still one row, and another member's Agent cannot take it over.
func agentNotificationRow(in agentNotification, membershipID string) store.Notification {
	// The Agent's outbox was session-only until workspace-level events appeared
	// (arch-residue). An empty targetType therefore has to keep meaning "session",
	// or an older Agent's events would all become untargeted after a CP upgrade.
	// For a non-session target, leave id/kind empty rather than passing the
	// (empty) session name through: the Console guards on target.type before
	// navigating, and a "session" target with a blank id is one it cannot open.
	targetType, targetID, targetKind := "session", in.SessionName, in.SessionKind
	if in.TargetType != "" && in.TargetType != "session" {
		targetType, targetID, targetKind = in.TargetType, "", ""
	}
	n := store.Notification{EventID: agentEventIDPrefix + in.ID, MembershipID: membershipID, Kind: in.Kind,
		TargetType: targetType, TargetID: targetID, TargetKind: targetKind,
		DisplayName: in.DisplayName, Payload: boundedPayload(in.Payload), CreatedAt: in.CreatedAt}
	if n.CreatedAt == "" {
		n.CreatedAt = store.NowTS()
	}
	return n
}

// The bounds on one /notifications response, vars only so a test can lower them. The Agent
// returns at most 100 entries and caps each entry's payload, but an older Agent does not, and the
// drain runs on a timer for every workspace, so the CP stops reading at a byte count and an entry
// count far above anything a healthy Agent sends. Entries read whole before a bound is hit are stored and
// acked; the rest stays queued for the next drain.
var (
	maxOutboxBody    int64 = 64 << 20
	maxOutboxEntries       = 200
)

// boundedReader is io.LimitReader that remembers it stopped the read, so a body cut by the
// bound can be told from one the Agent cut short.
type boundedReader struct {
	r   io.Reader
	n   int64
	hit bool
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.n <= 0 {
		b.hit = true
		return 0, io.EOF
	}
	if int64(len(p)) > b.n {
		p = p[:b.n]
	}
	n, err := b.r.Read(p)
	b.n -= int64(n)
	return n, err
}

// readOutboxEntries decodes {"notifications":[...]} entry by entry. A null or empty list is an
// empty success (the Agent sends null for an empty outbox). The body must be complete: a
// connection cut mid-body is not ok and yields nothing. Only a bound (bytes or entries) may
// end the read early; then the entries read whole are returned with more=true, and the caller
// asks again after acking them.
func readOutboxEntries(r io.Reader, limit int64) (entries []agentNotification, more, ok bool) {
	br := &boundedReader{r: r, n: limit}
	dec := json.NewDecoder(br)
	cut := func() ([]agentNotification, bool, bool) {
		if br.hit && len(entries) > 0 {
			return entries, true, true
		}
		return nil, false, false
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false, false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return cut()
		}
		if key != "notifications" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return cut()
			}
			continue
		}
		t, err := dec.Token()
		if err != nil {
			return cut()
		}
		if t == nil {
			continue
		}
		if t != json.Delim('[') {
			return nil, false, false
		}
		for dec.More() {
			if len(entries) >= maxOutboxEntries {
				return entries, true, true
			}
			var in agentNotification
			if dec.Decode(&in) != nil {
				return cut()
			}
			// Bounded here as well as in agentNotificationRow, so an oversized payload is not
			// held while the rest of the response is read.
			in.Payload = json.RawMessage(boundedPayload(in.Payload))
			entries = append(entries, in)
		}
		if t, err := dec.Token(); err != nil || t != json.Delim(']') {
			return cut()
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return cut()
	}
	return entries, false, true
}

// drainAgentOutbox pulls the Agent's notification outbox into the store and acks it.
//
// It deliberately takes no *resolved, because it is also called just before a workspace
// is stopped. The Agent's outbox is only drained when the Console comes looking, so
// stopping right after folding one up would hold "stopped with the question unanswered"
// until somebody woke the workspace again — the stop-for-cost decision swallowing the one
// notification that announces it.
func drainAgentOutbox(ctx context.Context, st store.NotificationStore, rt runtime.Runtime, membershipID string) string {
	state := "offline"
	var acked []string
	for round := 0; round < maxDrainRounds; round++ {
		var more bool
		state, more, acked = drainAgentOutboxOnce(ctx, st, rt, membershipID, acked)
		if state != "ready" || !more || ctx.Err() != nil {
			break
		}
	}
	return state
}

// maxDrainRounds bounds how many times one drain asks again after a response cut by a bound.
const maxDrainRounds = 8

// drainAgentOutboxOnce reads one response. more is true when a bound cut it and the entries
// read were stored and the Agent answered the ack with a 2xx, so asking again can make
// progress. acked is what this call acked; the caller passes it back as prevAcked.
//
// When every id in prevAcked is read again, the Agent dropped none of them, and storing and
// acking the same response again would only repeat the work: the call stops before either.
// The whole set is compared, not the first id, because the Agent answers the ack with a 204
// even when some deletes failed, so the first id can survive while the rest moved on.
func drainAgentOutboxOnce(ctx context.Context, st store.NotificationStore, rt runtime.Runtime, membershipID string, prevAcked []string) (state string, more bool, acked []string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rt.Endpoint()+"/notifications", nil)
	if tok := rt.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "offline", false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "unsupported", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "offline", false, nil
	}
	// Bounded by maxOutboxBody / maxOutboxEntries; see there. An unreadable first entry stores
	// nothing, so a single entry past the byte bound stays queued; only an Agent without the
	// payload cap in package notice can send one.
	entries, more, ok := readOutboxEntries(resp.Body, maxOutboxBody)
	resp.Body.Close() // before the ack, so one connection is not held across both requests
	if !ok {
		return "offline", false, nil
	}
	if noneDropped(prevAcked, entries) {
		return "ready", false, nil
	}
	acked = make([]string, 0, len(entries))
	for _, in := range entries {
		n := agentNotificationRow(in, membershipID)
		if err := st.InsertNotification(ctx, n); err != nil {
			return "offline", false, nil
		}
		acked = append(acked, in.ID)
	}
	if len(acked) > 0 {
		b, _ := json.Marshal(map[string]any{"ids": acked})
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, rt.Endpoint()+"/notifications/ack", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if tok := rt.Token(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		ackResp, err := agentHTTPClient.Do(req)
		if err != nil {
			return "ready", false, acked
		}
		ackResp.Body.Close()
		if ackResp.StatusCode/100 != 2 {
			return "ready", false, acked
		}
	}
	if len(acked) == 0 {
		return "ready", false, nil
	}
	return "ready", more, acked
}

// noneDropped reports whether every id acked last round is among the entries read now.
func noneDropped(prevAcked []string, entries []agentNotification) bool {
	if len(prevAcked) == 0 {
		return false
	}
	read := make(map[string]struct{}, len(entries))
	for _, in := range entries {
		read[in.ID] = struct{}{}
	}
	for _, id := range prevAcked {
		if _, ok := read[id]; !ok {
			return false
		}
	}
	return true
}

// listPayload composes the GET /api/notifications body (drain the Agent's
// outbox into the store, then list). Shared by the REST handler and the
// /api/events push channel so both emit the identical shape.
func (a notificationAPI) listPayload(ctx context.Context, res *resolved) (map[string]any, *apiError) {
	sourceState := a.drainAgent(ctx, res)
	cutoff := notificationCutoff()
	_ = a.store.SweepNotifications(ctx, cutoff)
	rows, err := a.store.ListNotifications(ctx, res.mv.MembershipID, cutoff, 50)
	if err != nil {
		return nil, internalErr(err)
	}
	unseen, err := a.store.CountUnseenNotifications(ctx, res.mv.MembershipID, cutoff)
	if err != nil {
		return nil, internalErr(err)
	}
	items := make([]notificationDTO, 0, len(rows))
	var maxSeq int64
	for _, n := range rows {
		items = append(items, notificationToDTO(n))
		if n.Seq > maxSeq {
			maxSeq = n.Seq
		}
	}
	return map[string]any{"items": items, "maxSeq": maxSeq, "unseenCount": unseen, "sourceState": sourceState}, nil
}

func (a notificationAPI) list(w http.ResponseWriter, r *http.Request, res *resolved) {
	p, aerr := a.listPayload(r.Context(), res)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a notificationAPI) seen(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in struct {
		ThroughSeq int64    `json:"throughSeq"`
		EventIDs   []string `json:"eventIds"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	now := store.NowTS()
	if in.ThroughSeq > 0 {
		if err := a.store.MarkNotificationsSeenThrough(r.Context(), mv.MembershipID, in.ThroughSeq, now); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	if err := a.store.MarkNotificationsSeen(r.Context(), mv.MembershipID, in.EventIDs, now); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// usageObservation is one window the Console's usage chip read. ObservedAt, Derived and Stale
// carry the observation contract of ADR 0111 decision 6: ObservedAt is the provider's own
// observation time as the Agent reported it (never the time the browser sends), and a window
// without it, or derived, or stale, never reaches the limit board. An older Console sends none
// of them, and its windows then only drive the reset notification, as before.
type usageObservation struct {
	WindowKey  string  `json:"windowKey"`
	Percent    float64 `json:"percent"`
	ResetsAt   string  `json:"resetsAt"`
	ObservedAt string  `json:"observedAt,omitempty"`
	Derived    bool    `json:"derived,omitempty"`
	Stale      bool    `json:"stale,omitempty"`
}

// usageWindowMins is the length of the two windows the chip posts.
var usageWindowMins = map[string]int{"5h": 5 * 60, "7d": 7 * 24 * 60}

func (a notificationAPI) observeUsage(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in struct {
		Source  string             `json:"source"`
		Windows []usageObservation `json:"windows"`
		// LimitEpoch is the opaque login epoch the Agent stamped on the reading, and
		// LimitEpochSince that login's time, which orders it against the stored epoch.
		LimitEpoch      string `json:"limitEpoch,omitempty"`
		LimitEpochSince string `json:"limitEpochSince,omitempty"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	in.Source = strings.ToLower(strings.TrimSpace(in.Source))
	if in.Source != "claude" && in.Source != "codex" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_source", "source must be claude or codex"})
		return
	}
	for _, obs := range in.Windows {
		if obs.WindowKey != "5h" && obs.WindowKey != "7d" || obs.Percent < 0 || obs.Percent > 100 {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_window", "invalid usage window"})
			return
		}
		if _, err := time.Parse(time.RFC3339, obs.ResetsAt); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_resets_at", "resetsAt must be RFC3339"})
			return
		}
	}
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	a.recordBoardObservations(r.Context(), mv, in.Source, in.LimitEpoch, in.LimitEpochSince, in.Windows, now)
	// The reset notification is fenced by the same login generation as the board, but on its
	// own row per source, so it keeps working with the tenant's board switched off. The whole
	// read-decide-write runs in one store transaction (ApplyUsageObservation).
	gen := ""
	if in.LimitEpoch != "" {
		gen, _ = limitEpochSince(in.LimitEpochSince, now)
	}
	byKey := map[string]usageObservation{}
	var keys []string
	for _, obs := range in.Windows {
		// The reset notification's armed state is never set from a derived or stale value: a
		// window rolled past its reset is the Agent's guess, and an old reading is no evidence
		// that the limit is still (or no longer) hit.
		if obs.Derived || obs.Stale {
			continue
		}
		if obs.ObservedAt != "" {
			if t, err := time.Parse(time.RFC3339, obs.ObservedAt); err != nil || now.Sub(t) > limitStaleAfter {
				continue
			}
		}
		if _, dup := byKey[obs.WindowKey]; !dup {
			keys = append(keys, obs.WindowKey)
		}
		byKey[obs.WindowKey] = obs
	}
	if len(keys) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	epoch := ""
	if gen != "" {
		epoch = in.LimitEpoch
	}
	_, err := a.store.ApplyUsageObservation(r.Context(), mv.MembershipID, in.Source, epoch, gen, keys,
		func(key string, st store.UsageNotificationState, found bool) (store.UsageNotificationState, *store.Notification) {
			obs := byKey[key]
			previousReset, _ := time.Parse(time.RFC3339, st.ResetsAt)
			observedReset, _ := time.Parse(time.RFC3339, obs.ResetsAt)
			reset := found && st.Armed == 1 && st.ResetsAt != "" && observedReset.After(previousReset)
			armed := st.Armed
			if obs.Percent >= 90 {
				armed = 1
			} else if reset {
				armed = 0
			}
			next := store.UsageNotificationState{MembershipID: mv.MembershipID, Source: in.Source, WindowKey: key,
				ResetsAt: obs.ResetsAt, Armed: armed}
			if !reset {
				return next, nil
			}
			payload, _ := json.Marshal(map[string]any{"source": in.Source, "windowKey": key})
			return next, &store.Notification{EventID: fmt.Sprintf("usage:%s:%s:%s:%s", mv.MembershipID, in.Source, key, obs.ResetsAt),
				MembershipID: mv.MembershipID, Kind: "usage-reset", TargetType: "usage", TargetID: in.Source,
				DisplayName: strings.ToUpper(in.Source), Payload: string(payload), CreatedAt: store.NowTS()}
		})
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// recordBoardObservations is the browser fallback of the limit board. The board is fed only
// when the Agent stamped both an observation time and a login epoch; the time this post was
// sent is never used in their place.
//
// A post can arrive long after it was read (a tab that slept). The store refuses one whose
// epoch is not the current one unless its login time is later, so a late post from before a
// re-login never brings the previous account back.
func (a notificationAPI) recordBoardObservations(ctx context.Context, mv store.MembershipView, kind, epoch, epochSince string,
	windows []usageObservation, now time.Time) {
	if a.limits == nil || epoch == "" || len(epoch) > 64 {
		return
	}
	since, ok := limitEpochSince(epochSince, now)
	if !ok {
		return
	}
	tenantID := mv.TenantID
	if tenantID == "" {
		if m, ok, err := a.limits.GetMembershipByID(ctx, mv.MembershipID); err == nil && ok {
			tenantID = m.TenantID
		}
	}
	if on, err := a.limits.LimitBoardEnabled(ctx, tenantID); err != nil || !on {
		return
	}
	units := make([]limitUnitWire, 0, len(windows))
	for _, w := range windows {
		if w.Stale || w.ObservedAt == "" {
			continue
		}
		units = append(units, limitUnitWire{Unit: w.WindowKey, UsedPct: w.Percent, ResetsAt: w.ResetsAt,
			WindowMins: usageWindowMins[w.WindowKey], ObservedAt: w.ObservedAt, Derived: w.Derived})
	}
	obs := validLimitUnits(kind, units, now, "browser")
	if len(obs) == 0 {
		return
	}
	if _, err := a.limits.RecordLimitObservations(ctx, mv.MembershipID, kind, epoch, since, now.UTC().Format(time.RFC3339), obs); err != nil {
		log.Printf("limit board: browser observation %s/%s: %v", mv.MembershipID, kind, err)
	}
}
