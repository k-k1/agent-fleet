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
	// Start brings the workspace up and returns once the launch is COMMITTED — which
	// is not the same as "the Agent answers". The local adapters block for a courtesy
	// grace on /healthz so the common start comes back already usable, but ECS commits
	// a desiredCount-1 service and converges asynchronously (every Fargate launch pays
	// a full image pull), so callers must not read a nil error as "Agent reachable":
	// poll State(), use ensureWorkspaceReady, or let the Agent call itself fail and
	// retry. Start runs inside an HTTP request, so no adapter may block it past the
	// ingress idle timeout (docs/log/62 §62.5 — a 90s wait here is exactly what made a
	// cold ECS Start come back as a 504).
	//
	// ★ A readiness overrun is NOT an error. Returning one flips a workspace that is
	//   merely still booting into "start failed" — a red toast in front of the user,
	//   a dropped scheduled fire, and a DB row left at "stopped" while the container
	//   runs (runtime_health.go の冒頭に経緯).
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// State reports the live container/service state:
	//   running  — up and the Agent answers
	//   starting — a launch is converging: ECS has desired 1 but no task RUNNING yet
	//              (a multi-minute Fargate cold image pull), or a local container /
	//              process is up but its entrypoint has not reached the Agent yet
	//              (pinned boot-install, opt-in CLI self-update). Callers must NOT
	//              re-Start (the adapter would force a new deployment / kill the boot)
	//              and must NOT idle-stop it; read paths treat it like stopped (Agent
	//              not reachable yet). Always time-boxed by the adapter — a "starting"
	//              that never converges is a workspace nobody can operate.
	//   stopped  — exists but not running (docker: exited container; ECS: desired 0)
	//   none     — no container / service
	State(ctx context.Context) string // running | starting | stopped | none
	Endpoint() string                 // http base URL for CP→Agent REST
	Token() string                    // Bearer secret for CP→Agent (may be "")
	Name() string                     // container / display name
}

// workspaceAlive reports whether a State() value means "there is a live container /
// process behind this workspace right now". Anything that would destroy state under a
// running workspace (wiping the home bind-mount) must test THIS, not `== "running"`:
// a workspace whose Agent has not answered yet is still a container writing to disk.
func workspaceAlive(state string) bool { return state == "running" || state == "starting" }

// runtimeDestroyer tears down everything a Workspace owns that OUTLIVES its container:
// the home data, and on the cloud adapters the per-membership resources the CP created
// along the way (ECS service, EFS access points, SSM parameters, EBS volume, snapshot).
//
// It is a separate port from Runtime on purpose — `Destroy` is irreversible and must not
// be reachable from a Runtime value by accident. Every adapter implements it (ADR 0045
// 決定 13-3): a delete button that works on one deployment profile and silently does
// nothing on another is worse than no button.
//
// The []string return is NOT an error channel — it lists resources this adapter KNOWS it
// could not remove, so the caller can put them in the audit log instead of letting the
// operator believe the data is gone. Today that is only the Fargate adapter's EFS
// directories: an access point can be deleted from the API, but the directory it pointed
// at cannot (docs/log/64 §64.18.4), and EFS keeps billing for it.
type runtimeDestroyer interface {
	Destroy(context.Context) ([]string, error)
}

// destroyRuntime runs the adapter's teardown. The "does not support" error is unreachable
// with the four adapters in tree and exists so a future adapter fails loudly rather than
// leaking resources quietly.
func destroyRuntime(ctx context.Context, rt Runtime) ([]string, error) {
	d, ok := rt.(runtimeDestroyer)
	if !ok {
		return nil, fmt.Errorf("runtime %T does not support destroy", rt)
	}
	return d.Destroy(ctx)
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

func (m *manager) acquireWorkspaceOperationFence(ctx context.Context, workspaceID string, rt Runtime) (func(), error) {
	releaseDB, err := m.store.AcquireWorkspaceOperationFence(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	releaseRuntime, err := acquireRuntimeOperationFence(ctx, rt)
	if err != nil {
		releaseDB()
		return nil, err
	}
	return func() { releaseRuntime(); releaseDB() }, nil
}

// dockerRuntime is the first Runtime adapter; the ECS adapter (P3-7) must satisfy
// the same contract. This assertion fails the build if either drifts.
var _ Runtime = (*dockerRuntime)(nil)

// Every adapter destroys (ADR 0045 決定 13-3). Asserted here rather than next to each
// adapter so a new one cannot be added without meeting this list.
var (
	_ runtimeDestroyer = (*dockerRuntime)(nil)
	_ runtimeDestroyer = (*nativeRuntime)(nil)
	_ runtimeDestroyer = (*ecsRuntime)(nil)
	_ runtimeDestroyer = (*ecsEC2Runtime)(nil)
)

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

// runtimeDocsMounter marks the adapters that hand the container its role-scoped docs by
// bind-mounting <dataDir>/docs (docker, native). The start path stages that directory
// only for them; an adapter without a host seam — ECS — leaves it alone, and its
// container pulls the same subset over the CP's /internal/docs (docs_bridge.go).
type runtimeDocsMounter interface{ mountsStagedDocs() }

// newRuntimeFactory selects the Runtime adapter by deployment profile (AF_RUNTIME):
// "" / "local" / "docker" → Docker Engine (compose, the on-prem default); "ecs" /
// "aws" → AWS ECS on Fargate (P3-7); "ecs-ec2" → the same ECS substrate on the EC2
// launch type with a pool of slots and a persistent per-user EBS home (docs/log/64,
// ADR 0045 決定 10); "native" / "wsl" → containerless host processes for
// Docker-less WSL2 / dev hosts (single-user only; docs/log/34). Unknown profiles fail
// fast at boot rather than silently defaulting to Docker. The docker factory
// captures the manager's template fields by value, so it MUST be built after
// those fields are finalized (e.g. extraEnv appends in main.go).
//
// ecs and ecs-ec2 are separate profiles ON PURPOSE (ADR 0045 決定 10-1): the EC2 pool
// trades a proven, two-resource Fargate workspace for a six-resource one on a
// substrate with no production mileage, so a deployment must opt in and can fall back
// by editing this one value — not by reverting code.
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
	case "ecs-ec2":
		return newECSEC2Factory(m)
	case "native", "wsl":
		return newNativeFactory(m)
	default:
		return nil, fmt.Errorf("unknown AF_RUNTIME profile %q (want local|ecs|ecs-ec2|native)", profile)
	}
}
