package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// shellAgent / ssmAgent — shell・ssm 種別の Agent 実装（docs/log/23 P1残: CLI 縦割りファイル分割）

// --- shell ---------------------------------------------------------------------

type shellAgent struct{ agents.NoGenericTranscript }

func (shellAgent) Kind() string      { return session.KindShell }
func (shellAgent) Caps() agents.Caps { return agents.Caps{} }

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

// --- ssm -----------------------------------------------------------------------

type ssmAgent struct{ agents.NoGenericTranscript }

func (ssmAgent) Kind() string      { return session.KindSSM }
func (ssmAgent) Caps() agents.Caps { return agents.Caps{} }

func (ssmAgent) BuildLaunch(m session.Meta, opts agents.LaunchOpts) (agents.LaunchPlan, error) {
	// An SSM session logs into the operator's OWN AWS via `aws sso login` (the
	// device-code URL is surfaced in this terminal — click it to authenticate in
	// another tab) then opens Session Manager on the target instance. No AWS
	// credentials pass through Agent Fleet: the aws CLI authenticates directly and
	// caches the short-lived token in the home volume. Launch dir is home (the work
	// happens on the remote instance).
	if m.SSM == nil || m.SSM.Target == "" {
		return agents.LaunchPlan{}, agents.ErrSSMNoTarget
	}
	p, err := buildSSMProgram(m.Name, *m.SSM, opts.SSMForce)
	if err != nil {
		return agents.LaunchPlan{}, err
	}
	return agents.LaunchPlan{Program: p, Cwd: homeDir()}, nil
}

func (ssmAgent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	return agents.LiveInfo{Resumable: true}
}

func (ssmAgent) ClearResume(string) {}
