package imagegen

// The image studio's store (ADR 0100 decision 2): one small JSON file per studio, written whole
// under an in-memory lock with tmp→rename, next to its append-only edit log (studio_log.go).
//
// fstore has neither a lock nor an atomic write, and a studio has two writers that land within
// the same half second — the pane's debounced edit and the agent's set_image_draft — so a
// read-modify-write without the lock loses one of them silently.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// StudioSessionAlive answers whether a session is running, installed by the Agent's main
// package (sessionx owns liveness). A create may take a studio from a stopped session but not
// from a live one. nil counts every session that still has a meta as alive, which refuses
// rather than steals.
var StudioSessionAlive func(name string) bool

// SessionStudioCAS sets a session's Meta.Studio from `from` to `to` under the session meta
// lock, and does nothing when the meta names something else by then. Installed by the Agent's
// main package: the lock is sessionx's, and a write here without it would race every other
// meta write. nil leaves the metas alone — the studio is the truth of the binding, and every
// tool call checks it.
var SessionStudioCAS func(name, from, to string)

// Locale is the member's display language ("ja" or "en"), installed by the Agent's main
// package from the synced UI preferences. nil means "ja", the Console's default.
var Locale func() string

func studioLocale() string {
	if Locale != nil && Locale() == "en" {
		return "en"
	}
	return "ja"
}

// studioRec is the file on disk: the studio as the wire knows it, plus get_image_studio's read
// ledger. The ledger is per (studio, session) (decision 5) — kept per studio alone, a newly
// bound agent would start from where the previous one stopped reading and miss everything in
// between.
type studioRec struct {
	ImageStudio
	Seen map[string]studioSeen `json:"seen,omitempty"`
}

// studioSeen is where one session last read up to.
type studioSeen struct {
	Seq int    `json:"seq"`
	At  string `json:"at"`
	// History is how many lines the picture history had: the lines after it are the pictures
	// that arrived since. A line count rather than a time, because a picture's time is when its
	// job was queued, and a job queued before the last call can finish after it.
	History    int    `json:"history"`
	HistoryGen string `json:"history_gen,omitempty"`
}

var studioLocks sync.Map // id -> *sync.Mutex

func lockStudio(id string) func() {
	m, _ := studioLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func studioPath(id string) string { return filepath.Join(paths.ImagegenStudiosDir(), id+".json") }
func studioLogPath(id string) string {
	return filepath.Join(paths.ImagegenStudiosDir(), id+".log.jsonl")
}

// loadStudio reads one studio. The caller holds its lock when it is about to write.
func loadStudio(id string) (*studioRec, error) {
	if !paths.ValidIDSegment(id) {
		return nil, ErrStudioNotFound
	}
	b, err := os.ReadFile(studioPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrStudioNotFound
	}
	if err != nil {
		return nil, err
	}
	var rec studioRec
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("image studio %s is unreadable: %w", id, err)
	}
	rec.ID = id
	return &rec, nil
}

func saveStudio(rec *studioRec) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	dir := paths.ImagegenStudiosDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".studio-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), studioPath(rec.ID)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// studioNow is the clock, a var so a test can pin it.
var studioNow = time.Now

// touchStudio moves UpdatedAt. It is also the version If-Match names, so two writes must never
// share one: a clock that did not move is nudged forward by a nanosecond.
func touchStudio(rec *studioRec) {
	now := studioNow().UTC()
	if prev, err := time.Parse(time.RFC3339Nano, rec.UpdatedAt); err == nil && !now.After(prev) {
		now = prev.Add(time.Nanosecond)
	}
	rec.UpdatedAt = now.Format(time.RFC3339Nano)
}

// newStudioID is a random UUID (v4), the shape paths.ValidIDSegment accepts.
func newStudioID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func listStudioIDs() []string {
	ents, err := os.ReadDir(paths.ImagegenStudiosDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if ok && paths.ValidIDSegment(id) {
			out = append(out, id)
		}
	}
	return out
}

