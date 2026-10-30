package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// --- task definition / service ---

// lastTaskDef caches the (instance, fingerprint, ARN) of the task definition each
// workspace last registered, so a Start that re-attaches to the SAME slot with
// otherwise-unchanged inputs can reuse it instead of registering a new revision and
// force-deploying. registerTaskDef + upsertService's ForceNewDeployment together
// retire the running task and start a fresh one — Service Connect load-balances
// across both for the ~1-2 minutes it takes to settle, which silently drops
// anything a caller stashed in the Agent process's memory (an OAuth flow_id;
// confirmed 2026-08-19 on the dev deployment). Most re-wakes change nothing (same image,
// same env, same secrets ARNs), so most re-wakes can skip that window entirely.
//
// Same shape as startGen/startPhase for the same reason: the Runtime object is
// rebuilt per request, so this cannot live on it. Process-local scratch, keyed by
// workspace name — so a CP restart or a second replica misses it, and that miss used
// to mean "register a redundant revision and roll the service for nothing", which
// every workspace paid on its first Start after every deploy. It is now only a miss of
// the FAST path: serviceTaskDefIfFingerprint asks AWS the same question by reading the
// fingerprint back off the revision the service is already on. Never
// incorrect (the fingerprint covers every per-Start-dynamic input, including the
// ws-settings/tenant-limits env that workspaceExtraEnv recomputes on every Start —
// see registerConnectionRoutes… no, see manager.workspaceExtraEnv), only sometimes
// a missed optimization.
var lastTaskDef sync.Map // workspace name -> ecTaskDefCacheEntry

type ecTaskDefCacheEntry struct {
	instanceID  string
	fingerprint string
	arn         string
}

// taskDefFingerprint hashes OUR OWN RegisterTaskDefinitionInput before it is sent —
// not ECS's read-back representation, which AWS is free to reorder or pad with
// defaults. Two calls that build byte-identical input (same code path, same
// arguments) always hash the same; anything that actually differs (env, secrets,
// image, instance) always hashes differently. That is all reuseOrRegisterTaskDef
// needs — it never has to understand what changed, only that something did.
func taskDefFingerprint(in *ecs.RegisterTaskDefinitionInput) string {
	b, err := json.Marshal(in)
	if err != nil {
		return "" // never matches a cached fingerprint; falls back to registering
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// reuseOrRegisterTaskDef returns the task definition to launch p on, reusing the
// last one registered for this workspace when the instance and the fingerprint
// both still match (see lastTaskDef), and registering a fresh revision otherwise.
// reused=false also covers the ordinary case of a genuinely new revision (image
// bump, changed settings, moved slot) — upsertService only needs to know whether
// to force a new deployment, and a fresh revision always needs one.
func (e *ecsEC2Runtime) reuseOrRegisterTaskDef(ctx context.Context, p ec2Placement, prep ec2Prep) (arn string, reused bool, err error) {
	in := e.buildTaskDef(ctx, p, prep)
	fp := taskDefFingerprint(in)
	if fp != "" {
		if v, ok := lastTaskDef.Load(e.base.name); ok {
			if entry, ok := v.(ecTaskDefCacheEntry); ok &&
				entry.instanceID == p.instanceID && entry.fingerprint == fp {
				return entry.arn, true, nil
			}
		}
		// Process-local miss, which is not the same thing as "something changed": the
		// map is empty after every CP restart and on whichever replica did not serve the
		// last Start. Ask AWS the same question before paying for a revision nobody
		// needs — see serviceTaskDefIfFingerprint for what that costs and buys.
		if arn := e.serviceTaskDefIfFingerprint(ctx, fp); arn != "" {
			lastTaskDef.Store(e.base.name, ecTaskDefCacheEntry{instanceID: p.instanceID, fingerprint: fp, arn: arn})
			return arn, true, nil
		}
		// ⚠️ AFTER the hash, never inside buildTaskDef. The label is the hash, so a
		// revision that carried it into the hash could never match the next Start's
		// freshly built (unlabelled) input, and reuse would be dead again — silently,
		// since the only symptom is a slower Start.
		stampTaskDefFingerprint(in, fp)
	}
	out, err := e.base.ecs.RegisterTaskDefinition(ctx, in)
	if err != nil {
		return "", false, err
	}
	arn = aws.ToString(out.TaskDefinition.TaskDefinitionArn)
	if fp != "" {
		lastTaskDef.Store(e.base.name, ecTaskDefCacheEntry{instanceID: p.instanceID, fingerprint: fp, arn: arn})
	}
	return arn, false, nil
}

// afTaskDefFingerprintLabel carries taskDefFingerprint on the revision it describes, so
// "have we already registered exactly this?" can be answered from AWS instead of only
// from lastTaskDef. Same trick and the same reason as afImageStampLabel
// (runtime_ecs_stale.go): a docker label rather than a task-definition tag, so reading it
// back needs neither include=TAGS nor ecs:TagResource.
const afTaskDefFingerprintLabel = "af.taskdef.fingerprint"

// stampTaskDefFingerprint writes fp onto the revision about to be registered. Must be
// called after taskDefFingerprint, never before — see the caller.
func stampTaskDefFingerprint(in *ecs.RegisterTaskDefinitionInput, fp string) {
	for i := range in.ContainerDefinitions {
		if in.ContainerDefinitions[i].DockerLabels == nil {
			in.ContainerDefinitions[i].DockerLabels = map[string]string{}
		}
		in.ContainerDefinitions[i].DockerLabels[afTaskDefFingerprintLabel] = fp
	}
}

// serviceTaskDefIfFingerprint returns the revision the service already points at, but
// only when that revision was registered from byte-identical input — i.e. only when
// registering again would produce the same thing. "" means "register a fresh one".
//
// This is what makes reuse survive a CP restart, and that matters more than it sounds:
// lastTaskDef is process-local, so before this every workspace's FIRST Start after a
// deploy registered a redundant revision, which made launch() split-and-wait (and, before
// §64.39, cost the full 40 seconds) for a task definition that had not changed at all.
// Everyone paid it, every deploy.
//
// Two AWS reads on the Start path, both already made elsewhere for other reasons, and
// only on a cache miss. Deliberately NOT wired into the /api/workspace poll.
//
// ⚠️ It must refuse an INACTIVE revision. Nothing here deregisters task definitions, but
// an operator's cleanup does, and UpdateService onto a deregistered revision fails — a
// reuse that turns a Start into an error is far worse than a redundant registration.
func (e *ecsEC2Runtime) serviceTaskDefIfFingerprint(ctx context.Context, fp string) string {
	s, ok, err := e.base.describeService(ctx)
	if err != nil || !ok {
		return ""
	}
	arn := aws.ToString(s.TaskDefinition)
	if arn == "" {
		return ""
	}
	out, err := e.base.ecs.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
	})
	if err != nil || out.TaskDefinition == nil ||
		out.TaskDefinition.Status != ecstypes.TaskDefinitionStatusActive {
		return ""
	}
	for _, c := range out.TaskDefinition.ContainerDefinitions {
		if c.DockerLabels[afTaskDefFingerprintLabel] == fp {
			return arn
		}
	}
	return ""
}

