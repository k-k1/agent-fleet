// deps.go — この package が CP から必要とするものを、切断面の**こちら側**で宣言する。
//
// internal/mcpsrv は MCP の 3 つの面（/mcp の JSON-RPC ツールサーバー、
// /api/admin/mcp-servers のテナント配布 CRUD、/internal/mcp-servers の配布face）で、
// CP の `manager` / `memberAuth` / handler 群には依存しない。manager は CP の god
// object で、そのメソッドは resolver.go / workspace_lifecycle.go / reaper.go ほかに
// 散っている——運べないし、運ぶべきでもない。
//
// なので外向きの依存は 1 本のインタフェース CP に集める。**struct の公開フィールド
// （関数フィールドの束）にしていない**のは ADR 0067 決定 5 の但し書きどおり: フィールド
// を 1 本埋め忘れると nil のまま無言で no-op になり、reflect の網羅検査を別に書かないと
// 気付けない。インタフェースなら埋め忘れは**コンパイルエラー**なので、検査そのものが要らない。
//
// 逆向き（main が mcpsrv を呼ぶ側）は control-plane/alias_mcp.go 1 枚で吸収する。
package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// CP is everything mcpsrv needs from the control plane. The implementation is
// control-plane/alias_mcp.go's adapter over *manager.
type CP interface {
	// --- 素材 ------------------------------------------------------------------
	// Store is the CP metadata store. mcpsrv reads PATs, identities, memberships,
	// tenants, sessions, memos, audit, egress and the MCP server rows through it.
	Store() store.Store
	// Custodian wraps/unwraps the tenant KEK for sealed header maps; nil in dev
	// (no master key), which sealHeaders/openHeaders handle explicitly.
	Custodian() KeyCustodian
	// Master32 is the deployment master key digest (nil in dev). Only its
	// emptiness is consulted — it decides whether headers are sealed at all.
	Master32() []byte
	// TokenSignMaster is the master the AF_MCP_TOKEN signing key is derived from.
	TokenSignMaster() []byte

	// --- 解決 / ライフサイクル（manager のメソッド） ------------------------------
	RuntimeFor(ws store.Workspace, secretKey string, extraEnv ...string) runtime.Runtime
	ResolveByMembership(ctx context.Context, identityID, membershipID string) (*Resolved, *APIError)
	WorkspaceStateByMembership(ctx context.Context, membershipID string) (container, state string)
	StopWorkspaceByMembership(ctx context.Context, membershipID string) error
	ResolveWorkspaceSize(ctx context.Context, ws store.Workspace) (memBytes int64, cpuUnits, diskGB int)
	ResolveSlotClass(ctx context.Context, ws store.Workspace) (id, note string)
	EvictMembershipCache(membershipID string)
	CountRunningInTenant(ctx context.Context, tenantID string) (int, error)
	AgentSessions(ctx context.Context, rt runtime.Runtime) ([]Session, error)

	// --- HTTP の認可 / テナント選択（memberAuth と httpapi.go） -------------------
	// TenantAdminFor gates the admin face. It writes the 401/403 itself and
	// returns ok=false, exactly as memberAuth does for every other feature API.
	TenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (store.Identity, store.Tenant, bool)
	// TenantSel is the CP-wide tenant selection convention (header, then query).
	// Taken from the CP rather than copied: a divergence here would silently send
	// an admin's edit to a different tenant than the rest of the console.
	TenantSel(r *http.Request) string

	// --- CP→Agent ---------------------------------------------------------------
	// AgentText performs an authenticated CP→Agent request and returns the body.
	//
	// ⚠️ It deliberately stayed in package main (control-plane/mcp_agent_text.go).
	// The error it returns is *agentHTTPError, whose status/body are UNEXPORTED and
	// are read back by memo.go through errors.As; moving the type here would make
	// that assertion compile and silently return false — the docs/log の #310 型の
	// 「コンパイルは通るのに無言で false」事故そのもの。
	AgentText(ctx context.Context, rt runtime.Runtime, method, path string, body []byte) (string, error)

	// --- 他家系のサービス層（そのまま呼ぶ。ロジックはこちらに持たない） -------------
	ScheduleGuardErr(ctx context.Context, membershipID, repoName, sessionName string) error
	MemoList(ctx context.Context, membershipID string) (any, error)
	MemoCreate(ctx context.Context, mv store.MembershipView, repo, category, kind, body, refPath string) (any, error)
	MemoUpdate(ctx context.Context, membershipID, id string, repo, category, body, refPath *string, position *int) (any, error)
	MemoFlush(ctx context.Context, rt runtime.Runtime, membershipID, sessionName string, ids []string) (map[string]any, error)

	// --- 方針・一次情報（写すと二つ目の出所になるもの） ----------------------------
	// ScopeRank is the PAT scope ladder (pat.go). Taken from the CP because a tier
	// inserted there must move this package's tool visibility with it.
	ScopeRank(scope string) int
	// HashPAT is the PAT storage hash. Taken from the CP for the same reason a
	// password hash is never re-implemented: a drift makes every token invalid.
	HashPAT(token string) string
	// TenantLimits parses tenant.limits JSON. mcpsrv only reports two of the quotas.
	TenantLimits(raw string) (maxWorkspaces, maxSessions int)
	// EgressDefaults is the product's built-in egress allowlist (egress_policy.go),
	// reported verbatim by the list_allowlist tool.
	EgressDefaults() []string
	// HostStats is the super_admin-only host load/memory reading (metrics.go).
	HostStats() (load1 float64, ncpu int, memUsed, memTotal uint64)
}

