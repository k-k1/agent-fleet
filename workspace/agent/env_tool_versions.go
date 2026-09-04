package main

// env_tool_versions.go — GET /env/tool-versions (read-only). For every bundled tool it
// returns three versions — effective (what PATH resolves to), baked (the image's own
// binary) and user local (the ~/.local/bin override) — plus the image build-time pin
// (/usr/local/share/agent-fleet/versions.json, which the Dockerfile writes from its ARGs).
// ~/.local/bin comes before /usr/local on PATH, so effective != baked happens routinely
// (gh's home shadow, the same shape as docs/build/08 §8.3); making that visible is the
// point. `claude --version` and the like take ~1s, so results are cached briefly and the
// tools are probed in parallel (concurrency capped by probeSlots, and toolProbe execs the
// same binary only once).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// buildPinsPath is the pin list the Dockerfile writes from its ARGs. It is a var only so
// tests can swap it (the install_kiro pin comparison); never rewritten at runtime.
var buildPinsPath = "/usr/local/share/agent-fleet/versions.json"

// bakedUVToolRoot is the root where the image puts `uv tool install` results (the
// Dockerfile's UV_TOOL_DIR). Like buildPinsPath it is a var only so tests can swap it;
// never rewritten at runtime. Hardcoded, a test checking "this must not exist on the baked
// side" would pick up the *real container's* baked tree, so it would pass on CI (which has
// nothing baked) and fail in a real Workspace — brittle in the wrong direction.
var bakedUVToolRoot = "/usr/local/share/uv/tools"

// toolSpec is the observation point for one tool. Cmd is used both for PATH resolution
// (effective) and for ~/.local/bin/<Cmd> (user local). Baked is the real path the image
// bakes (for gh the libexec binary rather than the wrapper, for go the tarball's extract
// directory).
type toolSpec struct {
	Name  string   // display name
	Cmd   string   // name to resolve with command -v
	Baked string   // real path of the binary baked into the image
	Args  []string // arguments that print the version (default --version)
	Pin   string   // key in versions.json (empty for unpinned tools)
	// PyDist is the PyPI distribution name of a Python MCP server installed with
	// `uv tool install`. These cannot be asked for their version by running them
	// (measured 2026-08-06):
	//   - cloudwatch MCP: `--version` prints no version and starts the server instead
	//     (loading 1179 metric metadata entries). Starting it on every probe is out.
	//   - AWS MCP proxy: `--version` is rejected by argparse with exit 2 and empty stdout.
	//     The version appears only on line 13 of `--help`, which probeVersion cannot reach
	//     because it looks at the first line only.
	// So read it from the dist-info directory name in uv's venv instead (uvToolVersion).
	PyDist string
}

