package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// darkHarness is a slot that holds alice's home, launched long ago, whose ECS agent is
// disconnected and whose SSM agent is not listed: every leg of the predicate is true.
func darkHarness(t *testing.T) (*ec2Harness, *ecsEC2Factory, *[]SlotDark) {
	t.Helper()
	h := newEC2Harness(t)
	inst := h.ec2.addSlot("i-dark", "ap-northeast-1a", "m7i.large", true, false)
	inst.LaunchTime = aws.Time(time.Now().Add(-2 * time.Hour))
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-dark", time.Now().Add(-2*time.Hour))
	h.ci.registered["i-dark"] = false
	// The workspace service is nominally up, which is what keeps the volume sweep from
	// releasing the home and is exactly the shape the scan exists for.
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	h.ci.running = append(h.ci.running, runningTask("af-ws-acme-alice", "i-dark"))
	h.rt.pool.darkAction = darkActionDetect
	h.rt.pool.darkAfter = 10 * time.Minute
	h.rt.pool.darkBootGrace = 5 * time.Minute
	f := h.factory()
	// The service is up, so the Agent leg decides: the default fake drops the connection.
	f.agentHTTP = &fakeAgentDoer{err: &url.Error{Op: "Get", URL: "http://af-ws-acme-alice:7700/healthz", Err: syscall.ECONNRESET}}
	var events []SlotDark
	f.onSlotDark = func(_ context.Context, ev SlotDark) { events = append(events, ev) }
	return h, f, &events
}

func darkStampOf(h *ec2Harness) string {
	return ec2TagValue(h.ec2.instances["i-dark"].Tags, ec2TagDarkSince)
}

// backdateDark moves the stamp's first observation into the past, the way time would.
func backdateDark(t *testing.T, h *ec2Harness, ago time.Duration) {
	t.Helper()
	s, ok := parseDarkStamp(darkStampOf(h))
	if !ok {
		t.Fatalf("no stamp to backdate: %q", darkStampOf(h))
	}
	s.since = time.Now().Add(-ago)
	h.ec2.setInstanceTag("i-dark", ec2TagDarkSince, s.String())
}

func TestSweepDarkSlotStampsThenReportsOncePerEpisode(t *testing.T) {
	ctx := context.Background()
	h, f, events := darkHarness(t)

	if err := f.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if darkStampOf(h) == "" {
		t.Fatal("first sweep did not stamp the dark slot")
	}
	if len(*events) != 0 {
		t.Fatalf("reported on the first sighting: %v", *events)
	}

	// Still inside darkAfter: nothing.
	backdateDark(t, h, 5*time.Minute)
	if err := f.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatalf("reported after 5m of a 10m threshold: %v", *events)
	}

	backdateDark(t, h, 11*time.Minute)
	if err := f.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %v, want exactly one after darkAfter", *events)
	}
	ev := (*events)[0]
	if ev.Slot != "i-dark" || ev.Volume != "vol-1" || ev.MembershipID != "M-1" || ev.Workspace != "af-ws-acme-alice" {
		t.Fatalf("event = %+v", ev)
	}

	// Once per episode, and detect never acts on the box.
	for i := 0; i < 3; i++ {
		if err := f.sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(*events) != 1 {
		t.Fatalf("a long episode was reported %d times", len(*events))
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances") || strings.HasPrefix(c, "DetachVolume") || strings.HasPrefix(c, "TerminateInstances") {
			t.Fatalf("detect-only mode touched the box: %q", c)
		}
	}
	if len(h.ci.deregistered) != 0 {
		t.Fatalf("detect-only mode deregistered %v", h.ci.deregistered)
	}
}

func TestSweepDarkSlotLegsEachClearTheClock(t *testing.T) {
	ctx := context.Background()
	for name, heal := range map[string]func(h *ec2Harness){
		"ecs agent reconnects": func(h *ec2Harness) { h.ci.registered["i-dark"] = true },
		"ssm comes online":     func(h *ec2Harness) { h.ssmc.online = map[string]bool{"i-dark": true} },
	} {
		t.Run(name, func(t *testing.T) {
			h, f, events := darkHarness(t)
			if err := f.sweep(ctx); err != nil {
				t.Fatal(err)
			}
			backdateDark(t, h, 30*time.Minute)
			heal(h)
			if err := f.sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if got := darkStampOf(h); got != "" {
				t.Fatalf("stamp %q survived a healthy sweep", got)
			}
			if len(*events) != 0 {
				t.Fatalf("reported a slot that recovered: %v", *events)
			}
		})
	}
}

