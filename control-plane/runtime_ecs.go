package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// ecsAgentPort is the fixed container port the workspace Agent listens on (the
// same 7700 the docker adapter host-publishes). On ECS it is reached over Service
// Connect by DNS name instead of a host-published port.
const ecsAgentPort int32 = 7700

// wsScratchPath is where a workspace finds its TASK-LOCAL working disk — the fast,
// non-persistent place for regenerable build output and caches (ADR 0044 決定 3).
// It is a plain directory on the task's ephemeral storage in the normal case, and the
// mount point of an ECS-managed EBS volume when the requested disk exceeds Fargate's
// 200 GiB ephemeral ceiling; the workspace sees the same path either way.
const (
	wsScratchPath    = "/scratch"
	ecsScratchVolume = "scratch"
	// ecsDefaultWorkDiskGiB is the deployment default for the working disk. It must
	// stay above the entrypoint's arming threshold (AF_WS_SCRATCH_MIN_GB, 30 GiB) or
	// the relocation never happens, and it has to hold the measured caches (~10.5 GiB)
	// plus the image layers and /tmp, which share the same disk. Only the GiB above
	// Fargate's free 20 are billed (~$0.097/GiB-month, and only while the task runs).
	ecsDefaultWorkDiskGiB = 50
)

// --- narrow AWS client ports (only the calls the adapter makes), so the runtime
// is unit-testable against fakes. The real *ecs.Client / *efs.Client / *ssm.Client
// satisfy these. ---

type ecsAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	CreateService(context.Context, *ecs.CreateServiceInput, ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	RegisterTaskDefinition(context.Context, *ecs.RegisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	DeleteService(context.Context, *ecs.DeleteServiceInput, ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error)
}

type efsAPI interface {
	DescribeAccessPoints(context.Context, *efs.DescribeAccessPointsInput, ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error)
	CreateAccessPoint(context.Context, *efs.CreateAccessPointInput, ...func(*efs.Options)) (*efs.CreateAccessPointOutput, error)
	DeleteAccessPoint(context.Context, *efs.DeleteAccessPointInput, ...func(*efs.Options)) (*efs.DeleteAccessPointOutput, error)
}

type ssmAPI interface {
	PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(context.Context, *ssm.DeleteParameterInput, ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

// ecsRuntime is the `aws` Runtime adapter (P3-7 段2). It maps one per-membership
// Workspace onto an ECS Service (desiredCount 0/1 = scale-to-zero) with two EFS
// access points for the persistent home + claude-config, injects the CP↔Agent
// token and at-rest DEK via SSM SecureString, and reaches the Agent over Service
// Connect (frozen spec docs/history/p3-7-aws-adapter.md §20b.7). The adapter holds
// NO CP-side state: every resource is addressed by a deterministic name/tag and
// created-or-got on Start, so there is no schema change vs the docker adapter.
type ecsRuntime struct {
	cfg          ecsConfig
	ecs          ecsAPI
	efs          efsAPI
	ssm          ssmAPI
	name         string // ECS service name / SC dnsName (Workspace.ContainerName)
	membershipID string // EFS access-point tag key (af-membership)
	token        string // CP↔Agent bearer (Workspace.AgentToken)
	secretKey    string // per-workspace at-rest DEK (hex); "" in dev
	extraEnv     []string
	// cpu / memory are the Fargate task size for THIS workspace: the cfg defaults, or
	// a size snapped up to hold the per-workspace RAM/CPU caps (fargateSize) when one
	// is set. registerTaskDef stamps them into the task definition revision.
	cpu, memory string
	// diskGiB is the task's ephemeral storage size, or 0 for "leave the field out"
	// (Fargate's free 20 GiB default). Above the ephemeral ceiling the request is an
	// ECS-managed EBS volume instead — see ebsGiB (ADR 0044 決定 2).
	diskGiB int32
	ebsGiB  int32
	// waitReady polls the Agent /healthz through Endpoint(); a field so tests can
	// stub it out (real path hits HTTP, unavailable in unit tests). Start runs it in
	// the background (watchReady), never on the caller's thread.
	waitReady func(ctx context.Context, endpoint string, timeout time.Duration) error
}

var _ Runtime = (*ecsRuntime)(nil)

// ecsConfig holds the deployment-wide AWS placement the ECS adapter needs, read
// once at boot from AF_ECS_* env. IaC (deploy/aws/ecs, CloudFormation) owns the
// static substrate these point at.
type ecsConfig struct {
	region         string
	cluster        string   // ECS cluster ARN/name hosting the workspace services
	subnets        []string // awsvpc private subnets for the tasks
	securityGroup  string   // ws SG: CP -> Agent:7700 + egress to git/Anthropic
	efsFileSystem  string   // EFS id; two per-membership access points back each home
	namespaceArn   string   // Service Connect (Cloud Map) namespace ARN
	execRole       string   // task execution role (pull image, logs, read SSM secrets)
	taskRole       string   // task role (least-privilege; Agent needs no AWS APIs)
	infraRole      string   // ECS infrastructure role — only needed for managed EBS (disk > 200 GiB)
	logGroup       string   // CloudWatch Logs group for workspace tasks
	workspaceImage string   // ECR image URI:tag for the Workspace Agent
	sessionCmd     string   // AGENT_SESSION_CMD passthrough
	cpu, memory    string   // Fargate task size (e.g. "1024" / "2048")
	diskGiB        int      // AF_ECS_WS_DISK_GB: deployment default working disk (0 = Fargate's free 20 GiB)
	posixUID       int64    // EFS access-point owner uid/gid (container dev user)
	posixGID       int64
	startTimeout   time.Duration // budget for the background readiness watch (see watchReady)
}

// ecsFactory is the `aws` RuntimeFactory. It carries the shared AWS clients and
// config and stamps each Workspace record into an ecsRuntime.
type ecsFactory struct {
	cfg ecsConfig
	ecs ecsAPI
	efs efsAPI
	ssm ssmAPI
}

func (f *ecsFactory) New(ws Workspace, secretKey string, extraEnv []string) Runtime {
	// Default to the deployment task size; when a per-workspace RAM or CPU cap is set,
	// snap onto the smallest VALID Fargate (cpu, memory) pair that holds both — Fargate
	// only accepts specific combinations, so a memory bump may raise CPU and a CPU bump
	// may raise memory.
	cpu, memory := f.cfg.cpu, f.cfg.memory
	if ws.MemBytes > 0 || ws.CPUUnits > 0 {
		memBytes := ws.MemBytes
		if memBytes == 0 { // CPU set alone: keep the deployment memory as the request
			if m, err := strconv.ParseInt(f.cfg.memory, 10, 64); err == nil {
				memBytes = m * mib
			}
		}
		cpu, memory = fargateSize(memBytes, ws.CPUUnits, f.cfg.cpu)
	}
	// Working disk: ephemeral storage up to Fargate's 200 GiB ceiling, an ECS-managed
	// EBS volume above it. Unset (0) leaves the task definition field out, which is the
	// free 20 GiB default.
	diskGiB, needsEBS := fargateDiskGiB(pickDiskGiB(ws.DiskGB, f.cfg.diskGiB))
	var ebsGiB int32
	if needsEBS {
		ebsGiB = int32(pickDiskGiB(ws.DiskGB, f.cfg.diskGiB))
	}
	return &ecsRuntime{
		cfg:          f.cfg,
		ecs:          f.ecs,
		efs:          f.efs,
		ssm:          f.ssm,
		name:         ws.ContainerName,
		membershipID: ws.MembershipID,
		token:        ws.AgentToken,
		secretKey:    secretKey,
		extraEnv:     extraEnv,
		cpu:          cpu,
		memory:       memory,
		diskGiB:      diskGiB,
		ebsGiB:       ebsGiB,
		waitReady:    httpHealthz,
	}
}

// pickDiskGiB resolves the working-disk request: the per-workspace value when set,
// otherwise the deployment default (AF_ECS_WS_DISK_GB), otherwise 0 = Fargate's free
// 20 GiB.
func pickDiskGiB(perWorkspace, deploymentDefault int) int {
	if perWorkspace > 0 {
		return perWorkspace
	}
	return deploymentDefault
}

var _ RuntimeFactory = (*ecsFactory)(nil)

// newECSFactory builds the AWS Runtime factory: it loads AWS config (region) and
// constructs the ECS/EFS/SSM clients once, then reads the placement from AF_ECS_*.
// Credentials are resolved lazily by the SDK (task role on ECS), so this does no
// network I/O at boot.
func newECSFactory(m *manager) (RuntimeFactory, error) {
	region := os.Getenv("AF_ECS_REGION")
	ac, err := awscfg.LoadDefaultConfig(context.Background(), awscfg.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	cfg := ecsConfig{
		region:         region,
		cluster:        os.Getenv("AF_ECS_CLUSTER"),
		subnets:        splitCSV(os.Getenv("AF_ECS_SUBNETS")),
		securityGroup:  os.Getenv("AF_ECS_SECURITY_GROUP"),
		efsFileSystem:  os.Getenv("AF_ECS_EFS_ID"),
		namespaceArn:   os.Getenv("AF_ECS_NAMESPACE_ARN"),
		execRole:       os.Getenv("AF_ECS_EXEC_ROLE"),
		taskRole:       os.Getenv("AF_ECS_TASK_ROLE"),
		infraRole:      os.Getenv("AF_ECS_INFRA_ROLE"),
		logGroup:       os.Getenv("AF_ECS_LOG_GROUP"),
		workspaceImage: envOr("AF_ECS_WORKSPACE_IMAGE", m.image),
		sessionCmd:     m.sessionCmd,
		cpu:            envOr("AF_ECS_TASK_CPU", "1024"),
		memory:         envOr("AF_ECS_TASK_MEMORY", "2048"),
		// Deployment default working disk in GiB. 0 keeps Fargate's free 20 GiB; a
		// value of 21–200 becomes the task's ephemeral storage.
		//
		// The default is ecsDefaultWorkDiskGiB, NOT 0: the relocation of regenerable
		// caches (ADR 0044 決定 3) only arms itself when the working disk is big enough
		// to hold them (entrypoint checks AF_WS_SCRATCH_MIN_GB, 30 GiB), and 20 GiB is
		// not — the free tier also carries the image layers and /tmp. Shipping 0 meant
		// the feature was inert in every deployment. Set AF_ECS_WS_DISK_GB=0 to go back
		// to the free tier (and with it, home entirely on EFS).
		diskGiB:  envInt("AF_ECS_WS_DISK_GB", ecsDefaultWorkDiskGiB),
		posixUID: int64(envInt("AF_ECS_POSIX_UID", 1000)),
		posixGID: int64(envInt("AF_ECS_POSIX_GID", 1000)),
		// How long the BACKGROUND readiness watch keeps polling /healthz after Start
		// returns (observability only — nothing waits on it, see watchReady). Sized
		// over the whole convergence, not under the ALB idle timeout: a Fargate cold
		// pull plus a fresh home's boot-install runs past 100s (docs/62 §62.3), and a
		// budget shorter than that would just log a false "not ready" every Start.
		// Same reasoning as runtime_native's 300s health wait.
		startTimeout: time.Duration(envInt("AF_ECS_START_TIMEOUT_SEC", 300)) * time.Second,
	}
	log.Printf("runtime=ecs region=%s cluster=%s namespace=%s efs=%s", cfg.region, cfg.cluster, cfg.namespaceArn, cfg.efsFileSystem)
	return &ecsFactory{
		cfg: cfg,
		ecs: ecs.NewFromConfig(ac),
		efs: efs.NewFromConfig(ac),
		ssm: ssm.NewFromConfig(ac),
	}, nil
}

func (e *ecsRuntime) Token() string { return e.token }
func (e *ecsRuntime) Name() string  { return e.name }

// Endpoint returns the Agent's internal Service Connect URL. The client alias the
// workspace service advertises is its ContainerName, so the CP reaches it at
// http://<name>:7700 from inside the same namespace/VPC.
func (e *ecsRuntime) Endpoint() string {
	return fmt.Sprintf("http://%s:%d", e.name, ecsAgentPort)
}

// State maps the ECS service to running | starting | stopped | none. A missing/
// INACTIVE service is "none"; desiredCount 0 is "stopped"; desired 1 with no task
// RUNNING yet is "starting" — the workspace image cold-pulls for minutes on
// Fargate, and reporting that window as "stopped" (the pre-revision §20b.7.8
// mapping) made a legitimately starting workspace look dead in the Console.
func (e *ecsRuntime) State(ctx context.Context) string {
	s, ok, err := e.describeService(ctx)
	if err != nil || !ok {
		return "none"
	}
	switch {
	case s.DesiredCount >= 1 && s.RunningCount >= 1:
		return "running"
	case s.DesiredCount >= 1:
		return "starting"
	default:
		return "stopped"
	}
}

func (e *ecsRuntime) describeService(ctx context.Context) (ecstypes.Service, bool, error) {
	out, err := e.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(e.cfg.cluster),
		Services: []string{e.name},
	})
	if err != nil {
		return ecstypes.Service{}, false, err
	}
	for _, s := range out.Services {
		if aws.ToString(s.Status) == "INACTIVE" {
			return ecstypes.Service{}, false, nil // deleted; treat as none
		}
		return s, true, nil
	}
	return ecstypes.Service{}, false, nil
}

// Stop scales the service to zero (home persists on EFS; resume on next Start). A
// missing service is already-stopped. Graceful: ECS task stop SIGTERMs the
// container and SIGKILLs after the task def's stopTimeout (set from
// AF_STOP_GRACE_SEC at registration, i.e. a grace change applies from the next
// Start) — the Agent's shutdown handler does the in-container Ctrl-C sweep.
func (e *ecsRuntime) Stop(ctx context.Context) error {
	_, ok, err := e.describeService(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	_, err = e.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(e.cfg.cluster),
		Service:      aws.String(e.name),
		DesiredCount: aws.Int32(0),
	})
	return err
}

// Destroy tears down everything this adapter created for the membership: the ECS
// service, the two EFS access points and the two SSM SecureString parameters.
// ecsEC2Runtime.Destroy calls it for the same resources (it shares this adapter as a
// library) after it has released the slot and deleted the EBS home.
//
// ⚠️ It CANNOT delete the home itself. The EFS directories the access points pointed at
// (/home/<membership>, /claude-config/<membership>) survive the access points, and EFS
// keeps billing for them — deleting them needs a mount, i.e. a throwaway task
// (docs/64 §64.18.4, ADR 0045 決定 13-3). They come back as leftovers rather than as an
// error so the caller can record them; an error here would only make the operator retry
// a teardown that already did everything it can.
//
// Every step is idempotent (already-gone is success): a partial Destroy must be safe to
// re-run, which is the normal case after a CP restart mid-teardown.
func (e *ecsRuntime) Destroy(ctx context.Context) ([]string, error) {
	if err := e.Stop(ctx); err != nil {
		return nil, err
	}
	// Force: the service is at desired 0 but its last task may still be draining, and
	// DeleteService refuses a service with running tasks without it.
	if _, ok, err := e.describeService(ctx); err != nil {
		return nil, err
	} else if ok {
		if _, err := e.ecs.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(e.cfg.cluster),
			Service: aws.String(e.name),
			Force:   aws.Bool(true),
		}); err != nil && !isAWSNotFound(err) {
			return nil, fmt.Errorf("delete service %s: %w", e.name, err)
		}
	}
	leftovers, err := e.destroySharedResources(ctx)
	if err != nil {
		return nil, err
	}
	return leftovers, nil
}

