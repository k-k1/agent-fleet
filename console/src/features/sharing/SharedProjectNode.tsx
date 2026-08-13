// SharedProjectNode — one project cluster in the 共有セッション tree: a project
// heading, its base + worktree working copies indented under it (mirroring the
// owner-side ProjectTree/RepoNode visual language via project.css's
// .proj-node/.proj-children — reused for CSS only; RepoNode itself is tightly
// coupled to owner-only actions (clone/delete/branch switch) that don't exist
// on the receiving side, so it isn't reused as a component).
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { openSharedSession } from "./open.ts";
import type { SharedProjectGroup, SharedWorkingCopy } from "./sharedProject.ts";
import type { SharedSession } from "./store.ts";
import "../project/project.css";
import "./sharing.css";

function SharedSessionRow({ s }: { s: SharedSession }) {
  const tr = useT();
  return (
    <li>
      <button
        className="shared-rail-row"
        type="button"
        title={`${s.ownerUserKey} · ${s.repo || s.name}`}
        onClick={(e) => openSharedSession(s.id, e.ctrlKey || e.metaKey)}
      >
        <Icon name="comment-discussion" />
        {/* claude の --name 由来の label は "[AF] " 接頭辞付きのことがある。 */}
        <span className="name">{(s.title || s.label || s.name).replace(/^\[AF\]\s*/, "")}</span>
        <small>{tr(s.permission === "rw" ? "share.permission_rw" : "share.permission_ro")}</small>
        {/* アーカイブ済み/削除済みは CP 側で一覧から外れる(docs/59 §1)ので、ここに
            並ぶのは所有者の手元に今ある会話だけ。 */}
        {s.workspaceState !== "running" && <Icon name="debug-pause" title={tr("share.owner_stopped")} />}
      </button>
    </li>
  );
}

function SharedCopyNode({ copy }: { copy: SharedWorkingCopy }) {
  return (
    <li className={"proj-node" + (copy.worktree ? " wt" : "")}>
      <div className="proj-node-head">
        <span className="proj-node-caret" aria-hidden="true">
          <Icon name={copy.worktree ? "git-branch" : "root-folder"} />
        </span>
        <span className="shared-copy-name">{copy.repo}</span>
      </div>
      <ul className="proj-node-body sess-list">
        {copy.sessions.map((s) => <SharedSessionRow key={s.id} s={s} />)}
      </ul>
    </li>
  );
}

export function SharedProjectNode({ group, showOwner }: { group: SharedProjectGroup; showOwner: boolean }) {
  // ベースのみ(共有された worktree が無い)の一番よくあるケースは、プロジェクト名と
  // その唯一の working copy 名が同一になり二重見出しになるので、1階層に畳む。
  const flat = group.copies.length === 1 && !group.copies[0].worktree;
  return (
    <li className="shared-project-group">
      <div className="shared-project-head">
        <Icon name="root-folder" />
        <strong>{group.projectName}</strong>
        {showOwner && <small>{group.ownerUserKey}</small>}
      </div>
      {flat ? (
        <ul className="proj-node-body sess-list">
          {group.copies[0].sessions.map((s) => <SharedSessionRow key={s.id} s={s} />)}
        </ul>
      ) : (
        <ul className="proj-children">
          {group.copies.map((c) => <SharedCopyNode key={c.workingCopyId} copy={c} />)}
        </ul>
      )}
    </li>
  );
}
