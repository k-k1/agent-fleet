package main

// Live check of the on-demand engine adapter against a real ECS service (ADR 0070 P1).
//
// The fakes prove the rules; they cannot prove that the fields the controller judges by are
// actually populated by AWS. `deployments[].createdAt` is how the start deadline is measured
// and `events[]` is the only place ECS writes down why a start failed, so both are read here
// from the real API. The mutating half (a start/stop round trip) is behind a second switch
// because it costs a cold start.
//
//	AF_TTS_LIVE=1 AF_TTS_LIVE_CLUSTER=… AF_TTS_LIVE_SERVICE=… \
//	  AWS_PROFILE=… AWS_REGION=… go test -run TestTTSEngineLive ./...
//
// Add AF_TTS_LIVE_MUTATE=1 to also move the desired count (it is put back to 0 afterwards).

import (
	"context"
	"os"
	"testing"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

func liveTTSEngine(t *testing.T) *engineECS {
	t.Helper()
	if os.Getenv("AF_TTS_LIVE") != "1" {
		t.Skip("set AF_TTS_LIVE=1 AF_TTS_LIVE_CLUSTER=… AF_TTS_LIVE_SERVICE=… to run the live engine check")
	}
	cluster, service := os.Getenv("AF_TTS_LIVE_CLUSTER"), os.Getenv("AF_TTS_LIVE_SERVICE")
	if cluster == "" || service == "" {
		t.Fatal("AF_TTS_LIVE_CLUSTER and AF_TTS_LIVE_SERVICE are required")
	}
	ac, err := awscfg.LoadDefaultConfig(t.Context())
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	return &engineECS{api: ecs.NewFromConfig(ac), cluster: cluster, service: service}
}

// TestTTSEngineLiveView reads a real service: the state mapping, the primary deployment's
// creation time and the service events all have to survive contact with the real payload.
func TestTTSEngineLiveView(t *testing.T) {
	eng := liveTTSEngine(t)
	v, err := eng.view(t.Context())
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	t.Logf("state=%q desired=%d running=%d lastStart=%s events=%q",
		v.state, v.desired, v.running, v.lastStart.Format(time.RFC3339), v.events)
	switch v.state {
	case "running", "starting", "stopped":
	default:
		t.Fatalf("state = %q, want one of running/starting/stopped (none means the service is gone)", v.state)
	}
	if v.lastStart.IsZero() {
		t.Error("lastStart is zero: DescribeServices returned no PRIMARY deployment, and the start deadline of decision 9 has nothing to measure against")
	}
	if len(v.events) == 0 {
		t.Error("no service events: the only place ECS records why a start failed came back empty")
	}

	// The TTL cache is what keeps /api/tts/status off DescribeServices once every client
	// polls; against the real API it must serve the second read without a second call.
	before := time.Now()
	if _, err := eng.view(t.Context()); err != nil {
		t.Fatalf("cached view: %v", err)
	}
	if d := time.Since(before); d > 50*time.Millisecond {
		t.Errorf("the second view took %s — that is a real API call, not the cache", d)
	}
}

// TestTTSEngineLiveRoundTrip moves the desired count for real and puts it back. It proves
// UpdateService and the cache invalidation behind it; it deliberately does not wait for the
// task to run (that is a 70-77 s cold start nobody needs to pay for here).
func TestTTSEngineLiveRoundTrip(t *testing.T) {
	eng := liveTTSEngine(t)
	if os.Getenv("AF_TTS_LIVE_MUTATE") != "1" {
		t.Skip("set AF_TTS_LIVE_MUTATE=1 to start and stop the real engine (costs part of a cold start)")
	}
	before, err := eng.view(t.Context())
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if before.desired != 0 {
		t.Skipf("the engine is at desired %d; this test only runs from a stopped service", before.desired)
	}
	// Always hand it back stopped, whatever happens below.
	defer func() {
		if err := eng.setEnabled(context.WithoutCancel(t.Context()), false); err != nil {
			t.Errorf("returning the service to desired 0 failed — CHECK IT BY HAND: %v", err)
		}
	}()

	if err := eng.setEnabled(t.Context(), true); err != nil {
		t.Fatalf("setEnabled(true): %v", err)
	}
	v, err := eng.view(t.Context())
	if err != nil {
		t.Fatalf("view after start: %v", err)
	}
	if v.desired != 1 || v.state != "starting" {
		t.Errorf("after start: state=%q desired=%d, want starting/1 (a stale cache would still say stopped/0)", v.state, v.desired)
	}
	t.Logf("started: state=%q desired=%d lastStart=%s", v.state, v.desired, v.lastStart.Format(time.RFC3339))
}
