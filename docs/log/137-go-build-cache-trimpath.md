# 137. Go build cache growth: `-trimpath` across worktrees (2026-11-19)

status: **Measured; `GOFLAGS=-trimpath` made the workspace default.** Issue #4560 (stays open to
watch the cache size). The automatic age trim of the issue's option 1 is deliberately not built yet.

## Incident

A 466 GB home reached 100% with `~/.cache/go-build` at 162 GB, inside Go's 5-day trim window.
Cause (inferred, then measured below): many worktrees build the same packages at different absolute
paths; without `-trimpath` the path enters the action ID, so every worktree adds its own copies.

## Measurement

Throwaway `GOCACHE`, `GOMAXPROCS=2 go … -p 2`, the same commit extracted to two different absolute
paths (a, b), a fresh cache per row. Sizes are `du` apparent bytes.

| Run | `-trimpath` | after a | after b | b adds |
|---|---|---|---|---|
| `go build ./...` (workspace/agent) | no | 342 MB | 473 MB | +131 MB (+38%), 51 s |
| same | yes | 341 MB | 342 MB | +0.9 MB, 5 s |
| `go test -run 'TestMainFilesAllowlist\|TestWireMapGolden' .` in both modules | no | 1368 MB | 1733 MB | +365 MB (+27%) |
| same | yes | 1366 MB | 1367 MB | +1.3 MB, 7 s |

`-trimpath` leaves a single checkout's cache the same size; it removes the per-worktree multiplier.
Test results and test binaries are shared too. It does not shrink growth across commits in one tree.

## What `-trimpath` broke

`internal/ingresstest` located `deploy/aws/ecs/cfn/30-ingress.yaml` with `runtime.Caller(0)`, which
under `-trimpath` is a module-relative path, so `TestSyncSuggestBudgetStaysUnderTheIngressIdleTimeout`
failed with "no such file". Review of the PR found two more, in modules the first sweep (which only
grepped `workspace/` and `control-plane/`) did not cover: `scripts/model-id-lint` (`defaultRoot()`, so
`go run .` failed, and its tests bypass it) and `e2e/fleet_test.go` (`repoRoot`). All three now walk up
from the working directory, and no `runtime.Caller` is left in the repository. With
`GOFLAGS=-trimpath`, `go test -count=1` of the `workspace/agent` root and every
`workspace/agent/internal/...` package except `memoryx` passed, and `-race` built and passed for
`internal/gitx`.

## Not verified

A real home-volume fill; the `control-plane` full suite under `-trimpath` (CI
runs without it); the effect on a long-lived home whose cache already holds untrimmed entries (they
age out by Go's own 5-day trim or the "empty go-build" button).

## Low-disk notice (option 2)

Already shipped (`HomeLowNotice`, `ReclaimModal`, #3880). It raises at >90% used or <2 GiB free,
which on a 466 GB home is about 47 GB of headroom, so the threshold was not changed. The list put
working copies before tool caches in survey order, so a 160 GB go-build cache could sit last; the
list is now sorted by size, largest first.

2026-11-20: for the `native` runtime (rootfs mode gets the image ENV via `image-env.json`; the host-agent mode does not, so it needs `WS_ENV=GOFLAGS=-trimpath`), see "Sharing Go build-cache entries between worktrees" in `guide/operate/native/install.md`.
