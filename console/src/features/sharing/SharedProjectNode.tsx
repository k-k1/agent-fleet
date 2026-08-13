// SharedProjectNode — one project cluster in the 共有セッション tree: a project
// heading, its base + worktree working copies indented under it (mirroring the
// owner-side ProjectTree/RepoNode visual language via project.css's
// .proj-node/.proj-children — reused for CSS only; RepoNode itself is tightly
// coupled to owner-only actions (clone/delete/branch switch) that don't exist
// on the receiving side, so it isn't reused as a component).
//
// 折りたたみは所有者側ツリーと同じ usePersistedOpen(localStorage)。受信側は共有元が
// worktree を切るたびにノードが増えるので、畳めないと rail が一瞬で埋まる。
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { kindClass, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { usePersistedOpen } from "../../lib/usePersistedOpen.ts";
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
        {/* 所有者側の SessionRow と同じ kind 色付きアイコン — どのエージェントの会話かが
            一目で分かる(共有先には kind 以外の手掛かりが無いので特に効く)。 */}
        <span className={"sess-kic kind-" + kindClass(s.kind)} title={kindLabel(s.kind)}>
          <Icon name={kindIcon(s.kind)} />
        </span>
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
          <span className="shared-copy-name">{copy.repo}</span>
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

export function SharedProjectNode({ group, showOwner }: { group: SharedProjectGroup; showOwner: boolean }) {
  const tr = useT();
  // ベースのみ(共有された worktree が無い)の一番よくあるケースは、プロジェクト名と
  // その唯一の working copy 名が同一になり二重見出しになるので、1階層に畳む。
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
        {showOwner && <small className="shared-project-owner">{group.ownerUserKey}</small>}
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
