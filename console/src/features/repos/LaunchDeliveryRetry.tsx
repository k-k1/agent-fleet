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
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { deliverLaunchPrompt, uploadLaunchFiles } from "./launchDelivery.ts";
import {
  clearLaunchDraft, editLaunchDraft, launchDraftOwner, markLaunchDraftDelivery, restoreLaunchDrafts, useHeldLaunchDrafts,
  withLaunchDraft, type HeldLaunchDraft,
} from "./launchDeliveryDraft.ts";

function focusAfterDelivery() {
  document.querySelector<HTMLElement>("[data-launch-delivery-focus]")?.focus();
}

type DialogState = { open: boolean };
type ActionStatus = "locked" | "unavailable";
const actionMessage = {
  locked: "rp.delivery_other_tab",
  unavailable: "rp.delivery_storage_unavailable",
} as const;

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
        if (dialog.open || useHeldLaunchDrafts.getState().operations[draftKey]) return false;
        const draft = useHeldLaunchDrafts.getState().drafts[draftKey];
        if (!draft) return true;
        if (launchDraftOwner(useTenantStore.getState()).prefix !== draft.owner) return false;
        const opener = document.activeElement;
        void withLaunchDraft(draftKey, async () => {
          if (launchDraftOwner(useTenantStore.getState()).prefix === draft.owner) await clearLaunchDraft(draftKey);
        }).then((status) => {
          if (launchDraftOwner(useTenantStore.getState()).prefix !== draft.owner) return;
          if (status) {
            toast(t(actionMessage[status]), { kind: "warn", key: draftKey + ":action" });
            return;
          }
          const focused = document.activeElement;
          if (focused === opener || focused === document.body) focusAfterDelivery();
        });
        return false;
      },
    });
    return () => {
      dismissToast(draftKey);
      dismissToast(draftKey + ":storage");
      dismissToast(draftKey + ":action");
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
  const { key, session, kind, prompt, files, deliveryPending } = draft;
  const [open, setOpen] = useState(false);
  const [status, setStatus] = useState<ActionStatus>();
  const operation = useHeldLaunchDrafts((s) => s.operations[key]);
  const busy = !!operation;
  const inScope = () => {
    const tenant = useTenantStore.getState();
    return !tenant.notProvisioned && launchDraftOwner(tenant).prefix === draft.owner;
  };
  const reportStatus = (status: ActionStatus | void) => {
    if (!status || !inScope()) return;
    if (dialog.open) setStatus(status);
    else toast(tr(actionMessage[status]), { kind: "warn", key: key + ":action" });
  };
  const toggleDialog = (value: boolean) => {
    if (value && useHeldLaunchDrafts.getState().operations[key]) return;
    dialog.open = value;
    setOpen(value);
    setStatus(undefined);
    if (!value) return;
    dismissToast(key + ":action");
    if (deliveryPending) void withLaunchDraft(key, async () => {}).then(reportStatus);
  };
  const save = async (changes: Parameters<typeof editLaunchDraft>[1]) => {
    const saved = await editLaunchDraft(key, changes);
    if (!saved && inScope()) {
      toast(tr("rp.delivery_storage_failed"), { kind: "warn", key: key + ":storage" });
    }
  };
  const edit = (changes: Parameters<typeof editLaunchDraft>[1]) => {
    if (!useHeldLaunchDrafts.getState().operations[key] && inScope()) void save(changes);
  };
  const send = async () => {
    if (!inScope() || busy || deliveryPending) return;
    setStatus(undefined);
    let switched = false;
    const unsubscribe = useTenantStore.subscribe(() => { if (!inScope()) switched = true; });
    const current = () => !switched && inScope();
    const deliveryToast: typeof toast = (...args) => { if (current()) toast(...args); };
    try {
      const blocked = await withLaunchDraft(key, async (draft, signal, phase) => {
        const { session, kind, prompt, files, repo } = draft;
        if (!current() || draft.deliveryPending || (!prompt.trim() && !files.length)) return;
        if (files.some((entry) => !entry.path)) phase("uploading");
        const uploaded = await uploadLaunchFiles(session, files, deliveryToast, current, signal);
        if (uploaded.some((entry, index) => entry !== files[index])) await save({ files: uploaded });
        if (!current() || signal.aborted || uploaded.some((entry) => !entry.path)) return;
        if (!await markLaunchDraftDelivery(key, true)) {
          if (current()) reportStatus("unavailable");
          return;
        }
        if (!current() || signal.aborted) {
          if (!await markLaunchDraftDelivery(key, false) && current()) reportStatus("unavailable");
          return;
        }
        phase("sending");
        const result = await deliverLaunchPrompt(session, kind, prompt.trim(), uploaded, deliveryToast, signal);
        if (result.unknown) return;
        if (!result.ok && !await markLaunchDraftDelivery(key, false) && current()) reportStatus("unavailable");
        if (!result.ok) return;
        await clearLaunchDraft(key);
        if (!current()) return;
        if (agentOf(kind).caps.chat) setLaunchSeed(session, result.seed);
        pushPromptHistory(repo, prompt);
        if (dialog.open) focusAfterDelivery();
      });
      if (current()) reportStatus(blocked);
    } finally {
      unsubscribe();
    }
  };

  const cancel = operation && operation.phase !== "checking" && (
    <button type="button" className="ghost" onClick={() => operation.controller.abort()}>
      {tr(operation.phase === "uploading" ? "rp.delivery_cancel_upload" : "rp.delivery_stop_waiting")}
    </button>
  );

  return (
    <>
      {tr(deliveryPending && !busy ? "rp.delivery_unknown" : "rp.delivery_held", { session })}{" "}
      <button type="button" className="ghost" disabled={busy} onClick={() => toggleDialog(true)}>{tr("rp.delivery_review")}</button>
      {!open && cancel}
      {open && (
        <Modal
          title={tr("rp.delivery_title", { session })}
          onClose={() => toggleDialog(false)}
          as="form"
          onSubmit={(e) => { e.preventDefault(); void send(); }}
        >
          <div className="ui-modal-body">
            {operation && <p role="status">{tr(operation.phase === "checking" ? "ui.processing"
              : operation.phase === "uploading" ? "rp.delivery_uploading" : "rp.delivery_sending")}</p>}
            {status && <p role="status">{tr(actionMessage[status])}</p>}
            {deliveryPending && !busy && <p role="status">{tr("rp.delivery_unknown", { session })}</p>}
            <label className="ui-field">
              {tr("rp.delivery_prompt")}
              <textarea {...NO_AUTOFILL} rows={5} value={prompt} readOnly={busy || deliveryPending} onChange={(e) => edit({ prompt: e.target.value })} />
            </label>
            {files.length > 0 && <ul>
              {files.map(({ file, path }, i) => (
                <li key={i}>
                  {file.name}{!path && (" · " + tr("rp.delivery_pending"))}{" "}
                  {!deliveryPending && <button
                    type="button"
                    className="ghost"
                    disabled={busy}
                    aria-label={tr("rp.delivery_remove_file", { name: file.name })}
                    onClick={() => edit({ files: files.filter((_, index) => index !== i) })}
                  >
                    {tr("chat.remove")}
                  </button>}
                </li>
              ))}
            </ul>}
            {!deliveryPending && <label className="ui-field">
              {tr("rp.delivery_add_files")}
              <input type="file" multiple disabled={busy} onChange={(e) => {
                const added = Array.from(e.target.files || [], (file) => ({ file, path: "" }));
                edit({ files: [...files, ...added] });
                e.target.value = "";
              }} />
            </label>}
          </div>
          <footer className="ui-modal-foot">
            {cancel}
            {deliveryPending && !busy ? (
              <button type="button" className="primary" onClick={() => {
                if (!inScope()) return;
                toggleDialog(false);
                (agentOf(kind).caps.chat ? openSessionChat : openSessionTerminal)(session);
              }}>{tr("rp.delivery_open_session")}</button>
            ) : (
              <button type="submit" className="primary" disabled={busy || (!prompt.trim() && !files.length)}>
                {tr("rp.delivery_send")}
              </button>
            )}
          </footer>
        </Modal>
      )}
    </>
  );
}
