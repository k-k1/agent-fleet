// StartModal — the Start hub: the WS bar's single entry point for starting anything. An
// accordion with exactly one section open (ADR 0142 decision 5): start in a new space (launched
// from here), in a repository (→ the per-repo start-work dialog), or Other (home agent, shell,
// SSM, clone, new folder). One draft is shown in every section. Entry points that already know
// the place (the repo row's launch) skip this hub.
import { useEffectiveDefaults } from "../../core/store/uiState.ts";
import type { TeamCloneTarget } from "./tenantLaunchDefaults.ts";
import { teamCloneHost, teamCloneTargets, teamFirst } from "./tenantLaunchDefaults.ts";
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, errText, isTransientErr } from "../../core/api/client.ts";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useKindCatalogVersion } from "../../agents/kindCatalog.ts";
import { useSettings, setSetting } from "../../lib/settings.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { groupedRepos } from "../../lib/project.ts";
import { hostColorBase } from "../../lib/termcolor.ts";
import { useSettingsUI } from "../settings/store.ts";
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
import { SsmLoginModal } from "../sessions/SsmLoginModal.tsx";
import type { SsmHost } from "../../types/session.ts";
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

interface SsmProfile {
  id: string;
  label: string;
  /** SSO account id. An attribute of the PROFILE, not of the host (it lives on the
   * control-plane's ssmProfileDTO). The host cards' subtitle reads it from here too — see
   * ssmAcctLabel below. */
  accountId: string;
}

interface SsmInstance {
  instanceId: string;
  name?: string;
  computerName?: string;
  ipAddress?: string;
  platformName?: string;
  pingStatus: string;
}

