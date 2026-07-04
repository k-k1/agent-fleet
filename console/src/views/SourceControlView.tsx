import { useCallback, useEffect, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON, raw } from "../api.js";
import Icon from "../components/Icon.jsx";
import BranchModal from "../components/BranchModal.jsx";
import CommitGraph from "../components/CommitGraph.jsx";
import { useConfirm } from "../components/ConfirmProvider.jsx";
import EmptyState from "../components/EmptyState.jsx";
import type { GraphCommit } from "../lib/gitgraph.js";

interface ScmStatus {
  branch?: string;
  ahead?: number;
  behind?: number;
}

// SourceControlView is the per-repo commit-graph (codeleaf lane layout), opened by
// clicking a repo in the Repos section. The working-tree changes + commit box and the
// per-commit detail/diff are split out into their own panes (変更 / commit): clicking a
// commit opens its detail beside the graph; the 変更 button opens the changes pane.
export default function SourceControlView({ repo }: { repo?: string; wrap?: boolean }) {
  const { scmRepo: ctxRepo, bumpRepos, bumpFiles, showTerminal, showChanges, showCommit } = useApp();
  const askConfirm = useConfirm();
  const scmRepo = repo !== undefined ? repo : ctxRepo;
  const enc = encodeURIComponent(scmRepo || "");
  const [status, setStatus] = useState<ScmStatus | null>(null);
  const [commits, setCommits] = useState<GraphCommit[]>([]);
  const [current, setCurrent] = useState("");
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState("");
  const [showBranch, setShowBranch] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setStatus(await api(`api/repos/${enc}/status`));
    } catch {}
    try {
      const d = await api(`api/repos/${enc}/graph?limit=300`);
      setCommits(d.commits || []);
      setCurrent(d.current || "");
    } catch {
      setCommits([]);
    } finally {
      setLoading(false);
    }
  }, [enc]);

  useEffect(() => {
    setSelected("");
    refresh();
  }, [refresh]);

  const onSelect = (sha: string) => {
    setSelected(sha);
    if (scmRepo) showCommit(scmRepo, sha);
  };

  const fetchRepo = async () => {
    await apiJSON(`api/repos/${enc}/fetch`, "POST", { prune: true });
    refresh();
    bumpRepos();
  };
  const del = async () => {
    const ok = await askConfirm({
      title: "ワーキングコピーを削除",
      body: `"${scmRepo}" のローカル作業コピーを削除します。履歴・リモートはそのまま残ります。`,
      confirmLabel: "削除する",
      danger: true,
    });
    if (!ok) return;
    await raw(`api/repos/${enc}`, { method: "DELETE" });
    bumpRepos();
    bumpFiles();
    showTerminal();
  };

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title"><Icon name="repo" /> {scmRepo}</span>
        <button className="branch" type="button" title="ブランチ切替" onClick={() => setShowBranch(true)}>
          <Icon name="git-branch" /> {status?.branch || current || "?"} <Icon name="chevron-down" />
        </button>
        {status && (status.ahead || status.behind) ? (
          <span
            className="repo-state-chip ab"
            title={`リモートに対して 先行 ${status.ahead ?? 0} / 遅延 ${status.behind ?? 0}`}
          >
            {status.ahead ? `↑${status.ahead}` : ""}
            {status.ahead && status.behind ? " " : ""}
            {status.behind ? `↓${status.behind}` : ""}
          </span>
        ) : null}
        <span className="spacer" />
        <button className="ghost" title="変更をコミット（別ペイン）" onClick={() => scmRepo && showChanges(scmRepo)}>
          <Icon name="git-commit" /> 変更
        </button>
        <button className="ghost" title="git fetch --prune" onClick={fetchRepo}>
          <Icon name="cloud-download" /> fetch
        </button>
        <button className="ghost" title="更新" onClick={refresh}>
          <Icon name="refresh" />
        </button>
        <button className="ghost danger" title="ワーキングコピーを削除" onClick={del}>
          <Icon name="trash" />
        </button>
      </header>
      <div className="cgraph-body">
        {loading && commits.length === 0 ? (
          <EmptyState icon="loading" message="読み込み中…" />
        ) : commits.length === 0 ? (
          <EmptyState icon="git-commit" message="コミットがありません" />
        ) : (
          <CommitGraph commits={commits} current={current} selectedSha={selected} onSelect={onSelect} />
        )}
      </div>
      {showBranch && (
        <BranchModal
          repoName={scmRepo || ""}
          onClose={() => setShowBranch(false)}
          onChecked={() => {
            setShowBranch(false);
            refresh();
            bumpRepos();
          }}
        />
      )}
    </div>
  );
}