var toolSpecs = []toolSpec{
	{Name: "claude", Cmd: "claude", Baked: "/usr/local/bin/claude", Pin: "claude"},
	{Name: "opencode", Cmd: "opencode", Baked: "/usr/local/bin/opencode", Pin: "opencode"},
	{Name: "codex", Cmd: "codex", Baked: "/usr/local/bin/codex", Pin: "codex"},
	// agy's true pin comes from the immutable GCS object the official installer manifest
	// names (workspace/Dockerfile: AGY_VERSION + AGY_RELEASE_BUILD + sha256 check). On a host
	// that does not expose RDRAND, `--version` itself SIGABRTs, so probeVersion yields the
	// fetch-failure marker "(取得失敗)" — which is itself a sign of such a host.
	{Name: "agy", Cmd: "agy", Baked: "/usr/local/bin/agy", Pin: "agy"},
	{Name: "copilot", Cmd: "copilot", Baked: "/usr/local/bin/copilot", Pin: "copilot"},
	// cursor (kind="cursor", docs/log/40) is a versioned Node.js tarball bundle, not npm. The
	// baked tree is /usr/local/share/cursor-agent/versions/<ver>/ and
	// /usr/local/bin/cursor-agent is a symlink to its wrapper (realpath resolves the version
	// directory). The version is a date (2026.07.20-8cc9c0b) rather than semver, but
	// `cursor-agent --version` returns exactly that string.
	{Name: "cursor", Cmd: "cursor-agent", Baked: "/usr/local/bin/cursor-agent", Pin: "cursor"},
	// kiro (kind="kiro", docs/log/43) installs on demand into ~/.local by default even where
	// it is baked (/usr/local, BAKE=1), because at ~855MB it must not be boot-installed for
	// every user. The effective/baked/user-local trio is displayed like any other CLI (with
	// nothing installed both effective and baked are null, which surfaces as "not installed").
	// `kiro-cli --version` prints "kiro-cli 2.14.1".
	{Name: "kiro", Cmd: "kiro-cli", Baked: "/usr/local/bin/kiro-cli", Pin: "kiro"},
	{Name: "rtk", Cmd: "rtk", Baked: "/usr/local/bin/rtk", Pin: "rtk"},
	{Name: "gh", Cmd: "gh", Baked: "/usr/local/libexec/gh", Pin: "gh"}, // /usr/local/bin/gh is the transparent-auth wrapper
	{Name: "go", Cmd: "go", Baked: "/usr/local/go/bin/go", Args: []string{"version"}, Pin: "go"},
	{Name: "node", Cmd: "node", Baked: "/usr/local/bin/node"},
	{Name: "python", Cmd: "python3", Baked: "/usr/bin/python3"},
	// AWS / ops MCP servers (docs/log/25). Less prominent than the CLIs, but they drift from
	// their pins and get home-shadowed under the same conditions (`install-awscli` writes to
	// ~/.local/bin, and grafana's fallback looks there too); in a lean variant nothing is
	// baked and the versions.json pin is the only clue left. Without these rows there is no
	// way to check from the Console that an MCP server is old or missing.
	{Name: "awscli", Cmd: "aws", Baked: "/usr/local/bin/aws", Pin: "awscli"},
	{Name: "mcp-grafana", Cmd: "mcp-grafana", Baked: "/usr/local/bin/mcp-grafana", Pin: "mcp_grafana"},
	{Name: "cloudwatch-mcp", Cmd: "awslabs.cloudwatch-mcp-server", Baked: "/usr/local/bin/awslabs.cloudwatch-mcp-server",
		Pin: "cloudwatch_mcp", PyDist: "awslabs-cloudwatch-mcp-server"},
	{Name: "aws-mcp", Cmd: "mcp-proxy-for-aws", Baked: "/usr/local/bin/mcp-proxy-for-aws",
		Pin: "aws_mcp_proxy", PyDist: "mcp-proxy-for-aws"},
}

type toolBin struct {
	Path    string `json:"path"`
	Version string `json:"version"` // extracted number (e.g. 2.1.207); same as raw when none matches
	Raw     string `json:"raw"`     // first line of the --version output
}

type toolReport struct {
	Name      string   `json:"name"`
	Pin       string   `json:"pin,omitempty"` // ARG pin from the image build
	Effective *toolBin `json:"effective"`     // what PATH resolves to (null when absent)
	Baked     *toolBin `json:"baked"`         // the image's binary (null when absent)
	UserLocal *toolBin `json:"userLocal"`     // the ~/.local/bin override (null when absent)
	// Overridden: a baked binary exists and the effective one is a different binary under
	// home (user local hides the baked one). In a lean variant nothing is baked, so ~/.local
	// IS the pinned binary and this stays false — otherwise every row carried a meaningless
	// override flag. It is not a plain effective-vs-baked path comparison because for some
	// tools, gh among them, resolving through a wrapper is normal: anything outside home
	// counts as coming from the image.
	Overridden bool `json:"overridden"`
}

var toolVerCache struct {
	sync.Mutex
	at  time.Time
	out []toolReport
}

const toolVerCacheTTL = 3 * time.Minute

// Time budget for a version probe. `--version` is supposed to be fast, but that assumes a
// warm local disk: measured (acrt / 0.14.0), only opencode and copilot showed (timeout) in
// the effective column, while the ~/.local column pointing at the same binary answered
// within the same request — so the binary was not broken, its first start simply did not fit
// in 5s. Three things matter:
//   - large Bun / Node bundles (opencode is a single ~100MB file, copilot many JS files)
//   - deployments where home is network storage (ECS) — the first read is an order slower
//   - the load we create ourselves by exec'ing every tool at once (probeSlots below)
//
// So the per-binary limit is loose (probeTimeout) and the whole collection is bounded
// instead (collectBudget). The original goal — a broken binary must not hang the handler —
// is carried by the latter.
const (
	probeTimeout  = 15 * time.Second
	collectBudget = 45 * time.Second
)

