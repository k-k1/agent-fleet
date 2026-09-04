// Package notice is the durable Agent-side notification outbox. One JSON file per
// event survives Workspace restarts until the Control Plane acknowledges it.
package notice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type Event struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SessionName string `json:"sessionName"`
	SessionKind string `json:"sessionKind"`
	// TargetType is what the notification is ABOUT. Empty = "session", which is what
	// every event was until workspace-level ones appeared (arch-residue), so old
	// agents talking to a new Control Plane keep their meaning. Set it explicitly for
	// anything that is not a session: an empty SessionName under the default target
	// produces a notification pointing at a session the Console cannot open.
	TargetType  string         `json:"targetType,omitempty"`
	DisplayName string         `json:"displayName"`
	CreatedAt   string         `json:"createdAt"`
	Payload     map[string]any `json:"payload"`
}

func dir() string { return filepath.Join(paths.AgentConfigDir(), "notification-outbox") }

func New(kind, sessionName, sessionKind, displayName string) Event {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return Event{ID: hex.EncodeToString(b), Kind: kind, SessionName: sessionName,
		SessionKind: sessionKind, DisplayName: displayName,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), Payload: map[string]any{}}
}

func Put(e Event) error {
	if e.ID == "" {
		return nil
	}
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir(), e.ID+".json"), b, 0o600); err != nil {
		return err
	}
	// docs/log/37 contract 4: as soon as the outbox write lands, also queue the event for
	// the chat bridge. Enqueue writes a single file (the network side is the daemon's
	// sender) and swallows its error, so a bridge outage structurally cannot take the
	// Console notification down with it.
	body, _ := e.Payload["body"].(string) // full-text bridge: the answer body (answer-ready only)
	// P2b: the pending AskUserQuestion payload rides the "question" event so an
	// interact-capable provider can render option buttons. Stored as raw JSON.
	var questions json.RawMessage
	if q, ok := e.Payload["questions"].(json.RawMessage); ok {
		questions = q
	}
	bridge.Enqueue(bridge.Message{Kind: e.Kind, SessionName: e.SessionName,
		SessionKind: e.SessionKind, DisplayName: e.DisplayName, CreatedAt: e.CreatedAt,
		Body: body, Questions: questions})
	return nil
}

// PutOnce persists an event only once for a stable source key. The marker is
// separate from the acked outbox file, so a still-open prompt is not re-enqueued
// on every Control Plane poll.
func PutOnce(key string, e Event) error {
	sum := sha256.Sum256([]byte(key))
	markerDir := filepath.Join(paths.AgentConfigDir(), "notification-markers")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return err
	}
	pruneMarkers(markerDir)
	marker := filepath.Join(markerDir, hex.EncodeToString(sum[:])+".seen")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := Put(e); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(e.CreatedAt), 0o600)
}

// Marker pruning: without it the markers grow monotonically (List() prunes only the
// outbox files). 30 days — well past List()'s 7-day event window — so a marker never
// dies while its prompt could still be re-enqueued. Throttled: PutOnce runs on every
// Control Plane poll and a ReadDir each time would be wasteful.
var (
	markerPruneMu sync.Mutex
	markerPruneAt time.Time
)

func pruneMarkers(markerDir string) {
	markerPruneMu.Lock()
	defer markerPruneMu.Unlock()
	now := time.Now()
	if now.Sub(markerPruneAt) < time.Hour {
		return
	}
	markerPruneAt = now
	entries, err := os.ReadDir(markerDir)
	if err != nil {
		return
	}
	cutoff := now.Add(-30 * 24 * time.Hour)
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".seen") {
			continue
		}
		if info, err := ent.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(markerDir, ent.Name()))
		}
	}
}

func List() []Event {
	entries, err := os.ReadDir(dir())
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	var out []Event
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir(), ent.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e Event
		if json.Unmarshal(b, &e) != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil && ts.Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out
}

func Ack(ids []string) {
	for _, id := range ids {
		if id == "" || strings.ContainsAny(id, `/\\`) {
			continue
		}
		_ = os.Remove(filepath.Join(dir(), id+".json"))
	}
}
