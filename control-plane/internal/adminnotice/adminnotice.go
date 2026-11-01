// Package adminnotice raises the Console notices a tenant's admins get about a member's
// workspace (#2427). It owns the decision (thresholds, hysteresis, once per crossing); the usage
// sampler and the member stats read only say "a new reading was stored".
package adminnotice

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const gib = 1 << 30

// Store is what the notices read and write; *store.SQL satisfies it.
type Store interface {
	GetWorkspaceDisk(ctx context.Context, workspaceID string) (store.WorkspaceDisk, bool, error)
	GetWorkspaceBootFailed(ctx context.Context, workspaceID string) (string, error)
	ListMembersByTenant(ctx context.Context, tenantID string) ([]store.MemberInfo, error)
	InsertNotification(ctx context.Context, n store.Notification) error
	store.WorkspaceNoticeStore
}

// Admin notices (#2427): a tenant's admins hear, once per crossing, that a member's own home
// volume is short of space or that the member's last observed start failed to install a CLI.
// The member sees the same conditions in their own Console; this is for the person who has to
// act when the member does not.

// The home-disk thresholds are the member-facing ones, number for number. They are stated here
// as well as in console/src/core/store/homeDiskPressure.ts because the CP decides when an admin
// is told; admin_notice_test.go reads that file and fails when either side moves alone.
const (
	homePressureRaiseFreeBytes = 2 * gib
	homePressureRaisePct       = 90
	homePressureClearFreeBytes = 3 * gib
	homePressureClearPct       = 85
)

// nextHomeDiskPressure is the Go twin of the Console's function of the same name: whether the
// home is in pressure after this reading, with hysteresis so a home hovering at the line does
// not notify on every crossing. An unusable reading changes nothing.
func nextHomeDiskPressure(prev bool, used, total uint64) bool {
	if total == 0 {
		return prev
	}
	var free uint64
	if used < total {
		free = total - used
	}
	pct := float64(used) / float64(total) * 100
	if prev {
		return !(free > homePressureClearFreeBytes && pct < homePressureClearPct)
	}
	return free < homePressureRaiseFreeBytes || pct > homePressureRaisePct
}

// bootInstallFailures lists the tools whose install really failed in the stored outcome. A
// skipped upgrade (the installed CLI keeps working) shares the map and is never a failure.
func bootInstallFailures(raw string) []string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	var tools []string
	for tool, reason := range m {
		if reason != "skipped_no_space" {
			tools = append(tools, tool)
		}
	}
	sort.Strings(tools)
	return tools
}

// HomeDisk runs after a home reading was stored for a workspace whose home is the
// member's own volume. It reads the STORED reading back, so a slow sweep whose write was refused
// as older cannot decide anything. Never called for an unknown reading or a shared home.
func HomeDisk(ctx context.Context, st Store, ws store.Workspace) {
	d, ok, err := st.GetWorkspaceDisk(ctx, ws.ID)
	if err != nil || !ok || d.Total == 0 {
		return
	}
	prev, err := st.WorkspaceNoticeRaised(ctx, ws.ID, store.WorkspaceNoticeHomeFull)
	if err != nil {
		return
	}
	next := nextHomeDiskPressure(prev, d.Used, d.Total)
	switch {
	case next && !prev:
		raise(ctx, st, ws, store.WorkspaceNoticeHomeFull, "member-home-full",
			map[string]any{"used": d.Used, "total": d.Total})
	case !next && prev:
		if _, err := st.ReleaseWorkspaceNotice(ctx, ws.ID, store.WorkspaceNoticeHomeFull); err != nil {
			log.Printf("admin notice: release home_full (%s): %v", ws.ContainerName, err)
		}
	}
}

// BootInstall runs after an install outcome was stored. Like the disk notice it decides
// from the stored outcome, which is "the last start observed".
func BootInstall(ctx context.Context, st Store, ws store.Workspace) {
	raw, err := st.GetWorkspaceBootFailed(ctx, ws.ID)
	if err != nil {
		return
	}
	tools := bootInstallFailures(raw)
	prev, err := st.WorkspaceNoticeRaised(ctx, ws.ID, store.WorkspaceNoticeBootInstall)
	if err != nil {
		return
	}
	switch {
	case len(tools) > 0 && !prev:
		raise(ctx, st, ws, store.WorkspaceNoticeBootInstall, "member-boot-install",
			map[string]any{"tools": tools})
	case len(tools) == 0 && prev:
		if _, err := st.ReleaseWorkspaceNotice(ctx, ws.ID, store.WorkspaceNoticeBootInstall); err != nil {
			log.Printf("admin notice: release boot_install (%s): %v", ws.ContainerName, err)
		}
	}
}

// raise claims the mark first and notifies only the claimant. Recipients are the
// active tenant_admin memberships of the workspace's own tenant, taken from that tenant's roster
// alone. When delivery fails the claim is withdrawn so the next sample retries.
func raise(ctx context.Context, st Store, ws store.Workspace, noticeKind, kind string, payload map[string]any) {
	members, err := st.ListMembersByTenant(ctx, ws.TenantID)
	if err != nil {
		return
	}
	var subject *store.MemberInfo
	var admins []string
	for i := range members {
		if members[i].MembershipID == ws.MembershipID {
			subject = &members[i]
		}
		if members[i].MemberRole == "tenant_admin" {
			admins = append(admins, members[i].MembershipID)
		}
	}
	// A removed member (not on the active roster) is offboarding, not a problem to act on.
	if subject == nil || len(admins) == 0 {
		return
	}
	claimed, err := st.ClaimWorkspaceNotice(ctx, ws.ID, noticeKind)
	if err != nil || !claimed {
		return
	}
	display := subject.Email
	if display == "" {
		display = subject.UserKey
	}
	payload["tenantSlug"] = ws.TenantSlug
	payload["userKey"] = subject.UserKey
	body, _ := json.Marshal(payload)
	stamp := time.Now().UTC().UnixNano()
	for _, adminID := range admins {
		// The event id carries the recipient: InsertNotification is idempotent on event_id alone.
		err := st.InsertNotification(ctx, store.Notification{
			EventID:      fmt.Sprintf("%s-%s-%d-%s", kind, ws.ID, stamp, adminID),
			MembershipID: adminID, Kind: kind,
			TargetType: "member", TargetID: subject.UserKey,
			DisplayName: display, Payload: string(body), CreatedAt: store.NowTS(),
		})
		if err != nil {
			log.Printf("admin notice: %s (%s): %v", kind, strings.TrimSpace(ws.ContainerName), err)
			_, _ = st.ReleaseWorkspaceNotice(ctx, ws.ID, noticeKind)
			return
		}
	}
}
