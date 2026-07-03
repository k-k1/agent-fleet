import { useEffect, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, apiJSON, errText } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import { useToast } from "../ToastProvider.jsx";
import EmptyState from "../EmptyState.jsx";
import { useDismiss } from "../../lib/useDismiss.js";
import NewRepoModal from "../NewRepoModal.jsx";
import { kindIcon, kindLabel } from "../../lib/sessionkind.js";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import type { MouseEvent as RMouseEvent } from "react";
import type { ConnectionsStatus, Session } from "../../types/session.ts";

// A working copy under ~/repos, from GET /api/repos.
interface Repo {
  name: string;
  path?: string;
  branch?: string;
  dirty?: boolean;
  ahead?: number;
  behind?: number;
}

// guessRepoName derives a display name from a clone URL (last path segment minus
// .git) for the in-progress spinner row, before the server reports the real name.
const guessRepoName = (u: string | null | undefined) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

// Repos: working copies under ~/repos. Clone new ones (provider picker or URL),
// switch branch, fetch, start a session in the repo, delete, or open Source
// Control (→ main area). The dirty dot mirrors uncommitted changes.
export default function ReposSection() {
  const { reposKey, connKey, bumpRepos, bumpSessions, bumpFiles, revealInFiles, showSCM, showSCMSplit, showTerminal, showTerminalSplit, showChat, showChatSplit, scmRepo, mode, session, wsState } =
    useApp();
  const toast = useToast();
  const running = wsState === "running"; // WS down → clone/open/launch are inert
  const [repos, setRepos] = useState<Repo[]>([]);
  const [conns, setConns] = useState<ConnectionsStatus | null>(null); // null = unknown (loading/failed) → show all
  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null); // while a clone runs (left-pane spinner)
  const [activeRepo, setActiveRepo] = useState<string | null>(null); // repo used by the attached session

  // Run a clone in the background: the modal closed already, so progress shows as a
  // spinner row here until the server finishes, then the repo appears + is revealed.
  const doClone = async ({ remote_url, branch, name }: { remote_url: string; branch: string; name: string }) => {
    setCloning({ name: name || guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch, name });
      if (res && res.error) {
        toast("clone に失敗: " + (res.error.message || res.error));
        return;
      }
      bumpRepos();
      if (res && res.name) revealInFiles("repos/" + res.name);
      else bumpFiles();
    } catch (e) {
      toast("clone に失敗: " + e);
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

  // Agent connection state, so the 起動 menu only offers agents the user has set up.
  // Refetched when a connect/disconnect bumps connKey. Only a clean response sets it;
  // on error/unknown we leave it null and show every kind (don't hide on a blip).
  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? d : null))
      .catch(() => alive && setConns(null));
    return () => {
      alive = false;
    };
  }, [connKey]);

  // shell is always available; an agent kind only appears once its connection is set
  // up (Claude/Codex signed in, opencode has at least one API key env). While conns
  // is unknown (null) we show all so a slow/failed probe never hides a valid option.
  // Availability lives on each agent descriptor (src/agents/registry).
  const ready = (k: string) => !conns || agentOf(k).available({ conns });
  const launchKinds = repoLaunchKinds.filter(ready);

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
        const s = (d.sessions || []).find((x: Session) => x.name === session);
        // A shell session doesn't "own" a repo for highlighting purposes — only
        // agent sessions (claude/opencode/codex, i.e. those that run in a dir)
        // mark their working repo.
        setActiveRepo(s && agentOf(s.kind).caps.runsInDir ? s.repo : null);
      })
      .catch(() => alive && setActiveRepo(null));
    return () => {
      alive = false;
    };
  }, [session]);

  return (
    <Section
      id="repos"
      title="Repos"
      icon="repo"
      count={repos.length}
      actions={
        <>
          <button
            className="ghost lblbtn"
            title={running ? "clone" : "clone（ワークスペース停止中）"}
            disabled={!!cloning || !running}
            onClick={() => setShowClone((s) => !s)}
          >
            <Icon name="add" />
            <span className="lbl">クローン</span>
          </button>
          <button className="ghost lblbtn" title="更新" onClick={bumpRepos}>
            <Icon name="refresh" />
            <span className="lbl">更新</span>
          </button>
        </>
      }
    >
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="list">
        {cloning && (
          <li className="repo-row cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {repos.length === 0 && !cloning && (
          <EmptyState
            icon="repo"
            message="リポジトリがありません"
            hint="clone するとここに並びます"
            action={running ? { label: "クローン", icon: "add", onClick: () => setShowClone(true) } : undefined}
          />
        )}
        {repos.map((r) => (
          <RepoRow
            key={r.name}
            r={r}
            kinds={launchKinds}
            running={running}
            active={mode === "scm" && scmRepo === r.name}
            selected={r.name === activeRepo}
            // One click on the repo: reveal it in the Files tree AND open the
            // (renewed) Source Control workbench in the main pane. The separate
            // "変更" chip is gone — the repo row itself is the entry point.
            // Ctrl/Cmd/middle-click opens Source Control in a freshly split pane.
            onOpen={(e) => {
              revealInFiles("repos/" + r.name);
              if (e && (e.ctrlKey || e.metaKey || e.button === 1)) showSCMSplit(r.name);
              else showSCM(r.name);
            }}
            // split=true (middle-click) opens the new session in a freshly split
            // pane instead of replacing the active pane's content.
            onLaunch={async (kind: string, split: boolean) => {
              // The server allocates the session's identity (slug); we only say where
              // (dir) and what kind. Open the created session by its returned slug —
              // claude → chat mirror (live, PTY attached in bg), other kinds → terminal.
              const res = await apiJSON("api/sessions", "POST", { dir: r.path, kind });
              if (res && res.error) {
                toast("起動に失敗: " + errText(res.error));
                return;
              }
              bumpSessions();
              const chat = agentOf(kind).caps.chat;
              (chat
                ? split ? showChatSplit : showChat
                : split ? showTerminalSplit : showTerminal)(res.name);
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
interface RepoRowProps {
  r: Repo;
  kinds?: string[];
  running?: boolean;
  active?: boolean;
  selected?: boolean;
  onOpen: (e: RMouseEvent) => void;
  onLaunch: (kind: string, split: boolean) => void;
}

function RepoRow({ r, kinds = repoLaunchKinds, running = true, active, selected, onOpen, onLaunch }: RepoRowProps) {
  const [showLaunch, setShowLaunch] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);

  // Close the launch dropdown on any outside click. Use a containment check
  // (not stopPropagation on the wrap): stopPropagation would swallow OTHER
  // dropdowns' document-level close listeners, so opening a session ⋯ menu
  // wouldn't close this one — both would stay open, both .pane-section would
  // lift to z-index:10, and the later REPOS section would paint over the
  // SESSIONS menu, blocking its 再開する item.
  useDismiss(wrapRef, showLaunch, () => setShowLaunch(false));

  return (
    <li className={"repo-row" + (active || selected ? " active" : "")}>
      <div className="repo-info">
        <button
          className="link grow repo-name"
          title={running ? "開く（ファイル + ソース管理）: " + r.path + "（Ctrl/中クリックで新ペイン）" : "ワークスペース停止中"}
          disabled={!running}
          onClick={onOpen}
          // Middle-click opens Source Control in a split pane (same as Ctrl+click).
          onMouseDown={(e) => e.button === 1 && e.preventDefault()}
          onAuxClick={(e) => e.button === 1 && onOpen(e)}
        >
          <Icon name="repo" className="repo-ic" />
          {r.name}
        </button>
        {(r.dirty || ((r.ahead || r.behind) ?? 0) > 0) && (
          <span className="repo-state">
            {r.dirty && (
              <span className="repo-state-chip dirty" title="未コミット変更あり">
                <Icon name="circle-filled" /> 未コミット
              </span>
            )}
            {((r.ahead || r.behind) ?? 0) > 0 && (
              <span
                className="repo-state-chip ab"
                title={`リモートに対して 先行 ${r.ahead ?? 0} / 遅延 ${r.behind ?? 0}`}
              >
                {r.ahead ? `↑${r.ahead}` : ""}
                {r.ahead && r.behind ? " " : ""}
                {r.behind ? `↓${r.behind}` : ""}
              </span>
            )}
          </span>
        )}
        <div className="launch-wrap" ref={wrapRef}>
          <button
            className="chip launch"
            title={running ? "このディレクトリでセッションを起動（複数可）" : "ワークスペース停止中"}
            disabled={!running}
            onClick={() => setShowLaunch((v) => !v)}
          >
            <Icon name="play" /> 起動 <Icon name="chevron-down" />
          </button>
          {showLaunch && (
            <div className="launch-menu">
              {kinds.map((k) => (
                <button
                  key={k}
                  title="Ctrl/中クリックで新ペインに起動"
                  // Ctrl/Cmd+click mirrors the middle-click: launch into a split pane.
                  onClick={(e) => { setShowLaunch(false); onLaunch(k, e.ctrlKey || e.metaKey); }}
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
      </div>
    </li>
  );
}