// probeSlots caps how many version probes run at once. With one goroutine per tool all
// exec'ing at the same time, a Workspace with few CPUs makes slow-starting CLIs time out on
// the load we create ourselves (waiting for a slot does not eat the exec deadline — that one
// starts once the process does).
var probeSlots = make(chan struct{}, 4)

// verNumRe extracts the version number from --version output (1.2 / 1.2.3 / 3.11.2, …).
var verNumRe = regexp.MustCompile(`[0-9]+\.[0-9]+(\.[0-9]+)?`)

// probeVersion reads the version of the binary at path into a toolBin. nil when there is no
// binary.
func probeVersion(ctx context.Context, path string, args []string) *toolBin {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil
	}
	if len(args) == 0 {
		args = []string{"--version"}
	}
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		return &toolBin{Path: path, Raw: "(timeout)"}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if ctx.Err() != nil {
		return &toolBin{Path: path, Raw: "(timeout)"}
	}
	if err != nil && len(out) == 0 {
		return &toolBin{Path: path, Raw: "(取得失敗)"}
	}
	raw := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return &toolBin{Path: path, Version: extractVer(raw), Raw: raw}
}

// uvToolRoot is the uv tool root that holds path. `uv tool install` puts the binary at
// <root>/<tool>/bin/<exe> and creates the venv at
// <root>/<tool>/lib/pythonX.Y/site-packages. There are two roots — baked into the image
// (/usr/local/share/uv/tools, the Dockerfile's UV_TOOL_DIR) and user-installed
// (~/.local/share/uv/tools, uv's default) — and which one applies follows from whether the
// exe sits under home. That matches exactly what the three columns (effective / baked /
// ~/.local) mean, so choose by this test instead of walking the path up (which also survives
// uv versions whose shim is a copy rather than a symlink).
func uvToolRoot(exePath, home string) string {
	if home != "" && strings.HasPrefix(exePath, home+string(os.PathSeparator)) {
		return filepath.Join(home, ".local", "share", "uv", "tools")
	}
	return bakedUVToolRoot
}

// uvToolVersion reads the version from the dist-info directory name in the uv tool's venv.
// The point is that it does not exec the binary (reason in the toolSpec.PyDist comment).
// dist-info names go through PEP 427 normalization, so "-" in the distribution name becomes
// "_" (awslabs-cloudwatch-mcp-server → awslabs_cloudwatch_mcp_server-0.1.4.dist-info).
func uvToolVersion(exePath, dist, home string) *toolBin {
	if fi, err := os.Stat(exePath); err != nil || fi.IsDir() {
		return nil
	}
	norm := strings.ReplaceAll(dist, "-", "_")
	pattern := filepath.Join(uvToolRoot(exePath, home), "*", "lib", "python*", "site-packages", norm+"-*.dist-info")
	for _, m := range globSorted(pattern) {
		base := strings.TrimSuffix(filepath.Base(m), ".dist-info")
		if v := strings.TrimPrefix(base, norm+"-"); v != base && v != "" {
			return &toolBin{Path: exePath, Version: extractVer(v), Raw: dist + " " + v}
		}
	}
	// The binary exists but no venv was found (dropped on PATH by a bare uvx run, say). Not
	// knowing the version must not turn into "not installed (—)" — a binary of unknown origin
	// is exactly the thing worth showing.
	return &toolBin{Path: exePath, Raw: "(版不明)"}
}

func globSorted(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Strings(m)
	return m
}

// probeTool reads the version of one binary. Only uv tool Python servers skip the exec.
func probeTool(ctx context.Context, spec toolSpec, path, home string) *toolBin {
	if spec.PyDist != "" {
		return uvToolVersion(path, spec.PyDist, home)
	}
	return probeVersion(ctx, path, spec.Args)
}

