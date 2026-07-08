// SessionModals — a single app-level host for every session dialog, so the modals
// don't belong to any one rail section (rows live in many containers now: the
// project tree's nodes and the orphan catch-all). Driven by the sessions store's
// newSessionTick (WS bar 新規 / onboarding / per-repo 起動 all bump it) and the
// useSessionUI store (per-row rename / branch-rename / SSM resume / archive browser).
import { useEffect, useRef, useState } from "react";
import { agentOf } from "../../agents/registry.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useSessionsStore } from "./store.ts";
import { useSessionUI } from "./ui.ts";
import { openSessionChat, openSessionTerminal } from "./open.ts";
import { NewSessionModal } from "./NewSessionModal.tsx";
import { ArchivedModal } from "./ArchivedModal.tsx";
import { SsmLoginModal } from "./SsmLoginModal.tsx";
import { SessionTitleModal } from "./SessionTitleModal.tsx";
import { BranchRenameModal } from "./BranchRenameModal.tsx";

export function SessionModals() {
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const newSessionTick = useSessionsStore((s) => s.newSessionTick);
  const [showNew, setShowNew] = useState(false);
  // Open New Session whenever the global tick changes (skip the mount value).
  const lastTickRef = useRef(newSessionTick);
  useEffect(() => {
    if (newSessionTick !== lastTickRef.current) {
      lastTickRef.current = newSessionTick;
      setShowNew(true);
    }
  }, [newSessionTick]);

  const rename = useSessionUI((s) => s.rename);
  const branchRename = useSessionUI((s) => s.branchRename);
  const ssmResume = useSessionUI((s) => s.ssmResume);
  const archivedOpen = useSessionUI((s) => s.archivedOpen);
  const close = useSessionUI((s) => s.close);

  return (
    <>
      {showNew && (
        <NewSessionModal
          onClose={() => setShowNew(false)}
          onCreated={(name, cloned, repo, kind) => {
            void refreshSessions();
            if (cloned) {
              void useReposStore.getState().refresh();
              // Clone finished server-side: refresh the Files tree (reveal when known).
              if (repo) useFilesStore.getState().revealInFiles("repos/" + repo);
              else useFilesStore.getState().bump();
            }
            // A fresh claude session opens as chat (its CLI is already live).
            (agentOf(kind).caps.chat ? openSessionChat : openSessionTerminal)(name);
            setShowNew(false);
          }}
        />
      )}
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
      {rename && (
        <SessionTitleModal
          name={rename.name}
          title={rename.title || ""}
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
