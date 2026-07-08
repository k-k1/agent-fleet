package main

// shellAgent / ssmAgent — shell・ssm 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）

// --- shell ---------------------------------------------------------------------

type shellAgent struct{ noGenericTranscript }

func (shellAgent) kind() string    { return kindShell }
func (shellAgent) caps() agentCaps { return agentCaps{} }

func (shellAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// A shell falls back to home if its recorded dir is gone.
	cwd := m.Dir
	if !dirExists(cwd) {
		cwd = homeDir()
	}
	return launchPlan{program: "bash -l", cwd: cwd}, nil
}

func (shellAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	return liveInfo{resumable: true}
}

func (shellAgent) clearResume(string) {}

// --- ssm -----------------------------------------------------------------------

type ssmAgent struct{ noGenericTranscript }

func (ssmAgent) kind() string    { return kindSSM }
func (ssmAgent) caps() agentCaps { return agentCaps{} }

func (ssmAgent) buildLaunch(m sessionMeta, opts launchOpts) (launchPlan, error) {
	// An SSM session logs into the operator's OWN AWS via `aws sso login` (the
	// device-code URL is surfaced in this terminal — click it to authenticate in
	// another tab) then opens Session Manager on the target instance. No AWS
	// credentials pass through Agent Fleet: the aws CLI authenticates directly and
	// caches the short-lived token in the home volume. Launch dir is home (the work
	// happens on the remote instance).
	if m.SSM == nil || m.SSM.Target == "" {
		return launchPlan{}, errSSMNoTarget
	}
	p, err := buildSSMProgram(m.Name, *m.SSM, opts.ssmForce)
	if err != nil {
		return launchPlan{}, err
	}
	return launchPlan{program: p, cwd: homeDir()}, nil
}

func (ssmAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	return liveInfo{resumable: true}
}

func (ssmAgent) clearResume(string) {}
