// CleanupModal — the on-demand tidy-up panel (docs/32). Two tabs:
//   Candidates — the /sessions/cleanup survey, laid out as the two stages of the
//     after-work tidy-up: ① stopped sessions (agent → archive, shell/ssm → delete),
//     ② worktrees and merged branches. Each stage has a one-shot bulk button next to
//     the fine-grained checkboxes, so the everyday "clear everything" is two clicks.
//     ARCHIVED sessions are NOT listed here — they are the shelf, managed (browsed,
//     restored, reclaimed) in ArchivedModal; a pointer row links there instead. The
//     survey still reports them (the assistant/operator has no shelf UI), we filter.
//     Rows are nested repo → working copy (cleanupGroups.ts) so a dozen worktrees of one
//     repo read as one repo, and each group shows what goes away together.
//   Trash — the gz safety net that delete_session/delete_branch write before removing
//     anything: restore (undo) or purge (reclaim for good).
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, rawJSON, raw } from "../../core/api/client.ts";
import { t, tMaybe, useT } from "../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../lib/intl.ts";
import { cleanupReasonParts } from "./cleanupReason.ts";
import { groupCandidates, rowLabel, type CleanupCandidate, type CleanupRepoGroup } from "./cleanupGroups.ts";
import { useSessionUI } from "./ui.ts";

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

