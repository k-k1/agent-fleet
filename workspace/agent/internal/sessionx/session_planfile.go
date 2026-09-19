package sessionx

// Where the plan a session is waiting on lives, as a PATH another session can read
// (docs/log/30, the plan review launch). The Console has only the plan TEXT — the pending
// card renders the hook payload — so a launch that wants to hand a reviewing session the
// plan has nothing to put in its first prompt. Embedding the text is not an option either:
// real plans measure 12-36 KB and a TUI launch types its first prompt into the pane.
//
// Two sources, in this order:
//
//	claude   — the path recorded from the Write/Edit hook (status.WritePlanFile). Current
//	           claude writes the plan through its own Write tool, so the path passes the
//	           hook while the plan is still pending, and a revision rewrites the SAME path.
//	snapshot — everything else (an older claude that wrote the file itself, a record whose
//	           file has since gone). The pending text is copied out once and that copy is
//	           handed over instead.
//
// The answer carries which one it was, because "suddenly always snapshot" is how this
// notices that claude stopped writing plans through Write.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// planSnapshots holds the copies made for sessions with no usable record. Keyed by
// sid + plan digest so a revision gets its own file and re-asking for the same plan
// settles on one path (the reviewer's prompt, already sent, keeps pointing at it).
var planSnapshots = fstore.Strings(paths.AgentStateDir, "plan-review", ".md")

// planFileOf accepts a Write/Edit target only when it is one of claude's plan files, and
// returns it cleaned. Everything else — including every write inside the repository —
// returns "", which is what keeps the hook from recording ordinary edits.
//
// The comparison allows the config dir's symlinked spelling as well: dotfiles in a
// Workspace are often links onto always-available storage, and claude writes whichever
// spelling its own CLAUDE_CONFIG_DIR resolution produced.
func planFileOf(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasSuffix(p, ".md") || !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	dir := filepath.Join(paths.ClaudeConfigDir(), "plans")
	if underDir(p, dir) {
		return p
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil && underDir(p, real) {
		return p
	}
	return ""
}

// underDir reports whether p sits directly under dir. Deliberately not a HasPrefix on the
// raw strings: "/var/lib/af/claude/plansible/x.md" has the prefix and is not in the
// directory.
func underDir(p, dir string) bool {
	return filepath.Dir(p) == filepath.Clean(dir)
}

// HandleSessionPlanFile (GET /sessions/{name}/plan-file) answers {path, source} for the
// plan this session is waiting on. It refuses when no plan is pending: the path alone
// would not say WHICH revision, and the only caller is the plan card's review launch.
func HandleSessionPlanFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	sid := session.UUID(meta.Dir, meta.Name)
	plan, ok := status.ReadPendingPlan(sid)
	if !ok || strings.TrimSpace(plan) == "" {
		httpx.WriteErr(w, http.StatusConflict, "no_plan", "no pending plan approval (already handled?)")
		return
	}
	if p, ok := status.ReadPlanFile(sid); ok {
		// Re-validate the stored path rather than trusting the record: the config dir can
		// move between agent versions, and a record pointing outside it must never be
		// handed to another session as "the plan".
		if p = planFileOf(p); p != "" {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": p, "source": "claude"})
				return
			}
		}
	}
	path, err := writePlanSnapshot(sid, plan)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "snapshot_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"path": path, "source": "snapshot"})
}

func writePlanSnapshot(sid, plan string) (string, error) {
	sum := sha256.Sum256([]byte(plan))
	key := sid + "-" + hex.EncodeToString(sum[:4])
	if err := planSnapshots.Write(key, plan); err != nil {
		return "", err
	}
	return planSnapshots.Path(key), nil
}
