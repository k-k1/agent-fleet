package main

import (
	"context"
	"fmt"
	"log"
	"os"
)

// ecsRuntime is the `aws` Runtime adapter (P3-7). It maps one per-membership
// Workspace onto an ECS Service (desiredCount 0/1 = scale-to-zero) with an EFS
// access point for the persistent home, and reaches the Agent over the internal
// network (Service Connect / awsvpc ENI) rather than a host-published port.
//
// This is the 段1 SKELETON: the type satisfies the Runtime port so the rest of the
// CP compiles and routes through it unchanged, but the AWS calls are not wired yet
// (段2). The lifecycle methods fail loudly instead of pretending to succeed, so a
// misconfigured `AF_RUNTIME=ecs` deployment cannot silently no-op. The intended
// mapping (documented here so 段2 implements against a fixed contract):
//
//	Start   -> UpdateService desiredCount=1 (create Service/TaskDef on first use),
//	           inject AGENT_TOKEN + AF_SECRET_KEY as task env, wait until the task
//	           is RUNNING and the Agent /healthz passes via Endpoint().
//	Stop    -> UpdateService desiredCount=0 (home persists on EFS; resume on next Start).
//	State   -> desiredCount/runningCount -> running | stopped | none.
//	Endpoint-> internal DNS for the task's Agent (Service Connect name or ENI IP).
//	Token   -> per-workspace CP↔Agent bearer (same contract as local).
type ecsRuntime struct {
	cfg   ecsConfig
	name  string // ECS service name (from Workspace.ContainerName)
	token string // CP↔Agent shared secret (Workspace.AgentToken)
	// secretKey is the per-workspace at-rest DEK, injected as AF_SECRET_KEY in the
	// task definition on Start (same contract the docker adapter satisfies via -e).
	secretKey string
}

var _ Runtime = (*ecsRuntime)(nil)

// ecsConfig holds the deployment-wide AWS placement the ECS adapter needs. It is
// read once at boot (env for now; a typed config source can back it in 段2). The
// fields are the ones docs/reference/aws.md §3.3–3.4 call out; they are declared
// here so 段2 fills them in against a fixed shape rather than inventing it.
type ecsConfig struct {
	region        string
	cluster       string   // ECS cluster ARN/name hosting the workspace services
	subnets       []string // awsvpc subnets (private) for the tasks
	securityGroup string   // SG allowing CP -> Agent (and egress to git/Anthropic)
	efsFileSystem string   // EFS id; a per-user access point backs each home
	taskRole      string   // task role (least-privilege; IMDS blocked)
	execRole      string   // execution role (pull image, write logs)
	logGroup      string   // CloudWatch Logs group for workspace tasks
}

// ecsFactory is the `aws` RuntimeFactory. It stamps each Workspace record into an
// ecsRuntime carrying the shared ecsConfig.
type ecsFactory struct {
	cfg ecsConfig
}

func (f *ecsFactory) New(ws Workspace, secretKey string) Runtime {
	return &ecsRuntime{
		cfg:       f.cfg,
		name:      ws.ContainerName,
		token:     ws.AgentToken,
		secretKey: secretKey,
	}
}

var _ RuntimeFactory = (*ecsFactory)(nil)

// newECSFactory builds the AWS Runtime factory from AF_ECS_* env. In 段1 it
// constructs successfully (so the profile switch is exercised end to end) but
// warns that the adapter is a skeleton — the lifecycle methods are not wired yet.
func newECSFactory(_ *manager) (RuntimeFactory, error) {
	cfg := ecsConfig{
		region:        os.Getenv("AF_ECS_REGION"),
		cluster:       os.Getenv("AF_ECS_CLUSTER"),
		subnets:       splitCSV(os.Getenv("AF_ECS_SUBNETS")),
		securityGroup: os.Getenv("AF_ECS_SECURITY_GROUP"),
		efsFileSystem: os.Getenv("AF_ECS_EFS_ID"),
		taskRole:      os.Getenv("AF_ECS_TASK_ROLE"),
		execRole:      os.Getenv("AF_ECS_EXEC_ROLE"),
		logGroup:      os.Getenv("AF_ECS_LOG_GROUP"),
	}
	log.Printf("WARNING: AF_RUNTIME=ecs selected but the ECS adapter is a P3-7 段1 skeleton — " +
		"workspace lifecycle is not implemented (段2). Use AF_RUNTIME=local for a working deployment.")
	return &ecsFactory{cfg: cfg}, nil
}

// errECSUnimplemented marks the 段1 skeleton lifecycle. 段2 replaces each method
// body with the AWS SDK calls documented on ecsRuntime.
func errECSUnimplemented(op string) error {
	return fmt.Errorf("ecs runtime: %s not implemented (P3-7 段2 skeleton)", op)
}

func (e *ecsRuntime) Start(_ context.Context) error { return errECSUnimplemented("Start") }
func (e *ecsRuntime) Stop(_ context.Context) error  { return errECSUnimplemented("Stop") }

// State reports "none" for the unimplemented skeleton so read paths (session list,
// admin state) degrade gracefully to "not running" instead of erroring.
func (e *ecsRuntime) State(_ context.Context) string { return "none" }

// Endpoint would return the task's internal Agent URL (Service Connect / ENI). The
// skeleton returns an obviously-unreachable placeholder so any accidental CP→Agent
// call fails fast rather than hitting a wrong host.
func (e *ecsRuntime) Endpoint() string { return "http://ecs-unimplemented.invalid" }

func (e *ecsRuntime) Token() string { return e.token }
func (e *ecsRuntime) Name() string  { return e.name }
