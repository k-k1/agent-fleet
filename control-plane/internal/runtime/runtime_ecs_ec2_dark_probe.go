package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Probe of the workspace Agent for a slot whose service is up (ADR 0121 decision 1, last
// bullet of the predicate). The ECS and SSM legs say the box is silent to the cluster; a
// service that is up may still be serving its member, so the sweeper asks the Agent itself
// before it calls such a slot dark.

// AgentProbe is the answer of one probe. The zero value is AgentProbeUnknown on purpose: a
// result nobody filled in must never read as "failed".
type AgentProbe int

const (
	// AgentProbeUnknown: the question was not answered. A probe that could not reach the
	// Agent's side at all (no route, a CP-side error, a cancelled context, a slow answer,
	// an exhausted budget) says nothing about the Agent.
	AgentProbeUnknown AgentProbe = iota
	// AgentProbeHealthy: the Agent answered.
	AgentProbeHealthy
	// AgentProbeFailed: the connection reached the Agent's side and was dropped (reset,
	// EOF) or the Service Connect proxy answered 502/503/504 for it.
	AgentProbeFailed
)

func (p AgentProbe) String() string {
	switch p {
	case AgentProbeHealthy:
		return "healthy"
	case AgentProbeFailed:
		return "failed"
	}
	return "unknown"
}

const (
	// darkProbeBudgetDefault bounds the probing of one sweep pass; darkProbeTimeoutDefault
	// bounds one probe. Probes run one after another, so the budget is what keeps a box
	// that answers slowly from stretching the pass for every slot behind it.
	darkProbeBudgetDefault  = 30 * time.Second
	darkProbeTimeoutDefault = 5 * time.Second
)

// probeObservation is the latest probe of a slot, kept so a later predicate (the wedged
// slot, #3963) can use it as a signal of its own without probing again.
type probeObservation struct {
	Result AgentProbe
	At     time.Time
	// TaskProven: the answer named a task the CP expects on the slot (see probeAgentTask).
	TaskProven bool
}

// agentProbes is the factory's memory of the latest probe per slot (instance id).
type agentProbes struct {
	mu sync.Mutex
	m  map[string]probeObservation
}

func (a *agentProbes) set(slot string, o probeObservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]probeObservation{}
	}
	a.m[slot] = o
}

// reset drops every observation. A pass starts from nothing so that a slot the pass did not
// probe (recovered, claimed, gone, a read failed) carries no value from an earlier pass, and
// a slot that disappeared does not stay in the map.
func (a *agentProbes) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m = nil
}

func (a *agentProbes) forget(slot string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, slot)
}

// LastAgentProbe returns the Agent probe of a slot from the latest dark-slot or wedged-slot
// pass that reached it, and when it ran. ok is false when that pass did not probe the slot (service
// down, a cluster leg healthy, a claim, the slot gone, a read failed) or none has run: each
// pass starts empty. A caller must still check At against its own freshness bound.
func (f *ecsEC2Factory) LastAgentProbe(slot string) (AgentProbe, time.Time, bool) {
	f.probes.mu.Lock()
	defer f.probes.mu.Unlock()
	o, ok := f.probes.m[slot]
	return o.Result, o.At, ok
}

// LastAgentTaskProven reports whether the latest probe of the slot was answered by a task the
// CP expects there. False for an Agent that does not report its task (older than #3993), a
// probe that failed, or no probe at all.
func (f *ecsEC2Factory) LastAgentTaskProven(slot string) bool {
	f.probes.mu.Lock()
	defer f.probes.mu.Unlock()
	return f.probes.m[slot].TaskProven
}

func (f *ecsEC2Factory) darkNow() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

func (f *ecsEC2Factory) agentDoer() httpDoer {
	if f.agentHTTP != nil {
		return f.agentHTTP
	}
	return healthzClient // the CP's client, with the Cloud Map fallback (deps.go)
}

// darkProbePass is the time budget of one sweep pass.
type darkProbePass struct{ deadline time.Time }

func (f *ecsEC2Factory) newDarkProbePass() *darkProbePass {
	b := f.pool.darkProbeBudget
	if b <= 0 {
		b = darkProbeBudgetDefault
	}
	return &darkProbePass{deadline: f.darkNow().Add(b)}
}

