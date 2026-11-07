package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

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
	Payload     map[string]any        `json:"payload"`
	CreatedAt   string                `json:"createdAt"`
	Seen        bool                  `json:"seen"`
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
	p := map[string]any{}
	_ = json.Unmarshal([]byte(n.Payload), &p)
	return notificationDTO{Seq: n.Seq, ID: n.EventID, Kind: n.Kind,
		Target:      notificationTargetDTO{Type: n.TargetType, ID: n.TargetID, Kind: n.TargetKind},
		DisplayName: n.DisplayName, Payload: p, CreatedAt: n.CreatedAt, Seen: n.SeenAt != ""}
}

type agentNotification struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SessionName string `json:"sessionName"`
	SessionKind string `json:"sessionKind"`
	// Empty = "session" (see drainAgentOutbox): the Agent only learned to send
	// workspace-level events later, and old Agents must keep their meaning.
	TargetType  string         `json:"targetType"`
	DisplayName string         `json:"displayName"`
	CreatedAt   string         `json:"createdAt"`
	Payload     map[string]any `json:"payload"`
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
	payload, _ := json.Marshal(in.Payload)
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
		DisplayName: in.DisplayName, Payload: string(payload), CreatedAt: in.CreatedAt}
	if n.CreatedAt == "" {
		n.CreatedAt = store.NowTS()
	}
	return n
}

// drainAgentOutbox pulls the Agent's notification outbox into the store and acks it.
//
// It deliberately takes no *resolved, because it is also called just before a workspace
// is stopped. The Agent's outbox is only drained when the Console comes looking, so
// stopping right after folding one up would hold "stopped with the question unanswered"
// until somebody woke the workspace again — the stop-for-cost decision swallowing the one
// notification that announces it.
func drainAgentOutbox(ctx context.Context, st store.NotificationStore, rt runtime.Runtime, membershipID string) string {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rt.Endpoint()+"/notifications", nil)
	if tok := rt.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "offline"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "unsupported"
	}
	if resp.StatusCode != http.StatusOK {
		return "offline"
	}
	var body struct {
		Notifications []agentNotification `json:"notifications"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return "offline"
	}
	acked := make([]string, 0, len(body.Notifications))
	for _, in := range body.Notifications {
		n := agentNotificationRow(in, membershipID)
		if err := st.InsertNotification(ctx, n); err != nil {
			return "offline"
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
		if ackResp, err := agentHTTPClient.Do(req); err == nil {
			ackResp.Body.Close()
		}
	}
	return "ready"
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