export function StartModal({ kinds, access, claudeExpired, settling, draft, onLaunchSpace, onClose, onPickRepo, onPickHome, pickPending = false }: StartModalProps) {
  useKindCatalogVersion(); // effort / start-mode rows and the driver sent depend on the catalog's facts
  const toast = useToast();
  const tr = useT();
  const settings = useSettings();
  const repos = useReposStore((s) => s.repos);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const startWork = useStartWork();

  // The Terminal view opens the hub on the SSM host picker; every other way in starts at the places.
  const [stage, setStage] = useState<Stage>(() => useSessionsStore.getState().startStage ?? "place");
  const [busy, setBusy] = useState(false);

  // --- place: the accordion (space / repository / other) ---
  const [storedSection, setSection] = useFoldValue(SECTION_KEY);
  const section: Section = storedSection === "repo" || storedSection === "other" ? storedSection : "space";
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
          useSpacesStore.getState().adopt(ticket, d);
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

  // --- ssm: host picker + SSO handshake (the SSM side of NewSessionModal, moved here in Ph3) ---
  const [ssmHosts, setSsmHosts] = useState<SsmHost[] | null>(null); // null = not fetched yet
  const [ssmHostId, setSsmHostId] = useState("");
  const [ssmQuery, setSsmQuery] = useState("");
  const [ssmProfileId, setSsmProfileId] = useState("");
  const [ssmInstances, setSsmInstances] = useState<SsmInstance[] | null>(null);
  const [ssmInstanceQuery, setSsmInstanceQuery] = useState("");
  const [ssmSearching, setSsmSearching] = useState(false);
  const [ssmSearchError, setSsmSearchError] = useState("");
  const [ssmForce, setSsmForce] = useState(false);
  // After creating a kind=ssm session: the created name while the SSO handshake runs.
  const [ssmLogin, setSsmLogin] = useState<string | null>(null);
  useEffect(() => {
    if (stage !== "ssm" || ssmHosts !== null) return;
    let alive = true;
    api("api/ssm/hosts")
      .then((hosts) => alive && setSsmHosts(Array.isArray(hosts) ? hosts : []))
      .catch(() => alive && setSsmHosts([]));
    return () => {
      alive = false;
    };
  }, [stage, ssmHosts]);
  const visibleSsmHosts = (ssmHosts || []).filter((h) => {
    const q = ssmQuery.trim().toLocaleLowerCase();
    return !q || h.alias.toLocaleLowerCase().includes(q) || h.instanceId.toLocaleLowerCase().includes(q);
  });
  // Quick-connect cards. Rank the (query-filtered) hosts by usage frequency, tie-broken by
  // most-recent use. When few hosts are registered (≤ SSM_CARD_ALL_MAX) show them ALL as
  // cards and drop the dropdown; otherwise surface only the top SSM_CARD_TOP as cards and
  // keep the dropdown for the long tail.
  const SSM_CARD_ALL_MAX = 8;
  const SSM_CARD_TOP = 6;
  // The profile a host refers to. Both the label and the account id are attributes of the
  // PROFILE, not of the host, so both are read from this one place. Reading `h.accountId`
  // instead always yields undefined — ssmHostDTO does not expose accountId — and the account
  // part of the card subtitle then never renders, silently, because the field is optional and
  // the type check stays quiet. Leave the wire alone and ride on this path, which already
  // resolves the profile.
  const ssmProfileOf = (pid: string) => ssmProfiles?.find((p) => p.id === pid);
  const ssmProfileLabel = (pid: string) => ssmProfileOf(pid)?.label || "";
  // While the profiles are unfetched (ssmProfiles === null) this falls back to empty, the same
  // degradation the label already has.
  const ssmAcctLabel = (pid: string) => {
    const acct = ssmProfileOf(pid)?.accountId;
    return acct ? tr("start.ssm_acct", { id: acct }) : "";
  };
  const ssmCardSub = (h: SsmHost) =>
    [ssmProfileLabel(h.profileId), ssmAcctLabel(h.profileId), h.instanceId].filter(Boolean).join(" · ");
  const ssmAllAsCards = (ssmHosts?.length || 0) <= SSM_CARD_ALL_MAX;
  const rankedSsmHosts = [...visibleSsmHosts].sort((a, b) => {
    const ua = settings.ssmHostUsage?.[a.id];
    const ub = settings.ssmHostUsage?.[b.id];
    const ca = ua?.count || 0;
    const cb = ub?.count || 0;
    if (cb !== ca) return cb - ca;
    return (ub?.at || 0) - (ua?.at || 0);
  });
  const ssmCardHosts = ssmAllAsCards ? rankedSsmHosts : rankedSsmHosts.slice(0, SSM_CARD_TOP);
  const searchSsmInstances = async () => {
    const profileId = ssmProfileId || ssmProfiles?.[0]?.id || "";
    if (!profileId || ssmSearching) return;
    setSsmSearchError("");
    setSsmSearching(true);
    const res = await apiJSON("api/ssm/instances", "POST", { profileId });
    setSsmSearching(false);
    if (res?.error) {
      const message = errText(res.error);
      setSsmSearchError(message);
      if (res.error.code !== "ssm_search_forbidden") toast(t("start.aws_search_failed", { msg: message }));
      return;
    }
    setSsmInstances(Array.isArray(res?.instances) ? res.instances : []);
  };
  const visibleSsmInstances = (ssmInstances || []).filter((instance) => {
    const q = ssmInstanceQuery.trim().toLocaleLowerCase();
    return !q || [instance.name, instance.instanceId, instance.computerName, instance.ipAddress]
      .some((value) => value?.toLocaleLowerCase().includes(q));
  });
  const registerSsmInstance = async (instance: SsmInstance) => {
    const profileId = ssmProfileId || ssmProfiles?.[0]?.id || "";
    if (!profileId || busy) return;
    setBusy(true);
    const res = await apiJSON("api/ssm/hosts", "POST", {
      alias: instance.name || instance.computerName || instance.instanceId,
      profileId,
      region: "",
      instanceId: instance.instanceId,
      documentName: "",
    });
    setBusy(false);
    if (res?.error) {
      toast(t("start.host_register_failed", { msg: errText(res.error) }));
      return;
    }
    setSsmHosts((cur) => [...(cur || []), res as SsmHost]);
    setSsmHostId(res.id);
    toast(t("start.host_registered", { id: instance.instanceId }));
  };
  // Start an SSM session. hostId lets a quick-connect card launch its host directly
  // (no setState round-trip); omitted → the dropdown selection (ssmHostId).
  const startSsm = async (hostId?: string) => {
    const id = hostId || ssmHostId;
    if (!id || busy) return;
    setBusy(true);
    const res = await apiJSON("api/sessions", "POST", {
      kind: "ssm",
      ssm_host_id: id,
      ssm_force_login: ssmForce,
      color: hostColorBase(settings.ssmHostColors?.[id], id),
    });
    setBusy(false);
    if (res && res.error) {
      toast(t("start.create_failed", { msg: errText(res.error) }));
      return;
    }
    // Tally usage so the quick-connect cards rank by frequency (recency breaks ties).
    const prev = settings.ssmHostUsage?.[id];
    setSetting("ssmHostUsage", { ...(settings.ssmHostUsage || {}), [id]: { count: (prev?.count || 0) + 1, at: Date.now() } });
    if (res?.name) autoAddToActiveWorkingSet("sessions", res.name); // docs/log/52 §1: repo-less session
    setSsmLogin((res && res.name) || "");
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
          <HubSection open={section === "space"} disabled={pickPending} icon="folder-library" label={tr("start.sec_space")} onOpen={() => setSection("space")}>
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
              onClose={onClose}
              onLaunch={onLaunchSpace}
            />
          </HubSection>
          <HubSection open={section === "repo"} disabled={pickPending} icon="repo" label={tr("start.sec_repo")} onOpen={() => setSection("repo")}>
            <HubComposer draft={draft} busy={busy} note={tr("start.draft_note_repo")} />
            <div className="ui-field start-repos">
              <label className="ui-field-label" htmlFor="start-repo-search">{tr("start.repo_launch_label")}</label>
              <input
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
          <HubSection open={section === "other"} disabled={pickPending} icon="ellipsis" label={tr("start.sec_other")} onOpen={() => setSection("other")}>
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
        <>
          <div className="ui-modal-body">
            <div className="ui-field">
              <span className="ui-field-label">{tr("start.ssm_host_label")}</span>
              {ssmHosts === null ? (
                <p className="sm-muted">{tr("chat.ph_loading")}</p>
              ) : ssmHosts.length === 0 ? (
                <span className="ui-field-hint">
                  {tr("start.ssm_no_hosts")}
                  <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
                    {tr("start.settings_ssm")}
                  </button>
                  {tr("start.register_there")}
                </span>
              ) : (
                <>
                  <input
                    type="search"
                    value={ssmQuery}
                    onChange={(e) => setSsmQuery(e.target.value)}
                    placeholder={tr("start.ssm_search_ph")}
                  />
                  {!ssmAllAsCards && ssmCardHosts.length > 0 && (
                    <span className="ui-field-hint">{tr("start.frequent_hosts")}</span>
                  )}
                  {ssmCardHosts.length > 0 && (
                    <div className="ssm-card-grid">
                      {ssmCardHosts.map((h) => (
                        <button
                          type="button"
                          key={h.id}
                          className="ssm-card"
                          disabled={busy}
                          title={tr("start.quick_connect")}
                          onClick={() => void startSsm(h.id)}
                        >
                          <span
                            className="ssm-card-dot"
                            style={{ background: hostColorBase(settings.ssmHostColors?.[h.id], h.id) }}
                            aria-hidden="true"
                          />
                          <span className="ssm-card-body">
                            <span className="ssm-card-alias">{h.alias}</span>
                            <span className="ssm-card-sub">{ssmCardSub(h)}</span>
                          </span>
                        </button>
                      ))}
                    </div>
                  )}
                  {!ssmAllAsCards && (
                    <select value={ssmHostId} onChange={(e) => setSsmHostId(e.target.value)}>
                      <option value="">{tr("start.select_host")}</option>
                      {visibleSsmHosts.map((h) => {
                        const acct = ssmAcctLabel(h.profileId); // same path as the card subtitle (the profile)
                        return (
                          <option key={h.id} value={h.id}>
                            {h.alias} — {h.instanceId}
                            {acct ? ` (${acct})` : ""}
                          </option>
                        );
                      })}
                    </select>
                  )}
                  {visibleSsmHosts.length === 0 && <span className="ui-field-hint">{tr("start.no_matching_hosts")}</span>}
                  <label className="ssm-check">
                    <input type="checkbox" checked={ssmForce} onChange={(e) => setSsmForce(e.target.checked)} />
                    {tr("start.force_relogin")}
                  </label>
                  {/* Two paragraphs, not one <br>-joined block: the warning has to read as
                      its own line rather than as the tail of the wrapped explanation. */}
                  <span className="ui-field-hint">
                    <Trans k="start.ssm_auth_note" components={[<code />]} />
                  </span>
                  <span className="ui-field-hint ui-field-hint--warn">
                    <Trans k="start.ssm_auth_warn" components={[<b />]} />
                  </span>
                </>
              )}
            </div>
            <div className="ui-field">
              <span className="ui-field-label">{tr("start.aws_instances_label")}</span>
              {ssmProfiles && ssmProfiles.length > 1 && (
                // The initial "" matches no option (there is no placeholder), leaving the display
                // undefined. Search/register fall back to profiles[0], so the display is pinned to
                // that same first entry.
                <select value={ssmProfileId || ssmProfiles[0].id} onChange={(e) => setSsmProfileId(e.target.value)}>
                  {ssmProfiles.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
                </select>
              )}
              <Button small icon="search" onClick={() => void searchSsmInstances()} disabled={ssmSearching}>
                {ssmSearching ? tr("start.searching") : tr("start.search_aws")}
              </Button>
              {ssmSearchError && <span role="alert" className="start-search-error">{ssmSearchError}</span>}
              {ssmInstances !== null && ssmInstances.length > 0 && (
                <input
                  type="search"
                  value={ssmInstanceQuery}
                  onChange={(e) => setSsmInstanceQuery(e.target.value)}
                  placeholder={tr("start.instance_filter_ph")}
                />
              )}
              {visibleSsmInstances.map((instance) => (
                <div key={instance.instanceId} className="ssm-instance-row">
                  <span className="start-row-body">
                    <span className="start-row-title">{instance.name || instance.computerName || instance.instanceId}</span>
                    <span className="start-row-desc">
                      {instance.instanceId}
                      {instance.name && instance.computerName ? ` · ${instance.computerName}` : ""}
                      {instance.ipAddress ? ` · ${instance.ipAddress}` : ""}
                      {instance.platformName ? ` · ${instance.platformName}` : ""}
                    </span>
                  </span>
                  <Button small variant="ghost" onClick={() => void registerSsmInstance(instance)} disabled={busy}>
                    {tr("start.register")}
                  </Button>
                </div>
              ))}
              {ssmInstances?.length === 0 && <span className="ui-field-hint">{tr("start.no_online_instances")}</span>}
              {ssmInstances !== null && ssmInstances.length > 0 && visibleSsmInstances.length === 0 && (
                <span className="ui-field-hint">{tr("start.no_matching_instances")}</span>
              )}
              <span className="ui-field-hint">{tr("start.sso_expired_note")}</span>
            </div>
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={() => setStage("place")} disabled={busy}>
              {tr("launch.back_to_hub")}
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              {tr("common.cancel")}
            </Button>
            {!ssmAllAsCards && (
              <Button variant="primary" onClick={() => void startSsm()} disabled={!ssmHostId || busy}>
                {busy ? tr("start.creating") : tr("start.connect")}
              </Button>
            )}
          </footer>
        </>
      )}
    </Modal>
  );
}
