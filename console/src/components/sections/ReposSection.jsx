import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, apiJSON, raw } from "../../api.js";
import Section from "../Section.jsx";
import NewRepoModal from "../NewRepoModal.jsx";

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
  const { reposKey, bumpRepos, bumpSessions, showSCM, showTerminal, scmRepo, mode } = useApp();
  const [repos, setRepos] = useState([]);
  const [showClone, setShowClone] = useState(false);

  useEffect(() => {
    let alive = true;
    api("api/repos")
      .then((d) => alive && setRepos(d.repos || []))
      .catch(() => alive && setRepos([]));
    return () => {
      alive = false;
    };
  }, [reposKey]);

  return (
    <Section
      title="Repos"
      actions={
        <>
          <button className="ghost" title="clone" onClick={() => setShowClone((s) => !s)}>
            ＋
          </button>
          <button className="ghost" title="更新" onClick={bumpRepos}>
            ⟳
          </button>
        </>
      }
    >
      {showClone && (
        <NewRepoModal
          onClose={() => setShowClone(false)}
          onCloned={() => {
            bumpRepos();
            setShowClone(false);
          }}
        />
      )}
      <ul className="list">
        {repos.length === 0 && <li className="muted">リポジトリなし</li>}
        {repos.map((r) => (
          <RepoRow
            key={r.name}
            r={r}
            active={mode === "scm" && scmRepo === r.name}
            onSCM={() => showSCM(r.name)}
            onLaunch={async (kind) => {
              const base = repoSafeSession(r.name) + (kind === "shell" ? "-sh" : "");
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

function RepoRow({ r, active, onSCM, onLaunch, onChanged }) {
  const [branches, setBranches] = useState(null);

  const loadBranches = async () => {
    if (branches) return;
    try {
      const b = await api(`api/repos/${encodeURIComponent(r.name)}/branches`);
      setBranches(b.local || []);
    } catch {}
  };
  const checkout = async (br) => {
    await apiJSON(`api/repos/${encodeURIComponent(r.name)}/checkout`, "POST", { branch: br });
    onChanged();
  };
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
    <li className={"repo-row" + (active ? " active" : "")}>
      <div className="repo-info">
        <span className={"dot " + (r.dirty ? "dirty" : "clean")} title={r.dirty ? "未コミット変更あり" : "clean"}>
          ●
        </span>
        <button className="link grow repo-name" title={r.path} onClick={onSCM}>
          <span className="repo-ic">📁</span>
          {r.name}
        </button>
        <select
          className="branch"
          value={r.branch || "?"}
          onMouseDown={loadBranches}
          onChange={(e) => checkout(e.target.value)}
          title="ブランチ切替"
        >
          {branches ? (
            branches.map((b) => <option key={b} value={b}>{b}</option>)
          ) : (
            <option>{r.branch || "?"}</option>
          )}
        </select>
        {(r.ahead || r.behind) > 0 && (
          <span className="ab">
            {r.ahead ? `↑${r.ahead}` : ""}
            {r.behind ? `↓${r.behind}` : ""}
          </span>
        )}
      </div>
      <div className="repo-actions">
        <button className="chip launch" title="このディレクトリで claude セッションを起動（複数可）" onClick={() => onLaunch("claude")}>
          ▶ claude
        </button>
        <button className="chip launch" title="このディレクトリで shell を起動" onClick={() => onLaunch("shell")}>
          ▶ shell
        </button>
        <button className="chip" title="ソース管理（変更 / diff / 履歴）" onClick={onSCM}>
          ⎇ 変更
        </button>
        <button className="chip" title="git fetch --prune" onClick={fetchRepo}>
          ⤓ fetch
        </button>
        <button className="chip danger" title="ワーキングコピーを削除" onClick={del}>
          🗑
        </button>
      </div>
    </li>
  );
}
