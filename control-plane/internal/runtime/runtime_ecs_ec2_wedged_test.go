package runtime

import (
	"context"
	"errors"
	"net/url"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// wedgedHarness is the slot of the 2026-11-12 incident: it holds alice's home, was launched
// long ago, its ECS agent is connected (and SSM online), its task is RUNNING and HEALTHY in
// ECS, and the Agent drops every connection. The dark scan is off, so only the wedged scan
// can see it.
func wedgedHarness(t *testing.T) (*ec2Harness, *ecsEC2Factory, *[]SlotWedged, *fakeAgentDoer) {
	t.Helper()
	h, f, _ := darkHarness(t)
	h.rt.pool.darkAction = darkActionOff
	f.pool.darkAction = darkActionOff
	f.pool.wedgedAction = wedgedActionDetect
	f.pool.wedgedAfter = 10 * time.Minute
	h.ci.registered["i-dark"] = true
	h.ssmc.online = map[string]bool{"i-dark": true}
	task := runningTask("af-ws-acme-alice", "i-dark")
	task.HealthStatus = ecstypes.HealthStatusHealthy
	h.ci.running = []ecstypes.Task{task}
	doer := &fakeAgentDoer{err: &url.Error{Op: "Get", URL: "http://af-ws-acme-alice:7700/healthz", Err: syscall.ECONNRESET}}
	f.agentHTTP = doer
	var events []SlotWedged
	f.onSlotWedged = func(_ context.Context, ev SlotWedged) { events = append(events, ev) }
	return h, f, &events, doer
}

func wedgedStampOf(h *ec2Harness) string {
	return ec2TagValue(h.ec2.instances["i-dark"].Tags, ec2TagWedgedSince)
}

func backdateWedged(t *testing.T, h *ec2Harness, ago time.Duration) {
	t.Helper()
	s, ok := parseDarkStamp(wedgedStampOf(h))
	if !ok {
		t.Fatalf("no stamp to backdate: %q", wedgedStampOf(h))
	}
	s.since = time.Now().Add(-ago)
	h.ec2.setInstanceTag("i-dark", ec2TagWedgedSince, s.String())
}

// stuckStoppingTask is the old task of the incident: asked to stop long ago on i-dark.
func stuckStoppingTask(ago time.Duration) ecstypes.Task {
	old := runningTask("af-ws-acme-alice", "i-dark")
	old.TaskArn = aws.String("arn:task/old")
	old.LastStatus = aws.String("STOPPING")
	old.DesiredStatus = aws.String("STOPPED")
	old.StoppingAt = aws.Time(time.Now().Add(-ago))
	return old
}

func TestSweepWedgedSlotProbeFailedStampsThenReportsOncePerEpisode(t *testing.T) {
	ctx := context.Background()
	h, f, events, _ := wedgedHarness(t)

	_ = f.sweep(ctx)
	if wedgedStampOf(h) == "" {
		t.Fatal("first sweep did not stamp the wedged slot")
	}
	if len(*events) != 0 {
		t.Fatalf("reported on the first sighting: %v", *events)
	}
	if darkStampOf(h) != "" {
		t.Fatal("a slot whose agents answer must not get a dark stamp")
	}

	backdateWedged(t, h, 5*time.Minute)
	_ = f.sweep(ctx)
	if len(*events) != 0 {
		t.Fatalf("reported after 5m of a 10m threshold: %v", *events)
	}

	backdateWedged(t, h, 11*time.Minute)
	_ = f.sweep(ctx)
	if len(*events) != 1 {
		t.Fatalf("events after the threshold = %d, want 1", len(*events))
	}
	ev := (*events)[0]
	if ev.Slot != "i-dark" || ev.Volume != "vol-1" || ev.Signal != wedgedSignalProbeFailed || ev.Workspace != "af-ws-acme-alice" {
		t.Fatalf("event = %+v", ev)
	}
	if st, _ := parseDarkStamp(wedgedStampOf(h)); !st.detected {
		t.Fatalf("stamp not marked detected: %q", wedgedStampOf(h))
	}
	_ = f.sweep(ctx)
	_ = f.sweep(ctx)
	if len(*events) != 1 {
		t.Fatalf("re-reported within one episode: %d events", len(*events))
	}
}

func TestSweepWedgedSlotStuckStoppingTaskIsASignalWithoutAProbe(t *testing.T) {
	ctx := context.Background()
	h, f, events, doer := wedgedHarness(t)
	// The member stopped the workspace: no running task, the old one never finished.
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ci.running = nil
	h.ci.stopping = []ecstypes.Task{stuckStoppingTask(40 * time.Minute)}

	_ = f.sweep(ctx)
	if wedgedStampOf(h) == "" {
		t.Fatal("a task STOPPING for 40m did not stamp the slot")
	}
	backdateWedged(t, h, 11*time.Minute)
	_ = f.sweep(ctx)
	if len(*events) != 1 || (*events)[0].Signal != wedgedSignalStopping {
		t.Fatalf("events = %+v", *events)
	}
	if len(doer.reqs) != 0 {
		t.Fatal("the stopping signal needs no probe")
	}
}

// Each of these is a healthy box, or one that cannot be judged, and must leave no stamp.
func TestSweepWedgedSlotFalsePositives(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer){
		"task stopping within stopTimeout plus grace": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ci.running = nil
			h.ci.stopping = []ecstypes.Task{stuckStoppingTask(2 * time.Minute)}
		},
		"task stopping with no stop time": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ci.running = nil
			old := stuckStoppingTask(time.Hour)
			old.StoppingAt = nil
			h.ci.stopping = []ecstypes.Task{old}
		},
		"old stopping task on another box": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ci.running = nil
			old := stuckStoppingTask(time.Hour)
			old.ContainerInstanceArn = aws.String("arn:ci/i-other")
			h.ci.stopping = []ecstypes.Task{old}
		},
		"deployment rolling: old and new task on two boxes": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			other := runningTask("af-ws-acme-alice", "i-other")
			other.TaskArn = aws.String("arn:task/second")
			other.HealthStatus = ecstypes.HealthStatusHealthy
			h.ci.running = append(h.ci.running, other)
		},
		"deployment rolling: old task stopping on another box": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			old := runningTask("af-ws-acme-alice", "i-other")
			old.TaskArn = aws.String("arn:task/old")
			old.LastStatus = aws.String("STOPPING")
			old.StoppingAt = aws.Time(time.Now().Add(-time.Minute))
			h.ci.stopping = []ecstypes.Task{old}
		},
		"task not yet healthy in ECS": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ci.running[0].HealthStatus = ecstypes.HealthStatusUnknown
		},
		"probe answers": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			d.err, d.status = nil, 200
		},
		"probe unknown (timeout)": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			d.err = timeoutErr{}
		},
		"probe unknown (no route)": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			d.err = errors.New("no route to host")
		},
		"ecs agent disconnected (the dark scan's case)": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ci.registered["i-dark"] = false
		},
		"still inside the boot grace": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Minute))
		},
		"service gone": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			delete(h.ecs.services, "af-ws-acme-alice")
		},
		"a live claim": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			h.ec2.volumes["vol-1"].Tags = append(h.ec2.volumes["vol-1"].Tags,
				ec2tagKV(EC2TagClaim, "i-dark"), ec2tagKV(ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339)))
		},
		"scan off": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			f.pool.wedgedAction = wedgedActionOff
		},
		"wedgedAfter zero": func(h *ec2Harness, f *ecsEC2Factory, d *fakeAgentDoer) {
			f.pool.wedgedAfter = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, f, events, doer := wedgedHarness(t)
			mutate(h, f, doer)
			for i := 0; i < 3; i++ {
				_ = f.sweep(ctx)
			}
			if s := wedgedStampOf(h); s != "" {
				t.Fatalf("stamped a box that is not provably wedged: %q", s)
			}
			if len(*events) != 0 {
				t.Fatalf("reported: %v", *events)
			}
		})
	}
}