// destroySharedResources removes the per-membership EFS access points and SSM secrets —
// the part of the teardown that is identical on both launch types. Split out so the EC2
// adapter can run it in its own order (slot and volume first, then this).
func (e *ecsRuntime) destroySharedResources(ctx context.Context) ([]string, error) {
	out, err := e.efs.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(e.cfg.efsFileSystem),
	})
	if err != nil {
		return nil, err
	}
	var leftovers []string
	for _, ap := range out.AccessPoints {
		if tagValue(ap.Tags, "af-membership") != e.membershipID {
			continue
		}
		if rd := ap.RootDirectory; rd != nil && aws.ToString(rd.Path) != "" {
			leftovers = append(leftovers, "efs:"+e.cfg.efsFileSystem+aws.ToString(rd.Path))
		}
		if _, err := e.efs.DeleteAccessPoint(ctx, &efs.DeleteAccessPointInput{
			AccessPointId: ap.AccessPointId,
		}); err != nil && !isAWSNotFound(err) {
			return nil, fmt.Errorf("delete access point %s: %w", aws.ToString(ap.AccessPointId), err)
		}
	}
	for _, suffix := range []string{"agent-token", "secret-key"} {
		name := fmt.Sprintf("/af-ws/%s/%s", e.name, suffix)
		if _, err := e.ssm.DeleteParameter(ctx, &ssm.DeleteParameterInput{
			Name: aws.String(name),
		}); err != nil && !isAWSNotFound(err) {
			return nil, fmt.Errorf("delete parameter %s: %w", name, err)
		}
	}
	return leftovers, nil
}

