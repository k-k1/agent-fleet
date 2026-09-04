// runtime_stale_test.go — contract tests for "would a stop→start run different code?".
// A false positive leaves an unclearable "restart required" in the WS bar and costs the
// badge all of its credibility; a false negative hides an update that never took effect.
// When the answer is unknown, it is not stale.
//
// Lives in the same package as the four adapters that implement the check: dockerRuntime
// and nativeRuntime are built here from unexported fields, so this cannot be written from
// the CP side.
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDockerStaleImageStamp pins the docker comparison: the image content stamped at start
// against the content the tag resolves to now. Two ways of getting it wrong are guarded
// here, both of which lit the badge permanently on a fleet that had changed nothing.
//
//   - Never read a running container's {{.Image}}. With the containerd image store a
//     container's {{.Image}} (the platform config digest) and the image's {{.Id}} (the
//     manifest/index digest) are different values even when the container was started from
//     that exact image, so they never agree (measured on the dev fleet, Driver=overlayfs).
//   - Never stamp a digest. A docker build that hits every layer cache and only re-attaches
//     provenance moves the tag's {{.Id}} from a config digest to an index digest without a
//     byte of content changing. Comparing content (layer chain + config) stays silent there.
func TestDockerStaleImageStamp(t *testing.T) {
	orig := Freshness
	defer func() { Freshness = orig }()
	now := time.Unix(1000, 0)
	Freshness = &TTLCache{m: map[string]TTLEntry{}, now: func() time.Time { return now }}

	const (
		built    = "sha256:b05a9622 |dev|/home/dev|[/usr/local/bin/entrypoint.sh]|[workspace-agent]|[]|map[]"
		rebuilt  = "sha256:aaaa1111 |dev|/home/dev|[/usr/local/bin/entrypoint.sh]|[workspace-agent]|[]|map[]"
		envOnly  = "sha256:b05a9622 |dev|/home/dev|[/usr/local/bin/entrypoint.sh]|[workspace-agent]|[TZ=Asia/Tokyo]|map[]"
		ctrLocal = "sha256:02a946de" // {{.Image}} of a container started from that same image
	)
	dir := t.TempDir()
	fp := built
	d := &dockerRuntime{image: "agent-fleet/workspace:dev", name: "af-ws-x", dataDir: dir}
	d.inspect = func(_ context.Context, typ, _, format string) string {
		if typ == "container" {
			t.Errorf("コンテナの {{.Image}} を参照した — 表現差で恒久誤点灯する二辺比較に戻っている")
			return ctrLocal
		}
		if strings.Contains(format, "{{.Id}}") {
			t.Errorf("イメージの {{.Id}} を控えている — 内容が同じでも動く表現で、再び恒久誤点灯する")
		}
		return fp
	}
	ctx := context.Background()

	// No stamp (started outside the CP, or before this existed) = unknown → false.
	if d.Stale(ctx) {
		t.Fatal("no stamp: stale, want false")
	}

	// Stand-in for Start. From here on nothing may light while the image is unchanged.
	d.recordImageStamp(ctx)
	if d.Stale(ctx) {
		t.Fatal("same image: stale, want false")
	}

	// Same content, only the tag re-pointed (a rebuild that hit every layer cache): this is
	// the exact path that lit the badge permanently, so it must stay silent.
	now = now.Add(2 * time.Minute) // past the TTL that keeps 4s polling from inspecting every time
	if d.Stale(ctx) {
		t.Fatal("cache-hit rebuild (same content, new tag digest): stale, want false")
	}

	// Layers actually rebuilt (a local docker build, or pulling a new release) → stale.
	fp = rebuilt
	now = now.Add(2 * time.Minute)
	if !d.Stale(ctx) {
		t.Fatal("rebuilt layers: not stale, want stale")
	}

	// A restart re-stamps the new image, so the badge clears at once, TTL or not.
	d.recordImageStamp(ctx)
	if d.Stale(ctx) {
		t.Fatal("restarted onto the new image: stale, want false")
	}

	// An ENV-only Dockerfile change produces no new layer, and must still be caught.
	fp = envOnly
	now = now.Add(2 * time.Minute)
	if !d.Stale(ctx) {
		t.Fatal("config-only change (ENV): not stale, want stale")
	}

	// Unreadable tag (registry-only tag, no docker present) = unknown → false.
	fp = ""
	now = now.Add(2 * time.Minute)
	if d.Stale(ctx) {
		t.Fatal("unreadable image: stale, want false")
	}
}

