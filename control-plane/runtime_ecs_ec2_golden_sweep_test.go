// runtime_ecs_ec2_golden_sweep_test.go — sweepOrphans, the AWS half of "the database
// forgot about a bake workspace but AWS did not" (docs/log/64 §64.29.5).
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

const sweepSeedName = "af-ws-af-golden-af-golden-seed"

// addService puts a service into the fake at a given size, which is the one thing this
// sweep decides on.
func (f *fakeECS) addService(name, status string, desired, running int32) {
	if f.services == nil {
		f.services = map[string]ecstypes.Service{}
	}
	f.services[name] = ecstypes.Service{
		ServiceName: aws.String(name), Status: aws.String(status),
		DesiredCount: desired, RunningCount: running,
	}
}

// The leak this exists for: a promoted bake left the seed's service at desiredCount 0
// and its 50 GiB home detached, with no row anywhere pointing at either.
func TestGoldenSweepOrphansRemovesWhatNoRowOwns(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ecs.addService(sweepSeedName, "ACTIVE", 0, 0)
	h.ec2.addHomeVolume("vol-seed", "M-seed", sweepSeedName, "ap-northeast-1a")

	removed, err := h.factory().sweepOrphans(ctx, sweepSeedName)
	if err != nil {
		t.Fatalf("sweepOrphans: %v", err)
	}
	if len(removed) != 2 || !strings.Contains(removed[0], sweepSeedName) || !strings.Contains(removed[1], "vol-seed") {
		t.Fatalf("removed = %v, want the service and the home", removed)
	}
	if len(h.ecs.deleteCalls) != 1 || aws.ToString(h.ecs.deleteCalls[0].Service) != sweepSeedName {
		t.Fatalf("delete calls = %v, want one for %s", h.ecs.deleteCalls, sweepSeedName)
	}
	if _, ok := h.ec2.volumes["vol-seed"]; ok {
		t.Fatal("the orphaned home is still there")
	}
}

// Nothing left over is the ordinary answer — tidy asks on every tick once a golden is
// current — so it must be cheap, silent and above all not an error.
func TestGoldenSweepOrphansIsANoOpWhenThereIsNothing(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)

	removed, err := h.factory().sweepOrphans(ctx, sweepSeedName)
	if err != nil || len(removed) != 0 {
		t.Fatalf("sweepOrphans = %v, %v; want nothing removed and no error", removed, err)
	}
	if len(h.ecs.deleteCalls) != 0 {
		t.Fatalf("deleted %v although there was nothing to delete", h.ecs.deleteCalls)
	}
}

// The caller's guard is "the database has no row"; this one is "AWS says it is in use".
// Both have to hold, because a row can go missing WHILE the workspace is running — that
// is exactly the state the leak was found in.
func TestGoldenSweepOrphansLeavesALiveServiceAlone(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ecs.addService(sweepSeedName, "ACTIVE", 1, 1)
	h.ec2.addHomeVolume("vol-seed", "M-seed", sweepSeedName, "ap-northeast-1a")

	removed, err := h.factory().sweepOrphans(ctx, sweepSeedName)
	if err == nil {
		t.Fatal("swept a service that is running a task")
	}
	if len(removed) != 0 || len(h.ecs.deleteCalls) != 0 {
		t.Fatalf("removed %v / deleted %v from a live workspace", removed, h.ecs.deleteCalls)
	}
	if _, ok := h.ec2.volumes["vol-seed"]; !ok {
		t.Fatal("deleted the home of a live workspace")
	}
}

// An attached home means a slot is holding it: releasing that is releaseSlot's job and
// needs the runtime, not a volume delete.
func TestGoldenSweepOrphansLeavesAnAttachedHomeAlone(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-seed", "M-seed", sweepSeedName, "ap-northeast-1a")
	h.ec2.attach("vol-seed", "i-1", time.Now())

	if _, err := h.factory().sweepOrphans(ctx, sweepSeedName); err == nil {
		t.Fatal("swept a home that is still attached")
	}
	if _, ok := h.ec2.volumes["vol-seed"]; !ok {
		t.Fatal("deleted an attached home")
	}
}

// EBS is happy to delete the source of a snapshot it is still copying, and the result is
// a candidate nobody can boot. The refusal is ours, not the API's.
func TestGoldenSweepOrphansWaitsForACaptureInFlight(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-seed", "M-seed", sweepSeedName, "ap-northeast-1a")
	h.ec2.snapshots["snap-cand"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-cand"), VolumeId: aws.String("vol-seed"),
		State: ec2types.SnapshotStatePending,
	}

	removed, err := h.factory().sweepOrphans(ctx, sweepSeedName)
	if err != nil {
		t.Fatalf("sweepOrphans: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v while a snapshot was still copying", removed)
	}
	if _, ok := h.ec2.volumes["vol-seed"]; !ok {
		t.Fatal("deleted the volume a pending snapshot is reading")
	}
}

// A service being deleted answers neither UpdateService nor CreateService. Saying which
// it is beats "Create service is not idempotent", which reads like a caller bug and sent
// a real deployment looking in the wrong place.
func TestECSEC2UpsertServiceWaitsForADrainingService(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ecs.addService(h.rt.base.name, "DRAINING", 0, 1)

	err := h.rt.upsertService(ctx, "arn:task/af:1", ec2Placement{
		volumeID: "vol-1", instanceID: "i-1", az: "ap-northeast-1a",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "still being deleted") {
		t.Fatalf("upsertService = %v, want a 'still being deleted' error", err)
	}
	if len(h.ecs.createCalls) != 0 {
		t.Fatalf("called CreateService on a draining service: %v", h.ecs.createCalls)
	}
}
