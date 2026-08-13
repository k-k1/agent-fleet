// ChangesView — working-tree changes + commit workbench (stage / unstage /
// discard, commit box, repo identity). A file's diff opens in a SEPARATE pane
// (openFileDiff). Port of views/ChangesView onto the zustand stores.
import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, rawJSON, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { openFileDiff } from "./open.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";

interface Change {
  path: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

export function ChangesView({ repo, headerActions }: { repo: string; headerActions?: ReactNode }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
  const bumpFiles = useFilesStore((s) => s.bump);
  const enc = encodeURIComponent(repo || "");
  const [changes, setChanges] = useState<Change[]>([]);
  const [selPath, setSelPath] = useState("");
  const [msg, setMsg] = useState("");
  const [all, setAll] = useState(false);

  // Imperative refresh (after stage/commit/discard). Returns whether the load reached a
  // terminal result; a transient gateway failure (WS agent still booting) reports false so
  // the mount's retry loop keeps trying. Called with a signal from useRetryLoad, and with
  // none from the action buttons.
  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      const d = await api(`api/repos/${enc}/changes`);
      if (signal?.aborted) return true;
      if (isTransientErr(d)) return false;
      setChanges(d.changes || []);
      return true;
    } catch {
      return false;
    }
  }, [enc]);

  useEffect(() => {
    setSelPath("");
  }, [enc]);
  useRetryLoad((signal) => refresh(signal), [refresh]);

  const showDiff = (path: string, staged: boolean) => {
    setSelPath(path);
    if (repo) openFileDiff(repo, path, staged);
  };

  const op = async (name: string, paths: string[]) => {
    if (name === "discard") {
      const ok = await askConfirm({
        title: tr("scm.discard_changes"),
        body: tr("scm.discard_confirm", { paths: paths.join(", ") }),
        confirmLabel: tr("scm.discard_do"),
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
      toast(tr("scm.commit_msg_required"), { kind: "warn" });
      return;
    }
    const r = await rawJSON(`api/repos/${enc}/commit`, "POST", { message: msg.trim(), all });
    if (r.ok) {
      setMsg("");
      void refresh();
      void refreshRepos();
    } else {
      const e = await r.json().catch(() => ({}));
      toast(tr("scm.commit_failed", { err: e.error?.message || r.status }));
    }
  };

  return (
    <div className="scmview">
      <ViewHead
        actions={
          <>
            <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("scm.refresh")} onClick={() => void refresh()}>
              <Icon name="refresh" />
            </button>
            {headerActions}
          </>
        }
      >
        <span className="view-title">
          <Icon name="git-commit" /> {repo} — {tr("scm.changes")}
        </span>
      </ViewHead>
      <div className="changes-body">
        <ul className="changes">
          {changes.length === 0 && <EmptyState icon="check" title={tr("scm.no_changes")} />}
          {changes.map((c) => (
            <ChangeRow key={c.path + (c.untracked ? "?" : "")} c={c} selected={selPath === c.path} onOpen={showDiff} onOp={op} />
          ))}
        </ul>

        <div className="commitbox">
          <textarea rows={2} placeholder={tr("scm.commit_message")} value={msg} onChange={(e) => setMsg(e.target.value)} />
          <label className="commitbox-all">
            <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> {tr("scm.stage_all_tracked")}
          </label>
          <button type="button" className="ui-btn ui-btn-primary" onClick={() => void commitOp()}>
            {tr("scm.commit")}
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
  const tr = useT();
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
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("scm.unstage")} onClick={() => void onOp("unstage", [c.path])}>
            <Icon name="remove" />
          </button>
        ) : (
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("scm.stage")} onClick={() => void onOp("stage", [c.path])}>
            <Icon name="add" />
          </button>
        )}
        {!c.untracked && (
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn chg-discard" title={tr("scm.discard_changes")} onClick={() => void onOp("discard", [c.path])}>
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
  const tr = useT();
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
    info.source === "manual" ? tr("scm.src_manual") : info.source === "provider" ? tr("scm.src_provider") : tr("scm.src_global");
  const openEdit = () => {
    setName(info.override?.name || eff.name || "");
    setEmail(info.override?.email || eff.email || "");
    setEdit(true);
  };
  const put = async (n: string, e: string) => {
    const res = await apiJSON(`api/repos/${enc}/identity`, "PUT", { name: n, email: e });
    if (res && res.error) {
      toast(tr("scm.save_failed", { err: res.error.message || res.error }));
      return;
    }
    setInfo(res);
    setEdit(false);
  };
  return (
    <div className="repo-identity">
      {!edit ? (
        <button type="button" className="ri-line" title={tr("scm.change_committer")} onClick={openEdit}>
          <Icon name="account" />
          <span className="ri-who">
            {eff.name || tr("scm.unset")}
            {eff.email ? ` <${eff.email}>` : ""}
          </span>
          <span className="ri-src">{srcLabel}</span>
        </button>
      ) : (
        <div className="ri-edit">
          <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <input placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button type="button" className="ui-btn ui-btn-sm" onClick={() => void put(name.trim(), email.trim())}>
            {tr("scm.save")}
          </button>
          {info.source === "manual" && (
            <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title={tr("scm.clear_override")} onClick={() => void put("", "")}>
              {tr("scm.clear")}
            </button>
          )}
          <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title={tr("scm.close")} onClick={() => setEdit(false)}>
            ×
          </button>
        </div>
      )}
    </div>
  );
}
