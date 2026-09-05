import { useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useLayoutStore } from "../../../layout/store.ts";
import { useSessionsStore } from "../../sessions/store.ts";
import { useReposStore } from "../../repos/store.ts";
import { useFilesStore } from "../../files/store.ts";
import { ConfirmDialog } from "../../../ui/ConfirmDialog.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { confirmDirtyNavigation } from "../../editor/dirtyRegistry.ts";

// DangerTab: the destructive workspace-lifecycle actions (recreate 「作り直す」 / clean home
// 「ホームを掃除する」), kept in their own rail item so a data-loss action isn't mixed in with
// routine toolchain selection. Still tucked deep in settings (not on the always-visible WS
// bar) and behind a warning dialog, since recreating discards sessions and cloned repos
// (logins/connections survive).
export function DangerTab() {
  const tr = useT();
  const toast = useToast();
  // Both destructive actions share the same post-teardown refresh: everything the
  // views point at is about to go away (the terminal reconciler disposes the other
  // panes' xterms after resetToTerminal), then we refresh sessions/repos/files.
  const runDestructive = async (action: () => Promise<string | null>, failMsg: string) => {
    if (!(await confirmDirtyNavigation("workspace_lifecycle"))) return;
    const err = await action();
    if (err) toast(failMsg + err);
    // The lifecycle store performs the dirty guard before issuing the request.
    // Resetting first could destroy a buffer before that guard is shown.
    useLayoutStore.getState().resetToTerminal();
    void useSessionsStore.getState().refresh();
    void useReposStore.getState().refresh();
    useFilesStore.getState().bump();
  };
  const [confirm, setConfirm] = useState<null | "recreate" | "cleanHome">(null);
  const [busy, setBusy] = useState(false);

  const run = async (action: () => Promise<string | null>, failMsg: string) => {
    setBusy(true);
    try {
      await runDestructive(action, failMsg);
      setConfirm(null);
    } finally {
      setBusy(false);
    }
  };
  const doRecreate = () => run(() => useWorkspaceStore.getState().recreate(true), tr("env.recreate_failed"));
  const doCleanHome = () => run(() => useWorkspaceStore.getState().cleanHome(true), tr("env.cleanhome_failed"));

  return (
    <div className="display-settings">
      <section className="danger-zone">
        <h4 className="danger-zone-title">
          <Icon name="warning" /> {tr("env.danger_zone")}
        </h4>
        <div className="danger-zone-row">
          <div className="danger-zone-text">
            <strong>{tr("env.recreate_head")}</strong>
            <span className="muted">
              {tr("env.recreate_desc_1")}
              <code>~/repos</code>
              {tr("env.recreate_desc_2")}
            </span>
          </div>
          <button className="danger-btn" onClick={() => setConfirm("recreate")}>
            {tr("env.recreate_btn")}
          </button>
        </div>
        <div className="danger-zone-row">
          <div className="danger-zone-text">
            <strong>{tr("env.cleanhome_head")}</strong>
            <span className="muted">
              {tr("env.cleanhome_desc_1")}
              <code>~/repos</code>
              {tr("common.mid_dot")}
              <code>~/.local</code>
              {tr("env.cleanhome_desc_2")}
            </span>
          </div>
          <button className="danger-btn" onClick={() => setConfirm("cleanHome")}>
            {tr("env.cleanhome_btn")}
          </button>
        </div>
        {confirm === "recreate" && (
          <ConfirmDialog
            title={tr("env.recreate_confirm_title")}
            confirmLabel={tr("env.recreate_btn")}
            busy={busy}
            onConfirm={doRecreate}
            onCancel={() => setConfirm(null)}
          >
            <p>{tr("env.recreate_confirm_body")}</p>
            <ul className="confirm-list">
              <li className="keep"><Icon name="check" /> {tr("env.dz_keep_login")}</li>
              <li className="keep"><Icon name="check" /> <code>~/repos</code>{tr("env.dz_keep_home_1")}<code>~/.local</code>{tr("env.dz_keep_home_2")}</li>
              <li className="lose"><Icon name="close" /> {tr("env.dz_lose_sessions")}</li>
              <li className="lose"><Icon name="close" /> {tr("env.dz_lose_repos")}</li>
            </ul>
          </ConfirmDialog>
        )}
        {confirm === "cleanHome" && (
          <ConfirmDialog
            title={tr("env.cleanhome_confirm_title")}
            confirmLabel={tr("env.cleanhome_btn")}
            busy={busy}
            onConfirm={doCleanHome}
            onCancel={() => setConfirm(null)}
          >
            <p>{tr("env.cleanhome_confirm_body")}</p>
            <ul className="confirm-list">
              <li className="keep"><Icon name="check" /> {tr("env.dz_keep_login")}</li>
              <li className="lose"><Icon name="close" /> {tr("env.dz_lose_sessions")}</li>
              <li className="lose"><Icon name="close" /> {tr("env.dz_lose_repos")}</li>
              <li className="lose"><Icon name="close" /> <code>~/.local</code>{tr("env.dz_lose_home_rest")}</li>
            </ul>
          </ConfirmDialog>
        )}
      </section>
    </div>
  );
}