// isAWSNotFound reports whether err is one of the "it is already gone" shapes the
// teardown treats as success. Matching on the message rather than errors.As over a dozen
// per-service NotFound types: the four services here spell it four different ways, and a
// teardown that fails because something was already deleted is the bug we are avoiding.
func isAWSNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"NotFound", // AccessPointNotFound, ParameterNotFound, InvalidVolume.NotFound, …
		"ServiceNotActive",
		"ServiceNotFoundException",
		"does not exist",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// Start brings the workspace up: ensure the two EFS access points, push the token
// + DEK to SSM SecureString, register a fresh task definition (current image/env),
// then create-or-update the service to desiredCount 1 and RETURN — the launch
// converges asynchronously and State() reports "starting" until it does.
func (e *ecsRuntime) Start(ctx context.Context) error {
	switch e.State(ctx) {
	case "running":
		return nil
	case "starting":
		// Already converging (desired 1, task pulling/booting). Re-issuing Start
		// would register a fresh task def and ForceNewDeployment — restarting the
		// multi-minute cold pull from zero. Let the in-flight launch finish.
		return nil
	}
	homeAP, err := e.ensureAccessPoint(ctx, "home", "/home/"+e.membershipID)
	if err != nil {
		return fmt.Errorf("efs home access point: %w", err)
	}
	claudeAP, err := e.ensureAccessPoint(ctx, "claude", "/claude-config/"+e.membershipID)
	if err != nil {
		return fmt.Errorf("efs claude access point: %w", err)
	}
	secrets, err := e.putSecrets(ctx)
	if err != nil {
		return fmt.Errorf("ssm secrets: %w", err)
	}
	taskDefArn, err := e.registerTaskDef(ctx, homeAP, claudeAP, secrets)
	if err != nil {
		return fmt.Errorf("register task def: %w", err)
	}
	if err := e.upsertService(ctx, taskDefArn); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	e.watchReady(ctx)
	return nil
}

