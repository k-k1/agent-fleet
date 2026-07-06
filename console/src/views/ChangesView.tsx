import { useCallback, useEffect, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON, rawJSON } from "../api.js";
import Icon from "../components/Icon.jsx";
import { useConfirm } from "../components/ConfirmProvider.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import EmptyState from "../components/EmptyState.jsx";

interface Change {
  path: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

// ChangesView is the working-tree changes + commit workbench, split out of the SCM
// graph into its own pane (opened via the graph's 変更 button or the repo context menu).
// It lists changed files (stage / unstage / discard) with a commit box + the repo
// identity; a file's diff opens in a SEPARATE pane (showFileDiff), like the commit graph.
export default function ChangesView({ repo }: { repo?: string; wrap?: boolean }) {
  const { scmRepo: ctxRepo, bumpRepos, bumpFiles, showFileDiff } = useApp();
  const askConfirm = useConfirm();
  const toast = useToast();
  const scmRepo = repo !== undefined ? repo : ctxRepo;
  const enc = encodeURIComponent(scmRepo || "");
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
    refresh();
  }, [refresh]);

  // Open the file's diff in its own pane (reuses a single diff pane; see showFileDiff).
  const showDiff = (path: string, staged: boolean) => {
    setSelPath(path);
    if (scmRepo) showFileDiff(scmRepo, path, staged);
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
    refresh();
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
      refresh();
      bumpRepos();
    } else {
      const e = await r.json().catch(() => ({}));
      toast("commit 失敗: " + (e.error?.message || r.status));
    }
  };

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title"><Icon name="git-commit" /> {scmRepo} — 変更</span>
        <span className="spacer" />
        <button className="ghost" title="更新" onClick={refresh}>
          <Icon name="refresh" />
        </button>
      </header>
      <div className="scmbody scm-changes-single">
        <div className="scmleft">
          <div className="sub-head">変更</div>
          <ul className="changes">
            {changes.length === 0 && <EmptyState icon="check" message="変更はありません" />}
            {changes.map((c) => (
              <ChangeRow key={c.path + (c.untracked ? "?" : "")} c={c} selected={selPath === c.path} onOpen={showDiff} onOp={op} />
            ))}
          </ul>

          <div className="commitbox">
            <textarea rows={2} placeholder="コミットメッセージ" value={msg} onChange={(e) => setMsg(e.target.value)} />
            <label className="muted">
              <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> 追跡中を全て stage (-a)
            </label>
            <button onClick={commitOp}>Commit</button>
            <RepoIdentity enc={enc} />
          </div>
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
          <button className="icon" title="unstage" onClick={() => onOp("unstage", [c.path])}>
            <Icon name="remove" />
          </button>
        ) : (
          <button className="icon" title="stage" onClick={() => onOp("stage", [c.path])}>
            <Icon name="add" />
          </button>
        )}
        {!c.untracked && (
          <button className="icon danger" title="変更を破棄" onClick={() => onOp("discard", [c.path])}>
            <Icon name="discard" />
          </button>
        )}
      </span>
    </li>
  );
}

// RepoIdentity shows the repo's effective commit identity and lets the user pin a
// per-repo override (written to the repo's local .git/config by the Agent).
function RepoIdentity({ enc }: { enc: string }) {
  const toast = useToast();
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
          <input className="cinput" placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <input className="cinput" placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button onClick={() => put(name.trim(), email.trim())}>保存</button>
          {info.source === "manual" && (
            <button className="ghost" title="上書きを解除（プロバイダ既定に戻す）" onClick={() => put("", "")}>
              解除
            </button>
          )}
          <button className="ghost" title="閉じる" onClick={() => setEdit(false)}>
            ×
          </button>
        </div>
      )}
    </div>
  );
}