// Archived sessions belong to the shelf (ArchivedModal), not the cleanup survey — one
// object, one venue. An Agent older than the reason keys sends prose only; for those,
// session+delete_session can only mean "archived" (ephemeral grading shipped together
// with the keys), so the fallback stays correct across version skew.
const isShelfRow = (c: CleanupCandidate) =>
  c.type === "session" &&
  (c.reason_key === "clean.reason.archived" || (!c.reason_key && c.action === "delete_session"));

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

  // The shelf rows are counted for the pointer, everything else is the two stages.
  const visible = useMemo(() => (items || []).filter((c) => !isShelfRow(c)), [items]);
  const shelfCount = (items?.length ?? 0) - visible.length;
  const stage1 = useMemo(() => visible.filter((c) => c.type === "session"), [visible]);
  const stage2 = useMemo(() => visible.filter((c) => c.type !== "session"), [visible]);
  const actionable = useMemo(
    () => visible.filter((c) => c.action && c.safety !== "keep"),
    [visible],
  );
  // repo → working copy → rows. `keep` rows live in their group too: a group is "what
  // this working copy holds", and a live/dirty copy is exactly what the user needs to
  // see to understand why it is not going anywhere.
  const repos1 = useMemo(() => groupCandidates(stage1), [stage1]);
  const repos2 = useMemo(() => groupCandidates(stage2), [stage2]);

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
  // copies rather than a second layer of closed nodes. Node keys carry the stage prefix
  // so the same repo appearing in both stages folds independently.
  const repoNodes = useMemo(
    () => [
      ...repos1.map((r) => "1|repo:" + r.repo),
      ...repos2.map((r) => "2|repo:" + r.repo),
    ],
    [repos1, repos2],
  );
  const allCollapsed = repoNodes.length > 0 && repoNodes.every((k) => collapsed.has(k));
  const toggleAll = () => setCollapsed(allCollapsed ? new Set() : new Set(repoNodes));

  // The shared executor behind 選択を実行 and the per-stage one-shot buttons.
  const runTargets = async (
    targets: CleanupCandidate[],
    confirm: { title: string; body: string; confirmLabel: string },
  ) => {
    if (targets.length === 0) return;
    const ok = await askConfirm({ ...confirm, danger: true });
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

  const runSelected = () => {
    const targets = actionable.filter((c) => checked.has(rowKey(c)));
    return runTargets(targets, {
      title: tr("clean.confirm_title", { count: targets.length }),
      body: tr("clean.confirm_body"),
      confirmLabel: tr("clean.confirm_do", { count: targets.length }),
    });
  };

  // Stage ① one-shot — replaces the old tree-header clear-all button: archive every
  // stopped agent session, delete stopped shell/ssm. Review rows included: archive is
  // reversible, and the survey grades every plain stopped session review.
  const stage1Targets = useMemo(
    () => stage1.filter((c) => c.action && c.safety !== "keep"),
    [stage1],
  );
  const runStage1 = () => {
    const archN = stage1Targets.filter((c) => c.action === "archive_session").length;
    const delN = stage1Targets.filter((c) => c.action === "delete_session").length;
    const parts = [];
    if (archN) parts.push(t("sess.cleanup_archive_n", { count: archN }));
    if (delN) parts.push(t("sess.cleanup_delete_n", { count: delN }));
    return runTargets(stage1Targets, {
      title: tr("clean.stage1_confirm_title"),
      body: tr("clean.stage1_confirm_body", { parts: parts.join(tr("common.list_sep")) }),
      confirmLabel: tr("clean.confirm_do", { count: stage1Targets.length }),
    });
  };

  // Stage ② one-shot takes SAFE rows only (merged+clean worktrees, merged branches) —
  // review rows (unmerged commits) stay a deliberate checkbox choice.
  const stage2Safe = useMemo(
    () => stage2.filter((c) => c.action && c.safety === "safe"),
    [stage2],
  );
  const runStage2 = () =>
    runTargets(stage2Safe, {
      title: tr("clean.stage2_confirm_title"),
      body: tr("clean.stage2_confirm_body", { count: stage2Safe.length }),
      confirmLabel: tr("clean.confirm_do", { count: stage2Safe.length }),
    });

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

  // Stage ③ lives on the shelf: swap this modal for the archive browser.
  const openArchived = useSessionUI((s) => s.openArchived);
  const openShelf = () => {
    onClose?.();
    openArchived();
  };

  // One stage's repo → working copy → rows tree. `prefix` namespaces the collapse
  // keys so the same repo folds independently per stage.
  const renderTree = (repos: CleanupRepoGroup[], prefix: string) => (
    <ul className="clean-list">
      {repos.map((r) => {
        const repoNode = prefix + "repo:" + r.repo;
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
                  const copyNode = prefix + "copy:" + g.key;
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
                            // A worktree row has no target of its own — the heading
                            // names it; its first line is just badges + action.
                            const label = rowLabel(c);
                            const reason = cleanupReasonParts(c);
                            return (
                              <li
                                key={key}
                                className={"clean-row" + (selectable ? "" : " is-keep")}
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
                                {/* Agent 由来の動的キーは未知値がありうる — 未検査キャストで生キーを
                                    出さず、訳が無ければ原文をそのまま見せる。 */}
                                <span className={"clean-badge clean-badge-" + c.safety}>
                                  {tMaybe("clean.safety_" + c.safety) ?? c.safety}
                                </span>
                                <span className={"clean-type clean-type-" + c.type}>
                                  {tMaybe("clean.type_" + c.type) ?? c.type}
                                </span>
                                {label && (
                                  <span className="clean-target" title={label}>
                                    {label}
                                  </span>
                                )}
                                <span className="clean-act">
                                  {c.action ? (tMaybe("clean.action_" + c.action) ?? c.action) : ""}
                                </span>
                                {/* 2行目: 状態バッジ＋補足。1行に押し込んで見切れていた理由列の置き換え。 */}
                                <span className="clean-reason">
                                  {reason.badge && <span className="clean-reason-badge">{reason.badge}</span>}
                                  {reason.text && <span className="clean-reason-text">{reason.text}</span>}
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
  );

  return (
    <Modal title={tr("clean.title")} onClose={onClose} className="clean-modal">
      {/* ★ 共有の ui-modal-body に載せる。ui-modal 自身に padding は無く（見出しが
          自分で持つ形）、直に子を置くと本文だけが枠に貼りつく。 */}
      <div className="ui-modal-body">
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

                <section className="clean-stage">
                  <div className="clean-stage-head">
                    <span className="clean-stage-title">{tr("clean.stage1_title")}</span>
                    <span className="clean-toolbar-spacer" />
                    <Button
                      small
                      disabled={busy || stage1Targets.length === 0}
                      title={tr("clean.stage1_run_title")}
                      onClick={() => void runStage1()}
                    >
                      {tr("clean.stage1_run")}
                      {stage1Targets.length ? tr("common.paren", { v: stage1Targets.length }) : ""}
                    </Button>
                  </div>
                  {repos1.length === 0 ? (
                    <p className="clean-stage-empty">{tr("clean.stage1_empty")}</p>
                  ) : (
                    renderTree(repos1, "1|")
                  )}
                </section>

                <section className="clean-stage">
                  <div className="clean-stage-head">
                    <span className="clean-stage-title">{tr("clean.stage2_title")}</span>
                    <span className="clean-toolbar-spacer" />
                    <Button
                      small
                      variant="danger"
                      disabled={busy || stage2Safe.length === 0}
                      title={tr("clean.stage2_run_title")}
                      onClick={() => void runStage2()}
                    >
                      {tr("clean.stage2_run")}
                      {stage2Safe.length ? tr("common.paren", { v: stage2Safe.length }) : ""}
                    </Button>
                  </div>
                  {repos2.length === 0 ? (
                    <p className="clean-stage-empty">{tr("clean.stage2_empty")}</p>
                  ) : (
                    renderTree(repos2, "2|")
                  )}
                </section>

                {shelfCount > 0 && (
                  <div className="clean-shelf">
                    <Icon name="archive" />
                    <span className="clean-shelf-text">{tr("clean.shelf_n", { count: shelfCount })}</span>
                    <Button small variant="ghost" onClick={openShelf}>
                      {tr("clean.open_shelf")}
                    </Button>
                  </div>
                )}
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
                    {/* at は Agent の RFC3339(UTC) — 生加工では 9 時間ずれるのでロケール日時へ。
                        at 欠落時の id は日時として不正なので fmtDateTime がそのまま返す。 */}
                    <span className="clean-arch-when">{fmtDateTime(a.at || a.id, DATETIME_FULL)}</span>
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
      </div>
    </Modal>
  );
}
