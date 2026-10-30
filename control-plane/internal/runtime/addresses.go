package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// HeldAddress is one network address a workspace holds or held, as its orchestrator reports
// it (ADR 0122, addendum 2026-10-24). Ref names the holder (task ARN, pod UID, container
// id) and, with From, one incarnation of it. Until is zero while the address is still held.
//
// From and Until are the orchestrator's times, never the time the CP looked: the CP looks
// every few seconds at best, and a connection made before the first look must still land
// on the right workspace.
type HeldAddress struct {
	Address, Ref string
	From, Until  time.Time
}

// AddressReport is one read of a workspace's addresses. Held is every address it holds now,
// plus any it held recently that the orchestrator still reports with an end time.
// Unresolved is the refs the orchestrator could not account for this time (ECS answers
// MISSING both for a task its eventually consistent index has not caught up with and for a
// stopped task it no longer keeps); the caller keeps those open for a while rather than
// reading them as gone, and its other observations still count.
type AddressReport struct {
	Held       []HeldAddress
	Unresolved []string
}

// AddressReporter is the optional capability behind egress attribution. open is the refs the
// caller still has as holding an address; each must come back in Held (with an end if it
// has one) or in Unresolved, unless the orchestrator positively says it is gone.
//
// An address missing from the report is taken as no longer held, so an error must be
// returned rather than a partial report whenever the orchestrator could not be read: a
// partial report closes intervals that are still running.
type AddressReporter interface {
	HeldAddresses(ctx context.Context, open []string) (AddressReport, error)
}

// AddressReleaseHook is Config.OnAddressesReleasing: told what a workspace holds just before
// a Stop takes it away. On docker and Kubernetes a stopped workspace leaves nothing to read
// afterwards, so a workspace started and stopped between two recorder sweeps would
// otherwise leave no record of its address at all.
type AddressReleaseHook func(ctx context.Context, membershipID, workspaceID string, held []HeldAddress)

// noteReleasingTimeout bounds what the hook adds to a Stop.
const noteReleasingTimeout = 10 * time.Second

// noteReleasing reads rep's addresses and hands them to hook. Best effort: a failed read
// leaves the recorder's next sweep to do what it can, and must never fail the Stop. It runs
// on its own short deadline, detached from the Stop's: a Stop that is cancelled is exactly
// when this record is wanted, and a slow read must not hold the Stop for long.
func noteReleasing(ctx context.Context, hook AddressReleaseHook, rep AddressReporter, membershipID, workspaceID string) {
	if hook == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noteReleasingTimeout)
	defer cancel()
	r, err := rep.HeldAddresses(ctx, nil)
	if err != nil || len(r.Held) == 0 {
		return
	}
	hook(ctx, membershipID, workspaceID, r.Held)
}

// HeldAddresses lists the service's tasks, running and recently stopped, and reports each
// one's ENI address from creation to stop. A task's ENI is created after the task and is
// gone by STOPPED, so two tasks' intervals for one address do not overlap.
//
// ListTasks and DescribeTasks are eventually consistent and can leave out a task that is
// running, so a task in open that the listing missed is described by ARN. Only a STOPPED
// answer ends it; MISSING proves nothing (the same rule as waitServiceTasksGone), so such a
// task comes back as unresolved. Any other describe failure fails the read.
func (e *ecsRuntime) HeldAddresses(ctx context.Context, open []string) (AddressReport, error) {
	lister, ok := e.ecs.(ecsTaskLister)
	if !ok {
		return AddressReport{}, errors.New("ecs client cannot list tasks")
	}
	var out AddressReport
	seen := map[string]bool{}
	for _, status := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		var next *string
		for {
			listed, err := lister.ListTasks(ctx, &ecs.ListTasksInput{
				Cluster:       aws.String(e.cfg.cluster),
				ServiceName:   aws.String(e.name),
				DesiredStatus: status,
				NextToken:     next,
			})
			if err != nil {
				var nf *ecstypes.ServiceNotFoundException
				if errors.As(err, &nf) {
					break // no service, no task: nothing is held
				}
				return AddressReport{}, err
			}
			if err := describeAddressTasks(ctx, lister, e.cfg.cluster, listed.TaskArns, seen, &out); err != nil {
				return AddressReport{}, err
			}
			if listed.NextToken == nil {
				break
			}
			next = listed.NextToken
		}
	}
	if err := describeMissedOpen(ctx, lister, e.cfg.cluster, open, seen, &out); err != nil {
		return AddressReport{}, err
	}
	return out, nil
}

// describeAddressTasks describes arns in batches (DescribeTasks takes at most 100 tasks) and
// adds what it finds to out. MISSING is unresolved; any other failure is an error.
func describeAddressTasks(ctx context.Context, lister ecsTaskLister, cluster string, arns []string, seen map[string]bool, out *AddressReport) error {
	for len(arns) > 0 {
		n := min(len(arns), 100)
		described, err := lister.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   arns[:n],
		})
		if err != nil {
			return err
		}
		for _, f := range described.Failures {
			if aws.ToString(f.Reason) != "MISSING" {
				return fmt.Errorf("describe task %s: %s", aws.ToString(f.Arn), aws.ToString(f.Reason))
			}
			seen[aws.ToString(f.Arn)] = true
			out.Unresolved = append(out.Unresolved, aws.ToString(f.Arn))
		}
		for _, t := range described.Tasks {
			seen[aws.ToString(t.TaskArn)] = true
		}
		out.Held = append(out.Held, taskHeldAddresses(described.Tasks)...)
		arns = arns[n:]
	}
	return nil
}

