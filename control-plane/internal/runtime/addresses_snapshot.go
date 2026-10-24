package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// AddressSnapshotter is the optional factory-level capability next to AddressReporter: a
// runtime family that can answer many workspaces from one read instead of one read each.
// ECS asks about every workspace's own service otherwise, about three requests per running
// workspace per sweep against the account-shared ECS read throttle.
//
// A factory that does not implement it keeps the per-workspace path.
type AddressSnapshotter interface {
	// AddressSnapshot starts one sweep's snapshot. It reads nothing yet: each cluster is
	// read the first time a workspace in it asks, with ctx (the sweep's, not the asking
	// workspace's), and the answer is kept for the rest of the sweep.
	AddressSnapshot(ctx context.Context) AddressSnapshot
}

// AddressSnapshot serves the per-workspace reporters of one sweep.
type AddressSnapshot interface {
	// ReporterFor returns the reporter that answers rt's workspace from the snapshot, or
	// false when rt is not of the family the snapshot covers (use rt itself then).
	ReporterFor(rt Runtime) (AddressReporter, bool)
}

// addressSnapshotTimeout bounds one cluster's read (every page and batch of it). The
// per-workspace read has 20s; this one covers the whole cluster, so it gets more, but it must
// end well before the sweep's deadline: a read that held until then would leave the first
// workspace unmarked as asked, and the next sweep would start at the same place again.
const addressSnapshotTimeout = 60 * time.Second

// ecsAddressSnapshot lists each cluster once per sweep and groups its tasks by service.
type ecsAddressSnapshot struct {
	ctx     context.Context
	timeout time.Duration

	mu       sync.Mutex
	clusters map[string]*clusterTasks
}

// clusterTasks is one cluster's read. A failed read is kept as the error: every workspace
// in the cluster then fails this sweep and is read again at the next one, rather than each
// of them re-running the listing against the throttle.
type clusterTasks struct {
	once sync.Once
	// byService is the described tasks per "service:<name>" group, deduplicated by ARN.
	byService map[string][]ecstypes.Task
	err       error
}

func newECSAddressSnapshot(ctx context.Context) *ecsAddressSnapshot {
	return &ecsAddressSnapshot{ctx: ctx, timeout: addressSnapshotTimeout, clusters: map[string]*clusterTasks{}}
}

func (s *ecsAddressSnapshot) ReporterFor(rt Runtime) (AddressReporter, bool) {
	var e *ecsRuntime
	switch r := rt.(type) {
	case *ecsRuntime:
		e = r
	case *ecsEC2Runtime:
		e = r.base
	default:
		return nil, false
	}
	return &ecsSnapshotReporter{snap: s, e: e}, true
}

func (s *ecsAddressSnapshot) tasksOf(cluster string, lister ecsTaskLister) (map[string][]ecstypes.Task, error) {
	s.mu.Lock()
	c := s.clusters[cluster]
	if c == nil {
		c = &clusterTasks{}
		s.clusters[cluster] = c
	}
	s.mu.Unlock()
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
		defer cancel()
		c.byService, c.err = listClusterTasks(ctx, lister, cluster)
	})
	return c.byService, c.err
}

// listClusterTasks lists the cluster's RUNNING and STOPPED tasks (paged) and describes them
// in batches of 100. Any failed page or batch fails the whole read: a partial snapshot would
// read the workspaces its missing part covered as holding nothing, and close their intervals.
func listClusterTasks(ctx context.Context, lister ecsTaskLister, cluster string) (map[string][]ecstypes.Task, error) {
	var arns []string
	listed := map[string]bool{}
	for _, status := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		var next *string
		for {
			page, err := lister.ListTasks(ctx, &ecs.ListTasksInput{
				Cluster:       aws.String(cluster),
				DesiredStatus: status,
				NextToken:     next,
			})
			if err != nil {
				return nil, err
			}
			for _, a := range page.TaskArns {
				if !listed[a] {
					listed[a] = true
					arns = append(arns, a)
				}
			}
			if page.NextToken == nil {
				break
			}
			next = page.NextToken
		}
	}
	byService := map[string][]ecstypes.Task{}
	for len(arns) > 0 {
		n := min(len(arns), 100) // DescribeTasks takes at most 100 tasks
		described, err := lister.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(cluster), Tasks: arns[:n]})
		if err != nil {
			return nil, err
		}
		for _, f := range described.Failures {
			// MISSING is a task that aged out between the listing and the describe. The
			// workspace that still has it open describes it by ARN and gets it back as
			// unresolved, so it needs no entry here.
			if aws.ToString(f.Reason) != "MISSING" {
				return nil, errors.New("describe task " + aws.ToString(f.Arn) + ": " + aws.ToString(f.Reason))
			}
		}
		for _, t := range described.Tasks {
			g := aws.ToString(t.Group)
			byService[g] = append(byService[g], t)
		}
		arns = arns[n:]
	}
	return byService, nil
}

// ecsSnapshotReporter answers one workspace from the snapshot. What the snapshot does not
// hold is not read as gone: the workspace's open refs missing from it are described by ARN,
// the same as the per-workspace path does for a listing that left a task out.
type ecsSnapshotReporter struct {
	snap *ecsAddressSnapshot
	e    *ecsRuntime
}

func (r *ecsSnapshotReporter) HeldAddresses(ctx context.Context, open []string) (AddressReport, error) {
	lister, ok := r.e.ecs.(ecsTaskLister)
	if !ok {
		return AddressReport{}, errors.New("ecs client cannot list tasks")
	}
	byService, err := r.snap.tasksOf(r.e.cfg.cluster, lister)
	if err != nil {
		return AddressReport{}, err
	}
	tasks := byService["service:"+r.e.name]
	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		seen[aws.ToString(t.TaskArn)] = true
	}
	out := AddressReport{Held: taskHeldAddresses(tasks)}
	if err := describeMissedOpen(ctx, lister, r.e.cfg.cluster, open, seen, &out); err != nil {
		return AddressReport{}, err
	}
	return out, nil
}

// AddressSnapshot makes the ECS factory an AddressSnapshotter. The group a task carries is
// "service:<name>", which is how a task is told from the other tasks in the cluster.
func (f *ecsFactory) AddressSnapshot(ctx context.Context) AddressSnapshot {
	return newECSAddressSnapshot(ctx)
}

func (f *ecsEC2Factory) AddressSnapshot(ctx context.Context) AddressSnapshot {
	return newECSAddressSnapshot(ctx)
}

var (
	_ AddressSnapshotter = (*ecsFactory)(nil)
	_ AddressSnapshotter = (*ecsEC2Factory)(nil)
)
