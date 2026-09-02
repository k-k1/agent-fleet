package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
}

func newNotificationAPI(m *manager) notificationAPI {
	return notificationAPI{memberAuth: memberAuth{m}, store: m.store}
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
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	SessionName string         `json:"sessionName"`
	SessionKind string         `json:"sessionKind"`
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

// drainAgentOutbox pulls the Agent's notification outbox into the store and acks it.
//
// res を取らないのは、**Workspace を止める直前にも呼ぶ**から（docs/log/75）。Agent の
// アウトボックスは Console が見に来たときにしか drain されないので、畳んだ直後に
// 止めると「未回答のまま停止しました」の通知が、次に Workspace を起こすまで誰にも
// 届かない — 費用のために止めた結果、止めたことを知らせる通知だけが止めたせいで
// 消える、という一番まずい形になる。
func drainAgentOutbox(ctx context.Context, st store.NotificationStore, rt Runtime, membershipID string) string {
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
		payload, _ := json.Marshal(in.Payload)
		n := store.Notification{EventID: in.ID, MembershipID: membershipID, Kind: in.Kind,
			TargetType: "session", TargetID: in.SessionName, TargetKind: in.SessionKind,
			DisplayName: in.DisplayName, Payload: string(payload), CreatedAt: in.CreatedAt}
		if n.CreatedAt == "" {
			n.CreatedAt = store.NowTS()
		}
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

type usageObservation struct {
	WindowKey string  `json:"windowKey"`
	Percent   float64 `json:"percent"`
	ResetsAt  string  `json:"resetsAt"`
}

func (a notificationAPI) observeUsage(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in struct {
		Source  string             `json:"source"`
		Windows []usageObservation `json:"windows"`
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
		st, found, err := a.store.GetUsageNotificationState(r.Context(), mv.MembershipID, in.Source, obs.WindowKey)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		previousReset, _ := time.Parse(time.RFC3339, st.ResetsAt)
		observedReset, _ := time.Parse(time.RFC3339, obs.ResetsAt)
		reset := found && st.Armed == 1 && st.ResetsAt != "" && observedReset.After(previousReset)
		armed := st.Armed
		if obs.Percent >= 90 {
			armed = 1
		} else if reset {
			armed = 0
		}
		st = store.UsageNotificationState{MembershipID: mv.MembershipID, Source: in.Source, WindowKey: obs.WindowKey, ResetsAt: obs.ResetsAt, Armed: armed}
		if err := a.store.PutUsageNotificationState(r.Context(), st); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if reset {
			payload, _ := json.Marshal(map[string]any{"source": in.Source, "windowKey": obs.WindowKey})
			n := store.Notification{EventID: fmt.Sprintf("usage:%s:%s:%s:%s", mv.MembershipID, in.Source, obs.WindowKey, obs.ResetsAt), MembershipID: mv.MembershipID,
				Kind: "usage-reset", TargetType: "usage", TargetID: in.Source, DisplayName: strings.ToUpper(in.Source), Payload: string(payload), CreatedAt: store.NowTS()}
			if err := a.store.InsertNotification(r.Context(), n); err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