func TestSweepDarkSlotDoesNotActOnWhatItCannotProve(t *testing.T) {
	ctx := context.Background()

	t.Run("boot grace", func(t *testing.T) {
		h, f, _ := darkHarness(t)
		h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Minute))
		_ = f.sweep(ctx)
		if darkStampOf(h) != "" {
			t.Fatal("stamped a box that booted a minute ago")
		}
	})
	t.Run("ssm lookup fails", func(t *testing.T) {
		h, f, _ := darkHarness(t)
		h.ssmc.infoErr = errors.New("throttled")
		_ = f.sweep(ctx)
		if darkStampOf(h) != "" {
			t.Fatal("a failed SSM read was taken as 'SSM is silent'")
		}
		// And it must not clear an existing stamp either.
		h.ssmc.infoErr = nil
		_ = f.sweep(ctx)
		stamp := darkStampOf(h)
		h.ssmc.infoErr = errors.New("throttled")
		_ = f.sweep(ctx)
		if darkStampOf(h) != stamp {
			t.Fatalf("a failed SSM read changed the stamp: %q -> %q", stamp, darkStampOf(h))
		}
	})
	t.Run("ecs describe fails", func(t *testing.T) {
		h, f, _ := darkHarness(t)
		h.ci.describeErr = errors.New("throttled")
		_ = f.sweep(ctx)
		if darkStampOf(h) != "" {
			t.Fatal("a failed ECS read was taken as 'not registered'")
		}
	})
	t.Run("live claim belongs to the Start", func(t *testing.T) {
		h, f, _ := darkHarness(t)
		h.ec2.volumes["vol-1"].Tags = append(h.ec2.volumes["vol-1"].Tags,
			ec2tagKV(EC2TagClaim, "i-dark"), ec2tagKV(ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339)))
		_ = f.sweep(ctx)
		if darkStampOf(h) != "" {
			t.Fatal("scanned a slot a Start is converging on")
		}
	})
	t.Run("stopped box is not dark and loses a stale stamp", func(t *testing.T) {
		h, f, _ := darkHarness(t)
		_ = f.sweep(ctx)
		if darkStampOf(h) == "" {
			t.Fatal("setup: no stamp")
		}
		h.ec2.instances["i-dark"].State.Name = "stopped"
		_ = f.sweep(ctx)
		if darkStampOf(h) != "" {
			t.Fatal("stamp survived the box being stopped")
		}
	})
	t.Run("terminated box is not tagged", func(t *testing.T) {
		h, f, _ := darkHarness(t)
		_ = f.sweep(ctx)
		h.ec2.instances["i-dark"].State.Name = "terminated"
		h.ec2.calls = nil
		_ = f.sweep(ctx)
		for _, c := range h.ec2.calls {
			if strings.HasPrefix(c, "DeleteTags") {
				t.Fatalf("tried to untag a terminated box: %q", c)
			}
		}
	})
	t.Run("off and unset do nothing", func(t *testing.T) {
		for _, action := range []string{darkActionOff, ""} {
			h, f, _ := darkHarness(t)
			f.pool.darkAction = action
			_ = f.sweep(ctx)
			if darkStampOf(h) != "" {
				t.Fatalf("action %q stamped", action)
			}
		}
	})
}

func TestSweepDarkSlotRearmsOnANewLaunchEpoch(t *testing.T) {
	ctx := context.Background()
	h, f, events := darkHarness(t)
	_ = f.sweep(ctx)
	backdateDark(t, h, 30*time.Minute)
	// The box was stopped and started again since: LaunchTime moved, the old clock is void.
	h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-20 * time.Minute))
	_ = f.sweep(ctx)
	if len(*events) != 0 {
		t.Fatalf("an old epoch's stamp was honoured: %v", *events)
	}
	s, ok := parseDarkStamp(darkStampOf(h))
	if !ok || time.Since(s.since) > time.Minute || s.detected {
		t.Fatalf("stamp not re-armed: %q", darkStampOf(h))
	}
}

