import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, apiJSON } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import NewRepoModal from "../NewRepoModal.jsx";
import { kindIcon, kindLabel } from "../../lib/sessionkind.js";

const repoSafeSession = (name) =>
  name.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 40) || "repo";

// guessRepoName derives a display name from a clone URL (last path segment minus
// .git) for the in-progress spinner row, before the server reports the real name.
const guessRepoName = (u) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

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
  const { reposKey, bumpRepos, bumpSessions, bumpFiles, revealInFiles, showSCM, showTerminal, showTerminalSplit, scmRepo, mode, session } =
    useApp();
  const [repos, setRepos] = useState([]);
  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState(null); // { name } while a clone runs (left-pane spinner)
  const [activeRepo, setActiveRepo] = useState(null); // repo used by the attached session

  // Run a clone in the background: the modal closed already, so progress shows as a
  // spinner row here until the server finishes, then the repo appears + is revealed.
  const doClone = async ({ remote_url, branch }) => {
    setCloning({ name: guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch });
      if (res && res.error) {
        alert("clone に失敗: " + (res.error.message || res.error));
        return;
      }
      bumpRepos();
      if (res && res.name) revealInFiles("repos/" + res.name);
      else bumpFiles();
    } catch (e) {
      alert("clone に失敗: " + e);
    } finally {
      setCloning(null);
    }
  };

  useEffect(() => {
    let alive = true;
    api("api/repos")
      .then((d) => alive && setRepos(d.repos || []))
      .catch(() => alive && setRepos([]));
    return () => {
      alive = false;
    };
  }, [reposKey]);

  // Resolve which repo the currently-attached session works in, so we can show it
  // selected (highlighted) in place. Refetched when the attached session changes.
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
        // A shell session doesn't "own" a repo for highlighting purposes — only
        // agent sessions (claude/opencode/codex) mark their working repo.
        setActiveRepo(s && s.kind !== "shell" ? s.repo : null);
      })
      .catch(() => alive && setActiveRepo(null));
    return () => {
      alive = false;
    };
  }, [session]);

  return (
    <Section
      title="Repos"
      actions={
        <>
          <button className="ghost" title="clone" disabled={!!cloning} onClick={() => setShowClone((s) => !s)}>
            <Icon name="add" />
          </button>
          <button className="ghost" title="更新" onClick={bumpRepos}>
            <Icon name="refresh" />
          </button>
        </>
      }
    >
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} />}
      <ul className="list">
        {cloning && (
          <li className="repo-row cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {repos.length === 0 && !cloning && <li className="muted">リポジトリなし</li>}
        {repos.map((r) => (
          <RepoRow
            key={r.name}
            r={r}
            active={mode === "scm" && scmRepo === r.name}
            selected={r.name === activeRepo}
            // One click on the repo: reveal it in the Files tree AND open the
            // (renewed) Source Control workbench in the main pane. The separate
            // "変更" chip is gone — the repo row itself is the entry point.
            onOpen={() => {
              revealInFiles("repos/" + r.name);
              showSCM(r.name);
            }}
            // split=true (middle-click) opens the new session in a freshly split
            // pane instead of replacing the active pane's content.
            onLaunch={async (kind, split) => {
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
              (split ? showTerminalSplit : showTerminal)(name);
            }}
          />
        ))}
      </ul>
    </Section>
  );
}

// RepoRow: click the name to open the repo (Files + Source Control in the right
// pane), or 起動 to spawn a session in it. Branch switch / fetch / delete used to
// live here too — they now live in the Source Control header (the right pane), so
// the row stays compact: name + 起動 (where the branch chip used to sit). `active`
// = open in the SCM pane; `selected` = the attached session's repo — both just
// highlight the row in place (no reordering).
function RepoRow({ r, active, selected, onOpen, onLaunch }) {
  const [showLaunch, setShowLaunch] = useState(false);

  // Close the launch dropdown on any outside click.
  useEffect(() => {
    if (!showLaunch) return;
    const close = () => setShowLaunch(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [showLaunch]);

  return (
    <li className={"repo-row" + (active || selected ? " active" : "")}>
      <div className="repo-info">
        <span className={"dot " + (r.dirty ? "dirty" : "clean")} title={r.dirty ? "未コミット変更あり" : "clean"}>
          ●
        </span>
        <button className="link grow repo-name" title={"開く（ファイル + ソース管理）: " + r.path} onClick={onOpen}>
          <Icon name="repo" className="repo-ic" />
          {r.name}
        </button>
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
                <button
                  key={k}
                  title="中クリックで新ペインに起動"
                  onClick={() => { setShowLaunch(false); onLaunch(k); }}
                  // Middle-click launches into a freshly split pane. Suppress the
                  // mousedown default so the browser doesn't start autoscroll.
                  onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                  onAuxClick={(e) => {
                    if (e.button === 1) {
                      e.preventDefault();
                      setShowLaunch(false);
                      onLaunch(k, true);
                    }
                  }}
                >
                  <Icon name={kindIcon(k)} /> {kindLabel(k)}
                </button>
              ))}
            </div>
          )}
        </div>
        {(r.ahead || r.behind) > 0 && (
          <span className="ab">
            {r.ahead ? `↑${r.ahead}` : ""}
            {r.behind ? `↓${r.behind}` : ""}
          </span>
        )}
      </div>
    </li>
  );
}
