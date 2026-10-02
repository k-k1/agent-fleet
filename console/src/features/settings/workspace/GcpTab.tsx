// GcpTab manages the member's own Google Cloud profiles (ADR 0107 decision 1): which
// project, account and impersonation target a command run through af-gcloud-exec is pointed
// at. No secret is entered or stored here — the workspace's gcloud logs in by itself, and
// service-account keys are refused by the CP in any field.
//
// The profile name (gcloud configuration af-<name>) is the CP's: this page shows `name` and
// `conflict` as the API returns them and never derives them, so it cannot disagree with what
// the workspace receives.
import { useCallback, useEffect, useState } from "react";
import type { ChangeEvent } from "react";
import { api } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { Field, Meta } from "../parts/mcpForm.tsx";
import { FieldGroup, deleteRow, pick, postJSON } from "./SsmTab.tsx";

/** One row of GET /api/gcp/profiles. */
export interface GcpProfile {
  id: string;
  label: string;
  loginMethod: string;
  project: string;
  quotaProject: string;
  account: string;
  region: string;
  zone: string;
  impersonateServiceAccount: string;
  name: string;
  conflict?: { reason: string; labels: string[] };
}

const emptyProfile: Record<string, string> = {
  label: "",
  project: "",
  quotaProject: "",
  account: "",
  impersonateServiceAccount: "",
  region: "",
  zone: "",
};

type FieldEvent = ChangeEvent<HTMLInputElement>;