// buildTaskDef assembles (without submitting) a fresh EC2-launch-type revision
// pinned to one slot.
//
// Differences from the Fargate revision that matter:
//   - placementConstraints `ec2InstanceId == i-…` — the whole point. It lives on the
//     task definition rather than on the service, so moving a user to another slot
//     never touches the service's own shape.
//   - no task cpu / memory. On EC2 those are RESERVATIONS against the instance
//     (docs/log/64 §64.4.5); with one user per slot the right answer is to reserve nothing
//     and let them have the box. A small memoryReservation keeps the API happy.
//   - /tmp is a tmpfs. The root volume is shared with whoever had the slot before, so
//     a disk-backed /tmp would both leak (readable from the host) and let one user fill
//     the volume out from under the next (ADR 0045 decision 8, price 2). tmpfs is the one
//     tool Fargate did not have.
//   - home is a host bind of the freshly mounted EBS, not an EFS volume.
//
// It takes a ctx because the backend-drift stamp is probed from ECR here
// (runtime_ecs_stale.go). That stamp is part of what taskDefFingerprint hashes, and
// that is LOAD-BEARING rather than incidental: with a mutable tag (`:dev`) the image
// STRING does not change when the image does, so the stamp is the only field that
// tells reuseOrRegisterTaskDef the inputs moved. Excluding it would make a re-wake
// after an image push reuse the old revision — the task would still pull the new
// image, but it would carry the OLD stamp, and the workspace's "restart required"
// badge would then never clear no matter how often the user restarted.
func (e *ecsEC2Runtime) buildTaskDef(ctx context.Context, p ec2Placement, prep ec2Prep) *ecs.RegisterTaskDefinitionInput {
	env := []ecstypes.KeyValuePair{
		{Name: aws.String("CLAUDE_CONFIG_DIR"), Value: aws.String("/var/lib/af/claude")},
		// Where the entrypoint keeps the auth/identity set (ADR 0045 decision 3-6).
		{Name: aws.String("AF_WS_KEEP"), Value: aws.String(ec2KeepPath)},
		{Name: aws.String("AGENT_STOP_GRACE_SEC"), Value: aws.String(strconv.Itoa(agentStopGraceSec()))},
		// NOTE: AF_WS_SCRATCH is deliberately absent (ADR 0045 decision 10-3). It exists to
		// keep small-file build output off EFS; here home IS local NVMe-backed EBS, so
		// the relocation would buy nothing and would move the user's caches onto a disk
		// that is wiped when the slot changes hands.
	}
	if e.base.cfg.sessionCmd != "" {
		env = append(env, ecstypes.KeyValuePair{Name: aws.String("AGENT_SESSION_CMD"), Value: aws.String(e.base.cfg.sessionCmd)})
	}
	for _, kv := range e.base.extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env = append(env, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
		}
	}
	container := ecstypes.ContainerDefinition{
		Name:      aws.String("agent"),
		Image:     aws.String(e.base.cfg.workspaceImage),
		Essential: aws.Bool(true),
		// Backend-drift stamp — same contract as the Fargate revision
		// (runtime_ecs_stale.go).
		DockerLabels: e.base.stampImage(ctx),
		// The soft reservation ECS requires; the hard cap is set below when the pool
		// declares a host reserve.
		MemoryReservation: aws.Int32(512),
		PortMappings: []ecstypes.PortMapping{
			{ContainerPort: aws.Int32(ecsAgentPort), Name: aws.String("agent")},
		},
		Environment: env,
		Secrets:     prep.secrets,
		MountPoints: []ecstypes.MountPoint{
			{SourceVolume: aws.String("home"), ContainerPath: aws.String("/home/dev")},
			{SourceVolume: aws.String("claude"), ContainerPath: aws.String("/var/lib/af/claude")},
			{SourceVolume: aws.String("keep"), ContainerPath: aws.String(ec2KeepPath)},
		},
		StopTimeout: aws.Int32(int32(stopGraceSec())),
		LinuxParameters: &ecstypes.LinuxParameters{
			InitProcessEnabled: aws.Bool(true),
			// Chromium's setuid sandbox creates PID/network namespaces, and Docker's
			// default bounding set omits SYS_ADMIN, so the root-owned chrome-sandbox
			// cannot (same reason as the docker adapter's --cap-add). Without it
			// Chromium dies at startup ("Zygote process exited prematurely") while
			// browser features are still advertised. The workspace image strips
			// setuid from every other executable, so chrome-sandbox is the only path
			// that can acquire the capability. The slot's user.max_user_namespaces=0
			// (deploy/aws/ecs/cfn/40-ec2-pool.yaml) keeps unprivileged userns closed.
			Capabilities: &ecstypes.KernelCapabilities{Add: []string{"SYS_ADMIN"}},
			Tmpfs: []ecstypes.Tmpfs{{
				ContainerPath: aws.String("/tmp"),
				Size:          e.pool.tmpfsMiB,
				MountOptions:  e.pool.tmpfsOpts,
			}},
		},
	}
	// The hard cap. Deliberately on the CONTAINER rather than the task: a task-level
	// memory limit would also have to cover the Service Connect sidecar ECS injects,
	// and that sidecar's size is not ours to predict.
	//
	// ⚠️ This is part of the task definition's fingerprint, so the first Start after a
	// deployment that changes the reserve registers a new revision and replaces the
	// running task — the same thing an image or env change already does.
	if e.memCapMiB > 0 {
		container.Memory = aws.Int32(int32(e.memCapMiB))
	}
	if e.base.cfg.logGroup != "" {
		container.LogConfiguration = &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         e.base.cfg.logGroup,
				"awslogs-region":        e.base.cfg.region,
				"awslogs-stream-prefix": "agent",
			},
		}
	}
	// The slot's CPU architecture, declared rather than left to chance.
	//
	// Measured: on an EC2-compatibility task definition, omitting runtimePlatform
	// leaves it null — it does NOT default to X86_64 the way it does on Fargate — so
	// this is not what makes an arm64 slot work (docs/log/70 §70.8). It is stated anyway
	// for two reasons: ECS then refuses to PLACE a task on a box of the wrong
	// architecture instead of starting it and letting the image fail to exec, and the
	// field is part of what taskDefFingerprint hashes, so a member moved to another
	// class provably gets a new revision rather than a reused one.
	arch := ecstypes.CPUArchitectureX8664
	if e.arch == EC2ArchArm {
		arch = ecstypes.CPUArchitectureArm64
	}
	in := &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(e.base.name),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityEc2},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RuntimePlatform: &ecstypes.RuntimePlatform{
			CpuArchitecture:       arch,
			OperatingSystemFamily: ecstypes.OSFamilyLinux,
		},
		ExecutionRoleArn:     strOrNil(e.base.cfg.execRole),
		TaskRoleArn:          strOrNil(e.base.cfg.taskRole),
		ContainerDefinitions: []ecstypes.ContainerDefinition{container},
		PlacementConstraints: []ecstypes.TaskDefinitionPlacementConstraint{{
			Type:       ecstypes.TaskDefinitionPlacementConstraintTypeMemberOf,
			Expression: aws.String(fmt.Sprintf("ec2InstanceId == %s", p.instanceID)),
		}},
		Volumes: []ecstypes.Volume{
			{
				Name: aws.String("home"),
				Host: &ecstypes.HostVolumeProperties{SourcePath: aws.String(e.homeMountPoint() + "/dev")},
			},
			efsVolume("claude", e.base.cfg.efsFileSystem, prep.claudeAP),
			efsVolume("keep", e.base.cfg.efsFileSystem, prep.keepAP),
		},
	}
	return in
}

