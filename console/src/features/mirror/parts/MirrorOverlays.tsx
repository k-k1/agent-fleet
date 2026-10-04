import { createPortal } from "react-dom";
import { downloadURL } from "../../../core/api/client.ts";
import type { Session } from "../../../types/session.ts";
import { writeDraft } from "../../../lib/draft.ts";
import { dirName } from "../../../lib/filemeta.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import type { useLayoutStore } from "../../../layout/store.ts";
import type { useToast } from "../../../ui/ToastProvider.tsx";
import { openGallery } from "../../gallery/open.ts";
import { ImageLightbox } from "../../viewer/ImageLightbox.tsx";
import { ForkAtModal } from "../ForkAtModal.tsx";
import { ManagedSettingsModal } from "../ManagedSettingsModal.tsx";
import type { MirrorState } from "./useMirrorState.ts";

/** MirrorOverlays holds the mirror's overlays: the image lightbox and its two modals. */
export function MirrorOverlays({
  session,
  sessionMeta,
  toast,
  bumpSessions,
  openTargetInNew,
  st,
}: {
  session: string;
  sessionMeta?: Session | null;
  toast: ReturnType<typeof useToast>;
  bumpSessions: () => void;
  openTargetInNew: ReturnType<typeof useLayoutStore.getState>["openTargetInNew"];
  st: MirrorState;
}) {
  const {
    lightbox, setLightbox, managedSettingsOpen, setManagedSettingsOpen, setManagedSettings, status,
    forkAtTarget, setForkAtTarget,
  } = st;
  return (
    <>
      {lightbox &&
        createPortal(
          <ImageLightbox
            src={lightbox.src}
            // The card's thumbnail is on screen already, so the enlarged view has something
            // to show while the screen-sized copy arrives. A pasted image has no path
            // and no thumbnail — it opens as it always did.
            placeholder={lightbox.path ? downloadURL(lightbox.path, 512) : undefined}
            // A pasted image has no path, so it gets no properties toggle either — the
            // same rule the folder button already follows (ADR 0081 decision 3).
            path={lightbox.path || undefined}
            onClose={() => setLightbox(null)}
            // Only a shared FILE has a folder; a pasted image has no path and so gets no
            // item. Closing first keeps the overlay from surviving the pane change.
            onOpenFolder={
              lightbox.path
                ? () => {
                    const path = lightbox.path!;
                    setLightbox(null);
                    openGallery(dirName(path), { focus: path });
                  }
                : undefined
            }
          />,
          document.body,
        )}
      {managedSettingsOpen && (
        <ManagedSettingsModal
          session={session}
          kind={sessionMeta?.kind || "codex"}
          working={status === "working"}
          onApplied={setManagedSettings}
          onClose={() => setManagedSettingsOpen(false)}
        />
      )}
      {forkAtTarget && (
        <ForkAtModal
          session={session}
          target={forkAtTarget}
          onDone={(name, { draft }) => {
            // In redo mode, seed the new session's draft with the fork point's message before
            // opening it: the point is being able to retype straight away, which is lost if the
            // user has to hunt down the original and paste it back. In continue mode the message
            // is still in the forked conversation, so the draft arrives empty.
            writeDraft("af.mirror-draft." + name, draft);
            bumpSessions();
            openTargetInNew({ content: { kind: "terminal", chat: true }, session: name });
            toast(tr("mirror.fork_at_done"));
          }}
          onClose={() => setForkAtTarget(null)}
        />
      )}
    </>
  );
}
