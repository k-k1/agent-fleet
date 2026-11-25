// NewShellModal — "Start a terminal" from the Terminal view's header (ADR 0142 decision 10): a shell
// at home, in a repository's base clone or in a space, or an SSM session. A shell has nothing to
// configure, so picking the place starts it; the pane opens without the left pane changing (ADR 0140
// decision 5). Worktrees are not listed: "Open shell here" on the tree row starts one there. The SSM
// section is the Start hub's SSM stage (SsmStage), so both list the same hosts and log in the same way.
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { orderedRepos, workingCopyLabel } from "../../lib/project.ts";
import type { ShellPlace } from "../../lib/project.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { api } from "../../core/api/client.ts";
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useReposStore } from "../repos/store.ts";
import { SsmStage } from "../repos/SsmStage.tsx";
import type { SsmProfile } from "../repos/SsmStage.tsx";
import { SsmLoginModal } from "../sessions/SsmLoginModal.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal } from "../sessions/open.ts";
import { useStartWork } from "../repos/useStartWork.ts";
import { failuresFor, spaceLabel, spacesFor, spacesOwner, useSpacesStore } from "../spaces/store.ts";
import { useToast } from "../../ui/ToastProvider.tsx";

// Where each place starts, as the `dir` of the create: relative to home, as "Open shell here" sends it.
export const shellDirOf = (p: ShellPlace): string =>
  p.kind === "home" ? "" : p.kind === "repo" ? `repos/${p.folder}` : `spaces/${p.folder}`;

// More places than this are folded behind a header; more than PLACES_FILTER_MIN get a filter box.
const PLACES_FOLD_MAX = 5;
const PLACES_FILTER_MIN = 8;

export function NewShellModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const startWork = useStartWork();
  const toast = useToast();
  const failures = useSpacesStore((s) => failuresFor(s, owner));
  const [busy, setBusy] = useState(false);
  const [query, setQuery] = useState("");
  const [unfolded, setUnfolded] = useState(false);
  const [ssmProfiles, setSsmProfiles] = useState<SsmProfile[] | null>(null);
  // The created SSM session while its SSO handshake runs (the hub's flow).
  const [ssmLogin, setSsmLogin] = useState<string | null>(null);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  useEffect(() => {
    let alive = true;
    api("api/ssm/profiles")
      .then((rows) => alive && setSsmProfiles(Array.isArray(rows) ? rows : []))
      .catch(() => alive && setSsmProfiles([]));
    return () => {
      alive = false;
    };
  }, []);
  // Every space, not only the ones a session names: the list is asked for when the dialog opens.
  useEffect(() => {
    void useSpacesStore.getState().refresh();
  }, []);

  const start = async (place: ShellPlace, title: string) => {
    if (busy) return;
    setBusy(true);
    try {
      const r = await startWork(
        { dir: shellDirOf(place), repo: "" },
        { kind: "shell", driver: "", model: "", effort: "", startMode: "normal", prompt: "", title, images: [], worktree: false, subdir: "", base: "", newBranch: "" },
      );
      // useStartWork files a home session in the active working set by itself; a space shell is
      // repo-less too and has no copy to inherit from.
      if (r.ok && r.name && place.kind === "space") autoAddToActiveWorkingSet("sessions", r.name);
      if (r.ok) onClose();
    } catch {
      // The create never got an answer (the network): useStartWork toasts the Agent's refusals itself.
      toast(tr("rp.launch_failed", { err: tr("err.network") }));
    } finally {
      setBusy(false);
    }
  };

  const row = (key: string, icon: string, title: string, desc: string, place: ShellPlace, shellTitle: string) => (
    <button key={key} type="button" className="start-row" disabled={busy} onClick={() => void start(place, shellTitle)}>
      <Icon name={icon} className="start-row-ic" />
      <span className="start-row-body">
        <span className="start-row-title">{title}</span>
        {desc && <span className="start-row-desc">{desc}</span>}
      </span>
    </button>
  );

  // Base clones only: a worktree's shell is started from its own tree row.
  const places = [
    ...orderedRepos(repos)
      .filter((r) => !r.worktree)
      .map((r) => ({ key: `repo:${r.name}`, icon: "repo", title: workingCopyLabel(r.name, r).project, desc: "", haystack: `${r.name} ${workingCopyLabel(r.name, r).project}`, place: { kind: "repo", folder: r.name } as ShellPlace, shellTitle: tr("proj.shell_title", { name: r.name }) })),
    ...spaces.map((sp) => ({ key: `space:${sp.folder}`, icon: "folder", title: spaceLabel(sp.folder, spaces), desc: tr("pj.space_folder_hint", { folder: sp.folder }), haystack: `${sp.folder} ${spaceLabel(sp.folder, spaces)}`, place: { kind: "space", folder: sp.folder } as ShellPlace, shellTitle: tr("proj.shell_title", { name: sp.folder }) })),
  ];
  const q = query.trim().toLocaleLowerCase();
  const shown = q ? places.filter((p) => p.haystack.toLocaleLowerCase().includes(q)) : places;
  // A few places read at a glance; more are folded, and a filter box appears once the list is long.
  const folded = places.length > PLACES_FOLD_MAX && !unfolded && !q;

  // The SSO handshake takes over the dialog, as it does in the Start hub.
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
          <Icon name="terminal" /> {tr("terms.new_shell")}
        </>
      }
      onClose={onClose}
      lockClose={busy}
    >
      <div className="ui-modal-body term-new">
        <section className="term-new-sec">
          <h3 className="term-sub-head">{tr("terms.shell")}</h3>
          <div className="start-list">{row("home", "home", tr("terms.home"), tr("terms.home_desc"), { kind: "home" }, "")}</div>
          {failures > 0 && (
            <div className="start-row-desc term-spaces-failed" role="status">
              {tr("terms.spaces_failed")}{" "}
              <button type="button" className="linklike" onClick={() => void useSpacesStore.getState().refresh()}>
                {tr("terms.spaces_retry")}
              </button>
            </div>
          )}
          {places.length > 0 && (
            <>
              {places.length > PLACES_FOLD_MAX && (
                <button type="button" className="term-new-fold" aria-expanded={!folded} disabled={!!q} onClick={() => setUnfolded(!unfolded)}>
                  <Icon name={folded ? "chevron-right" : "chevron-down"} />
                  {tr("terms.places", { n: places.length })}
                </button>
              )}
              {!folded && places.length > PLACES_FILTER_MIN && (
                <div className="ui-field">
                  <input {...NO_AUTOFILL} type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder={tr("terms.places_filter_ph")} aria-label={tr("terms.places_filter_ph")} />
                </div>
              )}
              {!folded && shown.length > 0 && <div className="start-list">{shown.map((p) => row(p.key, p.icon, p.title, p.desc, p.place, p.shellTitle))}</div>}
              {!folded && shown.length === 0 && <span className="ui-field-hint">{tr("terms.places_none")}</span>}
            </>
          )}
        </section>
        <section className="term-new-sec">
          <h3 className="term-sub-head">{tr("terms.ssm")}</h3>
          {ssmProfiles === null ? (
            <p className="sm-muted">{tr("chat.ph_loading")}</p>
          ) : ssmProfiles.length === 0 ? (
            <span className="ui-field-hint">
              {tr("terms.ssm_none")}{" "}
              <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
                {tr("start.settings_ssm")}
              </button>
            </span>
          ) : (
            <SsmStage busy={busy} setBusy={setBusy} ssmProfiles={ssmProfiles} onStarted={setSsmLogin} layout="inline" onClose={onClose} />
          )}
        </section>
      </div>
    </Modal>
  );
}
