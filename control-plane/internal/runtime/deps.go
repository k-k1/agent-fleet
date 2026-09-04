// deps.go — what this package needs from the CP, expressed on THIS side of the seam.
//
// internal/runtime is the four Runtime adapters (docker / ecs / ecs-ec2 / native)
// plus health and the EC2 golden bake. It deliberately does NOT import the CP's
// store, handlers or manager: every one of those is being refactored in parallel,
// and a dependency on any of them would make this package's merge wait on theirs.
// Everything the adapters need from outside is therefore declared here — a struct
// the caller fills in (Config), a value type the caller converts (Workspace), or a
// hook the caller sets at boot (healthzClient, initAgentResolver).
package runtime

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// Workspace is the CP's workspace record as the adapters read it. It is a field-for-
// field copy of the CP's own Workspace (control-plane/store.go), and main converts
// with a plain struct conversion — so if either side gains, loses or retypes a field
// the conversion stops compiling rather than silently dropping it.
//
// It is copied rather than imported because the store is moving into its own package
// in the same refactor wave; importing it here would chain this package's merge to
// that one. Collapsing the two is a job for the alias-collection pass at the wave
// boundary (ADR 0067 decision 3).
type Workspace struct {
	ID, TenantID, MembershipID      string
	ContainerName, Network, DataDir string
	TenantSlug                      string
	AgentPort, AgentToken, State    string
	CreatedAt, LastActiveAt         string
	MemBytes                        int64
	CPUUnits                        int
	DiskGB                          int
	SlotClass                       string
	PreviewSlug                     string
}

// Config is the deployment-wide template the adapters are built from — the subset of
// the CP manager's fields a factory reads at construction time. Passing a struct
// rather than the manager keeps the handler/store graph out of this package (and out
// of its test binary).
type Config struct {
	// Image is the workspace container image (docker/native template; the ECS
	// adapters override it with AF_ECS_WORKSPACE_IMAGE when set).
	Image string
	// AgentHost is the host the CP reaches a locally published Agent on.
	AgentHost string
	// Memory is the deployment default docker --memory value.
	Memory string
	// SessionCmd is the per-session command template handed to the Agent.
	SessionCmd string
	// ExtraEnv is the shared KEY=VAL env every workspace container gets.
	ExtraEnv []string
	// AuthMode is "dev" | "proxy". The native adapter refuses anything but "dev".
	AuthMode string
	// RootDataDir re-bases a workspace's on-disk root onto the CURRENT data root.
	//
	// It is a function, not the two strings it needs, and it must stay one: the CP
	// discovers its default tenant id LATE (workspace_lifecycle.go's adoption pass
	// assigns it long after the factory is built), and a factory that had copied the
	// value would keep re-basing against the empty one — silently handing a workspace
	// the wrong home directory. Use StaticRootDataDir when the values really are fixed.
	RootDataDir func(Workspace) string
}

// rootedDataDir is the adapters' way in. A nil RootDataDir means "the stored path is
// already current", which is what an adapter built without one should assume rather
// than re-basing onto "".
func (c Config) rootedDataDir(ws Workspace) string {
	if c.RootDataDir == nil {
		return ws.DataDir
	}
	return c.RootDataDir(ws)
}

// StaticRootDataDir builds a Config.RootDataDir for a deployment whose data root and
// default tenant are already known and cannot change.
func StaticRootDataDir(dataRoot, defaultTenantID string) func(Workspace) string {
	return func(ws Workspace) string { return RootedDataDir(dataRoot, defaultTenantID, ws) }
}

// RootedDataDir re-bases a workspace's on-disk root onto the CURRENT dataRoot.
// data_dir is persisted at creation with the then-current dataRoot, so a restore
// or move to a different DATA_DIR (or a changed WS_DATA) leaves the stored value
// stale — mounting it would silently give the workspace an empty home. The stable
// part is the suffix (<key> for the default tenant, <slug>/<key> otherwise, per
// workspaceNames); we keep the trailing segment(s) and swap the root. Idempotent
// when the path is already current. See docs/log/p3-10-packaging.md §20.3(B).
func RootedDataDir(dataRoot, defaultTenantID string, ws Workspace) string {
	if ws.DataDir == "" {
		return ws.DataDir
	}
	segs := strings.Split(filepath.ToSlash(strings.TrimRight(ws.DataDir, "/")), "/")
	n := 2 // <slug>/<key>
	if ws.TenantID == defaultTenantID {
		n = 1 // <key>
	}
	if n > len(segs) {
		n = len(segs)
	}
	return filepath.Join(append([]string{dataRoot}, segs[len(segs)-n:]...)...)
}

