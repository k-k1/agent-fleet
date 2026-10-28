package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// shellAgent / ssmAgent — the Agent implementations for the shell and ssm kinds.

// --- shell ---------------------------------------------------------------------

type shellAgent struct{ agents.NoGenericTranscript }

func (shellAgent) Kind() string      { return session.KindShell }
func (shellAgent) Caps() agents.Caps { return agents.Caps{MCPClient: agents.TriNo} }

func (shellAgent) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// A shell falls back to home if its recorded dir is gone.
	cwd := m.CWD()
	if !session.DirExists(cwd) {
		cwd = homeDir()
	}
	return agents.LaunchPlan{Program: "bash -l", Cwd: cwd}, nil
}

func (shellAgent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	return agents.LiveInfo{Resumable: true}
}

func (shellAgent) ClearResume(string) {}

