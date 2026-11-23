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
	"syscall"
	"time"
	"unicode/utf8"

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
	// NoBridge keeps the event out of the chat bridge: it goes to the notification center only.
	// For an event whose chat delivery is decided elsewhere (a scheduled run that named its own
	// targets, #1560). Not persisted: it only steers Put.
	NoBridge bool `json:"-"`
}

func dir() string { return filepath.Join(paths.AgentStateDir(), "notification-outbox") }

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
	stored := e
	stored.Payload = boundPayload(e.Payload)
	b, err := json.Marshal(stored)
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
	if e.NoBridge {
		return nil
	}
	body, _ := e.Payload["body"].(string) // full-text bridge: the answer body (answer-ready only)
	// P2b: the pending AskUserQuestion payload rides the "question" event so an
	// interact-capable provider can render option buttons. Stored as raw JSON.
	var questions json.RawMessage
	if q, ok := e.Payload["questions"].(json.RawMessage); ok {
		questions = q
	}
	permTok, _ := e.Payload["permissionToken"].(string)
	bridge.Enqueue(bridge.Message{Kind: e.Kind, SessionName: e.SessionName,
		SessionKind: e.SessionKind, DisplayName: e.DisplayName, CreatedAt: e.CreatedAt,
		Body: body, Questions: questions, PermissionToken: permTok})
	return nil
}

// The bound on the payload one outbox entry keeps. The Control Plane reads the outbox with a byte
// bound per response, and an entry it cannot read whole stays queued ahead of every later one, so
// an entry has to stay far below that bound. The Control Plane also keeps at most twice this per
// notification (maxNotificationPayload), so nothing that fits here is cut there. A healthy
// payload is a few KiB; the largest, an answer's body, is capped at chatx.BridgeBodyCap runes.
const (
	maxPayloadBytes = 128 << 10
	// maxPayloadValue bounds each value kept when an oversized payload is shrunk.
	maxPayloadValue = 8 << 10
)

// payloadTruncatedKey marks a payload boundPayload had to shrink.
const payloadTruncatedKey = "payloadTruncated"

// boundPayload returns p when it encodes within maxPayloadBytes. Otherwise it keeps what fits:
// strings cut to maxPayloadValue bytes, other values whose encoding fits maxPayloadValue, and
// drops the rest (an oversized question list among them), marking the result with
// payloadTruncatedKey. The identifiers the Console navigates by are short and survive. If even
// that is over the bound, only the mark is kept. Only the outbox copy is bounded: the chat bridge
// gets the event as given.
func boundPayload(p map[string]any) map[string]any {
	b, err := json.Marshal(p)
	if err != nil || len(b) <= maxPayloadBytes {
		return p // a marshal error fails the Put on its own
	}
	out := map[string]any{payloadTruncatedKey: true}
	for k, v := range p {
		if s, ok := v.(string); ok {
			out[k] = headBytes(s, maxPayloadValue)
			continue
		}
		if vb, err := json.Marshal(v); err == nil && len(vb) <= maxPayloadValue {
			out[k] = v
		}
	}
	if b, err := json.Marshal(out); err != nil || len(b) > maxPayloadBytes {
		return map[string]any{payloadTruncatedKey: true}
	}
	return out
}

// headBytes cuts s to at most n bytes without splitting a UTF-8 sequence.
func headBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// PutOnce persists an event only once for a stable source key. The marker is
// separate from the acked outbox file, so a still-open prompt is not re-enqueued
// on every Control Plane poll.
//
// Check, Put and marker run under an exclusive flock: two processes racing on one key
// (claude runs hooks in parallel) would otherwise both pass the check and both deliver.
// The marker is written only after the Put, so a process killed in between leaves no
// marker and the next call delivers — a duplicate at worst, never a lost event. The
// kernel drops the lock with the process, so a kill cannot wedge later calls.
func PutOnce(key string, e Event) error {
	sum := sha256.Sum256([]byte(key))
	markerDir := filepath.Join(paths.AgentStateDir(), "notification-markers")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		return err
	}
	pruneMarkers(markerDir)
	unlock, err := lockMarkers(markerDir)
	if err != nil {
		return err
	}
	defer unlock()
	marker := filepath.Join(markerDir, hex.EncodeToString(sum[:])+".seen")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if beforePutOnce != nil {
		beforePutOnce()
	}
	if err := Put(e); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(e.CreatedAt), 0o600)
}

// beforePutOnce is a test seam: it runs after the marker check, before the Put.
var beforePutOnce func()

// lockMarkers takes the exclusive lock every PutOnce shares. One lock file for all
// keys: the critical section is two small file writes, and a per-key lock file would
// need pruning, which cannot be done safely — every caller must lock the same inode,
// so the file is never removed.
func lockMarkers(markerDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(markerDir, "putonce.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
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
		// The payload is decoded raw first, so an entry an older Agent wrote past maxPayloadBytes
		// is listed with only the mark instead of being expanded. Listed, it is acked and
		// removed rather than staying queued ahead of the entries after it.
		var raw struct {
			Event
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		e := raw.Event
		if len(raw.Payload) > maxPayloadBytes {
			e.Payload = map[string]any{payloadTruncatedKey: true}
		} else if len(raw.Payload) > 0 && json.Unmarshal(raw.Payload, &e.Payload) != nil {
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