// UnattendedStartEnv marks a start nobody is waiting on (the scheduler's wake). The
// entrypoint reads it to skip the opt-in CLI self-update, and the docker adapter reads
// it to shorten the courtesy health grace. The CP declares its const as an alias of
// this one: the string is a contract with the workspace image's entrypoint, so it must
// exist exactly once.
const UnattendedStartEnv = "AF_AGENT_SELF_UPDATE_SKIP=1"

// healthzClient bounds the /healthz probes: the poll loops re-issue every couple of
// seconds, so one hung request must not stall its caller.
//
// The CP replaces it at boot (SetHealthzClient) with the client built on agent_dial's
// Transport — CP→Agent must go through that Transport, because Service Connect aliases
// only exist in the /etc/hosts written at task start and a workspace created after the
// CP task resolves only through the Cloud Map fallback the Transport carries. The
// default here exists so this package's own tests (httptest servers) need no wiring;
// it must never be what a deployment uses.
var healthzClient httpDoer = &http.Client{Timeout: 5 * time.Second}

// httpDoer is the one method the probes use, so the CP can inject its own client
// without this package depending on how it is built.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// SetHealthzClient installs the CP's shared /healthz client. Called once at boot.
func SetHealthzClient(c httpDoer) {
	if c != nil {
		healthzClient = c
	}
}

// initAgentResolver is the CP's Cloud Map fallback wiring, armed by the ECS factory
// once it has an AWS config. It is a hook rather than a direct call because the
// resolver lives on the CP's Agent transport, which this package does not own. The
// default is a no-op so the adapters can be built in a test binary.
var initAgentResolver = func(context.Context, aws.Config, string) {}

// SetAgentResolverInit installs the CP's Cloud Map resolver wiring. Called once at boot.
func SetAgentResolverInit(f func(context.Context, aws.Config, string)) {
	if f != nil {
		initAgentResolver = f
	}
}

// --- small helpers -----------------------------------------------------------------
//
// The CP has its own copies of these four in main.go / mem.go / workspace_docs.go.
// They are three-line, side-effect-free utilities, and duplicating them is cheaper —
// and far less coupling — than exporting a seam for each. Collapsing them into one
// shared package is a job for the alias-collection pass.

// The 1024-based size units. Untyped so they flex to int64 at each use site.
const (
	kib = 1024
	mib = kib * 1024
	gib = mib * 1024
)

// envOr reads an env var, falling back to def when unset or empty.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// splitCSV parses "A=1,B=2" into ["A=1","B=2"], dropping blanks.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isDirPath reports whether p exists and is a directory.
func isDirPath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// --- pool budget ------------------------------------------------------------------
//
// PoolBudget is computed by the CP (it needs the tenant table, which this package has
// no access to) but is embedded in the EC2 pool status DTO the ecs-ec2 adapter
// produces, so the type has to be declared on this side. The CP aliases it in
// limits.go; its OK() came along because an alias cannot carry methods.

// PoolBudget is the comparison, as the API and the pool screen both need it. The pieces
// are separate on purpose: an operator who is told only "over" cannot tell whether to
// raise the cap, lower a tenant, or stop worrying.
type PoolBudget struct {
	// MaxSlots is the pool's hard cap — how many boxes may EXIST.
	MaxSlots int `json:"max_slots"`
	// Reserved is what a golden bake needs free at once (seed + probe). Subtracted
	// because a deployment that allocates every slot can never re-bake its golden, and
	// the symptom of that is "new members start slowly", weeks later.
	Reserved int `json:"reserved_slots"`
	// Capacity is MaxSlots - Reserved: the concurrency the tenants may share out.
	Capacity int `json:"capacity"`
	// Allocated is Σ(max_workspaces) over ACTIVE tenants — how many workspaces could be
	// running at once if every tenant used its full quota.
	Allocated int `json:"allocated"`
	// Unbounded names the active tenants whose max_workspaces is 0. 0 means UNLIMITED
	// here (like every other int quota), so ONE of these makes Allocated meaningless as
	// a bound — it is a different problem from "over", and needs saying differently.
	Unbounded []string `json:"unbounded_tenants,omitempty"`
	// Over reports Allocated > Capacity, and is false whenever Unbounded is non-empty:
	// there is no sum to compare.
	Over bool `json:"over"`
}

// OK reports whether this deployment's tenant quotas fit its pool. Both failure modes
// are WARNINGS, never rejections — see setTenantLimits for why.
func (b PoolBudget) OK() bool { return !b.Over && len(b.Unbounded) == 0 }
