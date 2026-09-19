package mcpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// Manager is segment F's actual seam for segment E: it turns a session's attached
// server list into one merged []harness.ToolDef and one place to route a tools/call
// back to whichever server advertised the name.
//
// Manager knows nothing about a session kind or a Control Plane (same rule
// harness/types.go states for its own seam) — it is handed a context at construction
// and a []mcpreg.ServerDef whenever the caller wants one, and that is the whole
// interface.
type Manager struct {
	mu      sync.Mutex
	servers map[string]*Server // by mcpreg.ServerDef.Name
	closed  bool
}

// NewManager returns an empty Manager tied to ctx's lifetime: when ctx is done, every
// connected server is closed automatically — this is what makes ADR 0093 decision 6's
// "stdio 子はセッションと共に死ぬこと" hold even if a caller forgets to call Close
// explicitly. Pass the session's own context, not context.Background().
func NewManager(ctx context.Context) *Manager {
	m := &Manager{servers: map[string]*Server{}}
	go func() {
		<-ctx.Done()
		_ = m.Close()
	}()
	return m
}

// Sync reconciles the live connection set against defs: connects anything new
// (by Name), closes anything no longer present, and leaves an already-connected
// server alone (this version does not detect an in-place edit of an existing
// definition — a renamed/re-pointed server needs a create+delete round trip in the
// registry to take effect here, which is what the registry UI already does).
//
// A single server failing to connect does not stop the others — the return value
// carries every error, keyed by server name, and the manager still serves whichever
// servers DID connect.
func (m *Manager) Sync(ctx context.Context, defs []mcpreg.ServerDef) map[string]error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return map[string]error{"": fmt.Errorf("mcpc: manager is closed")}
	}
	want := make(map[string]mcpreg.ServerDef, len(defs))
	for _, d := range defs {
		want[d.Name] = d
	}
	var toClose []*Server
	for name, s := range m.servers {
		if _, ok := want[name]; !ok {
			toClose = append(toClose, s)
			delete(m.servers, name)
		}
	}
	var toConnect []mcpreg.ServerDef
	for name, d := range want {
		if _, ok := m.servers[name]; !ok {
			toConnect = append(toConnect, d)
		}
	}
	m.mu.Unlock()

	for _, s := range toClose {
		if err := s.Close(); err != nil {
			log.Printf("mcpc: closing %q: %v", s.Name(), err)
		}
	}

	errs := map[string]error{}
	for _, d := range toConnect {
		s, err := Connect(ctx, d)
		if err != nil {
			errs[d.Name] = err
			continue
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			_ = s.Close()
			continue
		}
		m.servers[d.Name] = s
		m.mu.Unlock()
	}
	return errs
}

// ToolDefs merges every connected server's current tools/list snapshot, prefixed
// mcp__<server>__<tool>.
func (m *Manager) ToolDefs() []harness.ToolDef {
	m.mu.Lock()
	servers := make([]*Server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.mu.Unlock()

	var out []harness.ToolDef
	for _, s := range servers {
		out = append(out, s.ToolDefs()...)
	}
	return out
}

// CallTool routes a prefixed tool name (as ToolDefs produced it) to its server and
// executes it. ctx bounds the call — see Server.CallTool's doc for why this package
// applies no timeout of its own.
func (m *Manager) CallTool(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	m.mu.Lock()
	var target *Server
	var bare string
	for srvName, s := range m.servers {
		if t, ok := toolNameForServer(name, srvName); ok {
			target, bare = s, t
			break
		}
	}
	m.mu.Unlock()
	if target == nil {
		return "", true, fmt.Errorf("mcpc: %q does not name a tool on any attached server", name)
	}
	return target.CallTool(ctx, bare, args)
}

// Close disconnects every server. Idempotent; safe to call from the ctx-watcher
// goroutine NewManager started AND explicitly by the caller.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	servers := m.servers
	m.servers = map[string]*Server{}
	m.mu.Unlock()

	var firstErr error
	for _, s := range servers {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
