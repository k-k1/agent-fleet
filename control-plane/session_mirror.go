package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// sessionMirrorGate orders the writers of the session mirror. Each of them reads the
// Agent's list and then replaces the workspace's rows wholesale, so a read that started
// earlier but answered later would roll back a newer one: a deleted session reappears,
// and a new one (or a fresher Carried / Studio) vanishes while last_seen reads "just now".
// A write is therefore accepted only if its read STARTED no earlier than the read behind
// the write already applied. Process-local: it orders this Control Plane's writers, which
// is all of them while one instance serves a deployment.
type sessionMirrorGate struct {
	mu      sync.Mutex
	entries map[string]*mirrorEntry
}

type mirrorEntry struct {
	mu      sync.Mutex
	applied time.Time // start of the read behind the last accepted write
}

func (g *sessionMirrorGate) entry(wsID string) *mirrorEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.entries == nil {
		g.entries = map[string]*mirrorEntry{}
	}
	e := g.entries[wsID]
	if e == nil {
		e = &mirrorEntry{}
		g.entries[wsID] = e
	}
	return e
}

// mirrorSessions writes list into the mirror unless a read that started later has already
// been written. readStart is taken BEFORE the Agent request is sent. A rejected write is
// not an error: the mirror already holds something newer.
func (m *manager) mirrorSessions(ctx context.Context, wsID string, readStart time.Time, list []sessionWire) error {
	e := m.sessionMirror.entry(wsID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if readStart.Before(e.applied) {
		return nil
	}
	if err := m.store.ReplaceSessions(ctx, wsID, sessionRows(list)); err != nil {
		return err
	}
	e.applied = readStart
	return nil
}

// eraseHome and wipeHome are runtime.EraseHome / WipeHome plus the withdrawal of the last disk
// reading: after a successful wipe the roster card must not keep a "full disk" for files that
// are gone (an admin Clean home does not restart, so no later sample would replace it).
func (m *manager) eraseHome(ctx context.Context, ws store.Workspace, rt runtime.Runtime) error {
	err := runtime.EraseHome(ctx, rt)
	if err == nil {
		m.clearDiskReading(ws)
	}
	return err
}

func (m *manager) wipeHome(ctx context.Context, ws store.Workspace, rt runtime.Runtime, what runtime.HomeWipe) error {
	err := runtime.WipeHome(ctx, rt, what)
	if err == nil {
		m.clearDiskReading(ws)
	}
	return err
}

func (m *manager) clearDiskReading(ws store.Workspace) {
	// The cached du goes too: a sample within its 60 s TTL would otherwise read the old size and,
	// stamped after the wipe, outrank the cleared mark.
	diskUsages.forget(m.rootedDataDir(ws)+"/home", time.Now())
	wsID := ws.ID
	// Not the caller's context: the wipe has happened, and a cancelled lease must not leave the
	// stale figure behind.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.ClearWorkspaceDisk(ctx, wsID); err != nil {
		log.Printf("home wipe: clear disk reading (%s): %v", wsID, err)
	}
}