// watchReady polls the Agent /healthz for the record only: it runs on its own
// goroutine so Start returns as soon as the service sits at desiredCount 1.
//
// Start used to block here (docs/62 §62.5). Two things made that wait pure cost:
//
//   - It cannot succeed. Reaching this point means a task is launching FROM ZERO —
//     "running" and "starting" both return early above — and Fargate keeps no image
//     cache between tasks, so every launch pays a full ~1GB pull (docs/62 §62.1).
//     The "warm image" the old comment waited for does not exist on this runtime;
//     the wait timed out every time and Start returned nil anyway.
//   - It broke the caller. Start runs inside the HTTP request (workspace_handlers
//     ensureWorkspaceStartedRTLocked), and the ALB in front of the CP has the
//     default 60s idle timeout (deploy/aws/ecs/cfn/30-ingress.yaml), so a 90s wait
//     killed the *response* with a 504 while the workspace converged fine behind it.
//
// So convergence is the poller's job: startResolved answers with the live State()
// ("starting"), the Console keeps polling GET /api/workspace, and the scheduler's
// wake has its own tolerant awaitAgentReady after the start. A readiness failure
// must still NEVER fail Start — that would flip a legitimately starting workspace
// to "failed" (P3-7 段5 finding A) — which is now structural: nothing reads it.
//
// What is left is the log line, and it is the cheapest instrument we have for the
// unmeasured ~100s (docs/62 §62.7 P0): it records how long the Agent actually took
// to answer after Start, per workspace, without an AWS-side trace.
func (e *ecsRuntime) watchReady(ctx context.Context) {
	if e.waitReady == nil {
		return
	}
	// Detach from the caller's context. The whole point is to outlive the HTTP
	// response (and the lifecycle lease released right after Start returns), whose
	// cancelation would otherwise abort the poll immediately. WithoutCancel keeps
	// any request-scoped values while dropping the deadline/cancelation.
	watchCtx := context.WithoutCancel(ctx)
	name, endpoint, budget := e.name, e.Endpoint(), e.cfg.startTimeout
	started := time.Now()
	go func() {
		if err := e.waitReady(watchCtx, endpoint, budget); err != nil {
			log.Printf("ecs start: service %s at desired 1 but Agent not ready within %s (%v); it may still converge",
				name, budget, err)
			return
		}
		log.Printf("ecs start: service %s Agent healthy %.0fs after Start", name, time.Since(started).Seconds())
	}()
}