// A read that fails is not an answer: it neither stamps nor clears.
func TestSweepWedgedSlotFailedReadIsUnknown(t *testing.T) {
	ctx := context.Background()
	for name, fail := range map[string]func(h *ec2Harness){
		"task describe failure": func(h *ec2Harness) {
			h.ci.taskFailures = []ecstypes.Failure{{Arn: aws.String("arn:task/af-ws-acme-alice"), Reason: aws.String("MISSING")}}
		},
		"container instance read fails": func(h *ec2Harness) { h.ci.describeErr = errors.New("throttled") },
	} {
		t.Run(name, func(t *testing.T) {
			h, f, events, _ := wedgedHarness(t)
			_ = f.sweep(ctx)
			backdateWedged(t, h, 5*time.Minute)
			before := wedgedStampOf(h)
			fail(h)
			_ = f.sweep(ctx)
			if wedgedStampOf(h) != before {
				t.Fatalf("a failed read changed the stamp: %q -> %q", before, wedgedStampOf(h))
			}
			if len(*events) != 0 {
				t.Fatalf("reported on a failed read: %v", *events)
			}
		})
	}
	t.Run("no stamp is written either", func(t *testing.T) {
		h, f, _, _ := wedgedHarness(t)
		h.ci.taskFailures = []ecstypes.Failure{{Arn: aws.String("arn:task/af-ws-acme-alice"), Reason: aws.String("MISSING")}}
		_ = f.sweep(ctx)
		if wedgedStampOf(h) != "" {
			t.Fatal("stamped on a failed read")
		}
	})
}