func TestDarkStampRoundTripAndClamp(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, d := range []bool{false, true} {
		in := darkStamp{since: now, launch: now.Add(-time.Hour), detected: d}
		out, ok := parseDarkStamp(in.String())
		if !ok || out != in {
			t.Fatalf("round trip %q -> %+v ok=%v", in.String(), out, ok)
		}
	}
	for _, bad := range []string{"", "x", "a/b", now.Format(time.RFC3339), now.Format(time.RFC3339) + "/" + now.Format(time.RFC3339) + "/other"} {
		if _, ok := parseDarkStamp(bad); ok {
			t.Fatalf("parsed %q", bad)
		}
	}
	p := ec2PoolConfig{darkAfter: time.Minute, darkBootGrace: 5 * time.Minute, slotLostAfter: 5 * time.Minute, sweepEvery: 5 * time.Minute}
	p.clampDark()
	if p.darkAfter != 10*time.Minute {
		t.Fatalf("darkAfter = %v, want clamped to slotLostAfter+sweepEvery", p.darkAfter)
	}
	off := ec2PoolConfig{}
	off.clampDark()
	if off.darkAfter != 0 {
		t.Fatal("0 must stay off")
	}
	for in, want := range map[string]string{"": "detect", "off": "off", "DETECT": "detect", "rescue": "detect", "quarantine": "detect", "bogus": "detect"} {
		if got := parseDarkAction(in); got != want {
			t.Fatalf("parseDarkAction(%q) = %q, want %q", in, got, want)
		}
	}
}

func ec2tagKV(k, v string) ec2types.Tag {
	return ec2types.Tag{Key: aws.String(k), Value: aws.String(v)}
}

// fakeAgentDoer is the transport behind the Agent probe: it answers every request with err
// or status, and records the requests it saw.
type fakeAgentDoer struct {
	err    error
	status int
	onDo   func()
	reqs   []*http.Request
}

func (d *fakeAgentDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if d.onDo != nil {
		d.onDo()
	}
	if d.err != nil {
		return nil, d.err
	}
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestDarkAgentProbeDecidesAServiceUpSlot(t *testing.T) {
	ctx := context.Background()
	getErr := func(err error) error { return &url.Error{Op: "Get", URL: "http://x:7700/healthz", Err: err} }
	cases := []struct {
		name      string
		doer      *fakeAgentDoer
		want      AgentProbe
		wantStamp bool
	}{
		{"connection reset", &fakeAgentDoer{err: getErr(syscall.ECONNRESET)}, AgentProbeFailed, true},
		{"EOF", &fakeAgentDoer{err: getErr(io.EOF)}, AgentProbeFailed, true},
		{"proxy 503", &fakeAgentDoer{status: 503}, AgentProbeFailed, true},
		{"healthy", &fakeAgentDoer{status: 200}, AgentProbeHealthy, false},
		{"timeout is not an answer", &fakeAgentDoer{err: getErr(timeoutErr{})}, AgentProbeUnknown, false},
		{"deadline", &fakeAgentDoer{err: getErr(context.DeadlineExceeded)}, AgentProbeUnknown, false},
		{"no route", &fakeAgentDoer{err: getErr(&net.DNSError{Err: "no such host", IsNotFound: true})}, AgentProbeUnknown, false},
		{"cp side error", &fakeAgentDoer{err: errors.New("boom")}, AgentProbeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := darkHarness(t)
			f.agentHTTP = tc.doer
			if err := f.sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if got := darkStampOf(h) != ""; got != tc.wantStamp {
				t.Fatalf("stamped = %v, want %v", got, tc.wantStamp)
			}
			res, _, ok := f.LastAgentProbe("i-dark")
			if !ok || res != tc.want {
				t.Fatalf("LastAgentProbe = %v, %v; want %v", res, ok, tc.want)
			}
			if len(tc.doer.reqs) != 1 {
				t.Fatalf("probes = %d, want 1", len(tc.doer.reqs))
			}
			req := tc.doer.reqs[0]
			if req.URL.String() != "http://af-ws-acme-alice:7700/healthz" || req.Header.Get("Authorization") != "" {
				t.Fatalf("request = %s auth=%q", req.URL, req.Header.Get("Authorization"))
			}
		})
	}
}

func TestDarkAgentProbeUnknownKeepsAnExistingStamp(t *testing.T) {
	ctx := context.Background()
	h, f, _ := darkHarness(t)
	_ = f.sweep(ctx)
	stamp := darkStampOf(h)
	if stamp == "" {
		t.Fatal("setup: no stamp")
	}
	f.agentHTTP = &fakeAgentDoer{err: timeoutErr{}}
	_ = f.sweep(ctx)
	if darkStampOf(h) != stamp {
		t.Fatalf("an unknown probe changed the stamp: %q -> %q", stamp, darkStampOf(h))
	}
	f.agentHTTP = &fakeAgentDoer{status: 200}
	_ = f.sweep(ctx)
	if darkStampOf(h) != "" {
		t.Fatal("a healthy Agent did not clear the stamp")
	}
}