// ensureAccessPoint returns the id of the per-membership EFS access point for the
// given role (home|claude), creating it (tagged af-membership/af-role) if absent.
// Deterministic tag lookup = no CP-side state.
func (e *ecsRuntime) ensureAccessPoint(ctx context.Context, role, path string) (string, error) {
	out, err := e.efs.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(e.cfg.efsFileSystem),
	})
	if err != nil {
		return "", err
	}
	for _, ap := range out.AccessPoints {
		if tagValue(ap.Tags, "af-membership") == e.membershipID && tagValue(ap.Tags, "af-role") == role {
			return aws.ToString(ap.AccessPointId), nil
		}
	}
	created, err := e.efs.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{
		FileSystemId: aws.String(e.cfg.efsFileSystem),
		RootDirectory: &efstypes.RootDirectory{
			Path: aws.String(path),
			CreationInfo: &efstypes.CreationInfo{
				OwnerUid:    aws.Int64(e.cfg.posixUID),
				OwnerGid:    aws.Int64(e.cfg.posixGID),
				Permissions: aws.String("0755"),
			},
		},
		PosixUser: &efstypes.PosixUser{Uid: aws.Int64(e.cfg.posixUID), Gid: aws.Int64(e.cfg.posixGID)},
		Tags: []efstypes.Tag{
			{Key: aws.String("af-membership"), Value: aws.String(e.membershipID)},
			{Key: aws.String("af-role"), Value: aws.String(role)},
			{Key: aws.String("Name"), Value: aws.String(e.name + "-" + role)},
		},
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(created.AccessPointId), nil
}

// putSecrets writes the token + DEK to SSM SecureString (frozen spec §20b.7.5) and
// returns the container `secrets` list referencing them by valueFrom. Empty values
// (dev) are skipped, matching the docker adapter's "-e only if set".
func (e *ecsRuntime) putSecrets(ctx context.Context) ([]ecstypes.Secret, error) {
	var out []ecstypes.Secret
	put := func(envName, suffix, val string) error {
		if val == "" {
			return nil
		}
		name := fmt.Sprintf("/af-ws/%s/%s", e.name, suffix)
		if _, err := e.ssm.PutParameter(ctx, &ssm.PutParameterInput{
			Name:      aws.String(name),
			Value:     aws.String(val),
			Type:      ssmtypes.ParameterTypeSecureString,
			Overwrite: aws.Bool(true),
		}); err != nil {
			return err
		}
		out = append(out, ecstypes.Secret{Name: aws.String(envName), ValueFrom: aws.String(name)})
		return nil
	}
	if err := put("AGENT_TOKEN", "agent-token", e.token); err != nil {
		return nil, err
	}
	if err := put("AF_SECRET_KEY", "secret-key", e.secretKey); err != nil {
		return nil, err
	}
	return out, nil
}

