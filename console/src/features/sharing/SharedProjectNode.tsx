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
import { stateInfo } from "../../lib/sessionview.ts";
import { usePersistedOpen } from "../../lib/usePersistedOpen.ts";
import { openSharedSession } from "./open.ts";
import type { SharedProjectGroup, SharedWorkingCopy } from "./sharedProject.ts";
import { ownerLabel, type SharedSession } from "./store.ts";
import "../project/project.css";
import "./sharing.css";

function SharedSessionRow({ s }: { s: SharedSession }) {
  const tr = useT();
  const st = stateInfo({ kind: s.kind, alive: s.state === "running", state: s.activity });
  const sessionName = (s.title || s.label || s.name).replace(/^\[AF\]\s*/, "");
  return (
    <li>
      <button
        className="shared-rail-row"
        type="button"
        title={`${sessionName}\n${ownerLabel(s)} · ${s.repo || s.name}`}
        onClick={(e) => openSharedSession(s.id, e.ctrlKey || e.metaKey)}
      >
        {/* 所有者側の SessionRow と同じ kind 色付きアイコン — どのエージェントの会話かが
            一目で分かる(共有先には kind 以外の手掛かりが無いので特に効く)。 */}
        <span className={"sess-kic kind-" + kindClass(s.kind)} title={kindLabel(s.kind)}>
          <Icon name={kindIcon(s.kind)} />
        </span>
        {/* claude の --name 由来の label は "[AF] " 接頭辞付きのことがある。 */}
        <span className="name">{sessionName}</span>
        <small>{tr(s.permission === "rw" ? "share.permission_rw" : "share.permission_ro")}</small>
        {/* アーカイブ済み/削除済みは CP 側で一覧から外れる(docs/59 §1)ので、ここに
            並ぶのは所有者の手元に今ある会話だけ。
            状態チップは所有者側の SessionRow と同じ stateInfo。所有者 Workspace が
            停止中のときは、その1つの事実で全行が止まっているので、行ごとの
            停止中チップではなくワークスペース停止のアイコンだけを出す。 */}
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
          {/* 所有者側の RepoRow と同じ名乗り: worktree はブランチ名で呼ぶ(フォルダは
              "<base>@<ランダム slug>" で、どの作業か分からない)、ベースはフォルダ名＋
              現在のブランチを控えめに添える。ブランチ不明(SVN / 取得前)はフォルダ名。 */}
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
        {/* 1階層に畳んだ(ベース作業コピーだけ)ときは、この見出しがその作業コピーの行
            そのものなので、所有者側のベース行と同じくブランチを添える。worktree を
            束ねた見出しはプロジェクト名であって作業コピーではないので付けない。 */}
        {flat && group.copies[0].branch && <span className="repo-branch-inline">{group.copies[0].branch}</span>}
        {/* 誰の会話かは共有先にとって一番の手掛かりなので、共有元が1人でも常に出す。
            名乗りはログイン ID(メールアドレス) — 正規化キーは人が名乗る文字列ではない。 */}
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
