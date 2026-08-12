package main

import (
	"context"
	"fmt"
)

// Runtime is the port that abstracts where a Workspace container runs and how the
// CP reaches its Agent. dockerRuntime is the local/compose adapter (docker CLI);
// the AWS adapter (ECS, P3-7) implements the same port so the handlers, manager
// and reaper stay backend-agnostic. Endpoint() hides the reachability difference
// (host-published 127.0.0.1:port locally vs Service Connect on ECS).
type Runtime interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// State reports the live container/service state:
	//   running  — up and the Agent is reachable (or about to be)
	//   starting — a launch is converging (ECS: desired 1 but no task RUNNING yet,
	//              e.g. a multi-minute Fargate cold image pull). Callers must NOT
	//              re-Start (the adapter would force a new deployment) and must
	//              NOT idle-stop it; read paths treat it like stopped (Agent not
	//              reachable yet). The docker adapter starts in seconds and in
	//              practice never reports it.
	//   stopped  — exists but not running (docker: exited container; ECS: desired 0)
	//   none     — no container / service
	State(ctx context.Context) string // running | starting | stopped | none
	Endpoint() string                 // http base URL for CP→Agent REST
	Token() string                    // Bearer secret for CP→Agent (may be "")
	Name() string                     // container / display name
}

// runtimeOperationFencer is implemented by adapters whose lifecycle resource is
// local to the CP host and needs an OS-level fence in addition to the DB lease.
// nativeRuntime uses flock so a paused/expired CP cannot overlap a new holder.
type runtimeOperationFencer interface {
	AcquireOperationFence(context.Context) (func(), error)
}

type runtimeStartFencer interface {
	AbortUncommittedStart(context.Context) error
	CommitStart()
}

func acquireRuntimeOperationFence(ctx context.Context, rt Runtime) (func(), error) {
	if f, ok := rt.(runtimeOperationFencer); ok {
		return f.AcquireOperationFence(ctx)
	}
	return func() {}, nil
}

// dockerRuntime is the first Runtime adapter; the ECS adapter (P3-7) must satisfy
// the same contract. This assertion fails the build if either drifts.
var _ Runtime = (*dockerRuntime)(nil)

// RuntimeFactory is the single construction seam for the Runtime port. Every call
// site (handlers, manager, reaper, admin, mcp) builds its Runtime through the
// factory rather than instantiating a concrete adapter, so swapping the local
// Docker adapter for the ECS adapter (P3-7) is a one-line profile switch in
// main.go — no concrete type ever leaks into the backend-agnostic core.
//
// secretKey is the per-workspace at-rest DEK (injected as AF_SECRET_KEY on Start).
// Pass "" for state/stop/read-only calls that never touch secrets. extraEnv carries
// per-workspace KEY=VAL env appended after the shared template env (e.g. the
// per-tenant AF_AGENT_SELF_UPDATE_ALLOWED gate); nil for state/stop-only calls.
type RuntimeFactory interface {
	New(ws Workspace, secretKey string, extraEnv []string) Runtime
}

var _ RuntimeFactory = (*dockerFactory)(nil)

// newRuntimeFactory selects the Runtime adapter by deployment profile (AF_RUNTIME):
// "" / "local" / "docker" → Docker Engine (compose, the on-prem default); "ecs" /
// "aws" → AWS ECS (P3-7); "native" / "wsl" → containerless host processes for
// Docker-less WSL2 / dev hosts (single-user only; docs/34). Unknown profiles fail
// fast at boot rather than silently defaulting to Docker. The docker factory
// captures the manager's template fields by value, so it MUST be built after
// those fields are finalized (e.g. extraEnv appends in main.go).
func newRuntimeFactory(profile string, m *manager) (RuntimeFactory, error) {
	switch profile {
	case "", "local", "docker":
		return &dockerFactory{
			image:       m.image,
			agentHost:   m.agentHost,
			memory:      m.memory,
			sessionCmd:  m.sessionCmd,
			extraEnv:    m.extraEnv,
			rootDataDir: m.rootedDataDir,
		}, nil
	case "ecs", "aws":
		return newECSFactory(m)
	case "native", "wsl":
		return newNativeFactory(m)
	default:
		return nil, fmt.Errorf("unknown AF_RUNTIME profile %q (want local|ecs|native)", profile)
	}
}
