// SessionModals — a single app-level host for every session dialog, so the modals
// don't belong to any one rail section (rows live in many containers now: the
// project tree's nodes and the orphan catch-all). Driven by the useSessionUI
// store (per-row rename / branch-rename / SSM resume / archive browser). Session
// CREATION lives in the はじめる hub (repos/StartHost) since Ph3 retired the
// NewSessionModal.
import { useSessionsStore } from "./store.ts";
import { useSessionUI } from "./ui.ts";
import { displayName } from "../../lib/sessionview.ts";
import { openSessionTerminal } from "./open.ts";
import { ArchivedModal } from "./ArchivedModal.tsx";
import { CleanupModal } from "./CleanupModal.tsx";
import { SsmLoginModal } from "./SsmLoginModal.tsx";
import { SessionTitleModal } from "./SessionTitleModal.tsx";
import { BranchRenameModal } from "./BranchRenameModal.tsx";

export function SessionModals() {
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const rename = useSessionUI((s) => s.rename);
  const branchRename = useSessionUI((s) => s.branchRename);
  const ssmResume = useSessionUI((s) => s.ssmResume);
  const archivedOpen = useSessionUI((s) => s.archivedOpen);
  const cleanupOpen = useSessionUI((s) => s.cleanupOpen);
  const close = useSessionUI((s) => s.close);

  return (
    <>
      {ssmResume && (
        <SsmLoginModal
          name={ssmResume.name}
          start
          force={ssmResume.force}
          onReady={(n) => {
            close();
            openSessionTerminal(n);
            void refreshSessions();
            setTimeout(() => void refreshSessions(), 1200);
          }}
          onCancel={() => {
            close();
            void refreshSessions();
          }}
        />
      )}
      {archivedOpen && <ArchivedModal onClose={close} onRestored={() => void refreshSessions()} />}
      {cleanupOpen && <CleanupModal onClose={close} onChanged={() => void refreshSessions()} />}
      {rename && (
        // Prefill with the name as displayed (manual title, or the derived label /
        // repo@time for auto-named sessions) so editing starts from the current title
        // instead of an empty field. Clearing it still reverts to auto naming.
        <SessionTitleModal
          name={rename.name}
          kind={rename.kind}
          title={displayName(rename)}
          onClose={close}
          onSaved={() => void refreshSessions()}
        />
      )}
      {branchRename && (
        <BranchRenameModal
          name={branchRename.name}
          branch={branchRename.branch || ""}
          onClose={close}
          onSaved={() => void refreshSessions()}
        />
      )}
    </>
  );
}