// netConfig is the awsvpc configuration every service call carries. The ENI must land in
// the slot's own AZ, so it names only that AZ's subnet: its workspace subnet when
// AF_ECS_WORKSPACE_SUBNETS is set (taskSubnetIn).
func (e *ecsEC2Runtime) netConfig(ctx context.Context, p ec2Placement) (*ecstypes.NetworkConfiguration, error) {
	subnet, err := e.taskSubnetIn(ctx, p.az)
	if err != nil {
		return nil, err
	}
	return &ecstypes.NetworkConfiguration{
		AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets:        []string{subnet},
			SecurityGroups: []string{e.base.cfg.securityGroup},
			// Never AssignPublicIp on a slot: a task ENI that outlives a stop turns the
			// instance into a multi-ENI box that silently loses its auto-assigned public
			// IPv4, and the agent then cannot reach the control plane at all
			// (ADR 0045 decision 3-3, measured: 11 minutes offline).
			AssignPublicIp: ecstypes.AssignPublicIpDisabled,
		},
	}, nil
}

// pointServiceAt moves an EXISTING service onto taskDefArn WITHOUT touching
// desiredCount, so the deployment ECS spawns for the revision change can retire while
// the slot is still booting. See launch() for why the two halves must not be one call.
//
// It reports whether the caller may finish with a bare scaleUpService. Everything that
// makes that unsafe answers false and leaves the old single-call path in charge:
//   - no service yet (first ever Start) — CreateService has no old deployment to fight;
//   - the service already points here — no deployment is spawned, nothing to wait for;
//   - the call failed — say so and take the slow path rather than scaling up a service
//     that may still be on the previous revision.
//
// ⚠️ Leaving desiredCount alone is also what makes this safe to issue this early: it
// cannot start a workspace that is not being started, so a Start that dies before the
// mount just leaves a stopped service pointing at a revision nobody runs. The next Start
// recomputes it. Same reason it is safe when the mount later quarantines the slot.
func (e *ecsEC2Runtime) pointServiceAt(ctx context.Context, taskDefArn string, p ec2Placement) bool {
	s, ok, err := e.base.describeService(ctx)
	if err != nil || !ok || aws.ToString(s.Status) != "ACTIVE" {
		return false
	}
	if aws.ToString(s.TaskDefinition) == taskDefArn {
		return false
	}
	netCfg, err := e.netConfig(ctx, p)
	if err != nil {
		return false
	}
	// Under the same lock as the scale-up: an overtaken Start must not move a service that
	// a Stop or a newer Start has since taken over (it would put the old revision back).
	if err := e.underStart(ctx, p, func(c context.Context) error {
		_, err := e.base.ecs.UpdateService(c, &ecs.UpdateServiceInput{
			Cluster:                     aws.String(e.base.cfg.cluster),
			Service:                     aws.String(e.base.name),
			TaskDefinition:              aws.String(taskDefArn),
			NetworkConfiguration:        netCfg,
			DeploymentConfiguration:     ec2SingleTaskDeployment,
			AvailabilityZoneRebalancing: ec2NoAZRebalancing,
		})
		return err
	}); errors.Is(err, errStartSuperseded) {
		return false
	} else if err != nil {
		log.Printf("ecs-ec2 start: could not pre-point %s at %s (falling back to the slower single update): %v",
			e.base.name, taskDefArn, err)
		return false
	}
	return true
}

