package main

// The path conventions under home (docs/log/23 P1-W5). They live in internal/paths, so that
// internal/session, internal/status and main share one set of rules; the thin delegation here
// remains because main calls them from many places.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/paths"

func homeDir() string { return paths.HomeDir() }
