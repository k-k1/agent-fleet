// SharedProjectNode — one project cluster in the shared-sessions tree: a project
// heading, its base + worktree working copies indented under it (mirroring the
// owner-side ProjectTree/RepoNode visual language via project.css's
// .proj-node/.proj-children — reused for CSS only; RepoNode itself is tightly
// coupled to owner-only actions (clone/delete/branch switch) that don't exist
// on the receiving side, so it isn't reused as a component).
//
// Collapsing uses the same usePersistedOpen(localStorage) as the owner-side tree. On the
// receiving side a node appears for every worktree the sharer cuts, so without collapsing
// the rail fills up immediately.
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { kindClass, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { stateInfo, stripLabelTag } from "../../lib/sessionview.ts";
import { usePersistedOpen } from "../../lib/usePersistedOpen.ts";
import { openSharedSession } from "./open.ts";
import type { SharedProjectGroup, SharedWorkingCopy } from "./sharedProject.ts";
import { ownerLabel, type SharedSession } from "./store.ts";
import { useHandoffStore } from "./handoffStore.ts";
import "../project/project.css";
import "./sharing.css";

function SharedSessionRow({ s }: { s: SharedSession }) {
  const tr = useT();
  // An unprocessed handoff (docs/log/77). Never cleared by being read: "read but not yet
  // decided" would then disappear and be forgotten. It clears only on accept, decline or
  // expiry (§77.10).
  const handoff = useHandoffStore((st) => st.received.some((o) => o.sessionId === s.id));
  const st = stateInfo({ kind: s.kind, alive: s.state === "running", state: s.activity });
  const sessionName = stripLabelTag(s.title || s.label || s.name);
  return (
    <li>
      <button
        className="shared-rail-row"
        type="button"
        title={`${sessionName}\n${ownerLabel(s)} · ${s.repo || s.name}`}
        onClick={(e) => openSharedSession(s.id, e.ctrlKey || e.metaKey)}
      >
        {/* The same kind-coloured icon as the owner-side SessionRow, so which agent the
            conversation is with reads at a glance — the recipient has no other clue. */}
        <span className={"sess-kic kind-" + kindClass(s.kind)} title={kindLabel(s.kind)}>
          <Icon name={kindIcon(s.kind)} />
        </span>
        {/* A label coming from claude's --name can carry an "[AF:<name>] " prefix. */}
        <span className="name">{sessionName}</span>
        <small>{tr(s.permission === "rw" ? "share.permission_rw" : "share.permission_ro")}</small>
        {handoff && (
          <span className="shared-rail-handoff" title={tr("handoff.row_badge")}>
            <Icon name="git-branch" />
          </span>
        )}
        {/* Archived and deleted sessions drop out of the list on the CP side
            (docs/log/59 §1), so only conversations the owner currently has appear here.
            The state chip uses the same stateInfo as the owner-side SessionRow. When the
            owner's Workspace is stopped, that single fact stops every row, so the
            workspace-stopped icon replaces the per-row stopped chips. */}
        {s.workspaceState !== "running" ? (
          <Icon name="debug-pause" title={tr("share.owner_stopped")} />
        ) : (
          <span className={"session-state mini " + st.cls} title={st.text}>
            <Icon name={st.icon} spin={st.spin} />
          </span>
        )}
      </button>
    </li>
  );
}

function SharedCopyNode({ copy }: { copy: SharedWorkingCopy }) {
  const tr = useT();
  const node = usePersistedOpen(`af-shared-wc-${copy.workingCopyId}`, true);
  return (
    <li className={"proj-node" + (node.open ? "" : " collapsed") + (copy.worktree ? " wt" : " base")}>
      <div className="proj-node-head">
        <button
          type="button"
          className="shared-node-toggle"
          onClick={node.toggle}
          aria-expanded={node.open}
          title={tr(node.open ? "pj.collapse" : "pj.expand")}
        >
          <span className="proj-node-caret" aria-hidden="true">
            <Icon name={node.open ? "chevron-down" : "chevron-right"} />
          </span>
          <Icon name={copy.worktree ? "git-branch" : "root-folder"} />
          {/* Named like the owner-side RepoRow: a worktree goes by its branch (the folder
              is "<base>@<random slug>", which says nothing about the work), the base by
              folder name with its current branch appended quietly. With no known branch
              (SVN, or before the fetch) the folder name is used. */}
          <span className="shared-copy-name" title={copy.repo}>
            {copy.worktree ? copy.branch || copy.repo : copy.repo}
          </span>
          {!copy.worktree && copy.branch && <span className="repo-branch-inline">{copy.branch}</span>}
          {!node.open && <small>{copy.sessions.length}</small>}
        </button>
      </div>
      {node.open && (
        <ul className="proj-node-body sess-list">
          {copy.sessions.map((s) => <SharedSessionRow key={s.id} s={s} />)}
        </ul>
      )}
    </li>
  );
}

export function SharedProjectNode({ group }: { group: SharedProjectGroup }) {
  const tr = useT();
  // Base only (no shared worktree) is the common case, and there the project name and its
  // single working copy name are identical — a doubled heading — so flatten to one level.
  const flat = group.copies.length === 1 && !group.copies[0].worktree;
  const node = usePersistedOpen(`af-shared-proj-${group.ownerUserKey}:${group.projectName}`, true);
  const total = group.copies.reduce((n, c) => n + c.sessions.length, 0);
  return (
    <li className={"shared-project-group" + (node.open ? "" : " collapsed")}>
      <button
        type="button"
        className="shared-project-head"
        onClick={node.toggle}
        aria-expanded={node.open}
        title={tr(node.open ? "pj.collapse" : "pj.expand")}
      >
        <span className="proj-node-caret" aria-hidden="true">
          <Icon name={node.open ? "chevron-down" : "chevron-right"} />
        </span>
        <Icon name="root-folder" />
        <strong>{group.projectName}</strong>
        {/* Flattened to one level (base working copy only), this heading IS that working
            copy's row, so it carries the branch like the owner-side base row. A heading
            that groups worktrees is a project name, not a working copy, and gets none. */}
        {flat && group.copies[0].branch && <span className="repo-branch-inline">{group.copies[0].branch}</span>}
        {/* Whose conversation it is is the recipient's strongest clue, so it is always
            shown, even with a single sharer. It names the login id (email address): the
            normalised key is not a string anyone identifies themselves by. */}
        <small className="shared-project-owner">{ownerLabel(group)}</small>
        {!node.open && <small>{total}</small>}
      </button>
      {node.open && (flat ? (
        <ul className="proj-node-body sess-list">
          {group.copies[0].sessions.map((s) => <SharedSessionRow key={s.id} s={s} />)}
        </ul>
      ) : (
        <ul className="proj-children">
          {group.copies.map((c) => <SharedCopyNode key={c.workingCopyId} copy={c} />)}
        </ul>
      ))}
    </li>
  );
}
