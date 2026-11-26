// SpaceLaunchModal — the Start hub's space launch as a dialog of its own, for entry points that
// already hold the first prompt and only need a place: an inbox request. It is the hub's space
// section with the same form, the same prompt box and the same create path (useStartWork's
// `space` field, no repository dialog), so a space is chosen and launched exactly as there.
//
// The request is named by `inboxItemId`, never through the global launch seed: this dialog is
// loaded lazily and may be open while another request or a plain launch is started, and a seed
// left behind would send that launch to the wrong request.
import { useEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useSpaceSupport } from "../spaces/useSpaceSupport.ts";
import { SpaceSupportHint } from "../spaces/SpaceSupportHint.tsx";
import { HubComposer } from "./HubComposer.tsx";
import { useHubDraft } from "./hubDraft.ts";
import { SpaceLaunchForm } from "./LaunchModal.tsx";
import { useRepoRailContext } from "./useRepoRail.ts";
import { useStartWork } from "./useStartWork.ts";
import { agentOf } from "../../agents/registry.ts";

export function SpaceLaunchModal({ prompt, inboxItemId, onClose, onLaunched }: {
  /** The text the first prompt starts as; the member may edit it before launching. */
  prompt: string;
  /** The inbox request the session is started for (the CP's request launch). */
  inboxItemId: string;
  onClose: () => void;
  /** A session was created (its name): the caller finishes what the launch was for. */
  onLaunched?: (session: string) => void;
}) {
  const tr = useT();
  const ctx = useRepoRailContext();
  const draft = useHubDraft();
  const { support, recheck } = useSpaceSupport();
  const startWork = useStartWork({ policyInline: true });
  const [busy, setBusy] = useState(false);
  const submit = useRef<(() => void) | null>(null);
  // The draft starts from the seed once; a later render must not put an edited prompt back.
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    draft.reset({ prompt, attachments: [] });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- draft.reset is a fresh closure each render
  }, []);
  // Coding agents only, as in the hub: shell and ssm have no working directory to start in.
  const kinds = ctx.launchKinds.filter((k) => agentOf(k).caps.runsInDir);
  return (
    <Modal
      title={
        <>
          <Icon name="folder-library" /> {tr("start.sec_space")}
        </>
      }
      onClose={onClose}
      lockClose={busy}
      // Stacked on the inbox: while the create is out, Escape and back must not fall to it.
      holdKeys
    >
      <div className="ui-modal-body start-hub">
        <HubComposer draft={draft} busy={busy} onSubmit={() => submit.current?.()} />
        <SpaceSupportHint support={support} onRecheck={recheck} />
        <SpaceLaunchForm
          kinds={kinds}
          access={ctx.access}
          claudeExpired={ctx.claudeExpired}
          settling={ctx.connsSettling}
          shared={draft}
          busy={busy}
          setBusy={setBusy}
          launchBlocked={support !== "yes"}
          submitRef={submit}
          onClose={onClose}
          onLaunch={async (o) => {
            // dir "" with a space: the Agent makes ~/spaces/<folder> and starts the session there.
            const r = await startWork({ dir: "", repo: "", seedless: true, inboxItem: inboxItemId }, o);
            // The form closes the dialog itself once it sees ok.
            if (r.ok) onLaunched?.(r.name || "");
            return r;
          }}
        />
      </div>
    </Modal>
  );
}
