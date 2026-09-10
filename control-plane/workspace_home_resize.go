// workspace_home_resize.go — pushing a member's stored disk number at the home that
// already exists.
//
// The disk axis used to be write-only past the moment a home was created: an admin could
// type 100 into a member whose volume was made at 50 and nothing anywhere would object,
// or grow. That is the shape of the request this exists for — a member asks for more
// room in `~`, and their tenant administrator has to be able to give it without a ticket
// to whoever holds the AWS console.
//
// It is a separate step from writing the quota row, and deliberately runs AFTER it, on
// both callers (the admin API and the MCP tool). The row is the admin's intent and is
// kept whatever AWS does with it today; the resize is one attempt to make the world
// match, and it reports rather than fails (runtime.HomeResize).
package main

import (
	"context"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// homeResizer is the optional Runtime capability, probed the way machineProfiler and
// sizingProfiler are. Only the EC2 slot pool implements it — it is the only runtime whose
// disk axis is a volume that outlives the container. Everywhere else the number is a
// Fargate task's ephemeral storage or a display-only quota, and there is nothing to grow.
type homeResizer interface {
	ResizeHome(ctx context.Context) (runtime.HomeResize, error)
}

// resizeHomeByMembership grows a member's home to whatever their stored request now
// resolves to. The zero HomeResize (Outcome "") means "nothing to say" and is the answer
// on every runtime without the capability and for a member with no workspace row yet —
// both are ordinary, so neither is an error.
//
// The size is taken through resolveWorkspaceSize rather than from the request body, so
// the volume is grown to the CLAMPED figure. A tenant_admin typing 500 GiB under a
// tenant capped at 100 must not get 500 GiB of EBS just because the cap is enforced at
// container start; here the cap has to hold at the moment the money is spent.
func (m *manager) resizeHomeByMembership(ctx context.Context, membershipID string) (runtime.HomeResize, error) {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return runtime.HomeResize{}, err
	}
	// The three axes are re-resolved onto the record before the runtime is built, exactly
	// as a start does. Without it ws.DiskGB is the stored workspace row's stale copy, the
	// adapter's homeGiB() falls back to the deployment default, and the resize would push
	// every member back to 50 GiB instead of to what was just saved.
	ws.MemBytes, ws.CPUUnits, ws.DiskGB = m.resolveWorkspaceSize(ctx, ws)
	rt := m.runtimeFor(ws, "")
	hr, ok := rt.(homeResizer)
	if !ok {
		return runtime.HomeResize{}, nil
	}
	return hr.ResizeHome(ctx)
}
