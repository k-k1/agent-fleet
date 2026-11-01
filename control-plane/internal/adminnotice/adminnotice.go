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
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const gib = 1 << 30

// Store is what the notices need; *store.SQL satisfies it.
type Store interface {
	store.WorkspaceNoticeStore
}

// The home-disk thresholds are the member-facing ones, number for number. They are stated here
// as well as in console/src/core/store/homeDiskPressure.ts because the CP decides when an admin
// is told; adminnotice_test.go reads that file and fails when either side moves alone.
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

// HomeDisk runs after a home reading was stored for a workspace whose home is the member's own
// volume. The decision is taken from the reading the store hands back under its lock, so a slow
// sweep whose write was refused as older, or a concurrent evaluation, cannot decide on a stale
// value. An unknown reading changes nothing.
func HomeDisk(ctx context.Context, st Store, ws store.Workspace) {
	err := st.SyncWorkspaceNotice(ctx, ws, store.WorkspaceNoticeHomeFull,
		func(r store.NoticeReading, prev bool) store.NoticeDecision {
			if !r.DiskOK || r.Disk.Total == 0 {
				return store.NoticeDecision{Want: prev}
			}
			next := nextHomeDiskPressure(prev, r.Disk.Used, r.Disk.Total)
			return store.NoticeDecision{Want: next, Notes: func(members []store.MemberInfo) []store.Notification {
				return notes(ws, "member-home-full", members, map[string]any{"used": r.Disk.Used, "total": r.Disk.Total})
			}}
		})
	if err != nil {
		log.Printf("admin notice: home_full (%s): %v", ws.ContainerName, err)
	}
}

// BootInstall runs after an install outcome was stored; like HomeDisk it decides from the stored
// outcome, which is "the last start observed".
func BootInstall(ctx context.Context, st Store, ws store.Workspace) {
	err := st.SyncWorkspaceNotice(ctx, ws, store.WorkspaceNoticeBootInstall,
		func(r store.NoticeReading, prev bool) store.NoticeDecision {
			tools := bootInstallFailures(r.BootFailed)
			return store.NoticeDecision{Want: len(tools) > 0, Notes: func(members []store.MemberInfo) []store.Notification {
				return notes(ws, "member-boot-install", members, map[string]any{"tools": tools})
			}}
		})
	if err != nil {
		log.Printf("admin notice: boot_install (%s): %v", ws.ContainerName, err)
	}
}

// notes builds one notification per active tenant_admin of the workspace's own tenant, from that
// tenant's roster alone. A removed member (not on the active roster) is offboarding, not a
// problem to act on, so it yields none.
func notes(ws store.Workspace, kind string, members []store.MemberInfo, payload map[string]any) []store.Notification {
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
	if subject == nil {
		return nil
	}
	display := subject.Email
	if display == "" {
		display = subject.UserKey
	}
	payload["tenantSlug"] = ws.TenantSlug
	payload["userKey"] = subject.UserKey
	body, _ := json.Marshal(payload)
	stamp := time.Now().UTC().UnixNano()
	var out []store.Notification
	for _, adminID := range admins {
		// The event id carries the recipient: InsertNotification is idempotent on event_id alone.
		out = append(out, store.Notification{
			EventID:      fmt.Sprintf("%s-%s-%d-%s", kind, ws.ID, stamp, adminID),
			MembershipID: adminID, Kind: kind,
			TargetType: "member", TargetID: subject.UserKey,
			DisplayName: display, Payload: string(body), CreatedAt: store.NowTS(),
		})
	}
	return out
}