// toolProbe collects the versions for one tool. The three columns (effective / baked /
// ~/.local) often point at the same binary — in a lean variant all three are
// ~/.local/bin/<cmd> — yet each column used to exec on its own, i.e. the same binary three
// times. For heavy CLIs that alone caused the timeouts above, and it surfaced as one binary
// disagreeing with itself ("effective is (timeout) but ~/.local says 1.18.25"). Exec once
// per real path and hand the result to every column.
type toolProbe struct {
	spec toolSpec
	home string
	ctx  context.Context
	seen map[string]*toolBin // real path → result (nil = no binary, remembered as well)
}

func newToolProbe(ctx context.Context, spec toolSpec, home string) *toolProbe {
	return &toolProbe{spec: spec, home: home, ctx: ctx, seen: map[string]*toolBin{}}
}

// at returns the version at path. The Path it returns stays the path that was asked for:
// normalizing it to the resolved symlink target would erase the tooltip's information about
// which column points where.
func (tp *toolProbe) at(path string) *toolBin {
	real := path
	if abs, err := filepath.EvalSymlinks(path); err == nil {
		real = abs
	}
	b, ok := tp.seen[real]
	if !ok {
		b = probeTool(tp.ctx, tp.spec, path, tp.home)
		tp.seen[real] = b
	}
	if b == nil {
		return nil
	}
	c := *b
	c.Path = path
	return &c
}

func extractVer(raw string) string {
	if m := verNumRe.FindString(raw); m != "" {
		return m
	}
	return raw
}

func readBuildPins() map[string]string {
	pins := map[string]string{}
	if b, err := os.ReadFile(buildPinsPath); err == nil {
		_ = json.Unmarshal(b, &pins)
	}
	return pins
}

func collectToolVersions() []toolReport {
	ctx, cancel := context.WithTimeout(context.Background(), collectBudget)
	defer cancel()
	pins := readBuildPins()
	home := homeDir()
	out := make([]toolReport, len(toolSpecs))
	var wg sync.WaitGroup
	for i, spec := range toolSpecs {
		wg.Add(1)
		go func(i int, spec toolSpec) {
			defer wg.Done()
			tp := newToolProbe(ctx, spec, home)
			r := toolReport{Name: spec.Name, Pin: pins[spec.Pin]}
			effPath := ""
			if p, err := exec.LookPath(spec.Cmd); err == nil {
				// Judge a symlink (~/.local/bin/claude → somewhere under share) by its target.
				effPath = p
				if abs, err := filepath.EvalSymlinks(p); err == nil {
					effPath = abs
				}
				r.Effective = tp.at(p)
			}
			r.Baked = tp.at(spec.Baked)
			// go: a lean rootfs bakes no /usr/local/go — surface the on-demand
			// toolchain (install-go, docs/log/35 §35.7.2-5) in the image column instead.
			if r.Baked == nil && spec.Name == "go" {
				if vers := installedGoVersions(); len(vers) > 0 {
					r.Baked = tp.at(filepath.Join(goHomeRoot(), vers[len(vers)-1], "bin", "go"))
				}
			}
			// Overridden only when there is a baked binary to hide (see the struct comment).
			// go's on-demand toolchain (which puts a path under home in Baked) is the same
			// binary as the effective one, so the real-path comparison rules it out.
			if effPath != "" && r.Baked != nil {
				bakedPath := r.Baked.Path
				if abs, err := filepath.EvalSymlinks(bakedPath); err == nil {
					bakedPath = abs
				}
				r.Overridden = strings.HasPrefix(effPath, home+string(os.PathSeparator)) && effPath != bakedPath
			}
			r.UserLocal = tp.at(filepath.Join(home, ".local", "bin", spec.Cmd))
			out[i] = r
		}(i, spec)
	}
	wg.Wait()
	return out
}

// handleToolVersions serves GET /env/tool-versions. ?refresh=1 bypasses the cache.
func handleToolVersions(w http.ResponseWriter, r *http.Request) {
	toolVerCache.Lock()
	defer toolVerCache.Unlock()
	if r.URL.Query().Get("refresh") == "1" || toolVerCache.out == nil ||
		time.Since(toolVerCache.at) > toolVerCacheTTL {
		toolVerCache.out = collectToolVersions()
		toolVerCache.at = time.Now()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"tools":     toolVerCache.out,
		"checkedAt": toolVerCache.at.Format(time.RFC3339),
	})
}
