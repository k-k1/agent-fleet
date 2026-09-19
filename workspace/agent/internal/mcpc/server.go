package mcpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

const (
	// defaultHandshakeTimeout bounds initialize/discover + the first tools/list when
	// def.TimeoutMS is unset (mcpreg's own ServerDef.TimeoutMS is validated to
	// [1000,120000]ms by mcpreg.Validate — probe.go's defaultProbeTimeout is 10s; this
	// package reuses the same figure for the same reason: a server that cannot answer
	// its own handshake within it is not one this session should wait longer on).
	defaultHandshakeTimeout = 10 * time.Second
	// eraProbeTimeout bounds the server/discover step alone (mirrors
	// mcpreg/probe.go's eraProbeTimeout): a legacy server that ignores an unknown
	// method rather than answering -32601 must not stall the whole handshake.
	eraProbeTimeout = 2 * time.Second
)

// Server is one live connection to one registered MCP server (mcpreg.ServerDef):
// handshake done, tools/list cached, and notifications/tools/list_changed keeps the
// cache fresh until Close.
type Server struct {
	def       mcpreg.ServerDef
	c         conn
	stateless bool

	mu    sync.RWMutex
	tools []toolInfo

	closeOnce sync.Once
	watchDone chan struct{}
}

// Connect dials def and runs the handshake (era detection, tools/list). ctx bounds
// only the handshake — once Connect returns, the connection's lifetime is governed by
// Close, never by ctx (see stdio.go's dialStdio comment: a per-call deadline must never
// take a still-in-use connection down).
func Connect(ctx context.Context, def mcpreg.ServerDef) (*Server, error) {
	var c conn
	var err error
	switch def.Transport {
	case mcpreg.TransportStdio:
		c, err = dialStdio(def)
	case mcpreg.TransportHTTP:
		c, err = dialHTTP(def)
	default:
		return nil, fmt.Errorf("mcpc: unsupported transport %q", def.Transport)
	}
	if err != nil {
		return nil, err
	}

	s := &Server{def: def, c: c, watchDone: make(chan struct{})}
	if err := s.handshake(ctx); err != nil {
		_ = c.close()
		return nil, fmt.Errorf("mcpc: %s: %w", def.Name, err)
	}
	if hc, ok := c.(*httpConn); ok {
		hc.startStream(s.stateless)
	}
	go s.watchNotifications()
	return s, nil
}

// Name is the registered server name (mcpreg.ServerDef.Name) — the prefix segment on
// every tool this server contributes.
func (s *Server) Name() string { return s.def.Name }

func (s *Server) timeout() time.Duration {
	if s.def.TimeoutMS > 0 {
		return time.Duration(s.def.TimeoutMS) * time.Millisecond
	}
	return defaultHandshakeTimeout
}

// handshake speaks the same two-era detection sequence as mcpreg/probe.go's
// probeStdio/probeHTTP (server/discover first, initialize on a legacy signal), kept
// deliberately parallel to that logic rather than sharing it — see mcpc.go's package
// doc for why this lives in a new package instead of growing probe.go in place.
func (s *Server) handshake(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	eraCtx, eraCancel := context.WithTimeout(ctx, eraProbeTimeout)
	m, err := s.c.call(eraCtx, "server/discover", map[string]any{}, true)
	eraCancel()
	switch {
	case err == nil && m.Error == nil:
		s.stateless = true
		return s.fetchTools(ctx)
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		// Silence within budget: a legacy-era server that ignores an unknown method
		// instead of answering -32601 (mirrors probe.go's own rationale).
	case err != nil:
		return err
	case !isLegacyEraSignal(m.Error):
		return fmt.Errorf("server/discover refused: %s", m.Error.Message)
	}

	s.stateless = false
	m, err = s.c.call(ctx, "initialize", legacyInitParams(), false)
	if err != nil {
		return err
	}
	if m.Error != nil {
		return fmt.Errorf("initialize refused: %s", m.Error.Message)
	}
	if err := s.c.notify(ctx, "notifications/initialized", map[string]any{}, false); err != nil {
		return err
	}
	return s.fetchTools(ctx)
}

func (s *Server) fetchTools(ctx context.Context) error {
	m, err := s.c.call(ctx, "tools/list", map[string]any{}, s.stateless)
	if err != nil {
		return err
	}
	if m.Error != nil {
		return fmt.Errorf("tools/list refused: %s", m.Error.Message)
	}
	var tr toolsListResult
	if err := json.Unmarshal(m.Result, &tr); err != nil {
		return fmt.Errorf("cannot decode tools/list: %w", err)
	}
	s.mu.Lock()
	s.tools = tr.Tools
	s.mu.Unlock()
	return nil
}

// watchNotifications refreshes the tool cache on notifications/tools/list_changed
// until the connection's notification channel closes (Close was called, or the
// transport died). Any other notification name is ignored — this package advertises
// no other capability.
func (s *Server) watchNotifications() {
	defer close(s.watchDone)
	for method := range s.c.notifications() {
		if method != "notifications/tools/list_changed" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout())
		_ = s.fetchTools(ctx) // best-effort: a refresh failure just keeps the stale cache
		cancel()
	}
}

// ToolDefs returns this server's current tools/list snapshot as []harness.ToolDef,
// names prefixed mcp__<server>__<tool> (PrefixToolName) so two servers can never
// collide on a bare tool name.
func (s *Server) ToolDefs() []harness.ToolDef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]harness.ToolDef, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, harness.ToolDef{
			Name:        PrefixToolName(s.def.Name, t.Name),
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return out
}

func (s *Server) hasTool(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// CallTool executes name (the server's OWN, unprefixed name) with raw JSON arguments.
//
// ctx bounds the call; this package applies no timeout of its own on top of it. ADR
// 0093 decision 6's "kind別のタイムアウト" is segment E's job to apply (it is the one
// that knows the session kind driving the tool loop — see harness/types.go's package
// doc: nothing in this build's core knows what a kind is), by giving CallTool a ctx
// with the deadline it wants.
func (s *Server) CallTool(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	if !s.hasTool(name) {
		return "", true, fmt.Errorf("mcpc: %q is not in %s's tools/list (unadvertised names are not callable)", name, s.def.Name)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params := map[string]any{"name": name, "arguments": args}
	m, err := s.c.call(ctx, "tools/call", params, s.stateless)
	if err != nil {
		return "", false, err
	}
	if m.Error != nil {
		return "", true, fmt.Errorf("mcpc: tools/call refused: %s", m.Error.Message)
	}
	return decodeToolResult(m.Result)
}

func decodeToolResult(raw json.RawMessage) (string, bool, error) {
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", false, fmt.Errorf("mcpc: cannot decode tools/call result: %w", err)
	}
	var sb strings.Builder
	for i, c := range r.Content {
		if c.Type != "text" {
			continue
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(c.Text)
	}
	return sb.String(), r.IsError, nil
}

// Close tears the connection down (kills the stdio child / stops the HTTP stream) and
// waits for the notification watcher to stop. Idempotent.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.c.close()
		<-s.watchDone
	})
	return err
}
