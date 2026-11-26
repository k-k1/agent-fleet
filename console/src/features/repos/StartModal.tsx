// StartModal — the Start hub: the WS bar's single entry point for starting anything. An
// accordion with exactly one section open (ADR 0142 decision 5): start in a new space (launched
// from here), in a repository (→ the per-repo start-work dialog), or Other (home agent, shell,
// SSM, clone, new folder). One draft is shown in every section. Entry points that already know
// the place (the repo row's launch) skip this hub.
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useEffectiveDefaults } from "../../core/store/uiState.ts";
import type { TeamCloneTarget } from "./tenantLaunchDefaults.ts";
import { teamCloneHost, teamCloneTargets, teamFirst } from "./tenantLaunchDefaults.ts";
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { api, isTransientErr } from "../../core/api/client.ts";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useKindCatalogVersion } from "../../agents/kindCatalog.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { groupedRepos } from "../../lib/project.ts";
import { useReposStore } from "./store.ts";
import type { Repo } from "./store.ts";
import { CloneForm } from "./CloneForm.tsx";
import type { CloneSource } from "./CloneForm.tsx";
import { NewFolderForm, newFolderNameOk } from "./NewFolderForm.tsx";
import { cloneRepo, initRepo } from "./clone.ts";
import type { ConnectionsStatus } from "../../types/session.ts";
import { useStartWork } from "./useStartWork.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal } from "../sessions/open.ts";
import { SsmStage } from "./SsmStage.tsx";
import type { SsmProfile } from "./SsmStage.tsx";
import { SsmLoginModal } from "../sessions/SsmLoginModal.tsx";
import { deriveRepoName } from "./reponame.ts";
import { useFoldValue } from "../../lib/useFold.ts";
import { HubComposer } from "./HubComposer.tsx";
import { SpaceLaunchForm } from "./LaunchModal.tsx";
import { spacesOwner, useSpacesStore } from "../spaces/store.ts";
import type { LaunchAccess, LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import type { SharedDraft } from "./hubDraft.ts";

interface StartModalProps {
  /** Connection-gated coding-agent kinds (claude / codex / agy / opencode). */
  kinds: string[];
  /** The credential policy as the member sees it, for the space launch (as LaunchModal's). */
  access: LaunchAccess;
  claudeExpired?: boolean;
  settling?: boolean;
  /** The hub's one draft, shown in every section. */
  draft: SharedDraft;
  /** Starts a session in a new space from the space section. */
  onLaunchSpace: (o: LaunchOpts) => Promise<LaunchResult>;
  onClose: () => void;
  /** A working copy was picked (existing or freshly cloned) — the host closes
   * this hub and opens the per-repo start-work dialog on it. */
  onPickRepo: (r: Repo, fromRepoSection: boolean) => void;
  /** A pick is waiting on a memo restore: nothing else may start meanwhile. */
  pickPending?: boolean;
  /** "Launch an agent at home" was picked — the host opens the start-work dialog in its
   * repo-less (home) form on top of this hub. */
  onPickHome: () => void;
  /** A launch seeded by a session that runs in a space opens on that space (its folder) with the
   *  title the seed proposed (the title only rides along with a space; "" opens as usual). */
  seedSpace?: string;
  seedTitle?: string;
}

type Stage = "place" | "clone" | "newdir" | "ssm";

type Section = "space" | "repo" | "other";
/** The open section, per user and tenant like the rail's folds; space the very first time. */
const SECTION_KEY = "af-start-section";

/** Whether this workspace's Agent can start a session in a space: an older one ignores the
 *  field and would start in home instead (#4978), so the hub asks GET /api/spaces first. */
type SpaceSupport = "checking" | "yes" | "no" | "failed";

function HubSection({ open, icon, label, disabled, onOpen, children }: { open: boolean; icon: string; label: string; disabled: boolean; onOpen: () => void; children: ReactNode }) {
  return (
    <section className={"start-sec" + (open ? " start-sec--open" : "")}>
      <button type="button" className="start-sec-head" aria-expanded={open} disabled={disabled && !open} onClick={onOpen}>
        <Icon name={open ? "chevron-down" : "chevron-right"} className="start-sec-chev" />
        <Icon name={icon} className="start-row-ic" />
        <span className="start-sec-label">{label}</span>
      </button>
      {open && <div className="start-sec-body">{children}</div>}
    </section>
  );
}

export function StartModal({ kinds, access, claudeExpired, settling, draft, onLaunchSpace, onClose, onPickRepo, onPickHome, pickPending = false, seedSpace = "", seedTitle = "" }: StartModalProps) {
  useKindCatalogVersion(); // effort / start-mode rows and the driver sent depend on the catalog's facts
  const toast = useToast();
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const startWork = useStartWork();

  // The Terminal view opens the hub on the SSM host picker; every other way in starts at the places.
  const [stage, setStage] = useState<Stage>(() => useSessionsStore.getState().startStage ?? "place");
  const [busy, setBusy] = useState(false);
  // After creating a kind=ssm session: the created name while the SSO handshake runs.
  const [ssmLogin, setSsmLogin] = useState<string | null>(null);

  // --- place: the accordion (space / repository / other) ---
  const [storedSection, setSection] = useFoldValue(SECTION_KEY);
  // A space seeded by the launch (a handoff from a space session) opens the space section whatever
  // the remembered one is, until the member opens another; that choice is not written over theirs.
  const [pinSpace, setPinSpace] = useState(!!seedSpace);
  const section: Section = pinSpace ? "space" : storedSection === "repo" || storedSection === "other" ? storedSection : "space";
  const openSection = (k: Section) => {
    setPinSpace(false);
    setSection(k);
  };
  const [repoQuery, setRepoQuery] = useState("");
  const [ssmProfiles, setSsmProfiles] = useState<SsmProfile[] | null>(null);
  const [spaceSupport, setSpaceSupport] = useState<SpaceSupport>("checking");
  const [spaceProbe, setSpaceProbe] = useState(0);
  // A switch of workspace makes the answer for the old one meaningless: ask again.
  const spacesFrom = spacesOwner();
  useEffect(() => {
    let alive = true;
    setSpaceSupport("checking");
    const ticket = useSpacesStore.getState().begin();
    api("api/spaces")
      .then((d) => {
        if (!alive) return;
        if (d && !d.error && Array.isArray(d.spaces)) {
          setSpaceSupport("yes");
          // The same answer is the place chip's list: the hub asks the Agent once, and "Check
          // again" refreshes both.
          // Superseded meanwhile (a switch of workspace clears the store after this request began,
          // a later refresh started): the list is read again rather than left empty under a
          // support answer that says "yes".
          if (!useSpacesStore.getState().adopt(ticket, d)) void useSpacesStore.getState().refresh();
        }
        // Only "no such route" means an Agent without spaces; anything else may pass. A plain-text
        // 404 / 405 (the Agent's mux) arrives as http_404 / http_405 with no status.
        else if (d?.error && !isTransientErr(d) && (d.error.status === 404 || d.error.status === 405 || d.error.code === "http_404" || d.error.code === "http_405")) setSpaceSupport("no");
        else setSpaceSupport("failed");
      })
      .catch(() => alive && setSpaceSupport("failed"));
    return () => {
      alive = false;
    };
  }, [spaceProbe, spacesFrom]);
  // The composer's send key launches the space form below it.
  const spaceSubmit = useRef<(() => void) | null>(null);
  useEffect(() => {
    let alive = true;
    api("api/ssm/profiles")
      .then((rows) => alive && setSsmProfiles(Array.isArray(rows) ? rows : []))
      .catch(() => alive && setSsmProfiles([]));
    return () => {
      alive = false;
    };
  }, []);
  // Base clones only — worktrees are task copies, launched from their tree rows.
  const bases = groupedRepos(repos).map((g) => g[0]);
  // Team repositories first (ADR 0114 P3): the CP's last-known answer, so they lead even while the
  // workspace is stopped and the list is the cached one.
  const eff = useEffectiveDefaults();
  const { rows: orderedBases, team: teamBases } = teamFirst(bases, eff?.repos);
  const visibleBases = orderedBases.filter((r) => {
    const q = repoQuery.trim().toLocaleLowerCase();
    return !q || r.name.toLocaleLowerCase().includes(q) || (r.branch || "").toLocaleLowerCase().includes(q);
  });
  // Team repositories not cloned yet, as shortcuts into the same clone flow. The host comes from
  // the member's connected Git provider, so they wait for a fresh api/connections answer.
  // Only a fresh answer counts: a cached one may predate a connect / disconnect, and the host
  // decides what gets cloned.
  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  const reposLoaded = useReposStore((s) => s.loadState) === "ok";
  useEffect(() => {
    if (!eff?.repos?.length) return;
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? (d as ConnectionsStatus) : null))
      .catch(() => alive && setConns(null));
    return () => {
      alive = false;
    };
  }, [eff?.repos]);
  const teamClones = (reposLoaded ? teamCloneTargets(bases, eff?.repos, conns) : []).filter((c) => {
    const q = repoQuery.trim().toLocaleLowerCase();
    return !q || c.path.toLocaleLowerCase().includes(q);
  });

  const baseRow = (r: Repo) => (
    <button key={r.name} type="button" className="start-row" disabled={pickPending} onClick={() => onPickRepo(r, true)}>
      <Icon name="repo" className="start-row-ic" />
      <span className="start-row-body">
        <span className="start-row-title">{r.name}</span>
        <span className="start-row-desc">
          {teamBases.has(r) ? tr("start.team_repo") + " · " : ""}
          {r.branch || ""}
          {r.dirty ? tr("start.dirty_suffix") : ""}
        </span>
      </span>
      <Icon name="chevron-right" className="start-row-chev" />
    </button>
  );

  const startShell = async () => {
    if (busy) return;
    setBusy(true);
    const r = await startWork({ dir: "", repo: "" }, { kind: "shell", driver: "", model: "", effort: "", startMode: "normal", prompt: "", title: "", images: [], worktree: false, subdir: "", base: "", newBranch: "" });
    setBusy(false);
    if (r.ok) onClose();
  };

  // --- clone: shared CloneForm; forking/folder naming stays in the worktree stage ---
  const [src, setSrc] = useState<CloneSource>({ cloneUrl: "", branch: "" });
  const derived = src.cloneUrl ? deriveRepoName(src.cloneUrl) : "";
  const already = derived ? repos.find((r) => r.name === derived) : undefined;
  // fromRepoSection: a team repository's shortcut in the repository section, which takes the draft
  // like any repository row there; the clone stage is Other's and does not.
  const doClone = async (url = src.cloneUrl, branch = src.branch, fromRepoSection = false) => {
    if (!url || busy) return;
    setBusy(true);
    const res = await cloneRepo({ remote_url: url, branch, name: "" }, toast);
    setBusy(false);
    if (!res.ok) return; // stay here; the toast said why
    const repo = useReposStore.getState().repos.find((r) => r.name === res.name);
    if (repo) {
      setStage("place"); // the hub stays mounted below the launch stage — leave it reset
      onPickRepo(repo, fromRepoSection);
    } else onClose(); // clone landed but the fresh list hasn't caught up — the tree has it
  };

  // Re-reads the connections right before cloning: the host was inferred from them, so a
  // provider connected since the list was drawn must stop the clone instead of redirecting it.
  // `busy` is state and still reads false in a second click's closure, so the lock is a ref taken
  // before the first await; without it two clicks start two clone jobs for one repository.
  const teamCloneLock = useRef(false);
  const cloneTeam = async (c: TeamCloneTarget) => {
    if (busy || pickPending || teamCloneLock.current) return;
    teamCloneLock.current = true;
    setBusy(true);
    try {
      const d = await api("api/connections").catch(() => null);
      if (!d || d.error || teamCloneHost(d as ConnectionsStatus) !== c.host) {
        setConns(d && !d.error ? (d as ConnectionsStatus) : null);
        toast(tr("start.team_clone_host_changed"), { kind: "error" });
        return;
      }
      await doClone(c.cloneUrl, "", true);
    } finally {
      setBusy(false);
      teamCloneLock.current = false;
    }
  };

  // --- newdir: start with no import source (create ~/repos/<name> and `git init`) ---
  // Feeds the same junction as a clone: the working copy just created is handed straight to
  // the start-work dialog.
  const [newName, setNewName] = useState("");
  const takenNames = new Set(repos.map((r) => r.name));
  const doInit = async () => {
    if (busy || !newFolderNameOk(newName, takenNames)) return;
    setBusy(true);
    const res = await initRepo(newName.trim(), toast);
    setBusy(false);
    if (!res.ok) return; // stay here; the toast said why
    // With a working set selected, put the new working copy into it (docs/log/52 §1).
    autoAddToActiveWorkingSet("repos", res.name);
    const repo = useReposStore.getState().repos.find((r) => r.name === res.name);
    if (repo) {
      setStage("place"); // the hub stays mounted below — leave it reset
      onPickRepo(repo, false);
    } else onClose(); // created, but the list hasn't caught up — the tree has it
  };

  // SSO handshake takes over the dialog (modal swap — safe since backClose
  // suppresses its own history echoes).
  if (ssmLogin != null) {
    return (
      <SsmLoginModal
        name={ssmLogin}
        onReady={(n) => {
          void refreshSessions();
          openSessionTerminal(n);
          onClose();
        }}
        onCancel={onClose}
      />
    );
  }

  return (
    <Modal
      title={
        <>
          <Icon name="rocket" /> {tr("start.begin")}
        </>
      }
      onClose={onClose}
      lockClose={busy}
    >
      {stage === "place" && (
        <div className="ui-modal-body start-hub">
          <HubSection open={section === "space"} disabled={pickPending} icon="folder-library" label={tr("start.sec_space")} onOpen={() => openSection("space")}>
            <HubComposer draft={draft} busy={busy} onSubmit={() => spaceSubmit.current?.()} />
            {spaceSupport !== "yes" && (
              <span className={"ui-field-hint" + (spaceSupport === "checking" ? "" : " ui-field-hint--warn")} role="status">
                {tr(spaceSupport === "checking" ? "start.space_checking" : spaceSupport === "no" ? "start.space_unsupported" : "start.space_check_failed")}
                {spaceSupport === "failed" && (
                  <>
                    {" "}
                    <button type="button" className="linklike" onClick={() => setSpaceProbe((n) => n + 1)}>
                      {tr("start.space_check_again")}
                    </button>
                  </>
                )}
              </span>
            )}
            <SpaceLaunchForm
              kinds={kinds}
              access={access}
              claudeExpired={claudeExpired}
              settling={settling}
              shared={draft}
              busy={busy}
              setBusy={setBusy}
              launchBlocked={spaceSupport !== "yes" || pickPending}
              submitRef={spaceSubmit}
              initialSpaceFolder={seedSpace || undefined}
              initialTitle={(seedSpace && seedTitle) || undefined}
              onClose={onClose}
              onLaunch={onLaunchSpace}
            />
          </HubSection>
          <HubSection open={section === "repo"} disabled={pickPending} icon="repo" label={tr("start.sec_repo")} onOpen={() => openSection("repo")}>
            <HubComposer draft={draft} busy={busy} note={tr("start.draft_note_repo")} />
            <div className="ui-field start-repos">
              <label className="ui-field-label" htmlFor="start-repo-search">{tr("start.repo_launch_label")}</label>
              <input {...NO_AUTOFILL}
                id="start-repo-search"
                type="search"
                value={repoQuery}
                onChange={(e) => setRepoQuery(e.target.value)}
                placeholder={tr("start.repo_search_ph")}
              />
              <div className="start-list start-repo-list">
                {visibleBases.filter((r) => teamBases.has(r)).map(baseRow)}
                {teamClones.map((c) => (
                  <button key={c.path} type="button" className="start-row" disabled={busy || pickPending} onClick={() => void cloneTeam(c)}>
                    <Icon name="add" className="start-row-ic" />
                    <span className="start-row-body">
                      <span className="start-row-title">{c.path}</span>
                      <span className="start-row-desc">{tr("start.team_repo")} · {tr("start.team_clone_desc", { host: c.host })}</span>
                    </span>
                  </button>
                ))}
                {visibleBases.filter((r) => !teamBases.has(r)).map(baseRow)}
                {visibleBases.length === 0 && teamClones.length === 0 && <span className="start-empty">{tr("start.no_repos")}</span>}
              </div>
            </div>
          </HubSection>
          <HubSection open={section === "other"} disabled={pickPending} icon="ellipsis" label={tr("start.sec_other")} onOpen={() => openSection("other")}>
            <HubComposer draft={draft} busy={busy} note={tr("start.draft_note_other")} />
            <div className="start-list">
              {kinds.length > 0 && (
                <button type="button" className="start-row" onClick={onPickHome}>
                  <Icon name="home" className="start-row-ic" />
                  <span className="start-row-body">
                    <span className="start-row-title">{tr("start.home_title")}</span>
                    <span className="start-row-desc">{tr("start.home_desc")}</span>
                  </span>
                  <Icon name="chevron-right" className="start-row-chev" />
                </button>
              )}
              <button type="button" className="start-row" disabled={busy} onClick={() => void startShell()}>
                <Icon name="terminal" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">shell</span>
                  <span className="start-row-desc">{tr("start.shell_desc")}</span>
                </span>
              </button>
              {ssmProfiles != null && ssmProfiles.length > 0 && (
                <button type="button" className="start-row" onClick={() => setStage("ssm")}>
                  <Icon name="vm" className="start-row-ic" />
                  <span className="start-row-body">
                    <span className="start-row-title">{tr("start.ssm_title")}</span>
                    <span className="start-row-desc">{tr("start.ssm_desc")}</span>
                  </span>
                  <Icon name="chevron-right" className="start-row-chev" />
                </button>
              )}
              <button type="button" className="start-row action" onClick={() => setStage("clone")}>
                <Icon name="add" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">{tr("start.clone_title")}</span>
                </span>
              </button>
              {/* Starting with no import source: unlike a home launch this HAS a place, so from
                  here on it is the unit for a left-pane row, worktrees, diffs and sharing. */}
              <button type="button" className="start-row action" onClick={() => setStage("newdir")}>
                <Icon name="new-folder" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">{tr("start.newdir_title")}</span>
                  <span className="start-row-desc">{tr("start.newdir_desc")}</span>
                </span>
              </button>
            </div>
          </HubSection>
        </div>
      )}

      {stage === "clone" && (
        <>
          <div className="ui-modal-body">
            <CloneForm onChange={setSrc} />
            {already && (
              <div className="ui-field">
                <span className="ui-field-hint">
                  {tr("start.already_cloned", { name: already.name })}
                </span>
                <Button
                  small
                  icon="repo"
                  onClick={() => {
                    setStage("place");
                    onPickRepo(already, false);
                  }}
                >
                  {tr("start.begin_from_existing", { name: already.name })}
                </Button>
              </div>
            )}
            <span className="ui-field-hint">
              {tr("start.new_branch_note")}
            </span>
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={() => setStage("place")} disabled={busy}>
              {tr("launch.back_to_hub")}
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              {tr("common.cancel")}
            </Button>
            <Button variant="primary" onClick={() => void doClone()} disabled={!src.cloneUrl || !!already || busy}>
              {busy ? tr("start.cloning") : tr("start.clone_continue")}
            </Button>
          </footer>
        </>
      )}

      {stage === "newdir" && (
        <>
          <div className="ui-modal-body">
            <NewFolderForm value={newName} onChange={setNewName} taken={takenNames} onSubmit={() => void doInit()} autoFocus />
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={() => setStage("place")} disabled={busy}>
              {tr("launch.back_to_hub")}
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              {tr("common.cancel")}
            </Button>
            <Button variant="primary" onClick={() => void doInit()} disabled={busy || !newFolderNameOk(newName, takenNames)}>
              {busy ? tr("start.creating_folder") : tr("start.create_and_continue")}
            </Button>
          </footer>
        </>
      )}

      {stage === "ssm" && (
        <SsmStage busy={busy} setBusy={setBusy} ssmProfiles={ssmProfiles} onStarted={setSsmLogin} layout="modal" onBack={() => setStage("place")} onClose={onClose} />
      )}
    </Modal>
  );
}