func listStudios() []ImageStudioSummary {
	out := []ImageStudioSummary{}
	for _, id := range listStudioIDs() {
		rec, err := loadStudio(id)
		if err != nil {
			continue
		}
		out = append(out, ImageStudioSummary{ID: rec.ID, Title: rec.Title, Session: rec.Session, UpdatedAt: rec.UpdatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func studioSessionAlive(name string) bool {
	if StudioSessionAlive != nil {
		return StudioSessionAlive(name)
	}
	_, ok := session.ReadMeta(name)
	return ok
}

// StudioSessionUnsupported says why a session of this kind and execution method cannot be bound
// to a studio, or "" when it can (ADR 0100 decision 8). The studio tools trust the MCP child's
// idea of which session it is serving, and these children cannot tell: opencode's Managed child
// is shared by every session of its folder, so another session could write the draft; copilot,
// cursor, kiro and muse Managed children are not told their session name yet. Their Terminal
// sessions are told, and are fine.
func StudioSessionUnsupported(kind, driver string) string {
	if driver != session.DriverManaged {
		return ""
	}
	switch kind {
	case session.KindOpencode:
		return "an opencode Managed session shares its MCP server with the other sessions of its folder, so it cannot be bound to an image studio; use the Terminal execution method"
	case session.KindMuse:
		// muse has no Terminal execution method to point at.
		return "a muse session cannot tell the image studio tools which session it is yet, so it cannot be bound to an image studio for now"
	case session.KindCopilot, session.KindCursor, session.KindKiro:
		return "a " + kind + " Managed session cannot tell the image studio tools which session it is yet, so it cannot be bound to an image studio; use the Terminal execution method"
	}
	return ""
}

// bindStudioSession is BindStudioSession — see its contract in studio.go.
func bindStudioSession(studio, name, previous string) (string, error) {
	unlock := lockStudio(studio)
	defer unlock()
	rec, err := loadStudio(studio)
	if err != nil {
		return "", err
	}
	replaced := ""
	if previous == "" {
		if rec.Session != "" && rec.Session != name {
			if studioSessionAlive(rec.Session) {
				return "", fmt.Errorf("%w: %s", ErrStudioBound, rec.Session)
			}
			replaced = rec.Session
		}
	} else if rec.Session != previous {
		return "", fmt.Errorf("%w: it names %q, not %q", ErrStudioBound, rec.Session, previous)
	}
	if rec.Session == name {
		return replaced, nil
	}
	rec.Session = name
	touchStudio(rec)
	return replaced, saveStudio(rec)
}

// StartStudios installs the studio store and settles what the previous process left behind.
// Called once at Agent start, before the routes serve.
//
// The binding is written on two sides that cannot be written together — the studio's `session`
// and the session's Meta.Studio — so a crash between the two leaves one side only. The studio
// is the truth (decision 2): a studio naming a session that no longer exists is unbound, two
// studios naming one session keep only the newer, and every meta is then made to agree with
// the studios. After that, every press the previous process recorded without its result gets
// one (studio_press.go).
func StartStudios() {
	BindStudioSession = bindStudioSession
	bySession := map[string]*studioRec{}
	for _, id := range listStudioIDs() {
		unlock := lockStudio(id)
		rec, err := loadStudio(id)
		if err != nil {
			unlock()
			log.Printf("image studio %s: %v", id, err)
			continue
		}
		if rec.Session != "" {
			_, exists := session.ReadMeta(rec.Session)
			if prev := bySession[rec.Session]; exists && prev != nil {
				// The older of the two lets go.
				loser := rec
				if rec.UpdatedAt > prev.UpdatedAt {
					loser, bySession[rec.Session] = prev, rec
				}
				if loser != rec {
					unlock()
					unlock = lockStudio(loser.ID)
				}
				log.Printf("image studio %s: unbound from %s, which a newer studio also names", loser.ID, loser.Session)
				loser.Session = ""
				touchStudio(loser)
				_ = saveStudio(loser)
			} else if exists {
				bySession[rec.Session] = rec
			} else {
				log.Printf("image studio %s: unbound from %s, which no longer exists", id, rec.Session)
				rec.Session = ""
				touchStudio(rec)
				_ = saveStudio(rec)
			}
		}
		unlock()
	}
	if SessionStudioCAS != nil {
		for _, m := range session.ListMetas() {
			want := ""
			if rec := bySession[m.Name]; rec != nil {
				want = rec.ID
			}
			if m.Studio != want {
				log.Printf("session %s: studio %q → %q, to agree with the studios", m.Name, m.Studio, want)
				SessionStudioCAS(m.Name, m.Studio, want)
			}
		}
	}
	recoverStudioPresses()
}
