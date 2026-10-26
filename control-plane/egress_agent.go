package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressagent"
)

// runEgressAgent is `control-plane egress-agent`. SIGTERM cancels it, in-flight request
// included; an offset is committed only after the CP accepted the lines, so a batch cut
// off by the stop is resent on the next start and nothing is lost.
func runEgressAgent() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := egressagent.Run(ctx, egressagent.FromEnv()); err != nil {
		log.Fatalf("egress-agent: %v", err)
	}
}

// runEgressRelay is `control-plane egress-relay`. SIGTERM stops accepting; tunnels already
// open end with the process.
func runEgressRelay() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := egressagent.Relay{Listen: egressagent.RelayListen, Upstream: egressagent.RelayUpstream}
	if err := r.Run(ctx); err != nil {
		log.Fatalf("egress-relay: %v", err)
	}
}
