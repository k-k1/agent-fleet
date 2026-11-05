package main

import (
	"context"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressagent"
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
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
//
// `--mode tcp` runs the TCP passthrough relay instead (egressagent.TCPRelay), a separate
// process from the 443 relay so a fault in one leaves the other's connections alone.
func runEgressRelay() {
	if len(os.Args) > 2 {
		if len(os.Args) != 4 || os.Args[2] != "--mode" || os.Args[3] != "tcp" {
			log.Fatalf("usage: egress-relay [--mode tcp]")
		}
		runEgressTCPRelay()
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := egressagent.Relay{Listen: egressagent.RelayListen, Upstream: egressagent.RelayUpstream}
	if err := r.Run(ctx); err != nil {
		log.Fatalf("egress-relay: %v", err)
	}
}

func runEgressTCPRelay() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := egressagent.FromEnv()
	var fence []netip.Prefix
	for _, f := range strings.Fields(strings.ReplaceAll(envx.Or("AF_EGRESS_TCP_FENCE", ""), ",", " ")) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			log.Fatalf("egress-relay: AF_EGRESS_TCP_FENCE %q: %v", f, err)
		}
		fence = append(fence, p)
	}
	l := &egressagent.TCPLog{Path: cfg.TCPLogPath}
	defer l.Close()
	r := &egressagent.TCPRelay{Listen: egressagent.TCPRelayListen, Table: cfg.TCPFile, Log: l, Fence: fence}
	if err := r.Run(ctx); err != nil {
		log.Fatalf("egress-relay: %v", err)
	}
}
