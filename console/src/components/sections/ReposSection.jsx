import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, apiJSON, raw } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import NewRepoModal from "../NewRepoModal.jsx";
import BranchModal from "../BranchModal.jsx";
import { kindIcon, kindLabel } from "../../lib/sessionkind.js";
import { pinFirst } from "../../lib/listutil.js";

const repoSafeSession = (name) =>
  name.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 40) || "repo";

// freeName picks the first unused session name: base, base-2, base-3, … so the
// same repo can spawn several sessions ("複製") without name collisions.
const freeName = (base, used) => {
  if (!used.has(base)) return base;
  for (let n = 2; ; n++) {
    const c = `${base}-${n}`;
    if (!used.has(c)) return c;
  }
};

// Repos: working copies under ~/repos. Clone new ones (provider picker or URL),
// switch branch, fetch, start a session in the repo, delete, or open Source
// Control (→ main area). The dirty dot mirrors uncommitted changes.
export default function ReposSection() {
  const { reposKey, bumpRepos, bumpSessions, bumpFiles, revealInFiles, showSCM, showTerminal, scmRepo, mode, session } =
    useApp();
  const [repos, setRepos] = useState([]);
  const [showClone, setShowClone] = useState(false);
  const [activeRepo, setActiveRepo] = useState(null); // repo used by the attached session

  useEffect(() => {
    let alive = true;
    api("api/repos")
      .then((d) => alive && setRepos(d.repos || []))
      .catch(() => alive && setRepos([]));
    return () => {
      alive = false;
    };
  }, [reposKey]);

  // Resolve which repo the currently-attached session works in, so we can pin it to
  // the top (mirrors the session pin). Refetched when the attached session changes.
  useEffect(() => {
    if (!session) {
      setActiveRepo(null);
      return;
    }
    let alive = true;
    api("api/sessions")
      .then((d) => {
        if (!alive) return;
        const s = (d.sessions || []).find((x) => x.name === session);
        setActiveRepo(s ? s.repo : null);
      })
      .catch(() => alive && setActiveRepo(null));
    return () => {
      alive = false;
    };
  }, [session]);

  // Pin the active session's repo to the top (stable, keeps the rest in order).
  const ordered = pinFirst(repos, (r) => r.name === activeRepo);

  return (
    <Section
      title="Repos"
      actions={
        <>
          <button className="ghost" title="clone" onClick={() => setShowClone((s) => !s)}>
            <Icon name="add" />
          </button>
          <button className="ghost" title="更新" onClick={bumpRepos}>
            <Icon name="refresh" />
          </button>
        </>
      }
    >
      {showClone && (
        <NewRepoModal
          onClose={() => setShowClone(false)}
          onCloned={(res) => {
            bumpRepos();
            // Open the freshly-cloned dir in the Files tree (also refreshes it).
            if (res && res.name) revealInFiles("repos/" + res.name);
            else bumpFiles();
            setShowClone(false);
          }}
        />
      )}
      <ul className="list">
        {repos.length === 0 && <li className="muted">リポジトリなし</li>}
        {ordered.map((r) => (
          <RepoRow
            key={r.name}
            r={r}
            active={mode === "scm" && scmRepo === r.name}
            pinned={r.name === activeRepo}
            // One click on the repo: reveal it in the Files tree AND open the
            // (renewed) Source Control workbench in the main pane. The separate
            // "変更" chip is gone — the repo row itself is the entry point.
            onOpen={() => {
              revealInFiles("repos/" + r.name);
              showSCM(r.name);
            }}
            onLaunch={async (kind) => {
              const suffix =
                kind === "shell" ? "-sh" : kind === "opencode" ? "-oc" : kind === "codex" ? "-cx" : "";
              const base = repoSafeSession(r.name) + suffix;
              let used = new Set();
              try {
                const d = await api("api/sessions");
                used = new Set((d.sessions || []).map((s) => s.name));
              } catch {}
              const name = freeName(base, used);
              const res = await apiJSON("api/sessions", "POST", { name, dir: r.path, kind });
              if (res && res.error) {
                alert("起動に失敗: " + (res.error.message || res.error));
                return;
              }
              bumpSessions();
              showTerminal(name);
            }}
            onChanged={bumpRepos}
          />
        ))}
      </ul>
    </Section>
  );
}

function RepoRow({ r, active, pinned, onOpen, onLaunch, onChanged }) {
  const [showBranch, setShowBranch] = useState(false);
  const [showLaunch, setShowLaunch] = useState(false);

  // Close the launch dropdown on any outside click.
  useEffect(() => {
    if (!showLaunch) return;
    const close = () => setShowLaunch(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [showLaunch]);

  const fetchRepo = async () => {
    await apiJSON(`api/repos/${encodeURIComponent(r.name)}/fetch`, "POST", { prune: true });
    onChanged();
  };
  const del = async () => {
    if (!confirm(`ワーキングコピー "${r.name}" を削除しますか？（履歴・リモートはそのまま）`)) return;
    await raw(`api/repos/${encodeURIComponent(r.name)}`, { method: "DELETE" });
    onChanged();
  };

  return (
    <li className={"repo-row" + (active ? " active" : "") + (pinned ? " pinned" : "")}>
      {pinned && <Icon name="pin" className="repo-pin" title="現在のセッションのリポジトリ" />}
      <div className="repo-info">
        <span className={"dot " + (r.dirty ? "dirty" : "clean")} title={r.dirty ? "未コミット変更あり" : "clean"}>
          ●
        </span>
        <button className="link grow repo-name" title={"開く（ファイル + ソース管理）: " + r.path} onClick={onOpen}>
          <Icon name="repo" className="repo-ic" />
          {r.name}
        </button>
        <button
          className="branch"
          type="button"
          onClick={() => setShowBranch(true)}
          title="ブランチ切替"
        >
          <Icon name="git-branch" /> {r.branch || "?"} <Icon name="chevron-down" />
        </button>
        {(r.ahead || r.behind) > 0 && (
          <span className="ab">
            {r.ahead ? `↑${r.ahead}` : ""}
            {r.behind ? `↓${r.behind}` : ""}
          </span>
        )}
      </div>
      <div className="repo-actions">
        <div className="launch-wrap" onMouseDown={(e) => e.stopPropagation()}>
          <button
            className="chip launch"
            title="このディレクトリでセッションを起動（複数可）"
            onClick={() => setShowLaunch((v) => !v)}
          >
            <Icon name="play" /> 起動 <Icon name="chevron-down" />
          </button>
          {showLaunch && (
            <div className="launch-menu">
              {["claude", "opencode", "codex", "shell"].map((k) => (
                <button key={k} onClick={() => { setShowLaunch(false); onLaunch(k); }}>
                  <Icon name={kindIcon(k)} /> {kindLabel(k)}
                </button>
              ))}
            </div>
          )}
        </div>
        <button className="chip" title="git fetch --prune" onClick={fetchRepo}>
          <Icon name="cloud-download" /> fetch
        </button>
        <button className="chip danger" title="ワーキングコピーを削除" onClick={del}>
          <Icon name="trash" />
        </button>
      </div>
      {showBranch && (
        <BranchModal
          repoName={r.name}
          onClose={() => setShowBranch(false)}
          onChecked={() => {
            setShowBranch(false);
            onChanged();
          }}
        />
      )}
    </li>
  );
}
