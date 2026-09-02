// alias_runtime.go — the CP's side of the Runtime seam.
//
// The four Runtime adapters (docker / ecs / ecs-ec2 / native), the health probes and
// the EC2 golden reader live in internal/runtime. This file is the only place that
// knows that: every other file in the CP goes on writing `Runtime`, `ec2PoolStatus`,
// `waitAgentHealthy` and the rest exactly as before (ADR 0067 決定 3 — エイリアス移送).
//
// Three kinds of entry live here and nothing else:
//   - aliases, so a moved name still resolves on this side;
//   - the boot wiring the adapters cannot do for themselves (the shared /healthz
//     client, the Cloud Map resolver), injected once from main;
//   - the two compositions that genuinely span the seam — the workspace fence (DB
//     lease + OS fence) and the hibernation assertion.
//
// Collapsing this file is a job for the alias-collection pass at the wave boundary,
// not for the tracks running in parallel with it.
package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// --- the port and its data ----------------------------------------------------------

type (
	// Runtime / RuntimeFactory are the port itself.
	Runtime        = runtime.Runtime
	RuntimeFactory = runtime.RuntimeFactory

	// The optional capabilities the CP tests a Runtime value for.
	runtimeDocsMounter = runtime.DocsMounter
	runtimeStartFencer = runtime.StartFencer

	// The EC2 pool's reported shape, and the golden bake's view of a snapshot and of
	// the seed's home volume.
	ec2PoolStatus     = runtime.EC2PoolStatus
	goldenBakePool    = runtime.GoldenBakePool
	goldenSeedRuntime = runtime.GoldenSeedRuntime
	goldenSnap        = runtime.GoldenSnap
	goldenHome        = runtime.GoldenHome
)

// --- constants the CP quotes back ---------------------------------------------------

const (
	// The EC2 resource tags. The CP reads them when it audits or explains a resource;
	// the adapter is what writes them.
	ec2TagPool       = runtime.EC2TagPool
	ec2TagRole       = runtime.EC2TagRole
	ec2TagTenant     = runtime.EC2TagTenant
	ec2TagMembership = runtime.EC2TagMembership
	ec2TagSlotSize   = runtime.EC2TagSlotSize
	ec2TagWorkspace  = runtime.EC2TagWorkspace
	ec2TagBakeReason = runtime.EC2TagBakeReason

	// Tags the cost/audit views quote back when they explain a resource.
	ec2TagClaim       = runtime.EC2TagClaim
	ec2TagIdleSince   = runtime.EC2TagIdleSince
	ec2TagHibernating = runtime.EC2TagHibernating
	ec2TagBackupAt    = runtime.EC2TagBackupAt

	ec2RoleGolden          = runtime.EC2RoleGolden
	ec2RoleGoldenCandidate = runtime.EC2RoleGoldenCandidate
	ec2RoleGoldenRejected  = runtime.EC2RoleGoldenRejected

	ec2ArchX86 = runtime.EC2ArchX86
	ec2ArchArm = runtime.EC2ArchArm

	// Bake phases the CP's pool-status glue rewrites (workspace_lifecycle.go).
	ec2BakePhaseIdle    = runtime.EC2BakePhaseIdle
	ec2BakePhaseBlocked = runtime.EC2BakePhaseBlocked
	ec2BakePhaseOff     = runtime.EC2BakePhaseOff

	// bakeReservedSlots is what a bake needs free at once, subtracted from the pool
	// cap by the tenant-quota comparison in limits.go.
	bakeReservedSlots = runtime.BakeReservedSlots

	// agentBootBudget is the ceiling on how long a first boot may take.
	agentBootBudget = runtime.AgentBootBudget
)

// --- functions ----------------------------------------------------------------------

var (
	agentReadyWait      = runtime.AgentReadyWait
	waitAgentHealthy    = runtime.WaitAgentHealthy
	workspaceAlive      = runtime.WorkspaceAlive
	destroyRuntime      = runtime.DestroyRuntime
	cleanHomeContext    = runtime.CleanHomeContext
	removeAllContext    = runtime.RemoveAllContext
	dockerPublishedPort = runtime.DockerPublishedPort
	dockerEnvValue      = runtime.DockerEnvValue
	envInt              = runtime.EnvInt

	// awsConfigFor is a var on the far side too — the live AWS tests swap it.
	awsConfigFor = runtime.AWSConfigFor
)

// --- construction --------------------------------------------------------------------

// newRuntimeFactory builds the profile-selected adapter. It is kept as a function on
// this side (rather than an alias of runtime.NewFactory) because the adapters take a
// plain Config and the values in it are the manager's.
//
// ⚠️ RootDataDir is passed as a closure, NOT as the two strings it is made of. The CP
// discovers its default tenant id late — workspace_lifecycle.go's adoption pass assigns
// it after boot — and a factory that had copied the value would keep re-basing homes
// against the empty one.
func newRuntimeFactory(profile string, m *manager) (RuntimeFactory, error) {
	return runtime.NewFactory(profile, runtime.Config{
		Image:       m.image,
		AgentHost:   m.agentHost,
		Memory:      m.memory,
		SessionCmd:  m.sessionCmd,
		ExtraEnv:    m.extraEnv,
		AuthMode:    m.authMode,
		RootDataDir: func(ws runtime.Workspace) string { return m.rootedDataDir(Workspace(ws)) },
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
func (m *manager) acquireWorkspaceOperationFence(ctx context.Context, workspaceID string, rt Runtime) (func(), error) {
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
var _ = func(ws Workspace) runtime.Workspace { return runtime.Workspace(ws) }
