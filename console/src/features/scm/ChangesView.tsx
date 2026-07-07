// ChangesView — working-tree changes + commit workbench (stage / unstage /
// discard, commit box, repo identity). A file's diff opens in a SEPARATE pane
// (openFileDiff). Port of views/ChangesView onto the zustand stores.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, rawJSON } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { openFileDiff } from "./open.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";

interface Change {
  path: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

export function ChangesView({ repo }: { repo: string }) {
  const askConfirm = useConfirm();
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
  const bumpFiles = useFilesStore((s) => s.bump);
  const enc = encodeURIComponent(repo || "");
  const [changes, setChanges] = useState<Change[]>([]);
  const [selPath, setSelPath] = useState("");
  const [msg, setMsg] = useState("");
  const [all, setAll] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const d = await api(`api/repos/${enc}/changes`);
      setChanges(d.changes || []);
    } catch {
      setChanges([]);
    }
  }, [enc]);

  useEffect(() => {
    setSelPath("");
    void refresh();
  }, [refresh]);

  const showDiff = (path: string, staged: boolean) => {
    setSelPath(path);
    if (repo) openFileDiff(repo, path, staged);
  };

  const op = async (name: string, paths: string[]) => {
    if (name === "discard") {
      const ok = await askConfirm({
        title: "変更を破棄",
        body: `${paths.join(", ")} の変更を破棄します。元に戻せません。`,
        confirmLabel: "破棄する",
        danger: true,
      });
      if (!ok) return;
    }
    await apiJSON(`api/repos/${enc}/${name}`, "POST", { paths });
    void refresh();
    bumpFiles();
  };

  const commitOp = async () => {
    if (!msg.trim()) {
      toast("コミットメッセージが必要です", { kind: "warn" });
      return;
    }
    const r = await rawJSON(`api/repos/${enc}/commit`, "POST", { message: msg.trim(), all });
    if (r.ok) {
      setMsg("");
      void refresh();
      void refreshRepos();
    } else {
      const e = await r.json().catch(() => ({}));
      toast("commit 失敗: " + (e.error?.message || r.status));
    }
  };

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title">
          <Icon name="git-commit" /> {repo} — 変更
        </span>
        <span className="view-spacer" />
        <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title="更新" onClick={() => void refresh()}>
          <Icon name="refresh" />
        </button>
      </header>
      <div className="changes-body">
        <ul className="changes">
          {changes.length === 0 && <EmptyState icon="check" title="変更はありません" />}
          {changes.map((c) => (
            <ChangeRow key={c.path + (c.untracked ? "?" : "")} c={c} selected={selPath === c.path} onOpen={showDiff} onOp={op} />
          ))}
        </ul>

        <div className="commitbox">
          <textarea rows={2} placeholder="コミットメッセージ" value={msg} onChange={(e) => setMsg(e.target.value)} />
          <label className="commitbox-all">
            <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> 追跡中を全て stage (-a)
          </label>
          <button type="button" className="ui-btn ui-btn-primary" onClick={() => void commitOp()}>
            Commit
          </button>
          <RepoIdentity enc={enc} />
        </div>
      </div>
    </div>
  );
}

function ChangeRow({
  c,
  selected,
  onOpen,
  onOp,
}: {
  c: Change;
  selected: boolean;
  onOpen: (path: string, staged: boolean) => void;
  onOp: (name: string, paths: string[]) => void;
}) {
  const staged = !c.untracked && c.index !== " ";
  const tag = c.untracked ? "U" : staged ? c.index : c.worktree;
  const cls = c.untracked ? "untracked" : staged ? "staged" : "unstaged";
  return (
    <li className={"change" + (selected ? " active" : "")}>
      <span className={"chg " + cls}>{tag}</span>
      <span className="chg-name" title={c.path} onClick={() => onOpen(c.path, staged)}>
        {c.path}
      </span>
      <span className="chg-acts">
        {staged ? (
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title="unstage" onClick={() => void onOp("unstage", [c.path])}>
            <Icon name="remove" />
          </button>
        ) : (
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title="stage" onClick={() => void onOp("stage", [c.path])}>
            <Icon name="add" />
          </button>
        )}
        {!c.untracked && (
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn chg-discard" title="変更を破棄" onClick={() => void onOp("discard", [c.path])}>
            <Icon name="discard" />
          </button>
        )}
      </span>
    </li>
  );
}

// RepoIdentity — the repo's effective commit identity + a per-repo override
// (written to the repo's local .git/config by the Agent).
function RepoIdentity({ enc }: { enc: string }) {
  const toast = useToast();
  /* eslint-disable @typescript-eslint/no-explicit-any */
  const [info, setInfo] = useState<any>(null);
  const [edit, setEdit] = useState(false);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const load = useCallback(() => {
    api(`api/repos/${enc}/identity`)
      .then((d) => {
        if (d && !d.error) setInfo(d);
      })
      .catch(() => {});
  }, [enc]);
  useEffect(() => {
    load();
  }, [load]);
  if (!info) return null;
  const eff = info.effective || {};
  const srcLabel =
    info.source === "manual" ? "このリポ上書き" : info.source === "provider" ? "プロバイダ既定" : "グローバル既定";
  const openEdit = () => {
    setName(info.override?.name || eff.name || "");
    setEmail(info.override?.email || eff.email || "");
    setEdit(true);
  };
  const put = async (n: string, e: string) => {
    const res = await apiJSON(`api/repos/${enc}/identity`, "PUT", { name: n, email: e });
    if (res && res.error) {
      toast("保存に失敗: " + (res.error.message || res.error));
      return;
    }
    setInfo(res);
    setEdit(false);
  };
  return (
    <div className="repo-identity">
      {!edit ? (
        <button type="button" className="ri-line" title="コミット者を変更（このリポの上書き）" onClick={openEdit}>
          <Icon name="account" />
          <span className="ri-who">
            {eff.name || "(未設定)"}
            {eff.email ? ` <${eff.email}>` : ""}
          </span>
          <span className="ri-src">{srcLabel}</span>
        </button>
      ) : (
        <div className="ri-edit">
          <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <input placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button type="button" className="ui-btn ui-btn-sm" onClick={() => void put(name.trim(), email.trim())}>
            保存
          </button>
          {info.source === "manual" && (
            <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title="上書きを解除（プロバイダ既定に戻す）" onClick={() => void put("", "")}>
              解除
            </button>
          )}
          <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title="閉じる" onClick={() => setEdit(false)}>
            ×
          </button>
        </div>
      )}
    </div>
  );
}
