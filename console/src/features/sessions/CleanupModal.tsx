// CleanupModal — the on-demand tidy-up panel (docs/32). Two tabs:
//   Candidates — the /sessions/cleanup survey: stopped/archived sessions, unneeded
//     worktrees, merged branches, each with a safety grade (safe/review/keep). Check
//     the ones to clear and run their action (archive_session / delete_session /
//     delete_worktree / delete_branch) in bulk. `keep` rows are shown but not checkable.
//     Rows are nested repo → working copy (cleanupGroups.ts) so a dozen worktrees of one
//     repo read as one repo, and each group shows what goes away together.
//   Archives — the gz safety net that delete_session/delete_branch write before removing
//     anything: restore (undo) or purge (reclaim for good).
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, rawJSON, raw } from "../../core/api/client.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { cleanupReasonText } from "./cleanupReason.ts";
import { groupCandidates, rowLabel, type CleanupCandidate } from "./cleanupGroups.ts";

interface CleanupArchive {
  id: string;
  at?: string;
  reason?: string;
  sessions?: { name: string; display?: string }[];
  branches?: { repo: string; name: string }[];
}

interface CleanupModalProps {
  onClose?: () => void;
  onChanged?: () => void;
}

// A stable key for a candidate row (id alone can repeat across types, e.g. a repo id
// carrying several branch candidates).
const rowKey = (c: CleanupCandidate) => `${c.type}:${c.id}:${c.branch || ""}`;

// The endpoint call that performs a candidate's action. Returns the raw Response so the
// caller can count ok/failed. delete_worktree/delete_session never pass force/aggressive
// flags — the Agent keeps its dirty/ahead/live guards.
function runAction(c: CleanupCandidate): Promise<Response> {
  const enc = encodeURIComponent;
  switch (c.action) {
    case "archive_session":
      return raw(`api/sessions/${enc(c.id)}/archive`, { method: "POST" });
    case "delete_session":
      return raw(`api/sessions/${enc(c.id)}?reclaim=1`, { method: "DELETE" });
    case "delete_worktree":
      return raw(`api/repos/${enc(c.id)}?prune_sessions=1`, { method: "DELETE" });
    case "delete_branch":
      return raw(`api/repos/${enc(c.id)}/branch?branch=${enc(c.branch || "")}`, { method: "DELETE" });
    default:
      return Promise.resolve(new Response(null, { status: 400 }));
  }
}

