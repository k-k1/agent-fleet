package sessionx

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// SubagentFolds caches one derived value per subagent transcript, T being whatever the caller
// folds an agent's turns into (the budget's priced rows, the usage view's cumulative figure).
// The discovery, the stamp, the partial-read rules and the idle drop live here once, so the
// budget and the usage view cannot drift apart on which files they read or when they re-read.
//
// A finished agent never changes, and a Workflow run leaves a hundred or more of them: without
// the cache every call re-parses all of them (measured on the all-sessions usage read: seconds
// per call for transcripts that had not changed).
type SubagentFolds[T any] struct {
	mu   sync.Mutex
	m    map[string]subagentFoldSession[T]
	idle time.Duration
}

type subagentFoldEntry[T any] struct {
	stamp string // size and mtime of every copy
	val   T
}

type subagentFoldSession[T any] struct {
	createdAt string // a recreated name is a new session; never serve its predecessor's values
	agents    map[string]subagentFoldEntry[T]
	used      time.Time
}

// NewSubagentFolds returns an empty cache that drops a session's entries once nobody has read
// it for idle: a stopped, archived or deleted session is never read again, and without the drop
// its values would stay until the Agent restarts.
func NewSubagentFolds[T any](idle time.Duration) *SubagentFolds[T] {
	return &SubagentFolds[T]{m: map[string]subagentFoldSession[T]{}, idle: idle}
}

// Fold returns one value per subagent of m (an agent that cannot be read at all and was never
// read before is left out), folding each agent's turns on its own — an agent re-reports only its
// own context, so two agents must never share a logical turn. Only an agent whose copies changed
// size or mtime since the last call is parsed again.
func (c *SubagentFolds[T]) Fold(m session.Meta, now time.Time, fold func([]transcript.Turn) T) []T {
	groups := SubagentLogs(m)
	c.mu.Lock()
	prev := c.m[m.Name]
	c.mu.Unlock()
	if prev.createdAt != m.CreatedAt {
		prev.agents = nil
	}
	next := subagentFoldSession[T]{createdAt: m.CreatedAt, agents: make(map[string]subagentFoldEntry[T], len(groups)), used: now}
	var out []T
	for _, paths := range groups {
		// Stat before reading: a file that grows in between is read with more than the cached
		// size says, so the next call sees a different stamp and reads it again.
		var live []string
		var stamp strings.Builder
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil {
				live = append(live, p)
				fmt.Fprintf(&stamp, "%s\x00%d\x00%d\x00", p, fi.Size(), fi.ModTime().UnixNano())
			}
		}
		if len(live) == 0 {
			continue
		}
		key := strings.Join(paths, "\x00")
		e, ok := prev.agents[key]
		if !ok || e.stamp != stamp.String() {
			turns, err := SubagentTurns(m, live)
			switch {
			case err != nil && len(turns) == 0:
				// Not cached, so the next call reads it again; until then the last good fold
				// stands in, an undercount of only what was appended since.
				if ok {
					next.agents[key] = e
					out = append(out, e.val)
				}
				continue
			case err != nil:
				// Some copies read: use what they hold now, but keep the last complete fold
				// under its old stamp, so the next call reads every copy again and a later
				// total failure still has it to fall back on.
				if ok {
					next.agents[key] = e
				}
				out = append(out, fold(turns))
				continue
			}
			e = subagentFoldEntry[T]{stamp: stamp.String(), val: fold(turns)}
		}
		next.agents[key] = e
		out = append(out, e.val)
	}
	c.mu.Lock()
	if len(next.agents) == 0 {
		delete(c.m, m.Name)
	} else {
		c.m[m.Name] = next
	}
	for name, s := range c.m {
		if now.Sub(s.used) > c.idle {
			delete(c.m, name)
		}
	}
	c.mu.Unlock()
	return out
}

// Cached reports whether the session has any entry, and whether agent key (the joined paths of
// its copies) is one of them. For tests.
func (c *SubagentFolds[T]) Cached(name, key string) (session, agent bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, session := c.m[name]
	_, agent = s.agents[key]
	return session, agent
}

// Len is the number of agents cached for the session. For tests.
func (c *SubagentFolds[T]) Len(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m[name].agents)
}
