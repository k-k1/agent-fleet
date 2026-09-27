// StartStudioModal — "start an image studio" from a working-copy row: the attach dialog with the
// place fixed to that row, a new studio behind it, and the studio opened in a pane of its own.
//
// Its own module, loaded lazily by RepoRow: the rail's bundle must not carry the generation form
// the dialog's model picker comes from.
import { useRef } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useSessionsStore } from "../../sessions/store.ts";
import type { Repo } from "../../repos/store.ts";
import { getStudio } from "../api.ts";
import { attachAgent } from "../attach.ts";
import { openImagegen } from "../open.ts";
import { AttachAgentModal, type AttachOpts } from "./AttachAgentModal.tsx";

export function StartStudioModal({ repo, onClose }: { repo: Repo; onClose: () => void }) {
  const tr = useT();
  const toast = useToast();
  const refreshSessions = useSessionsStore((s) => s.refresh);
  // A studio made by an attempt whose session then failed: the retry binds that one instead of
  // leaving an empty studio behind per press.
  const made = useRef<string | null>(null);
  const onAttach = async (o: AttachOpts): Promise<boolean> => {
    // The retry's pick may differ from the first attempt's: `existing` writes it into the studio.
    const prev = made.current ? await getStudio(made.current).catch(() => null) : null;
    const existing = prev && !prev.error && prev.id ? { title: prev.title || "", updatedAt: prev.updated_at, draft: prev.draft || {} } : undefined;
    const r = await attachAgent({ studioId: existing ? made.current : null, draft: () => ({}), opts: o, existing });
    if (r.studioId) made.current = r.studioId;
    if (r.error) toast(r.error || tr("imggen.attach_failed"), { kind: "error" });
    if (!r.session || !r.studioId) return false;
    void refreshSessions();
    openImagegen({ studioId: r.studioId, newPane: true });
    return true;
  };
  return <AttachAgentModal fixedRepo={repo} onClose={onClose} onAttach={onAttach} />;
}