func TestDarkAgentProbeServiceDownIsNotProbed(t *testing.T) {
	ctx := context.Background()
	h, f, _ := darkHarness(t)
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	doer := &fakeAgentDoer{status: 200}
	f.agentHTTP = doer
	_ = f.sweep(ctx)
	if len(doer.reqs) != 0 {
		t.Fatalf("probed a slot whose service is down: %d requests", len(doer.reqs))
	}
	if darkStampOf(h) == "" {
		t.Fatal("a service-down dark slot was not stamped")
	}
	if _, _, ok := f.LastAgentProbe("i-dark"); ok {
		t.Fatal("a probe result is recorded for a slot that was not probed")
	}
}

func TestDarkAgentProbeBudgetExhaustedIsUnknown(t *testing.T) {
	ctx := context.Background()
	h, f, _ := darkHarness(t)
	// A second dark slot behind the first; each probe consumes the whole budget.
	inst := h.ec2.addSlot("i-dark2", "ap-northeast-1a", "m7i.large", true, false)
	inst.LaunchTime = aws.Time(time.Now().Add(-2 * time.Hour))
	h.ec2.addHomeVolume("vol-2", "M-2", "af-ws-acme-bob", "ap-northeast-1a")
	h.ec2.attach("vol-2", "i-dark2", time.Now().Add(-2*time.Hour))
	h.ci.registered["i-dark2"] = false
	h.ecs.services["af-ws-acme-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	h.ci.running = append(h.ci.running, runningTask("af-ws-acme-bob", "i-dark2"))
	now := time.Now()
	f.clock = func() time.Time { return now }
	f.pool.darkProbeBudget = 10 * time.Second
	doer := &fakeAgentDoer{err: syscall.ECONNRESET, onDo: func() { now = now.Add(11 * time.Second) }}
	f.agentHTTP = doer
	if err := f.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("probes = %d, want 1: the budget must stop the second", len(doer.reqs))
	}
	stamped := 0
	for _, id := range []string{"i-dark", "i-dark2"} {
		if ec2TagValue(h.ec2.instances[id].Tags, ec2TagDarkSince) != "" {
			stamped++
		}
	}
	if stamped != 1 {
		t.Fatalf("stamped %d slots, want exactly the one that was probed", stamped)
	}
	failed, unknown := 0, 0
	for _, id := range []string{"i-dark", "i-dark2"} {
		switch r, _, _ := f.LastAgentProbe(id); r {
		case AgentProbeFailed:
			failed++
		case AgentProbeUnknown:
			unknown++
		}
	}
	if failed != 1 || unknown != 1 {
		t.Fatalf("failed=%d unknown=%d, want 1 and 1", failed, unknown)
	}
}

func TestDarkAgentProbeCancelledContextIsUnknown(t *testing.T) {
	_, f, _ := darkHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doer := &fakeAgentDoer{err: syscall.ECONNRESET}
	f.agentHTTP = doer
	if got := f.probeAgent(ctx, f.newDarkProbePass(), "http://x:7700"); got != AgentProbeUnknown {
		t.Fatalf("cancelled context = %v", got)
	}
	if len(doer.reqs) != 0 {
		t.Fatal("sent a request on a cancelled context")
	}
}

func runningTask(service, instance string) ecstypes.Task {
	return ecstypes.Task{
		TaskArn: aws.String("arn:task/" + service), Group: aws.String("service:" + service),
		ContainerInstanceArn: aws.String("arn:ci/" + instance), LastStatus: aws.String("RUNNING"),
	}
}