// KeyCustodian is the CP's envelope-encryption seam as this package consumes it —
// declared here, structurally satisfied by control-plane/custodian.go's KeyCustodian.
type KeyCustodian interface {
	Wrap(ctx context.Context, keyRef string, dek []byte) (string, error)
	Unwrap(ctx context.Context, keyRef, ciphertext string) ([]byte, error)
}

// Resolved is the part of the CP's per-request resolution the member/drive tools
// use: the caller's own workspace runtime and their membership. The CP's own
// `resolved` also carries the workspace row and identity; those are deliberately
// absent here, so a tool that starts needing one shows up as a seam change rather
// than reaching through.
type Resolved struct {
	RT runtime.Runtime
	MV store.MembershipView
}

// Session is the Agent's live session as the admin tools report it — the subset of
// the CP's sessionWire that reaches an MCP answer.
type Session struct {
	Name    string
	Display string
	Kind    string
	Label   string
	Alive   bool
}

// APIError carries an HTTP status + machine code, mirroring the CP's apiError
// (manager.go). It is a copy rather than an import because apiError's fields are
// unexported and the CP is package main; the adapter converts at the seam, and the
// field ORDER matches so the positional literals in this package read the same.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func internalErr(err error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: err.Error()}
}

// writeJSON / writeAPIErr mirror httpapi.go. They are five-line, side-effect-free
// encoders and duplicating them is cheaper — and far less coupling — than putting
// an HTTP writer on the CP seam (the same call internal/runtime made for its own
// small helpers). The response SHAPE is the contract, and it is identical.
func writeJSON(w http.ResponseWriter, status int, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIErr(w http.ResponseWriter, e *APIError) {
	writeJSON(w, e.Status, map[string]any{
		"error": map[string]string{"code": e.Code, "message": e.Message},
	})
}

// The 1024-based size units and the human rendering of a byte count, copied from
// mem.go for the same reason as above (pure, and only used to format one audit
// detail string here).
const (
	kib = 1024
	mib = kib * 1024
	gib = mib * 1024
)

func formatMemHuman(b int64) string {
	switch {
	case b >= gib && b%gib == 0:
		return strconv.FormatInt(b/gib, 10) + "g"
	case b >= mib && b%mib == 0:
		return strconv.FormatInt(b/mib, 10) + "m"
	case b >= kib && b%kib == 0:
		return strconv.FormatInt(b/kib, 10) + "k"
	default:
		return strconv.FormatInt(b, 10)
	}
}
