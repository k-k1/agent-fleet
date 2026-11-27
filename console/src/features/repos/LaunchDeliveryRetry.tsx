import { useRef, useState } from "react";
import { agentOf } from "../../agents/registry.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Modal } from "../../ui/Modal.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { dismissToast } from "../../ui/toast.ts";
import { deliverLaunchPrompt, uploadLaunchFiles, type LaunchFile } from "./launchDelivery.ts";

export function launchDeliveryKey(session: string): string {
  return "launch-delivery:" + session;
}

/** The sticky notification owns the draft so closing any launch surface cannot unmount it. */
export function LaunchDeliveryRetry({ session, kind, repo, prompt: initialPrompt, files: initialFiles, dialog }: {
  session: string;
  kind: string;
  repo: string;
  prompt: string;
  files: LaunchFile[];
  dialog: { open: boolean };
}) {
  const tr = useT();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const toggleDialog = (value: boolean) => {
    dialog.open = value;
    setOpen(value);
  };
  const [busy, setBusy] = useState(false);
  const sending = useRef(false);
  const [prompt, setPrompt] = useState(initialPrompt);
  const [files, setFiles] = useState(initialFiles);
  const send = async () => {
    if (sending.current || (!prompt.trim() && !files.length)) return;
    sending.current = true;
    setBusy(true);
    try {
      const uploaded = await uploadLaunchFiles(session, files, toast);
      setFiles(uploaded);
      if (uploaded.some((entry) => !entry.path)) return;
      const result = await deliverLaunchPrompt(session, kind, prompt.trim(), uploaded, toast);
      if (!result.ok) return;
      if (agentOf(kind).caps.chat) setLaunchSeed(session, result.seed);
      pushPromptHistory(repo, prompt);
      dismissToast(launchDeliveryKey(session));
    } finally {
      sending.current = false;
      setBusy(false);
    }
  };

  return (
    <>
      {tr("rp.delivery_held", { session })}{" "}
      <button type="button" className="ghost" onClick={() => toggleDialog(true)}>{tr("rp.delivery_review")}</button>
      {open && (
        <Modal
          title={tr("rp.delivery_title", { session })}
          onClose={() => toggleDialog(false)}
          lockClose={busy}
          holdKeys
          as="form"
          onSubmit={(e) => { e.preventDefault(); void send(); }}
        >
          <div className="ui-modal-body">
            <label className="ui-field">
              {tr("rp.delivery_prompt")}
              <textarea rows={5} value={prompt} onChange={(e) => setPrompt(e.target.value)} />
            </label>
            {files.length > 0 && <ul>
              {files.map(({ file, path }, i) => (
                <li key={i}>
                  {file.name}{!path && (" · " + tr("rp.delivery_pending"))}{" "}
                  <button
                    type="button"
                    className="ghost"
                    aria-label={tr("rp.delivery_remove_file", { name: file.name })}
                    onClick={() => setFiles((list) => list.filter((_, index) => index !== i))}
                  >
                    {tr("chat.remove")}
                  </button>
                </li>
              ))}
            </ul>}
            <label className="ui-field">
              {tr("rp.delivery_add_files")}
              <input type="file" multiple onChange={(e) => {
                const added = Array.from(e.target.files || [], (file) => ({ file, path: "" }));
                setFiles((list) => [...list, ...added]);
                e.target.value = "";
              }} />
            </label>
          </div>
          <footer className="ui-modal-foot">
            <button type="submit" className="primary" disabled={busy || (!prompt.trim() && !files.length)}>
              {tr("rp.delivery_send")}
            </button>
          </footer>
        </Modal>
      )}
    </>
  );
}
