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

// AddressReporter is the optional capability behind egress attribution. HeldAddresses
// answers every address the workspace holds now, plus any it held recently that the
// orchestrator still reports with an end time. An address missing from the answer is one
// the workspace no longer holds, so an error must be returned rather than a partial list
// whenever the orchestrator could not be read: a partial list closes intervals that are
// still open.
type AddressReporter interface {
	HeldAddresses(ctx context.Context) ([]HeldAddress, error)
}

// HeldAddresses lists the service's tasks, running and recently stopped, and reports each
// one's ENI address from creation to stop. A task's ENI is created after the task and is
// gone by STOPPED, so two tasks' intervals for one address do not overlap.
func (e *ecsRuntime) HeldAddresses(ctx context.Context) ([]HeldAddress, error) {
	lister, ok := e.ecs.(ecsTaskLister)
	if !ok {
		return nil, errors.New("ecs client cannot list tasks")
	}
	var out []HeldAddress
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
				return nil, err
			}
			// DescribeTasks takes at most 100 tasks; ListTasks pages at 100 by default.
			if len(listed.TaskArns) > 0 {
				described, err := lister.DescribeTasks(ctx, &ecs.DescribeTasksInput{
					Cluster: aws.String(e.cfg.cluster),
					Tasks:   listed.TaskArns,
				})
				if err != nil {
					return nil, err
				}
				for _, f := range described.Failures {
					if aws.ToString(f.Reason) != "MISSING" {
						return nil, fmt.Errorf("describe task %s: %s", aws.ToString(f.Arn), aws.ToString(f.Reason))
					}
				}
				out = append(out, taskHeldAddresses(described.Tasks)...)
			}
			if listed.NextToken == nil {
				break
			}
			next = listed.NextToken
		}
	}
	return out, nil
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
				h := HeldAddress{Address: a.String(), Ref: aws.ToString(t.TaskArn), From: *t.CreatedAt}
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
func (e *ecsEC2Runtime) HeldAddresses(ctx context.Context) ([]HeldAddress, error) {
	return e.base.HeldAddresses(ctx)
}

// HeldAddresses reports the StatefulSet's pods that have an address. A pod deleted since the
// last look is simply absent, and the caller closes its interval when it sees that, which
// is later than the pod went; an overlap with the next holder then shows as ambiguous.
func (k *kubeRuntime) HeldAddresses(ctx context.Context) ([]HeldAddress, error) {
	pods, err := k.workspacePods(ctx)
	if err != nil {
		return nil, err
	}
	return podHeldAddresses(pods), nil
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
		out = append(out, HeldAddress{Address: a.String(), Ref: p.Metadata.UID, From: from})
	}
	return out
}

// errDockerNoSuchObject is `docker inspect` answering that the container does not exist,
// as opposed to docker not answering at all.
var errDockerNoSuchObject = errors.New("no such object")

// HeldAddresses reports the container's address on its per-user network while it runs.
func (d *dockerRuntime) HeldAddresses(ctx context.Context) ([]HeldAddress, error) {
	network := d.network
	if network == "" {
		network = "bridge"
	}
	format := `{{.Id}}|{{.State.StartedAt}}|{{with index .NetworkSettings.Networks "` + network + `"}}{{.IPAddress}}{{end}}`
	out, err := d.inspectAddresses(ctx, format)
	if errors.Is(err, errDockerNoSuchObject) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dockerHeldAddresses(out)
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
	return []HeldAddress{{Address: a.String(), Ref: parts[0], From: from}}, nil
}

// The adapters that run a workspace behind an address the egress gateway can see. native
// is not one: it runs on the CP's own host, with no gateway in front of it.
var (
	_ AddressReporter = (*ecsRuntime)(nil)
	_ AddressReporter = (*ecsEC2Runtime)(nil)
	_ AddressReporter = (*kubeRuntime)(nil)
	_ AddressReporter = (*dockerRuntime)(nil)
)
