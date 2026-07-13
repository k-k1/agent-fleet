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
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type Event struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	SessionName string         `json:"sessionName"`
	SessionKind string         `json:"sessionKind"`
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
	return os.WriteFile(filepath.Join(dir(), e.ID+".json"), b, 0o600)
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
	marker := filepath.Join(markerDir, hex.EncodeToString(sum[:])+".seen")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := Put(e); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(e.CreatedAt), 0o600)
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
