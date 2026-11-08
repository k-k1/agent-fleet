// image_refresh.go — opt-in automatic restart of a quiet workspace onto a new image (#3656).
//
// A running workspace keeps its image until the next Start, and the Console shows "Restart
// needed" meanwhile. A member who switched autoImageRefresh on (ws_settings.go) lets the
// reaper do that restart once the workspace has been quiet for image_refresh_quiet.
//
// Rules this file keeps:
//
//   - Restart only. It stops through the reaper's own stop path (stopWorkspaceIf: the same
//     three fences, the same re-read of every activity signal, cross-replica presence
//     included) and starts through the same ensureWorkspaceStarted* the scheduler's wake
//     uses. There is no second restart path, and nothing here can reach Recreate or Clean
//     home, which delete ~/repos.
//   - At most one automatic attempt per image per workspace. The claim is one conditional
//     upsert (ClaimImageRefresh) taken after the stop and never released, so a start that
//     fails is not retried by the next sweep, and neither is a second replica.
//   - A failed start leaves the workspace stopped. The member's next Start takes the new
//     image anyway, so nothing is left to loop on.
//   - "Unknown" never restarts: a runtime without StaleTarget, or one that cannot resolve
//     the image, answers "".
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// defaultImageRefreshQuiet is the quiet period when neither the tenant nor
// AF_IMAGE_REFRESH_QUIET says otherwise.
const defaultImageRefreshQuiet = 15 * time.Minute

// staleTargetRuntime is the optional half of Runtime that names the image a Start would
// launch from now. See the StaleTarget methods in internal/runtime.
type staleTargetRuntime interface {
	StaleTarget(ctx context.Context) string
}

// imageRefreshTarget is the short, stable identity of the image a restart would pick up,
// "" when unknown. Hashed so the long layer chain never lands in a table or a notice.
func imageRefreshTarget(ctx context.Context, rt runtime.Runtime) string {
	st, ok := rt.(staleTargetRuntime)
	if !ok {
		return ""
	}
	raw := st.StaleTarget(ctx)
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:16]
}

// imageRefreshWanted is the cheap half of the decision, shared by the sweep and the re-check
// under the fences: the member opted in, the runtime says a restart would change the code,
// and this image has not been attempted. It returns the target to claim.
func (rp *reaper) imageRefreshWanted(ctx context.Context, rt runtime.Runtime, ws store.Workspace) (string, bool) {
	raw, err := rp.mgr.store.GetWorkspaceSettings(ctx, ws.ID)
	if err != nil || !parseWSSettings(raw).AutoImageRefresh {
		return "", false
	}
	if !workspaceStale(ctx, rt) {
		return "", false
	}
	target := imageRefreshTarget(ctx, rt)
	return target, target != ""
}

// refreshImage restarts one quiet, opted-in, stale workspace. The caller has already seen it
// unwatched, not busy, and quiet for cl.refresh on this replica; everything is decided again
// under the fences.
func (rp *reaper) refreshImage(ctx context.Context, rt runtime.Runtime, ws store.Workspace, cl tierClocks) {
	target, ok := rp.imageRefreshWanted(ctx, rt, ws)
	if !ok {
		return
	}
	// Cheap peek so a target already attempted costs no fence at all on every sweep.
	if rp.imageAlreadyAttempted(ctx, ws.ID, target) {
		return
	}
	stopped := rp.stopWorkspaceIf(ctx, rt, ws, cl.refresh, cl.bgMax, func(c context.Context) bool {
		// The switch may have been turned off, or the image rebuilt again, while this sweep
		// waited for the fences.
		t, ok := rp.imageRefreshWanted(c, rt, ws)
		return ok && t == target
	})
	if !stopped {
		return
	}
	claimed, err := rp.mgr.store.ClaimImageRefresh(ctx, ws.ID, target, store.NowTS())
	if err != nil {
		log.Printf("image-refresh: claim %s: %v (left stopped)", ws.ContainerName, err)
		return
	}
	if !claimed {
		// Another replica holds this image's one attempt. It is the one that starts it.
		return
	}
	rp.startRefreshed(ctx, ws, target)
}

func (rp *reaper) imageAlreadyAttempted(ctx context.Context, wsID, target string) bool {
	got, ok, err := rp.mgr.store.GetImageRefreshTarget(ctx, wsID)
	return err != nil || (ok && got == target)
}

// startRefreshed is the Start half, through the scheduler wake's unattended start. It
// records the audit row and the member's notice whichever way it ends.
func (rp *reaper) startRefreshed(ctx context.Context, ws store.Workspace, target string) {
	ctx = context.WithoutCancel(ctx)
	start := rp.refreshStart
	if start == nil {
		start = rp.startUnattended
	}
	aerr := start(ctx, ws)
	if aerr != nil {
		log.Printf("image-refresh: start %s onto %s failed: %s (left stopped, not retried)", ws.ContainerName, target, aerr.message)
	} else {
		log.Printf("image-refresh: restarted %s (tenant %s) onto image %s", ws.ContainerName, ws.TenantID, target)
	}
	rp.recordImageRefresh(ctx, ws, target, aerr)
}

func (rp *reaper) startUnattended(ctx context.Context, ws store.Workspace) *apiError {
	identityID, ok, err := rp.mgr.store.IdentityIDForMembership(ctx, ws.MembershipID)
	if err != nil || !ok {
		return &apiError{message: fmt.Sprintf("identity lookup: found=%v err=%v", ok, err)}
	}
	res, aerr := rp.mgr.resolveByMembership(ctx, identityID, ws.MembershipID)
	if aerr != nil {
		return aerr
	}
	return newWorkspaceAPI(rp.mgr, true).ensureWorkspaceStartedUnattended(ctx, res)
}

func (rp *reaper) recordImageRefresh(ctx context.Context, ws store.Workspace, target string, aerr *apiError) {
	now := store.NowTS()
	outcome, detail := "ok", "restarted onto image "+target
	if aerr != nil {
		outcome, detail = "failed", "start onto image "+target+" failed, workspace left stopped: "+aerr.message
	}
	a := store.AuditLog{ID: store.NewID(), TenantID: ws.TenantID, ActorKind: "system", ActorID: "image-refresh",
		Action: "workspace.image_refresh", Target: ws.ID, Detail: detail, At: now}
	if err := rp.mgr.store.InsertAudit(ctx, a); err != nil {
		log.Printf("image-refresh: audit %s: %v", ws.ContainerName, err)
	}
	// The event ID names the image, so a repeated record of the same attempt is one notice.
	payload, _ := json.Marshal(map[string]any{"image": target, "at": now, "outcome": outcome})
	n := store.Notification{EventID: "image-refresh:" + ws.ID + ":" + target, MembershipID: ws.MembershipID,
		Kind: "image-refresh", TargetType: "workspace", Payload: string(payload), CreatedAt: now}
	if err := rp.mgr.store.InsertNotification(ctx, n); err != nil {
		log.Printf("image-refresh: notify %s: %v", ws.ContainerName, err)
	}
}
