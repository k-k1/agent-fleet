// workspace_stale.go — would a stop→start right now run different code?
//
// The Console only ever sees its own bundle version, so it has no way to notice that the
// backend (workspace image / rootfs / workspace-agent binary) moved. After a deploy a
// Console reload refreshes the frontend, but a container that keeps running keeps the old
// code, and only a stop→start picks up the new one. That gap is decided entirely on the CP
// side and carried as `stale` on GET /api/workspace (and its /api/events push).
//
// One rule:
//
//	compare the content this workspace actually started from (image ID / rootfs /
//	binary) against the content a Start would use now.
//
// Three things this must not do.
//
//   - Never compare the CP version against the version the Agent reports. A native release
//     deliberately decouples the rootfs version <r> from the app version <v> (docs/log/35
//     §35.3, the image-invariant releases of build.sh --rootfs-json), so CP 1.1.0 × Agent
//     1.0.0 is a healthy state. Comparing versions lights "restart required" permanently
//     there, and restarting does not clear it — which costs the badge all its credibility.
//     A content comparison correctly answers "unchanged".
//   - Never compare the two sides through different queries. Reading docker's "running
//     container {{.Image}}" against "the tag's image {{.Id}}" gives, on the containerd
//     image store, two digest representations of one image and lights the badge
//     permanently (measured; details in runtime_docker.go).
//   - Never treat a digest as the content. A digest is a representation and it moves while
//     the content does not: an all-layers-cached docker build that only re-attaches
//     provenance gives the tag a different {{.Id}} (measured, runtime_docker.go). Stamp
//     the content itself — the layer chain plus config.
//
// The implementation is the optional Runtime interface staleRuntime. When in doubt, answer
// false.
//
//	docker : image content stamped at launch ≠ the tag's image content now (runtime_docker.go)
//	native : spawn target stamped at launch ≠ the spawn target now (runtime_native.go)
//	ecs    : fingerprint baked into the task definition Start registered ≠ the tag's now
//	ecs-ec2: the same, delegated to the same implementation. Both in runtime_ecs_stale.go
package main

import (
	"context"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// staleRuntime is the optional half of Runtime: true when a stop→start would run different
// code. Contract: return false when the question cannot be answered (the image cannot be
// resolved, nothing was stamped at launch).
type staleRuntime interface {
	Stale(ctx context.Context) bool
}

// workspaceStale answers the question for a running workspace. Callers must only ask when
// state=="running": a stopped workspace picks up the new code on its next Start anyway, so
// the answer would be meaningless.
func workspaceStale(ctx context.Context, rt runtime.Runtime) bool {
	sr, ok := rt.(staleRuntime)
	return ok && sr.Stale(ctx)
}

// --- small TTL cache ---------------------------------------------------------
//
// freshness / ttlCache / ttlEntry used to live here. They moved to the adapters'
// package (internal/runtime/freshness.go) with the probes that are their only
// writers and readers — a Go alias cannot carry ttlCache's methods, and nothing on
// this side of the seam touches the cache, so no alias is left behind either. A var
// that could still be reassigned here while the adapters read another one would be a
// trap, not compatibility.
