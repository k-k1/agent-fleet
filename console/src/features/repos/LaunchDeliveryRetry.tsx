import { useEffect, useState } from "react";
import { agentOf } from "../../agents/registry.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { Modal } from "../../ui/Modal.tsx";
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { dismissToast } from "../../ui/toast.ts";
import { deliverLaunchPrompt, uploadLaunchFiles } from "./launchDelivery.ts";
import {
  clearLaunchDraft, editLaunchDraft, launchDraftOwner, restoreLaunchDrafts, useHeldLaunchDrafts,
  withLaunchDraft, type HeldLaunchDraft,
} from "./launchDeliveryDraft.ts";

function focusAfterDelivery() {
  document.querySelector<HTMLElement>("[data-launch-delivery-focus]")?.focus();
}

type DialogState = { open: boolean };

export function LaunchDeliveryRecovery() {
  const owner = useTenantStore((s) => launchDraftOwner(s).prefix);
  const available = useTenantStore((s) => !s.notProvisioned);
  const restore = useTenantStore((s) => s.resolved && !s.notProvisioned && launchDraftOwner(s).identified);
  const drafts = useHeldLaunchDrafts((s) => s.drafts);
  useEffect(() => {
    if (!restore) return;
    let active = true;
    void restoreLaunchDrafts(owner, () => active && launchDraftOwner(useTenantStore.getState()).prefix === owner);
    return () => { active = false; };
  }, [owner, restore]);
  return available && Object.values(drafts).filter((draft) => draft.owner === owner).map((draft) =>
    <LaunchDeliveryNotice key={draft.key} draftKey={draft.key} />,
  );
}

function LaunchDeliveryNotice({ draftKey }: { draftKey: string }) {
  const toast = useToast();
  useEffect(() => {
    const dialog: DialogState = { open: false };
    toast(<LaunchDeliveryRetry draftKey={draftKey} dialog={dialog} />, {
      kind: "warn",
      key: draftKey,
      duration: 0,
      closeLabel: t("rp.delivery_discard"),
      canClose: () => {
        if (dialog.open || useHeldLaunchDrafts.getState().busy.has(draftKey)) return false;
        const draft = useHeldLaunchDrafts.getState().drafts[draftKey];
        if (!draft) return true;
        if (launchDraftOwner(useTenantStore.getState()).prefix !== draft.owner) return false;
        const opener = document.activeElement;
        void withLaunchDraft(draft, () => clearLaunchDraft(draftKey)).finally(() => {
          const focused = document.activeElement;
          if ((focused === opener || focused === document.body) &&
              launchDraftOwner(useTenantStore.getState()).prefix === draft.owner) focusAfterDelivery();
        });
        return false;
      },
    });
    return () => {
      dismissToast(draftKey);
      dismissToast(draftKey + ":storage");
    };
  }, [draftKey, toast]);
  return null;
}

function LaunchDeliveryRetry({ draftKey, dialog }: { draftKey: string; dialog: DialogState }) {
  const draft = useHeldLaunchDrafts((s) => s.drafts[draftKey]);
  const current = useTenantStore((s) => launchDraftOwner(s).prefix === draft?.owner && !s.notProvisioned);
  if (!draft || !current) return null;
  return <RetryDialog draft={draft} dialog={dialog} />;
}

function RetryDialog({ draft, dialog }: { draft: HeldLaunchDraft; dialog: DialogState }) {
  const tr = useT();
  const toast = useToast();
  const { key, session, kind, repo, prompt, files } = draft;
  const [open, setOpen] = useState(false);
  const toggleDialog = (value: boolean) => {
    if (useHeldLaunchDrafts.getState().busy.has(key)) return;
    dialog.open = value;
    setOpen(value);
  };
  const busy = useHeldLaunchDrafts((s) => s.busy.has(key));
  const inScope = () => {
    const tenant = useTenantStore.getState();
    return !tenant.notProvisioned && launchDraftOwner(tenant).prefix === draft.owner;
  };
  const save = async (changes: Parameters<typeof editLaunchDraft>[1]) => {
    const saved = await editLaunchDraft(key, changes);
    if (!saved && inScope()) {
      toast(tr("rp.delivery_storage_failed"), { kind: "warn", key: key + ":storage" });
    }
  };
  const edit = (changes: Parameters<typeof editLaunchDraft>[1]) => {
    if (!useHeldLaunchDrafts.getState().busy.has(key) && inScope()) void save(changes);
  };
  const send = async () => {
    if (!inScope() || (!prompt.trim() && !files.length)) return;
    let switched = false;
    const unsubscribe = useTenantStore.subscribe(() => { if (!inScope()) switched = true; });
    const current = () => !switched && inScope();
    const deliveryToast: typeof toast = (...args) => { if (current()) toast(...args); };
    try {
      await withLaunchDraft(draft, async () => {
        if (!current()) return;
        const uploaded = await uploadLaunchFiles(session, files, deliveryToast, current);
        if (uploaded.some((entry, index) => entry !== files[index])) await save({ files: uploaded });
        if (!current() || uploaded.some((entry) => !entry.path)) return;
        const result = await deliverLaunchPrompt(session, kind, prompt.trim(), uploaded, deliveryToast);
        if (!result.ok) return;
        await clearLaunchDraft(key);
        if (!current()) return;
        if (agentOf(kind).caps.chat) setLaunchSeed(session, result.seed);
        pushPromptHistory(repo, prompt);
        focusAfterDelivery();
      });
    } finally {
      unsubscribe();
    }
  };

  return (
    <>
      {tr("rp.delivery_held", { session })}{" "}
      <button type="button" className="ghost" disabled={busy} onClick={() => toggleDialog(true)}>{tr("rp.delivery_review")}</button>
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
              <textarea {...NO_AUTOFILL} rows={5} value={prompt} disabled={busy} onChange={(e) => edit({ prompt: e.target.value })} />
            </label>
            {files.length > 0 && <ul>
              {files.map(({ file, path }, i) => (
                <li key={i}>
                  {file.name}{!path && (" · " + tr("rp.delivery_pending"))}{" "}
                  <button
                    type="button"
                    className="ghost"
                    aria-label={tr("rp.delivery_remove_file", { name: file.name })}
                    onClick={() => edit({ files: files.filter((_, index) => index !== i) })}
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
                edit({ files: [...files, ...added] });
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