// scaleUpService is the second half of the split: desiredCount ONLY, on a service
// pointServiceAt has already moved onto the right revision. Sending the task definition
// again here would be harmless, but sending ForceNewDeployment would not — it spawns the
// very ACTIVE deployment the split exists to retire first.
func (e *ecsEC2Runtime) scaleUpService(ctx context.Context) error {
	_, err := e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(e.base.cfg.cluster),
		Service:      aws.String(e.base.name),
		DesiredCount: aws.Int32(1),
		// Sent here too: this is the ONLY call on the path that raises the count, so a
		// service that somehow missed the setting must not raise it at 200%.
		DeploymentConfiguration:     ec2SingleTaskDeployment,
		AvailabilityZoneRebalancing: ec2NoAZRebalancing,
	})
	return err
}

// upsertService creates or updates the workspace's service at desiredCount 1 on the EC2
// launch type. Service Connect stays exactly as on Fargate, which is why Endpoint() did
// not have to change (docs/log/64 §64.4.4).
// forceNewDeployment should be false only when taskDefArn is a task definition
// reuseOrRegisterTaskDef found unchanged from what this service is already running —
// specifying it without forcing lets ECS notice nothing actually changed and skip
// the rolling replacement, instead of retiring a perfectly good running task.
//
// ⚠️ Since §64.39 this is no longer the ordinary path for a CHANGED revision: launch()
// splits that into pointServiceAt + scaleUpService, and only falls back here when the
// service does not exist yet (CreateService, which has no old deployment to fight) or
// when the pre-point failed. Sending a new revision together with desiredCount 1 is the
// thing that costs 40 seconds, so do not route more traffic back through it.
func (e *ecsEC2Runtime) upsertService(ctx context.Context, taskDefArn string, p ec2Placement, forceNewDeployment bool) error {
	netCfg, err := e.netConfig(ctx, p)
	if err != nil {
		return err
	}
	s, ok, err := e.base.describeService(ctx)
	if err != nil {
		return err
	}
	if ok && aws.ToString(s.Status) == "ACTIVE" {
		_, err = e.base.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster:                     aws.String(e.base.cfg.cluster),
			Service:                     aws.String(e.base.name),
			DesiredCount:                aws.Int32(1),
			TaskDefinition:              aws.String(taskDefArn),
			NetworkConfiguration:        netCfg,
			ForceNewDeployment:          forceNewDeployment,
			DeploymentConfiguration:     ec2SingleTaskDeployment,
			AvailabilityZoneRebalancing: ec2NoAZRebalancing,
		})
		return err
	}
	// DRAINING: the same name is being deleted and ECS has not finished. CreateService
	// answers "Create service is not idempotent" here — measured on a live deployment,
	// where a destroy and a re-create one minute apart collided (docs/log/64 §64.29.5) —
	// and that error reads like a bug in the caller rather than "come back shortly".
	// Neither call can succeed until the delete completes, so say so and let the retry
	// that already exists (the bake tick, a second Start) be the wait.
	if ok && aws.ToString(s.Status) == "DRAINING" {
		return fmt.Errorf("the previous %s service is still being deleted; retry once it is gone", e.base.name)
	}
	_, err = e.base.ecs.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:                     aws.String(e.base.cfg.cluster),
		ServiceName:                 aws.String(e.base.name),
		TaskDefinition:              aws.String(taskDefArn),
		DesiredCount:                aws.Int32(1),
		LaunchType:                  ecstypes.LaunchTypeEc2,
		NetworkConfiguration:        netCfg,
		DeploymentConfiguration:     ec2SingleTaskDeployment,
		AvailabilityZoneRebalancing: ec2NoAZRebalancing,
		ServiceConnectConfiguration: &ecstypes.ServiceConnectConfiguration{
			Enabled:   true,
			Namespace: strOrNil(e.base.cfg.namespaceArn),
			Services: []ecstypes.ServiceConnectService{{
				PortName:      aws.String("agent"),
				DiscoveryName: aws.String(e.base.name),
				ClientAliases: []ecstypes.ServiceConnectClientAlias{
					{DnsName: aws.String(e.base.name), Port: aws.Int32(ecsAgentPort)},
				},
			}},
		},
	})
	return err
}
