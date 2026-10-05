// WorkItemModalHost renders the work item detail and report modals for the whole app (#1659).
//
// They used to live inside WorkItemsSection, so only a rail row could open them. A ticket link in
// the mirror has to reach the same panel — with the rail collapsed, on a phone, or in a layout
// where the section never mounted — so the state moved to a store (modal.ts) and the launch
// hand-off moved here with it. Mounted once, next to StartHost.
import { useEffect, useMemo } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { api, raw } from "../../core/api/client.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useReposStore, useLaunchSeed, useLaunchTarget, type Repo } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettings } from "../../lib/settings.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { useWorkItemStore } from "./store.ts";
import { useWorkItemModal } from "./modal.ts";
import { WorkItemReportModal } from "./WorkItemReportModal.tsx";
import { WorkItemDetailModal } from "./WorkItemDetailModal.tsx";
import { readShelf, resolveSessionRef, useArchivedFor, type ResolvedSessionRef } from "./sessionRefs.ts";
import { branchForItem, promptForItem, repoForItem, sessionsForItem, titleForItem, type WorkItem } from "./read.ts";
import "./workitems.css";

/** Opens a session a ticket was started in. A slug that is not on the live list used to open
 * nothing at all (#1108): the shelf is read fresh at the click — the rail's started badge reaches
 * here with no modal open — and an archived session is offered back; a slug on neither list is
 * said to be gone. If the shelf cannot be read (a stopped workspace), it falls through to the
 * plain open, which is what it always did. */
export function useOpenWorkItemSession(): (name: string) => Promise<void> {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const openLive = (name: string) => {
    const s = useSessionsStore.getState().sessions.find((x) => x.name === name);
    (agentOf(s?.kind || "claude").caps.chat ? openSessionChat : openSessionTerminal)(name);
  };
  return async (name: string) => {
    if (useSessionsStore.getState().sessions.some((s) => s.name === name)) return openLive(name);
    const shelf = readShelf(await api("api/sessions/archived").catch(() => null));
    if (!shelf) return openLive(name);
    const ref = resolveSessionRef(name, [], shelf);
    if (ref.state === "gone") {
      toast(t("wi.session_gone", { name }));
      return;
    }
    const label = ref.title || name;
    // A session whose folder is gone restores all the same, as on the shelf: its conversation
    // can still be read, it just cannot resume — so the confirm says that rather than refusing.
    const ok = await askConfirm({
      title: tr("wi.restore_title"),
      body:
        tr("wi.restore_body", { name: label }) +
        (ref.session?.resumable === false ? "\n" + tr("wi.restore_folder_gone") : ""),
      confirmLabel: tr("arch.restore"),
      danger: false,
    });
    if (!ok) return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/restore`, { method: "POST" }).catch(() => null);
    if (!res?.ok) {
      toast(t("arch.restore_failed"));
      return;
    }
    // Open only once the row is on the list: a chat pane draws nothing for a session the list
    // does not have, and refresh() keeps the old list when its read fails.
    await useSessionsStore.getState().refresh();
    if (!useSessionsStore.getState().sessions.some((s) => s.name === name)) {
      toast(t("wi.restored_not_listed", { name: label }));
      return;
    }
    openLive(name);
  };
}

export function WorkItemModalHost() {
  const detail = useWorkItemModal((s) => s.detail);
  const report = useWorkItemModal((s) => s.report);
  const close = useWorkItemModal((s) => s.close);
  const openReport = useWorkItemModal((s) => s.openReport);
  const tenant = useTenantStore((s) => s.tenant);
  const payload = useWorkItemStore((s) => s.payload);
  const repos = useReposStore((s) => s.repos);
  const settings = useSettings();
  const sessions = useSessionsStore((s) => s.sessions);
  const seed = useLaunchSeed((s) => s.set);
  const openLaunch = useLaunchTarget((s) => s.open);
  const startHub = useSessionsStore((s) => s.openStart);
  const openSession = useOpenWorkItemSession();

  // A panel for the previous tenant's ticket must not survive a tenant switch.
  useEffect(() => close, [tenant, close]);

  const ledger = payload?.sessions || [];
  const folders = useMemo(() => repos.map((r) => r.name), [repos]);

  // The ledger names a slug; what the modals show for it is looked up here (#1108). The shelf is
  // read only while a modal lists a slug that is not on the live list.
  const shownRefs = [detail?.item, report].flatMap((i) => (i ? sessionsForItem(ledger, i.key) : []));
  const missing = [...new Set(shownRefs.map((r) => r.sessionName))].filter((n) => !sessions.some((s) => s.name === n));
  const archived = useArchivedFor(missing);
  const sessionRef = (name: string): ResolvedSessionRef => resolveSessionRef(name, sessions, archived);

  // reviewBranch: the PR's head branch, when the detail modal's live read resolved one and the
  // launch is landing in a fresh worktree (WorkItemDetailModal only sets it in that case).
  const seedFor = (item: WorkItem, reviewBranch = "") => {
    seed(promptForItem(item, undefined, reviewBranch), titleForItem(item), "", "", "", {
      provider: item.provider,
      key: item.key,
      branch: branchForItem(item, settings.workItemBranchTemplate),
      title: item.title,
      type: item.type || "",
      labels: item.labels ?? [],
    });
  };

  // Hand off to the existing launch stack only once the detail modal has settled WHERE
  // (docs/log/80 §80.8). A ticket knows nothing about working copies — a GitHub item names a
  // repository at most, Jira not even that — so the repository and new-worktree vs. existing-copy
  // choice are already decided by the time this runs.
  //
  // reviewBranch, when set, checks that branch out in the new worktree instead of cutting one
  // from the template (docs/log/80 §80.24) — reviewing a pull request means reading the code
  // it already has, not starting a new branch from it. It is dropped for inPlace: the user
  // picked that existing copy by hand, and launching it on a DIFFERENT branch than the one they
  // saw in the picker would be a silent switch under them.
  const pickTarget = (item: WorkItem, target: Repo, inPlace: boolean, reviewBranch: string) => {
    seedFor(item, reviewBranch);
    close();
    openLaunch(target, inPlace ? "" : reviewBranch, inPlace);
  };

  // Defer to the start hub (the clone path) only when there is no working copy at all.
  const toStartHub = (item: WorkItem) => {
    seedFor(item);
    close();
    startHub();
  };

  if (detail) {
    const item = detail.item;
    const queryHint = payload?.queries.find((q) => q.id === item.queryId)?.repoHint || "";
    return (
      <WorkItemDetailModal
        // A second link clicked while the panel is open re-points it; the key resets the launch
        // choices, which belong to the previous ticket.
        key={`${item.provider}:${item.key}`}
        item={item}
        reference={detail.reference}
        repos={repos}
        defaultRepo={repoForItem(item, detail.repoHint || queryHint, folders)}
        started={sessionsForItem(ledger, item.key)}
        onClose={close}
        onPick={(target, inPlace, reviewBranch, resolved) => pickTarget(resolved, target, inPlace, reviewBranch)}
        onStartHub={(resolved) => toStartHub(resolved)}
        sessionRef={sessionRef}
        onOpenSession={(name) => {
          close();
          void openSession(name);
        }}
        // openReport closes the detail modal in the same update: never stack two modals.
        onReport={() => openReport(item)}
      />
    );
  }
  if (report) {
    return (
      <WorkItemReportModal
        item={report}
        sessions={sessionsForItem(ledger, report.key)}
        sessionRef={sessionRef}
        onClose={close}
      />
    );
  }
  return null;
}