export function GcpTab() {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const [profiles, setProfiles] = useState<GcpProfile[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [f, setF] = useState<Record<string, string>>(emptyProfile);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<GcpProfile | null>(null);
  const [busy, setBusy] = useState(false);

  // A failed GET keeps the last list (null if none ever loaded) and says so, as SsmTab does:
  // replacing it with [] would close an open edit form as if its row had been deleted.
  const reload = useCallback(() => {
    api("api/gcp/profiles")
      .then((d) => {
        if (!Array.isArray(d)) throw new Error(d?.error?.message || "unexpected response");
        setProfiles(d);
        setLoadErr("");
      })
      .catch((e: any) => setLoadErr(String(e?.message || e)));
  }, []);
  useEffect(reload, [reload]);

  const close = () => {
    setOpen(false);
    setEditing(null);
    setF(emptyProfile);
  };
  useEffect(() => {
    if (editing && profiles && !profiles.some((p) => p.id === editing.id)) close();
  }, [profiles, editing]);

  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const valid = !!f.label.trim() && !!f.project.trim();

  const startAdd = () => {
    setEditing(null);
    setF(emptyProfile);
    setOpen(true);
  };
  const startEdit = (p: GcpProfile) => {
    setOpen(false);
    setF(pick(p, emptyProfile));
    setEditing(p);
  };
  const save = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const body: Record<string, string> = { loginMethod: "google" };
      for (const k of Object.keys(emptyProfile)) body[k] = f[k].trim();
      const ok = editing
        ? await postJSON(`api/gcp/profiles/${encodeURIComponent(editing.id)}`, "PUT", body, toast)
        : await postJSON("api/gcp/profiles", "POST", body, toast);
      if (!ok) return;
      close();
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    const ok = await askConfirm({
      title: tr("gcp.del_title"),
      body: tr("gcp.del_body"),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      if ((await deleteRow(`api/gcp/profiles/${encodeURIComponent(id)}`, toast)) !== "ok") return;
      reload();
    } finally {
      setBusy(false);
    }
  };

  const form = (
    <div className={"ssm-frm" + (editing ? " ssm-frm-edit" : "")}>
      <fieldset className="ssm-fieldset" disabled={busy}>
        <FieldGroup>
          <Field label={tr("gcp.f_label")} req hint={tr("gcp.f_label_hint")}>
            <input className="cinput" placeholder="prod" value={f.label} onChange={set("label")} autoFocus />
          </Field>
          <Field label={tr("gcp.f_login_method")} hint={tr("gcp.f_login_method_hint")}>
            <select className="cinput" value="google" disabled>
              <option value="google">{tr("gcp.login_google")}</option>
            </select>
          </Field>
          <Field label={tr("gcp.f_project")} req hint={tr("gcp.f_project_hint")}>
            <input className="cinput" placeholder="my-project-123" value={f.project} onChange={set("project")} />
          </Field>
          <Field label={tr("gcp.f_quota_project")} hint={tr("gcp.f_quota_project_hint")}>
            <input className="cinput" placeholder={f.project.trim() || "my-project-123"} value={f.quotaProject} onChange={set("quotaProject")} />
          </Field>
          <Field label={tr("gcp.f_account")} hint={tr("gcp.f_account_hint")}>
            <input className="cinput" placeholder="you@example.com" value={f.account} onChange={set("account")} />
          </Field>
          <Field label={tr("gcp.f_impersonate")} wide hint={tr("gcp.f_impersonate_hint")}>
            <input
              className="cinput"
              placeholder="deployer@my-project-123.iam.gserviceaccount.com"
              value={f.impersonateServiceAccount}
              onChange={set("impersonateServiceAccount")}
            />
          </Field>
          <Field label={tr("gcp.f_region")} hint={tr("gcp.f_optional")}>
            <input className="cinput" placeholder="asia-northeast1" value={f.region} onChange={set("region")} />
          </Field>
          <Field label={tr("gcp.f_zone")} hint={tr("gcp.f_optional")}>
            <input className="cinput" placeholder="asia-northeast1-a" value={f.zone} onChange={set("zone")} />
          </Field>
        </FieldGroup>
      </fieldset>
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || !valid} onClick={save}>
          {editing ? tr("common.save") : tr("gcp.add_profile")}
        </button>
        <button className="ghost" disabled={busy} onClick={close}>
          {tr("common.cancel")}
        </button>
        <span className="req-note">
          <b>*</b> {tr("ssm.req_note")}
        </span>
      </div>
    </div>
  );

  return (
    <div className="ssm-tab gcp-tab">
      <p className="field-help">
        {tr("gcp.intro_1")}
        <code>af-gcloud-exec</code>
        {tr("gcp.intro_2")}
        <b>{tr("gcp.intro_bold")}</b>
        {tr("gcp.intro_3")}
      </p>
      <section className="ds-group">
        <div className="ds-title">{tr("gcp.profile_cat")}</div>
        {loadErr && <p className="ssm-load-err">{tr(profiles === null ? "ssm.load_failed" : "ssm.refresh_failed", { msg: loadErr })}</p>}
        {profiles === null ? (
          !loadErr && <p className="muted pad">{tr("common.loading")}</p>
        ) : profiles.length === 0 ? (
          <p className="muted">{tr("gcp.empty")}</p>
        ) : (
          <ul className="ssm-list">
            {profiles.map((p) => (
              <li key={p.id} className="ssm-item" data-profile={p.id}>
                <div className="ssm-item-head">
                  <span className="ssm-alias">{p.label}</span>
                  {p.name && (
                    <code className="gcp-name" title={tr("gcp.name_title", { config: "af-" + p.name })}>
                      {p.name}
                    </code>
                  )}
                  <button
                    className="ghost ssm-edit push"
                    title={tr("gcp.edit_title")}
                    disabled={busy || editing?.id === p.id}
                    onClick={() => startEdit(p)}
                  >
                    {tr("ssm.edit")}
                  </button>
                  <button className="ghost danger ssm-del" title={tr("common.delete")} disabled={busy} onClick={() => remove(p.id)}>
                    {tr("common.delete")}
                  </button>
                </div>
                {p.conflict && (
                  <p className="ssm-edit-warn gcp-conflict">
                    {tr("gcp.conflict", { name: p.name, labels: (p.conflict.labels || []).join(", ") })}
                  </p>
                )}
                {editing?.id === p.id ? (
                  form
                ) : (
                  <div className="ssm-meta">
                    <Meta k={tr("gcp.f_project")} v={p.project} />
                    <Meta k={tr("gcp.f_quota_project")} v={p.quotaProject || tr("gcp.same_as_project")} mono={!!p.quotaProject} />
                    <Meta k={tr("gcp.f_account")} v={p.account || tr("gcp.account_at_login")} mono={!!p.account} />
                    <Meta k={tr("gcp.f_region")} v={p.region} />
                    <Meta k={tr("gcp.f_zone")} v={p.zone} />
                    <Meta k={tr("gcp.f_impersonate")} v={p.impersonateServiceAccount} wide />
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
        {open
          ? form
          : !editing && (
              <button className="ghost ssm-add-toggle" onClick={startAdd}>
                <Icon name="add" /> {tr("gcp.add_profile")}
              </button>
            )}
      </section>
    </div>
  );
}
