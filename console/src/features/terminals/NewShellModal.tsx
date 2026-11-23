// NewShellModal — "start a shell" from the Terminal view's header (ADR 0142 decision 10): home, a
// repository or a space. A shell has nothing to configure, so picking the place starts it; the
// pane opens without the left pane changing (ADR 0140 decision 5).
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { orderedRepos, workingCopyLabel, worktreeTag } from "../../lib/project.ts";
import type { ShellPlace } from "../../lib/project.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useReposStore } from "../repos/store.ts";
import { useStartWork } from "../repos/useStartWork.ts";
import { spaceLabel, spacesFor, spacesOwner, useSpacesStore } from "../spaces/store.ts";

// Where each place starts, as the `dir` of the create: relative to home, as "Open shell here" sends it.
export const shellDirOf = (p: ShellPlace): string =>
  p.kind === "home" ? "" : p.kind === "repo" ? `repos/${p.folder}` : `spaces/${p.folder}`;

export function NewShellModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const startWork = useStartWork();
  const [busy, setBusy] = useState(false);
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
      <div className="ui-modal-body">
        <div className="start-list">
          {row("home", "home", tr("terms.home"), tr("terms.home_desc"), { kind: "home" }, "")}
          {orderedRepos(repos).map((r) => {
            const { project } = workingCopyLabel(r.name, r);
            const tag = r.worktree ? worktreeTag(r.name, r) : "";
            return row(`repo:${r.name}`, "repo", project, tag, { kind: "repo", folder: r.name }, tr("proj.shell_title", { name: r.name }));
          })}
          {spaces.map((sp) =>
            row(`space:${sp.folder}`, "folder", spaceLabel(sp.folder, spaces), tr("pj.space_folder_hint", { folder: sp.folder }), { kind: "space", folder: sp.folder }, tr("proj.shell_title", { name: sp.folder })),
          )}
        </div>
      </div>
    </Modal>
  );
}
