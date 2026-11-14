package chatx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// chatLaunches resolves, per backend, what a chat or one-shot call runs on and the binding of
// its credential (ADR 0138 D2, D5b): the same credential the kind's sessions use with no
// selection, read when the call starts. Each resolver reads files and cached views only, never
// a CLI or a daemon. lcpp is not here: its call resolves the connection itself (EngineToken)
// and records what that answered.
var chatLaunches = map[string]func(model string) routes.Launch{
	session.KindClaude:   func(string) routes.Launch { return claude.DefaultLaunch() },
	session.KindCodex:    func(string) routes.Launch { return codex.Login() },
	session.KindOpencode: opencode.ModelLaunch,
	session.KindAgy:      func(string) routes.Launch { return agy.Login() },
	session.KindCursor:   func(string) routes.Launch { return cursor.Login() },
	session.KindMuse:     func(string) routes.Launch { return muse.Login() },
}

// chatLaunch is the route and binding a call on kind's backend runs on, resolved before it runs;
// "" for a kind with no resolver here.
func chatLaunch(kind, model string) (route, binding string) {
	if f, ok := chatLaunches[kind]; ok {
		l := f(model)
		return l.Route, l.Binding
	}
	return "", ""
}