// A healthy answer through the service alias proves nothing about this slot when the
// service's task runs on another box: it must neither clear the stamp nor be recorded.
func TestDarkAgentProbeNeedsTheServiceTaskOnThisSlot(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(h *ec2Harness){
		"task on another instance": func(h *ec2Harness) {
			h.ci.running = []ecstypes.Task{runningTask("af-ws-acme-alice", "i-other")}
		},
		"old and new task on two boxes": func(h *ec2Harness) {
			h.ci.running = append(h.ci.running, runningTask("af-ws-acme-alice", "i-other"))
			h.ci.running[1].TaskArn = aws.String("arn:task/second")
		},
		"old task still stopping on another instance": func(h *ec2Harness) {
			old := runningTask("af-ws-acme-alice", "i-other")
			old.TaskArn = aws.String("arn:task/old")
			old.LastStatus = aws.String("STOPPING")
			h.ci.stopping = []ecstypes.Task{old}
		},
		"old task stopped seconds ago on another instance": func(h *ec2Harness) {
			old := runningTask("af-ws-acme-alice", "i-other")
			old.TaskArn = aws.String("arn:task/old")
			old.LastStatus = aws.String("STOPPED")
			old.StoppedAt = aws.Time(time.Now().Add(-20 * time.Second))
			h.ci.stopping = []ecstypes.Task{old}
		},
		"old task stopped with unknown stop time": func(h *ec2Harness) {
			old := runningTask("af-ws-acme-alice", "i-other")
			old.TaskArn = aws.String("arn:task/old")
			old.LastStatus = aws.String("STOPPED")
			h.ci.stopping = []ecstypes.Task{old}
		},
		"no task listed": func(h *ec2Harness) { h.ci.running = nil },
		"task describe failure": func(h *ec2Harness) {
			h.ci.taskFailures = []ecstypes.Failure{{Arn: aws.String("arn:task/af-ws-acme-alice"), Reason: aws.String("MISSING")}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, f, _ := darkHarness(t)
			_ = f.sweep(ctx)
			stamp := darkStampOf(h)
			if stamp == "" {
				t.Fatal("setup: no stamp")
			}
			mutate(h)
			doer := &fakeAgentDoer{status: 200}
			f.agentHTTP = doer
			_ = f.sweep(ctx)
			if darkStampOf(h) != stamp {
				t.Fatalf("an answer from another task changed the stamp: %q -> %q", stamp, darkStampOf(h))
			}
			if len(doer.reqs) != 0 {
				t.Fatalf("probed although the task is not provably on this slot")
			}
			if r, _, ok := f.LastAgentProbe("i-dark"); !ok || r != AgentProbeUnknown {
				t.Fatalf("LastAgentProbe = %v, %v; want unknown", r, ok)
			}
		})
	}
}

// Each pass starts empty: a slot that recovered, was claimed, vanished or could not be read
// must not keep the failed of an earlier pass.
func TestLastAgentProbeDoesNotOutliveThePass(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(h *ec2Harness){
		"ecs agent reconnects": func(h *ec2Harness) { h.ci.registered["i-dark"] = true },
		"ssm comes online":     func(h *ec2Harness) { h.ssmc.online = map[string]bool{"i-dark": true} },
		"cluster read fails":   func(h *ec2Harness) { h.ci.describeErr = errors.New("throttled") },
		"slot gone": func(h *ec2Harness) {
			delete(h.ec2.instances, "i-dark")
			delete(h.ec2.volumes, "vol-1")
		},
		"live claim": func(h *ec2Harness) {
			h.ec2.volumes["vol-1"].Tags = append(h.ec2.volumes["vol-1"].Tags,
				ec2tagKV(EC2TagClaim, "i-dark"), ec2tagKV(ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, f, _ := darkHarness(t)
			_ = f.sweep(ctx)
			if r, _, ok := f.LastAgentProbe("i-dark"); !ok || r != AgentProbeFailed {
				t.Fatalf("setup: LastAgentProbe = %v, %v", r, ok)
			}
			change(h)
			_ = f.sweep(ctx)
			if r, _, ok := f.LastAgentProbe("i-dark"); ok {
				t.Fatalf("stale probe %v survived the pass", r)
			}
		})
	}
}

// A task that stopped long ago on another box cannot be what the alias reaches any more.
func TestDarkAgentProbeIgnoresLongStoppedTasks(t *testing.T) {
	h, f, _ := darkHarness(t)
	old := runningTask("af-ws-acme-alice", "i-other")
	old.TaskArn = aws.String("arn:task/old")
	old.LastStatus = aws.String("STOPPED")
	old.StoppedAt = aws.Time(time.Now().Add(-10 * time.Minute))
	h.ci.stopping = []ecstypes.Task{old}
	doer := &fakeAgentDoer{status: 200}
	f.agentHTTP = doer
	_ = f.sweep(context.Background())
	if len(doer.reqs) != 1 {
		t.Fatalf("probes = %d, want 1", len(doer.reqs))
	}
}