// registerTaskDef registers a fresh revision with the current image, env, EFS
// mounts and secrets, and returns its ARN. Registering every Start (rather than
// reusing) keeps the running task on the current image — the ECS analogue of the
// docker adapter's "rm -f + run". Old revisions are inert and free.
func (e *ecsRuntime) registerTaskDef(ctx context.Context, homeAP, claudeAP string, secrets []ecstypes.Secret) (string, error) {
	env := []ecstypes.KeyValuePair{
		{Name: aws.String("CLAUDE_CONFIG_DIR"), Value: aws.String("/var/lib/af/claude")},
		// The task-local working disk. Injected ONLY here: the docker adapter bind-mounts
		// a host directory for home, so on-prem has nothing to gain from moving caches
		// off it. The entrypoint uses this to relocate the small-file caches (ADR 0044
		// 決定 3); everything it points at is wiped when the task stops.
		{Name: aws.String("AF_WS_SCRATCH"), Value: aws.String(wsScratchPath)},
		// Graceful-shutdown budget for the Agent's SIGTERM handler — the container
		// stopTimeout minus a safety margin (see StopTimeout below).
		{Name: aws.String("AGENT_STOP_GRACE_SEC"), Value: aws.String(strconv.Itoa(agentStopGraceSec()))},
	}
	if e.cfg.sessionCmd != "" {
		env = append(env, ecstypes.KeyValuePair{Name: aws.String("AGENT_SESSION_CMD"), Value: aws.String(e.cfg.sessionCmd)})
	}
	for _, kv := range e.extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env = append(env, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
		}
	}
	container := ecstypes.ContainerDefinition{
		Name:      aws.String("agent"),
		Image:     aws.String(e.cfg.workspaceImage),
		Essential: aws.Bool(true),
		PortMappings: []ecstypes.PortMapping{
			{ContainerPort: aws.Int32(ecsAgentPort), Name: aws.String("agent")},
		},
		Environment: env,
		Secrets:     secrets,
		MountPoints: []ecstypes.MountPoint{
			{SourceVolume: aws.String("home"), ContainerPath: aws.String("/home/dev")},
			{SourceVolume: aws.String("claude"), ContainerPath: aws.String("/var/lib/af/claude")},
		},
		// Two-stage graceful stop (§20b.7.8 停止改訂): on Stop (desired 0) ECS
		// delivers SIGTERM, the Agent Ctrl-C's its panes and exits within its
		// budget, and past stopTimeout ECS SIGKILLs — the built-in second stage.
		// The ECS analogue of the docker adapter's `docker stop -t`.
		StopTimeout: aws.Int32(int32(stopGraceSec())),
		// docker --init parity: docker-init (tini) as PID 1 reaps zombies and
		// forwards SIGTERM to the Agent. Without it the Agent is PID 1, where the
		// kernel suppresses default-action signals — the pre-handler reason every
		// ECS stop silently sat out the full stopTimeout and then got SIGKILLed.
		LinuxParameters: &ecstypes.LinuxParameters{InitProcessEnabled: aws.Bool(true)},
	}
	if e.cfg.logGroup != "" {
		container.LogConfiguration = &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         e.cfg.logGroup,
				"awslogs-region":        e.cfg.region,
				"awslogs-stream-prefix": "agent",
			},
		}
	}
	volumes := []ecstypes.Volume{
		efsVolume("home", e.cfg.efsFileSystem, homeAP),
		efsVolume("claude", e.cfg.efsFileSystem, claudeAP),
	}
	if e.ebsGiB > 0 {
		// Above Fargate's 200 GiB ephemeral ceiling the working disk becomes an
		// ECS-managed EBS volume. The task definition only DECLARES it; the size and
		// type come from the service's volumeConfigurations at launch, which is what
		// configuredAtLaunch means. It is created per task and deleted when the task
		// stops, exactly like ephemeral storage — see ADR 0044 決定 4 for why this is
		// not a persistence mechanism.
		volumes = append(volumes, ecstypes.Volume{
			Name: aws.String(ecsScratchVolume), ConfiguredAtLaunch: aws.Bool(true),
		})
		container.MountPoints = append(container.MountPoints, ecstypes.MountPoint{
			SourceVolume: aws.String(ecsScratchVolume), ContainerPath: aws.String(wsScratchPath),
		})
	}
	in := &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(e.name),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String(e.cpu),
		Memory:                  aws.String(e.memory),
		ExecutionRoleArn:        strOrNil(e.cfg.execRole),
		TaskRoleArn:             strOrNil(e.cfg.taskRole),
		ContainerDefinitions:    []ecstypes.ContainerDefinition{container},
		Volumes:                 volumes,
	}
	if e.diskGiB > 0 {
		// Task-local working disk. Absent = Fargate's default 20 GiB, which is also the
		// only free amount, so an unset size must stay absent rather than be written
		// as 20 (docs/63 §63.2: the API rejects anything below 21).
		in.EphemeralStorage = &ecstypes.EphemeralStorage{SizeInGiB: e.diskGiB}
	}
	out, err := e.ecs.RegisterTaskDefinition(ctx, in)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.TaskDefinition.TaskDefinitionArn), nil
}