// TestDockerStaleLegacyIDStampIgnored pins that a leftover {{.Id}} stamp counts as unknown,
// not stale. An ID and a content fingerprint can never compare equal, so reading one back
// would reproduce the very permanent-badge bug the content stamp replaced. The next Start
// migrates it away.
func TestDockerStaleLegacyIDStampIgnored(t *testing.T) {
	orig := Freshness
	defer func() { Freshness = orig }()
	Freshness = &TTLCache{m: map[string]TTLEntry{}}

	dir := t.TempDir()
	d := &dockerRuntime{image: "agent-fleet/workspace:dev", name: "af-ws-x", dataDir: dir}
	d.inspect = func(context.Context, string, string, string) string { return "sha256:b05a9622 |dev|" }
	if err := os.WriteFile(d.legacyImageIDStampPath(), []byte("sha256:97f63692"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.Stale(context.Background()) {
		t.Fatal("legacy id stamp: stale, want false")
	}

	// The next Start migrates to the content fingerprint and clears the leftover stamp.
	d.recordImageStamp(context.Background())
	if _, err := os.Stat(d.legacyImageIDStampPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy stamp left behind: err=%v", err)
	}
	if d.Stale(context.Background()) {
		t.Fatal("after migration: stale, want false")
	}
}

// TestNativeStaleBinaryStamp covers plain native (dev, AF_NATIVE_AGENT_BIN), where the
// workspace-agent binary itself is the content being compared.
func TestNativeStaleBinaryStamp(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "workspace-agent")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := &nativeRuntime{agentBin: bin, dataDir: dir}

	// No stamp (a process started before this existed) = unknown → false.
	if n.Stale(context.Background()) {
		t.Fatal("no stamp: stale, want false")
	}

	if err := os.WriteFile(n.binStampPath(), []byte(n.spawnStamp()), 0o644); err != nil {
		t.Fatal(err)
	}
	if n.Stale(context.Background()) {
		t.Fatal("unchanged binary: stale, want false")
	}

	// The binary was replaced by a rebuild (both content and size change).
	if err := os.WriteFile(bin, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !n.Stale(context.Background()) {
		t.Fatal("replaced binary: not stale, want stale")
	}
}

// TestNativeStaleRootfsIdentity covers the packaged native build, which runs in rootfs mode:
// agentBin is bwrap and is identical across releases, so the content is the versioned rootfs
// directory instead. Picking the wrong one fails in both directions:
//
//   - watching bwrap: an af update that swaps the rootfs is never detected.
//   - comparing the CP version against the Agent version: the rootfs version <r> is decoupled
//     from the app version <v> (docs/log/35, build.sh --rootfs-json image-invariant releases),
//     so the badge lights permanently.
func TestNativeStaleRootfsIdentity(t *testing.T) {
	dir := t.TempDir()
	bwrap := filepath.Join(dir, "bwrap") // same content across releases
	if err := os.WriteFile(bwrap, []byte("bwrap"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkRootfs := func(ver string) string {
		p := filepath.Join(dir, "rootfs", ver)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, ".ok"), []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old, newer := mkRootfs("0.3.0"), mkRootfs("0.4.0")

	running := &nativeRuntime{agentBin: bwrap, rootfs: old, dataDir: dir}
	if err := os.WriteFile(running.binStampPath(), []byte(running.spawnStamp()), 0o644); err != nil {
		t.Fatal(err)
	}
	if running.Stale(context.Background()) {
		t.Fatal("same rootfs: stale, want false")
	}

	// af update apply → the CP restarts on a new rootfs version while the running agent
	// still comes from the old one.
	if !(&nativeRuntime{agentBin: bwrap, rootfs: newer, dataDir: dir}).Stale(context.Background()) {
		t.Fatal("moved rootfs: not stale, want stale")
	}

	// Image-invariant release (the app version rises, the rootfs pin stays): a restart runs
	// the same code, so nothing may light.
	if (&nativeRuntime{agentBin: bwrap, rootfs: old, dataDir: dir}).Stale(context.Background()) {
		t.Fatal("immutable-rootfs release: stale, want false")
	}
}

func TestTTLCache(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &TTLCache{m: map[string]TTLEntry{}, now: func() time.Time { return now }}
	calls := 0
	load := func() string {
		calls++
		return "v"
	}

	if got := c.get("k", time.Minute, load); got != "v" || calls != 1 {
		t.Fatalf("first: %q calls=%d", got, calls)
	}
	now = now.Add(30 * time.Second)
	if got := c.get("k", time.Minute, load); got != "v" || calls != 1 {
		t.Fatalf("within TTL re-probed: calls=%d", calls)
	}
	now = now.Add(31 * time.Second)
	if got := c.get("k", time.Minute, load); got != "v" || calls != 2 {
		t.Fatalf("after TTL: calls=%d, want 2", calls)
	}
	// Failures ("") are cached too, so a downed docker is not re-probed on every call.
	c2 := &TTLCache{m: map[string]TTLEntry{}, now: func() time.Time { return now }}
	fails := 0
	empty := func() string { fails++; return "" }
	c2.get("k", time.Minute, empty)
	c2.get("k", time.Minute, empty)
	if fails != 1 {
		t.Fatalf("empty result not cached: fails=%d", fails)
	}
}