// describeMissedOpen describes by ARN the open refs a listing did not return, so a task the
// eventually consistent listing left out is still resolved (or kept open as unresolved).
func describeMissedOpen(ctx context.Context, lister ecsTaskLister, cluster string, open []string, seen map[string]bool, out *AddressReport) error {
	var missed []string
	for _, arn := range open {
		if !seen[arn] {
			missed = append(missed, arn)
		}
	}
	return describeAddressTasks(ctx, lister, cluster, missed, seen, out)
}

// taskHeldAddresses reads the ENI address out of each awsvpc task's attachment details.
func taskHeldAddresses(tasks []ecstypes.Task) []HeldAddress {
	var out []HeldAddress
	for _, t := range tasks {
		if t.CreatedAt == nil {
			continue
		}
		for _, att := range t.Attachments {
			if aws.ToString(att.Type) != "ElasticNetworkInterface" {
				continue
			}
			for _, d := range att.Details {
				if aws.ToString(d.Name) != "privateIPv4Address" {
					continue
				}
				a, err := netip.ParseAddr(aws.ToString(d.Value))
				if err != nil {
					continue
				}
				h := HeldAddress{Address: a.Unmap().String(), Ref: aws.ToString(t.TaskArn), From: *t.CreatedAt}
				if t.StoppedAt != nil {
					h.Until = *t.StoppedAt
				}
				out = append(out, h)
			}
		}
	}
	return out
}

// HeldAddresses: an ecs-ec2 workspace task is an awsvpc task of the same service shape, so
// its ENI is read the same way. The slot instance's own address is not the workspace's.
func (e *ecsEC2Runtime) HeldAddresses(ctx context.Context, open []string) (AddressReport, error) {
	return e.base.HeldAddresses(ctx, open)
}

// HeldAddresses reports the StatefulSet's pods that have an address. A pod deleted since the
// last look is simply absent, and the caller closes its interval when it sees that, which
// is later than the pod went; an overlap with the next holder then shows as ambiguous. open
// is not needed: a list is served from the API server's current state, not a lagging index.
func (k *kubeRuntime) HeldAddresses(ctx context.Context, _ []string) (AddressReport, error) {
	pods, err := k.workspacePods(ctx)
	if err != nil {
		return AddressReport{}, err
	}
	return AddressReport{Held: podHeldAddresses(pods)}, nil
}

func podHeldAddresses(pods []kPod) []HeldAddress {
	var out []HeldAddress
	for _, p := range pods {
		a, err := netip.ParseAddr(p.Status.PodIP)
		if err != nil || p.Metadata.UID == "" {
			continue
		}
		// startTime is when the kubelet took the pod, before its sandbox got the address;
		// creation is the fallback for a pod whose status does not carry it yet.
		from, err := time.Parse(time.RFC3339, p.Status.StartTime)
		if err != nil {
			if from, err = time.Parse(time.RFC3339, p.Metadata.CreationTimestamp); err != nil {
				continue
			}
		}
		out = append(out, HeldAddress{Address: a.Unmap().String(), Ref: p.Metadata.UID, From: from})
	}
	return out
}

// errDockerNoSuchObject is `docker inspect` answering that the container does not exist,
// as opposed to docker not answering at all.
var errDockerNoSuchObject = errors.New("no such object")

// HeldAddresses reports the container's address on its per-user network while it runs. open
// is not needed: docker answers for the one container by name.
func (d *dockerRuntime) HeldAddresses(ctx context.Context, _ []string) (AddressReport, error) {
	network := d.network
	if network == "" {
		network = "bridge"
	}
	format := `{{.Id}}|{{.State.StartedAt}}|{{with index .NetworkSettings.Networks "` + network + `"}}{{.IPAddress}}{{end}}`
	out, err := d.inspectAddresses(ctx, format)
	if errors.Is(err, errDockerNoSuchObject) {
		return AddressReport{}, nil
	}
	if err != nil {
		return AddressReport{}, err
	}
	held, err := dockerHeldAddresses(out)
	return AddressReport{Held: held}, err
}

func (d *dockerRuntime) inspectAddresses(ctx context.Context, format string) (string, error) {
	if d.inspect != nil {
		if out := d.inspect(ctx, "container", d.name, format); out != "" {
			return out, nil
		}
		return "", errDockerNoSuchObject
	}
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--type=container", "-f", format, d.name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "No such") {
			return "", errDockerNoSuchObject
		}
		return "", fmt.Errorf("docker inspect %s: %v: %s", d.name, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// dockerHeldAddresses parses "<id>|<startedAt>|<ip>". A stopped container has no address
// on the network, which is the answer "nothing held".
func dockerHeldAddresses(out string) ([]HeldAddress, error) {
	parts := strings.Split(out, "|")
	if len(parts) != 3 {
		return nil, fmt.Errorf("docker inspect: unexpected output %q", out)
	}
	if parts[2] == "" {
		return nil, nil
	}
	a, err := netip.ParseAddr(parts[2])
	if err != nil {
		return nil, fmt.Errorf("docker inspect: address %q: %v", parts[2], err)
	}
	from, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return nil, fmt.Errorf("docker inspect: start time %q: %v", parts[1], err)
	}
	return []HeldAddress{{Address: a.Unmap().String(), Ref: parts[0], From: from}}, nil
}

// The adapters that run a workspace behind an address the egress gateway can see. native
// is not one: it runs on the CP's own host, with no gateway in front of it.
var (
	_ AddressReporter = (*ecsRuntime)(nil)
	_ AddressReporter = (*ecsEC2Runtime)(nil)
	_ AddressReporter = (*kubeRuntime)(nil)
	_ AddressReporter = (*dockerRuntime)(nil)
)