// volumeConfigurations supplies the launch-time settings for the configured-at-launch
// scratch volume, and is nil in the normal (ephemeral-storage) case. ECS creates one
// EBS volume per task from these and deletes it when the task stops; the infrastructure
// role is what lets it call the EC2 volume APIs on the deployment's behalf.
func (e *ecsRuntime) volumeConfigurations() []ecstypes.ServiceVolumeConfiguration {
	if e.ebsGiB <= 0 || e.cfg.infraRole == "" {
		return nil
	}
	return []ecstypes.ServiceVolumeConfiguration{{
		Name: aws.String(ecsScratchVolume),
		ManagedEBSVolume: &ecstypes.ServiceManagedEBSVolumeConfiguration{
			RoleArn:        aws.String(e.cfg.infraRole),
			SizeInGiB:      aws.Int32(e.ebsGiB),
			VolumeType:     aws.String("gp3"),
			Encrypted:      aws.Bool(true),
			FilesystemType: ecstypes.TaskFilesystemTypeExt4,
			TagSpecifications: []ecstypes.EBSTagSpecification{{
				ResourceType: ecstypes.EBSResourceTypeVolume,
				Tags: []ecstypes.Tag{
					{Key: aws.String("af-membership"), Value: aws.String(e.membershipID)},
					{Key: aws.String("af-role"), Value: aws.String("scratch")},
				},
			}},
		},
	}}
}

func efsVolume(name, fsID, apID string) ecstypes.Volume {
	return ecstypes.Volume{
		Name: aws.String(name),
		EfsVolumeConfiguration: &ecstypes.EFSVolumeConfiguration{
			FileSystemId:      aws.String(fsID),
			TransitEncryption: ecstypes.EFSTransitEncryptionEnabled,
			AuthorizationConfig: &ecstypes.EFSAuthorizationConfig{
				AccessPointId: aws.String(apID),
				// IAM auth off: the workspace task role is least-privilege (no EFS
				// perms, frozen spec §20b.7.9). The access point's posix uid/gid +
				// root-dir and the EFS mount-target SG (2049 from the ws SG only)
				// provide isolation. NOTE: task roles are shared across memberships,
				// so per-membership EFS isolation via IAM would need a per-membership
				// task role — a P3-8 (dedicated isolation) follow-up, not this milestone.
				Iam: ecstypes.EFSAuthorizationConfigIAMDisabled,
			},
		},
	}
}

// upsertService creates the service on first use or updates it to desiredCount 1
// with the new task definition. The service advertises itself over Service Connect
// under its ContainerName so the CP can reach the Agent at Endpoint().
func (e *ecsRuntime) upsertService(ctx context.Context, taskDefArn string) error {
	s, ok, err := e.describeService(ctx)
	if err != nil {
		return err
	}
	if ok && aws.ToString(s.Status) == "ACTIVE" {
		_, err = e.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster:              aws.String(e.cfg.cluster),
			Service:              aws.String(e.name),
			DesiredCount:         aws.Int32(1),
			TaskDefinition:       aws.String(taskDefArn),
			ForceNewDeployment:   true,
			VolumeConfigurations: e.volumeConfigurations(),
		})
		return err
	}
	_, err = e.ecs.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:              aws.String(e.cfg.cluster),
		ServiceName:          aws.String(e.name),
		TaskDefinition:       aws.String(taskDefArn),
		DesiredCount:         aws.Int32(1),
		LaunchType:           ecstypes.LaunchTypeFargate,
		VolumeConfigurations: e.volumeConfigurations(),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        e.cfg.subnets,
				SecurityGroups: []string{e.cfg.securityGroup},
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
		ServiceConnectConfiguration: &ecstypes.ServiceConnectConfiguration{
			Enabled:   true,
			Namespace: strOrNil(e.cfg.namespaceArn),
			Services: []ecstypes.ServiceConnectService{{
				PortName:      aws.String("agent"),
				DiscoveryName: aws.String(e.name),
				ClientAliases: []ecstypes.ServiceConnectClientAlias{
					{DnsName: aws.String(e.name), Port: aws.Int32(ecsAgentPort)},
				},
			}},
		},
	})
	return err
}

// httpHealthz polls the Agent /healthz through the Service Connect endpoint until
// it returns 200 or the timeout elapses (same contract as the docker adapter).
func httpHealthz(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// キャンセル済み ctx で最大タイムアウトまでポーリングし続けない
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("agent health wait canceled: %w", err)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", endpoint+"/healthz", nil)
		// healthzClient (5s cap): ポーリングは再発行されるので、1 本のハングした probe が
		// 呼び出し元(scheduler fire の wg.Wait 等)を巻き込んで固まらないようにする。
		if resp, err := healthzClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("agent did not become healthy within %s", timeout)
}

func tagValue(tags []efstypes.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}
	return ""
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