// probeAgent makes one GET /healthz against endpoint. It sends no credentials: /healthz is
// the Agent's open readiness route, so the bearer token never enters this path or its logs.
func (f *ecsEC2Factory) probeAgent(ctx context.Context, pass *darkProbePass, endpoint string) AgentProbe {
	res, _ := f.probeAgentTask(ctx, pass, endpoint, nil)
	return res
}

// maxHealthzBody bounds how much of the answer is read for the task id.
const maxHealthzBody = 64 << 10

// probeAgentTask is probeAgent plus the identity check. expected is the ids of the tasks the
// CP expects on the slot (the task ARN's last segment); the Agent reports its own as task_id.
//
//   - The Agent reports an id in expected: the answer is from a task of this slot, proven is true.
//   - The Agent reports an id outside expected: some other task answered the alias, so the
//     answer says nothing about this slot and the result is unknown.
//   - The Agent reports none (an Agent older than #3993, or an unreadable body): the answer
//     stands as healthy so detection behaves as before, but proven is false, and whatever
//     needs the identity (a rescue of a service-up slot) must not act on it.
//
// A failed probe carries no body and so never proves anything.
func (f *ecsEC2Factory) probeAgentTask(ctx context.Context, pass *darkProbePass, endpoint string, expected []string) (res AgentProbe, proven bool) {
	if endpoint == "" || ctx.Err() != nil {
		return AgentProbeUnknown, false
	}
	remaining := pass.deadline.Sub(f.darkNow())
	if remaining <= 0 {
		return AgentProbeUnknown, false // budget spent: this slot is not looked at this pass
	}
	each := f.pool.darkProbeTimeout
	if each <= 0 {
		each = darkProbeTimeoutDefault
	}
	pctx, cancel := context.WithTimeout(ctx, min(each, remaining))
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, endpoint+"/healthz", nil)
	if err != nil {
		return AgentProbeUnknown, false
	}
	resp, err := f.agentDoer().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return AgentProbeUnknown, false
		}
		return classifyProbeError(err), false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// What the Service Connect proxy says for an Agent nobody is listening for.
		return AgentProbeFailed, false
	}
	id := healthzTaskID(resp.Body)
	if id == "" {
		return AgentProbeHealthy, false
	}
	if len(expected) > 0 && slices.Contains(expected, id) {
		return AgentProbeHealthy, true
	}
	if expected != nil {
		return AgentProbeUnknown, false
	}
	return AgentProbeHealthy, false
}

// healthzTaskID reads the optional task_id of an Agent /healthz answer; "" when absent or
// the body is not JSON.
func healthzTaskID(r io.Reader) string {
	var h struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r, maxHealthzBody)).Decode(&h); err != nil {
		return ""
	}
	return h.TaskID
}

// taskIDOf is the id segment of a task ARN (arn:aws:ecs:<region>:<acct>:task/<cluster>/<id>).
func taskIDOf(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	return ""
}

