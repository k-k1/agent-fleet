import { useEffect } from "react";
import { api } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { humanSize } from "../../../lib/filemeta.ts";
import type { TreeKind, TreeProject } from "./memoryTypes.ts";

export interface RestoreScopeState {
  rev: string;
  all: boolean;
  /** 個別選択（キーは "project:<slug>" / "kind:<kind>"）。 */
  picked: Record<string, boolean>;
  tree: { kinds: TreeKind[]; projects: TreeProject[] } | null;
}
export interface RestoreBody {
  rev: string;
  scope: { all?: boolean; kinds?: string[]; projects?: string[] };
}

// RestorePanel — 戻す範囲を選ぶ。選択肢は**その時点のツリー**から作る（今の live では
// なく）。誤って消したメモリを戻す、が本命のユースケースなので、現在存在しない
// プロジェクトも選べなければ機能として成立しない（docs/log/39 ④）。
export function RestorePanel({
  state,
  patch,
  onClose,
  onSubmit,
  busy,
}: {
  state: RestoreScopeState;
  /** 部分更新。非同期の tree 取得が、その間に変わった選択を巻き戻さないようにするため
      「スナップショットを差し替える」ではなく「差分を当てる」形にしてある。 */
  patch: (p: Partial<RestoreScopeState>) => void;
  onClose: () => void;
  onSubmit: (rev: string, body: RestoreBody, label: string) => Promise<void>;
  busy: boolean;
}) {
  const tr = useT();
  const { rev, all, picked, tree } = state;

  useEffect(() => {
    let live = true;
    api("api/agents/memory/tree?rev=" + encodeURIComponent(rev))
      .then((d) => {
        if (!live || d?.error) return;
        patch({ tree: { kinds: d.kinds ?? [], projects: d.projects ?? [] } });
      })
      .catch(() => {});
    return () => {
      live = false;
    };
    // rev が変わったときだけ取り直す（選択の変更で再取得しない）。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rev]);

  const toggle = (key: string) => patch({ picked: { ...picked, [key]: !picked[key] } });
  // プロジェクト粒度を持たないルート（codex）は「まるごと」しか選べない。
  const wholeKinds = (tree?.kinds ?? []).filter((k) => !k.scopes);
  const projects = tree?.projects ?? [];
  const pickedProjects = projects.filter((p) => picked["project:" + p.slug]).map((p) => p.slug);
  const pickedKinds = wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => k.kind);
  const canSubmit = all || pickedProjects.length > 0 || pickedKinds.length > 0;

  const submit = () => {
    const body: RestoreBody = all
      ? { rev, scope: { all: true } }
      : { rev, scope: { projects: pickedProjects, kinds: pickedKinds } };
    const label = all
      ? tr("mem.scope_all")
      : [...projects.filter((p) => picked["project:" + p.slug]).map((p) => p.display), ...pickedKinds].join(
          tr("common.list_sep"),
        );
    void onSubmit(rev, body, label);
  };

  return (
    <section className="mem-section mem-restore">
      <div className="mem-head">
        <h3>{tr("mem.restore_title")}</h3>
        <code>{rev.slice(0, 8)}</code>
      </div>
      <div className="mem-scope">
        <label>
          <input type="radio" checked={all} onChange={() => patch({ all: true })} />
          {tr("mem.scope_all")}
        </label>
        <label>
          <input type="radio" checked={!all} onChange={() => patch({ all: false })} />
          {tr("mem.scope_pick")}
        </label>
      </div>
      {!all &&
        (tree === null ? (
          <p className="muted pad">{tr("common.loading")}</p>
        ) : projects.length === 0 && wholeKinds.length === 0 ? (
          <p className="muted pad">{tr("mem.tree_empty")}</p>
        ) : (
          <ul className="mem-picks">
            {projects.map((p) => (
              <li key={p.slug}>
                <label title={p.slug}>
                  <input
                    type="checkbox"
                    checked={!!picked["project:" + p.slug]}
                    onChange={() => toggle("project:" + p.slug)}
                  />
                  {p.display}
                  <span className="muted">
                    {" "}
                    {tr("mem.root_stats", { files: p.files, size: humanSize(p.bytes) })}
                  </span>
                </label>
              </li>
            ))}
            {wholeKinds.map((k) => (
              <li key={k.kind}>
                <label>
                  <input
                    type="checkbox"
                    checked={!!picked["kind:" + k.kind]}
                    onChange={() => toggle("kind:" + k.kind)}
                  />
                  {tr("mem.scope_whole_root", { label: k.label })}
                  <span className="muted">
                    {" "}
                    {tr("mem.root_stats", { files: k.files, size: humanSize(k.bytes) })}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        ))}
      <div className="flow">
        <button type="button" disabled={busy || !canSubmit} onClick={submit}>
          {tr("mem.restore_do")}
        </button>
        <button type="button" className="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </button>
      </div>
    </section>
  );
}