func TestSweepWedgedSlotClearsWhenItAnswersAgain(t *testing.T) {
	ctx := context.Background()
	h, f, _, doer := wedgedHarness(t)
	_ = f.sweep(ctx)
	if wedgedStampOf(h) == "" {
		t.Fatal("setup: no stamp")
	}
	doer.err, doer.status = nil, 200
	_ = f.sweep(ctx)
	if s := wedgedStampOf(h); s != "" {
		t.Fatalf("stamp survived a healthy probe: %q", s)
	}
	// And a box that went down clears it too.
	doer.err = &url.Error{Op: "Get", Err: syscall.ECONNRESET}
	_ = f.sweep(ctx)
	if wedgedStampOf(h) == "" {
		t.Fatal("setup: no stamp the second time")
	}
	h.ec2.instances["i-dark"].State.Name = "stopped"
	_ = f.sweep(ctx)
	if s := wedgedStampOf(h); s != "" {
		t.Fatalf("stamp survived the box stopping: %q", s)
	}
}

func TestSweepWedgedSlotRearmsOnANewLaunchEpoch(t *testing.T) {
	ctx := context.Background()
	h, f, events, _ := wedgedHarness(t)
	_ = f.sweep(ctx)
	backdateWedged(t, h, 11*time.Minute)
	_ = f.sweep(ctx)
	if len(*events) != 1 {
		t.Fatalf("setup: events = %d", len(*events))
	}
	// A stop/start moves LaunchTime: the old stamp (already detected) is of no use.
	h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Hour))
	_ = f.sweep(ctx)
	st, ok := parseDarkStamp(wedgedStampOf(h))
	if !ok || st.detected || !st.launch.Equal(h.ec2.instances["i-dark"].LaunchTime.UTC().Truncate(time.Second)) {
		t.Fatalf("stamp not re-armed for the new launch: %q", wedgedStampOf(h))
	}
	if len(*events) != 1 {
		t.Fatalf("re-armed stamp reported at once: %d events", len(*events))
	}
	backdateWedged(t, h, 11*time.Minute)
	_ = f.sweep(ctx)
	if len(*events) != 2 {
		t.Fatalf("second episode not reported: %d events", len(*events))
	}
}

func TestWedgedConfig(t *testing.T) {
	for in, want := range map[string]string{"": wedgedActionDetect, "detect": wedgedActionDetect, "OFF": wedgedActionOff, "release": wedgedActionDetect, "bogus": wedgedActionDetect} {
		if got := parseWedgedAction(in); got != want {
			t.Errorf("parseWedgedAction(%q) = %q, want %q", in, got, want)
		}
	}
	p := &ec2PoolConfig{wedgedAfter: 30 * time.Second, darkBootGrace: 300 * time.Second, slotLostAfter: 300 * time.Second, sweepEvery: 300 * time.Second}
	p.clampWedged()
	if p.wedgedAfter != 600*time.Second {
		t.Fatalf("wedgedAfter = %v, want the 600s floor", p.wedgedAfter)
	}
	p.wedgedAfter = 0
	p.clampWedged()
	if p.wedgedAfter != 0 {
		t.Fatalf("0 must stay off, got %v", p.wedgedAfter)
	}
}

func TestPoolStatusShowsWedgedSince(t *testing.T) {
	ctx := context.Background()
	h, f, _, _ := wedgedHarness(t)
	_ = f.sweep(ctx)
	st, err := f.PoolStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range st.Slots {
		if s.InstanceID == "i-dark" {
			found = true
			if s.WedgedSince == "" || s.WedgedRung != darkRungObserving {
				t.Fatalf("wedged_since=%q wedged_rung=%q", s.WedgedSince, s.WedgedRung)
			}
			if s.DarkSince != "" {
				t.Fatalf("dark_since=%q on a wedged slot", s.DarkSince)
			}
		}
	}
	if !found {
		t.Fatal("slot missing from the pool view")
	}
	// A stamp from an earlier launch must not show.
	old, _ := parseDarkStamp(wedgedStampOf(h))
	old.launch = old.launch.Add(-time.Hour)
	h.ec2.setInstanceTag("i-dark", ec2TagWedgedSince, old.String())
	st, _ = f.PoolStatus(ctx)
	for _, s := range st.Slots {
		if s.InstanceID == "i-dark" && s.WedgedSince != "" {
			t.Fatalf("stale stamp shown: %q", s.WedgedSince)
		}
	}
}