// runningTaskIDs are the ids of the RUNNING tasks among tasks.
func runningTaskIDs(tasks []ecstypes.Task) []string {
	ids := []string{}
	for _, t := range tasks {
		if aws.ToString(t.LastStatus) == "RUNNING" {
			if id := taskIDOf(aws.ToString(t.TaskArn)); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// classifyProbeError maps a transport error onto failed only for the shapes measured on a
// thrashing box (docs: ADR 0121 Open question 6): the connection was made and then dropped.
// Everything else, a slow answer included, is unknown: a healthy but slow Agent must not be
// called failed, and when in doubt the slot stays unjudged.
func classifyProbeError(err error) AgentProbe {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return AgentProbeUnknown
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return AgentProbeUnknown
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return AgentProbeFailed
	}
	return AgentProbeUnknown
}

// probeStaleGrace is a conservative operating margin, not a proof of endpoint identity: the
// Cloud Map fallback caches an address for 30 s (agent_dial.go) and a pooled HTTP connection
// may outlive that, so a task that stopped less than this long ago is still treated as able to
// answer the alias. The bound on a pooled connection's life is not known; 2 minutes is a
// generous multiple of the cache TTL.
const probeStaleGrace = 2 * time.Minute

// serviceRunsOnlyOn reports whether the service's answer can only come from the EC2 instance
// slot: at least one task is RUNNING on it, and every other task that could still answer the
// alias is on it too. The alias resolves to whichever task Service Connect (or the Cloud Map
// cache) points at, so a task being stopped on another box (desired STOPPED, not yet gone), or
// one that stopped less than probeStaleGrace ago, may still reply and must keep the slot
// unknown. Any read that fails, a missing container instance or an unreadable task is false.
func (f *ecsEC2Factory) serviceRunsOnlyOn(ctx context.Context, service, slot string) bool {
	tasks, ok := f.serviceTasks(ctx, service)
	return ok && f.tasksRunOnlyOn(ctx, tasks, slot)
}

// serviceTasks reads every task of a service that is desired RUNNING or desired STOPPED
// (a stopping task is the latter, and a stopped one stays listed for a while). ok is false
// when any read failed or a listed task could not be described: a partial view of the
// tasks is not an answer.
func (f *ecsEC2Factory) serviceTasks(ctx context.Context, service string) ([]ecstypes.Task, bool) {
	lister, ok := f.ci.(ecsTaskLister)
	if !ok {
		return nil, false
	}
	cluster := aws.String(f.base.cfg.cluster)
	var arns []string
	for _, desired := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		var next *string
		for {
			listed, err := lister.ListTasks(ctx, &ecs.ListTasksInput{
				Cluster: cluster, ServiceName: aws.String(service),
				DesiredStatus: desired, NextToken: next,
			})
			if err != nil {
				return nil, false
			}
			arns = append(arns, listed.TaskArns...)
			if listed.NextToken == nil {
				break
			}
			next = listed.NextToken
		}
	}
	var tasks []ecstypes.Task
	for _, chunk := range chunkStrings(arns, 100) {
		described, err := lister.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: cluster, Tasks: chunk})
		if err != nil || len(described.Failures) > 0 || len(described.Tasks) != len(chunk) {
			return nil, false
		}
		tasks = append(tasks, described.Tasks...)
	}
	return tasks, true
}

// containerInstanceSlots maps container instance ARNs to EC2 instance ids. ok is false when
// any ARN could not be resolved (a failure, a missing instance, a failed call).
func (f *ecsEC2Factory) containerInstanceSlots(ctx context.Context, arns []string) (map[string]string, bool) {
	// Several tasks can sit on one container instance, and the API answers once per distinct
	// instance, so the input is deduplicated before the counts are compared. An empty ARN
	// cannot be placed: not an answer.
	seen := map[string]bool{}
	var uniq []string
	for _, a := range arns {
		if a == "" {
			return nil, false
		}
		if !seen[a] {
			seen[a] = true
			uniq = append(uniq, a)
		}
	}
	m := make(map[string]string, len(uniq))
	for _, chunk := range chunkStrings(uniq, 100) {
		out, err := f.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster: aws.String(f.base.cfg.cluster), ContainerInstances: chunk,
		})
		if err != nil || len(out.Failures) > 0 || len(out.ContainerInstances) != len(chunk) {
			return nil, false
		}
		for _, ci := range out.ContainerInstances {
			m[aws.ToString(ci.ContainerInstanceArn)] = aws.ToString(ci.Ec2InstanceId)
		}
	}
	return m, true
}

// tasksRunOnlyOn applies the rule of serviceRunsOnlyOn to tasks already read.
func (f *ecsEC2Factory) tasksRunOnlyOn(ctx context.Context, tasks []ecstypes.Task, slot string) bool {
	now := f.darkNow()
	cis := map[string]bool{}
	running := false
	for _, t := range tasks {
		if aws.ToString(t.LastStatus) == "STOPPED" {
			if t.StoppedAt == nil {
				return false // no stop time, so no evidence the grace has passed
			}
			if now.Sub(*t.StoppedAt) >= probeStaleGrace {
				continue // stopped well past the margin
			}
		}
		ci := aws.ToString(t.ContainerInstanceArn)
		if ci == "" {
			return false
		}
		cis[ci] = true
		if aws.ToString(t.LastStatus) == "RUNNING" {
			running = true
		}
	}
	if !running || len(cis) == 0 {
		return false
	}
	var ciArns []string
	for a := range cis {
		ciArns = append(ciArns, a)
	}
	slots, ok := f.containerInstanceSlots(ctx, ciArns)
	if !ok {
		return false
	}
	for _, id := range slots {
		if id != slot {
			return false
		}
	}
	return true
}
