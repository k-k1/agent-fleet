// runtime_seam.go — the CP's side of the Runtime seam.
//
// The four Runtime adapters (docker / ecs / ecs-ec2 / native), the health probes and the
// EC2 golden reader live in internal/runtime, and the rest of the CP now names them
// directly as runtime.X (ADR 0067 decision 2 — the alias-collection pass).
//
// What stays here is only what is NOT an alias:
//   - the boot wiring the adapters cannot do for themselves (the shared /healthz client,
//     the Cloud Map resolver), injected once from main;
//   - awsConfigFor, which must dispatch per call and therefore cannot be an alias;
//   - the two compositions that genuinely span the seam — the workspace fence (DB lease +
//     OS fence) and the hibernation assertion.
package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// awsConfigFor is where the CP's own AWS clients get their credentials (cloudcost.go's
// Cost Explorer, and the store's Secrets Manager reader via store_seam.go).
//
// It is a FUNCTION, deliberately, and must not become `var awsConfigFor =
// runtime.AWSConfigFor`. runtime.AWSConfigFor is itself a variable — the one seam the
// live AWS harness swaps to point a whole run at a test account (docs/log/64 §64.23) —
// and a var here would capture its value at package init. The swap would then reach the
// four adapters — they read runtime.AWSConfigFor from inside that package on every call
// (runtime_ecs.go / runtime_ecs_ec2.go) — and NOT the two readers on this side, which
// would go on holding real AWS: a split-brain nobody would see, because every call still
// succeeds. Dispatching per call keeps one door.
//
// It is the only name in this file whose far side is a var. The rest are funcs.
func awsConfigFor(ctx context.Context, region string) (aws.Config, error) {
	return runtime.AWSConfigFor(ctx, region)
}

// --- construction --------------------------------------------------------------------

// newRuntimeFactory builds the profile-selected adapter. It is kept as a function on
// this side (rather than an alias of runtime.NewFactory) because the adapters take a
// plain Config and the values in it are the manager's.
//
// RootDataDir is passed as a closure, NOT as the two strings it is made of. The CP
// discovers its default tenant id late — workspace_lifecycle.go's adoption pass assigns
// it after boot — and a factory that had copied the value would keep re-basing homes
// against the empty one.
func newRuntimeFactory(profile string, m *manager) (runtime.RuntimeFactory, error) {
	return runtime.NewFactory(profile, runtime.Config{
		Image:       m.image,
		AgentHost:   m.agentHost,
		Memory:      m.memory,
		SessionCmd:  m.sessionCmd,
		ExtraEnv:    m.extraEnv,
		AuthMode:    m.authMode,
		RootDataDir: func(ws runtime.Workspace) string { return m.rootedDataDir(store.Workspace(ws)) },
	})
}

// --- boot wiring -----------------------------------------------------------------------

func init() {
	// CP→Agent must go through agent_dial's Transport: a Service Connect alias only
	// exists in the /etc/hosts written at task start, so a workspace created after the
	// CP task resolves only through the Cloud Map fallback that Transport carries. The
	// adapters' own default client has no such fallback (deps.go).
	runtime.SetHealthzClient(healthzClient)
	runtime.SetAgentResolverInit(func(ctx context.Context, ac aws.Config, namespaceArn string) {
		initAgentResolver(ctx, ac, namespaceArn)
	})
}

// --- the two compositions that span the seam --------------------------------------------

// acquireWorkspaceOperationFence takes both fences a lifecycle operation needs: the DB
// lease (the CP owns the store) and, on the adapters that have one, an OS-level fence.
// Released in reverse.
func (m *manager) acquireWorkspaceOperationFence(ctx context.Context, workspaceID string, rt runtime.Runtime) (func(), error) {
	releaseDB, err := m.store.AcquireWorkspaceOperationFence(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	releaseRuntime, err := runtime.AcquireOperationFence(ctx, rt)
	if err != nil {
		releaseDB()
		return nil, err
	}
	return func() { releaseRuntime(); releaseDB() }, nil
}

// The ecs-ec2 adapter is the one runtime that can put a home to sleep. The interface is
// the reaper's (reaper.go), the implementation is the adapter's, and neither package can
// see the other — so the assertion is chained through runtime.Hibernating. Without it a
// drift between the two declarations would not break the build: the reaper's
// `rt.(hibernatingRuntime)` would simply stop matching, and homes would quietly never
// hibernate again.
var _ hibernatingRuntime = runtime.Hibernating

// Workspace must stay field-for-field identical to the adapters' copy, or this
// conversion — the one the manager does on every runtime build — stops compiling. That
// is the point: the two declarations exist only until the store's own move lands, and a
// silent divergence would hand a workspace the wrong home directory.
var _ = func(ws store.Workspace) runtime.Workspace { return runtime.Workspace(ws) }