export function CleanupModal({ onClose, onChanged }: CleanupModalProps) {
  const [tab, setTab] = useState<"candidates" | "archives">("candidates");
  const [items, setItems] = useState<CleanupCandidate[] | null>(null);
  const [archives, setArchives] = useState<CleanupArchive[] | null>(null);
  const [checked, setChecked] = useState<Set<string>>(new Set());
  // Collapsed tree nodes, keyed "repo:<name>" / "copy:<folder>". Default is expanded:
  // the survey is a to-do list, and a collapsed-by-default one hides the work.
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const askConfirm = useConfirm();
  const toast = useToast();
  const tr = useT();

  const loadCandidates = () =>
    api("api/sessions/cleanup")
      .then((d) => setItems(Array.isArray(d.candidates) ? d.candidates : []))
      .catch(() => setItems([]));
  const loadArchives = () =>
    api("api/cleanup/archives")
      .then((d) => setArchives(Array.isArray(d.archives) ? d.archives : []))
      .catch(() => setArchives([]));

  useEffect(() => {
    void loadCandidates();
    void loadArchives();
  }, []);

  const actionable = useMemo(
    () => (items || []).filter((c) => c.action && c.safety !== "keep"),
    [items],
  );
  // repo → working copy → rows. `keep` rows live in their group too: a group is "what
  // this working copy holds", and a live/dirty copy is exactly what the user needs to
  // see to understand why it is not going anywhere.
  const repos = useMemo(() => groupCandidates(items || []), [items]);

  const toggle = (key: string) =>
    setChecked((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const selectAllSafe = () =>
    setChecked(new Set(actionable.filter((c) => c.safety === "safe").map(rowKey)));
  const clearSelection = () => setChecked(new Set());

  const toggleNode = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  // Collapse-all folds the repo level only — reopening one repo then shows its working
  // copies rather than a second layer of closed nodes.
  const allCollapsed = repos.length > 0 && repos.every((r) => collapsed.has("repo:" + r.repo));
  const toggleAll = () =>
    setCollapsed(allCollapsed ? new Set() : new Set(repos.map((r) => "repo:" + r.repo)));

  const runSelected = async () => {
    const targets = actionable.filter((c) => checked.has(rowKey(c)));
    if (targets.length === 0) return;
    const ok = await askConfirm({
      title: tr("clean.confirm_title", { count: targets.length }),
      body: tr("clean.confirm_body"),
      confirmLabel: tr("clean.confirm_do", { count: targets.length }),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    let done = 0;
    let failed = 0;
    try {
      // Sequential: cleanup mutates the workspace Agent; no gain from bursting.
      for (const c of targets) {
        try {
          const res = await runAction(c);
          if (res.ok) done += 1;
          else failed += 1;
        } catch {
          failed += 1;
        }
      }
    } finally {
      setBusy(false);
    }
    toast(failed ? t("clean.run_done", { done, failed }) : t("clean.run_done_ok", { done }));
    clearSelection();
    await loadCandidates();
    await loadArchives();
    onChanged?.();
  };

  const restore = async (id: string) => {
    setBusy(true);
    try {
      const res = await rawJSON(`api/cleanup/archives/${encodeURIComponent(id)}/restore`, "POST");
      toast(res.ok ? t("clean.restored") : t("clean.restore_failed"));
      await loadArchives();
      await loadCandidates();
      onChanged?.();
    } finally {
      setBusy(false);
    }
  };

  const purge = async (id: string) => {
    const ok = await askConfirm({
      title: tr("clean.purge_title"),
      body: tr("clean.purge_body"),
      confirmLabel: tr("clean.purge_do"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      await raw(`api/cleanup/archives/${encodeURIComponent(id)}`, { method: "DELETE" }).catch(() => {});
      await loadArchives();
    } finally {
      setBusy(false);
    }
  };

  const selectedCount = useMemo(
    () => actionable.filter((c) => checked.has(rowKey(c))).length,
    [actionable, checked],
  );

  return (
    <Modal title={tr("clean.title")} onClose={onClose} className="clean-modal">
      <p className="clean-subtitle">{tr("clean.subtitle")}</p>

      <div className="clean-tabs" role="tablist">
        <button
          role="tab"
          aria-selected={tab === "candidates"}
          className={"clean-tab" + (tab === "candidates" ? " is-active" : "")}
          onClick={() => setTab("candidates")}
        >
          {tr("clean.tab_candidates")}
        </button>
        <button
          role="tab"
          aria-selected={tab === "archives"}
          className={"clean-tab" + (tab === "archives" ? " is-active" : "")}
          onClick={() => setTab("archives")}
        >
          {tr("clean.tab_archives")}
        </button>
        <span className="clean-tabs-spacer" />
        <IconButton
          icon="refresh"
          label={tr("clean.reload")}
          onClick={() => {
            void loadCandidates();
            void loadArchives();
          }}
        />
      </div>

      {tab === "candidates" && (
        <div className="clean-body">
          {items === null ? (
            <div className="clean-empty">{tr("clean.loading")}</div>
          ) : items.length === 0 ? (
            <div className="clean-empty">{tr("clean.empty")}</div>
          ) : (
            <>
              <div className="clean-toolbar">
                <Button variant="ghost" onClick={selectAllSafe} disabled={busy}>
                  {tr("clean.select_all_safe")}
                </Button>
                <Button variant="ghost" onClick={clearSelection} disabled={busy || checked.size === 0}>
                  {tr("clean.clear_selection")}
                </Button>
                <Button
                  variant="ghost"
                  icon={allCollapsed ? "expand-all" : "collapse-all"}
                  title={allCollapsed ? tr("clean.expand_all") : tr("clean.collapse_all")}
                  onClick={toggleAll}
                >
                  {allCollapsed ? tr("clean.expand_all") : tr("clean.collapse_all")}
                </Button>
                <span className="clean-toolbar-spacer" />
                {selectedCount > 0 && (
                  <span className="clean-selected">{tr("clean.selected_n", { count: selectedCount })}</span>
                )}
                <Button variant="danger" onClick={runSelected} disabled={busy || selectedCount === 0}>
                  {tr("clean.run_selected")}
                </Button>
              </div>

              <ul className="clean-list">
                {repos.map((r) => {
                  const repoNode = "repo:" + r.repo;
                  const repoOpen = !collapsed.has(repoNode);
                  return (
                    <li key={repoNode} className="clean-repo">
                      <button type="button" className="sess-group-btn" onClick={() => toggleNode(repoNode)}>
                        <Icon name={repoOpen ? "chevron-down" : "chevron-right"} />
                        <Icon name="folder" />
                        <span className="sess-group-name">
                          <span className="sess-group-branch">{r.repo || tr("clean.group_other")}</span>
                        </span>
                        {r.safeCount > 0 && (
                          <span className="clean-group-safe">{tr("clean.group_safe_n", { count: r.safeCount })}</span>
                        )}
                        <span className="sess-group-count">{r.count}</span>
                      </button>
                      {repoOpen && (
                        <ul className="clean-copies">
                          {r.copies.map((g) => {
                            const copyNode = "copy:" + g.key;
                            const copyOpen = !collapsed.has(copyNode);
                            // A repo whose only working copy is the clone itself needs no
                            // second heading — it would just repeat the repo name. Rows
                            // with no known working copy (orphan panes) have none either.
                            const headed = g.key !== "" && (r.copies.length > 1 || g.isWorktree);
                            return (
                              <li key={copyNode} className="clean-copy">
                                {headed && (
                                  <button
                                    type="button"
                                    className="sess-group-btn clean-copy-btn"
                                    title={g.rows.find((c) => c.path)?.path || g.key}
                                    onClick={() => toggleNode(copyNode)}
                                  >
                                    <Icon name={copyOpen ? "chevron-down" : "chevron-right"} />
                                    <span className="sess-group-name">
                                      <span className="clean-copy-seg">{g.isWorktree ? g.suffix : g.key}</span>
                                      {g.isWorktree ? (
                                        g.branch && <span className="clean-copy-branch">{g.branch}</span>
                                      ) : (
                                        <span className="clean-copy-main">{tr("clean.group_main")}</span>
                                      )}
                                    </span>
                                    <span className="sess-group-count">{g.rows.length}</span>
                                  </button>
                                )}
                                {(!headed || copyOpen) && (
                                  <ul className="clean-rows">
                                    {g.rows.map((c) => {
                                      const key = rowKey(c);
                                      const selectable = !!c.action && c.safety !== "keep";
                                      // A worktree row has no target of its own (the heading
                                      // names it) — give its column to the reason instead of
                                      // leaving a gap.
                                      const label = rowLabel(c);
                                      return (
                                        <li
                                          key={key}
                                          className={
                                            "clean-row" +
                                            (selectable ? "" : " is-keep") +
                                            (label ? "" : " is-notarget")
                                          }
                                          title={selectable ? undefined : tr("clean.keep_hint")}
                                        >
                                          {selectable ? (
                                            <label className="clean-check">
                                              <input
                                                type="checkbox"
                                                checked={checked.has(key)}
                                                disabled={busy}
                                                onChange={() => toggle(key)}
                                              />
                                            </label>
                                          ) : (
                                            <span className="clean-check" aria-hidden="true" />
                                          )}
                                          <span className={"clean-badge clean-badge-" + c.safety}>
                                            {tr(("clean.safety_" + c.safety) as "clean.safety_safe")}
                                          </span>
                                          <span className={"clean-type clean-type-" + c.type}>
                                            {tr(("clean.type_" + c.type) as "clean.type_session")}
                                          </span>
                                          {label && <span className="clean-target">{label}</span>}
                                          <span className="clean-reason">{cleanupReasonText(c)}</span>
                                          <span className="clean-act">
                                            {c.action
                                              ? tr(("clean.action_" + c.action) as "clean.action_archive_session")
                                              : ""}
                                          </span>
                                        </li>
                                      );
                                    })}
                                  </ul>
                                )}
                              </li>
                            );
                          })}
                        </ul>
                      )}
                    </li>
                  );
                })}
              </ul>
            </>
          )}
        </div>
      )}

      {tab === "archives" && (
        <div className="clean-body">
          {archives === null ? (
            <div className="clean-empty">{tr("clean.loading")}</div>
          ) : archives.length === 0 ? (
            <div className="clean-empty">{tr("clean.archives_empty")}</div>
          ) : (
            <ul className="clean-list">
              {archives.map((a) => (
                <li key={a.id} className="clean-row clean-archive-row">
                  <span className="clean-arch-when">{(a.at || a.id).replace("T", " ").replace("Z", "")}</span>
                  <span className="clean-arch-what">
                    {a.reason === "delete_branch"
                      ? tr("clean.archive_reason_delete_branch")
                      : tr("clean.archive_reason_delete_session")}
                    {a.sessions && a.sessions.length > 0
                      ? " · " + tr("clean.archive_sessions_n", { count: a.sessions.length })
                      : ""}
                    {a.branches && a.branches.length > 0
                      ? " · " + tr("clean.archive_branches_n", { count: a.branches.length })
                      : ""}
                  </span>
                  <span className="clean-arch-actions">
                    <Button variant="ghost" onClick={() => void restore(a.id)} disabled={busy}>
                      {tr("clean.restore")}
                    </Button>
                    <Button variant="danger" onClick={() => void purge(a.id)} disabled={busy}>
                      {tr("clean.purge")}
                    </Button>
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Modal>
  );
}
