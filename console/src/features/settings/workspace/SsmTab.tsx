import { useCallback, useEffect, useRef, useState } from "react";
import type { ChangeEvent, ReactNode, RefObject } from "react";
import { api, raw, rawJSON } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { SSM_HOST_COLORS, hostColorBase, termBackground } from "../../../lib/termcolor.ts";
import { useT, t, type MsgKey } from "../../../lib/i18n/index.ts";
import { Field, Meta } from "../parts/mcpForm.tsx";
import { ProfileLoginModal, type LoginProfile } from "../../awslogin/ProfileLoginModal.tsx";

// SsmTab manages the member's own AWS profiles and SSM hosts (docs/log/p3-ssm-session.md)
// in two tiers so the form isn't cluttered:
//   profile (shared) = the shared auth bundle (SSO portal + account/role/region); many
//                      hosts reuse one. Maps to a ~/.aws named profile.
//   host (per-instance) = per-instance only (alias, instance id, run-as document,
//                      optional region override) + which profile to use.
// NO AWS secrets are stored or entered here — at session start the in-container aws CLI
// runs `aws sso login` (device-code URL surfaced in the terminal) and caches the
// short-lived token in the workspace home.

// postJSON POSTs/PUTs and surfaces failures loudly (a stale CP without the routes
// returns 404 non-JSON, which would otherwise be swallowed). Returns true on success.
async function postJSON(path: string, method: string, body: unknown, toast: (msg: string) => void): Promise<boolean> {
  let res;
  try {
    res = await rawJSON(path, method, body);
  } catch (e: any) {
    toast(t("ssm.comm_failed", { msg: String(e?.message || e) }));
    return false;
  }
  if (!res.ok) {
    toast(t("ssm.save_failed_http", { status: res.status, detail: await failDetail(res) }));
    return false;
  }
  return true;
}

// deleteRow DELETEs and, like postJSON, says why it failed. Callers clean up only on true:
// a refused delete leaves the row, and its form and marks with it.
async function deleteRow(path: string, toast: (msg: string) => void): Promise<boolean> {
  let res;
  try {
    res = await raw(path, { method: "DELETE" });
  } catch (e: any) {
    toast(t("ssm.comm_failed", { msg: String(e?.message || e) }));
    return false;
  }
  if (!res.ok) {
    toast(t("ssm.delete_failed_http", { status: res.status, detail: await failDetail(res) }));
    return false;
  }
  return true;
}

async function failDetail(res: Response): Promise<string> {
  const j = await res.json().catch(() => null);
  return j?.error?.message ? " — " + j.error.message : res.status === 404 ? t("ssm.save_failed_404") : "";
}

// Meta / Field reuse the shared primitives from mcpForm.tsx (they were identical).
// FieldGroup / Field build the labeled add-form: a bordered group holding fields that
// each carry a label, an optional required * marker, and a hint (where the value comes
// from and its format). Required vs optional is conveyed per-field (the * marker + hint),
// so one group suffices — no separate required / advanced boxes. `title` is optional
// (omitted for the single merged group).
function FieldGroup({ title, optional, children }: { title?: ReactNode; optional?: ReactNode; children: ReactNode }) {
  return (
    <div className="ssm-fgroup">
      {title && (
        <div className="ssm-fg-title">
          {title}
          {optional && <span className="opt"> {optional}</span>}
        </div>
      )}
      <div className="ssm-fgrid">{children}</div>
    </div>
  );
}

export function SsmTab() {
  const tr = useT();
  const [profiles, setProfiles] = useState<any[] | null>(null);
  const [hosts, setHosts] = useState<any[] | null>(null);
  const profileLabelRef = useRef<HTMLInputElement>(null);
  // The profile add-form open state is lifted here so the host form's "add a profile"
  // CTA (「プロファイルを追加」) can expand it (and scroll to it) when none exists yet.
  const [profileOpen, setProfileOpen] = useState(false);

  const reload = useCallback(() => {
    api("api/ssm/profiles").then((d) => setProfiles(Array.isArray(d) ? d : [])).catch(() => setProfiles([]));
    api("api/ssm/hosts").then((d) => setHosts(Array.isArray(d) ? d : [])).catch(() => setHosts([]));
  }, []);
  useEffect(reload, [reload]);

  const focusProfile = () => {
    setProfileOpen(true);
    requestAnimationFrame(() => profileLabelRef.current?.scrollIntoView({ block: "center" }));
  };

  return (
    <div className="ssm-tab">
      <p className="field-help">
        {tr("ssm.intro_1")}
        <b>{tr("ssm.intro_bold")}</b>
        {tr("ssm.intro_2")}
        <code>aws sso login</code>
        {tr("ssm.intro_3")}
      </p>
      <ProfileSection
        profiles={profiles}
        hosts={hosts}
        reload={reload}
        labelRef={profileLabelRef}
        open={profileOpen}
        setOpen={setProfileOpen}
      />
      <HostSection hosts={hosts} profiles={profiles} reload={reload} onNeedProfile={focusProfile} />
    </div>
  );
}

// --- profiles (common) ----------------------------------------------------------

// The Agent's login states (GET /api/aws-login/profiles). No time is shown: the cache knows
// only the access token's expiry, which the CLI renews until the portal session ends (#1029).
const STATE_KEYS: Record<string, { label: MsgKey; title: MsgKey }> = {
  signed_in: { label: "ssm.state_signed_in", title: "ssm.state_signed_in_title" },
  renew: { label: "ssm.state_renew", title: "ssm.state_renew_title" },
  none: { label: "ssm.state_none", title: "ssm.state_none_title" },
};

const emptyProfile: Record<string, string> = { label: "", startUrl: "", ssoRegion: "", accountId: "", roleName: "", region: "" };

// awsProfileName mirrors the CP's ssmProfileName (control-plane/ssm.go): the ~/.aws profile
// name a label becomes. The workspace keys a profile's sign-in by it (sso-session af-<name>),
// so an edit that changes it leaves the profile signed out under its new name.
export function awsProfileName(label: string): string {
  return label.trim().replace(/[^A-Za-z0-9._@-]+/g, "-") || "ssm";
}

// Profiles saved with a new workspace name or a new portal, which need a fresh login before
// the Agent's badge means anything: its states come from the last 5-minute poll and its token
// cache is keyed by name alone, so it can show a renamed row nothing and a re-pointed row the
// old portal's "Signed in". Module scope so closing and reopening Settings keeps them; cleared
// by a login from the row, which the Agent starts only after re-reading Settings.
const reloginNeeded = new Set<string>();

/** Test hook: forget the marks between cases (they outlive a mount on purpose). */
export function resetReloginMarks(): void {
  reloginNeeded.clear();
}

// pick copies a row's form fields as strings, so a field the CP left out edits as "".
function pick(row: any, empty: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const k of Object.keys(empty)) out[k] = row?.[k] == null ? "" : String(row[k]);
  return out;
}

// authMoved: the edit points the profile at another portal (start URL or SSO region).
function authMoved(was: any, now: { startUrl: string; ssoRegion: string }): boolean {
  return now.startUrl !== String(was.startUrl || "") || now.ssoRegion !== String(was.ssoRegion || "");
}

type FieldEvent = ChangeEvent<HTMLInputElement | HTMLSelectElement>;

function ProfileSection({
  profiles,
  hosts,
  reload,
  labelRef,
  open,
  setOpen,
}: {
  profiles: any[] | null;
  hosts: any[] | null;
  reload: () => void;
  labelRef: RefObject<HTMLInputElement | null>;
  open: boolean;
  setOpen: (v: boolean) => void;
}) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const [f, setF] = useState<Record<string, string>>(emptyProfile);
  const [busy, setBusy] = useState(false);
  // The row being edited; its form replaces the row's details. Never open together with
  // the add form, which shares f.
  const [editing, setEditing] = useState<any | null>(null);
  const [loginFor, setLoginFor] = useState<(LoginProfile & { id: string }) | null>(null);
  // Bumped when reloginNeeded changes, which React cannot see.
  const [, setMarks] = useState(0);
  // Each row's login state, from the Agent (absent while the workspace is stopped). Asked on
  // open and after the login modal closes; nothing polls.
  const [states, setStates] = useState<Record<string, string>>({});
  const loadStates = useCallback(() => {
    api("api/aws-login/profiles")
      .then((d) => {
        const m: Record<string, string> = {};
        for (const p of Array.isArray(d?.profiles) ? d.profiles : []) m[String(p.name)] = String(p.state);
        setStates(m);
      })
      .catch(() => setStates({}));
  }, []);
  useEffect(loadStates, [loadStates]);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const valid = f.label.trim() && /^https:\/\//.test(f.startUrl.trim()) && f.ssoRegion.trim();
  // Why a row cannot log in from here (null when it can; "" when a CP too old to send the
  // name leaves nothing to say). The Agent refuses the same rows; saying so up front beats
  // a failed press. The button is also off while a save or delete is out: a login begun
  // before the PUT lands signs in to the old portal, and its completion would clear the new
  // relogin mark.
  const loginOff = (p: any): string | null =>
    !p.name
      ? ""
      : p.nameCollides
        ? tr("ssm.login_off_collides", { name: p.name })
        : !p.accountId || !p.roleName
          ? tr("ssm.login_off_incomplete")
          : null;

  const close = () => {
    setOpen(false);
    setEditing(null);
    setF(emptyProfile);
  };
  // A row that leaves the list (deleted here or elsewhere) takes its form with it; otherwise
  // the section would keep hiding Add for a form it no longer renders.
  useEffect(() => {
    if (editing && profiles && !profiles.some((p) => p.id === editing.id)) close();
  }, [profiles, editing]);
  const startAdd = () => {
    setEditing(null);
    setF(emptyProfile);
    setOpen(true);
  };
  const startEdit = (p: any) => {
    setOpen(false);
    setF(pick(p, emptyProfile));
    setEditing(p);
  };
  // An edit keeps the profile's id, so the hosts that use it follow; delete and re-add
  // would leave them on an id that no longer exists (ssm_host.profile_id has no FK).
  const save = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const body = {
        label: f.label.trim(),
        startUrl: f.startUrl.trim(),
        ssoRegion: f.ssoRegion.trim(),
        accountId: f.accountId.trim(),
        roleName: f.roleName.trim(),
        region: f.region.trim(),
      };
      const ok = editing
        ? await postJSON(`api/ssm/profiles/${encodeURIComponent(editing.id)}`, "PUT", body, toast)
        : await postJSON("api/ssm/profiles", "POST", body, toast);
      if (!ok) return;
      if (editing && (awsProfileName(String(editing.label || "")) !== awsProfileName(body.label) || authMoved(editing, body))) {
        reloginNeeded.add(editing.id);
        setMarks((n) => n + 1);
      }
      close();
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    const using = (hosts || []).filter((h) => h.profileId === id).length;
    const ok = await askConfirm({
      title: tr("ssm.profile_del_title"),
      body: using > 0 ? tr("ssm.profile_del_body_hosts", { n: using }) : tr("ssm.profile_del_body"),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (!ok) return;
    // busy holds every row and the form still while the DELETE is out, so no later edit can be
    // started and then lost. The open form closes through the list effect, once the row is gone.
    setBusy(true);
    try {
      if (!(await deleteRow(`api/ssm/profiles/${encodeURIComponent(id)}`, toast))) return;
      reloginNeeded.delete(id);
      reload();
    } finally {
      setBusy(false);
    }
  };

  // What an edit changes outside this page: the sign-in is cached per profile name, so a
  // rename starts signed out, and a new portal under the same name would reuse a token
  // issued by the old one.
  const was = editing ? awsProfileName(String(editing.label || "")) : "";
  const now = awsProfileName(f.label);
  const portalChanged = !!editing && authMoved(editing, { startUrl: f.startUrl.trim(), ssoRegion: f.ssoRegion.trim() });
  const form = (
    <div className={"ssm-frm" + (editing ? " ssm-frm-edit" : "")}>
      <fieldset className="ssm-fieldset" disabled={busy}>
        <FieldGroup>
          <Field label={tr("ssm.f_label")} req hint={tr("ssm.f_label_hint")}>
            <input
              ref={labelRef}
              className="cinput"
              placeholder="my-profile"
              value={f.label}
              onChange={set("label")}
              autoFocus
            />
          </Field>
          <Field label={tr("ssm.meta_sso_region")} req hint={tr("ssm.f_sso_region_hint")}>
            <input className="cinput" placeholder="ap-northeast-1" value={f.ssoRegion} onChange={set("ssoRegion")} />
          </Field>
          <Field
            label="start URL"
            req
            wide
            hint={
              <>
                {tr("ssm.f_starturl_hint_1")}
                <code>https://…awsapps.com/start</code>
                {tr("ssm.f_starturl_hint_2")}
              </>
            }
          >
            <input
              className="cinput"
              placeholder="https://my-company.awsapps.com/start"
              value={f.startUrl}
              onChange={set("startUrl")}
            />
          </Field>
          <Field label={tr("ssm.f_account_id")} hint={tr("ssm.f_optional_login_pick")}>
            <input className="cinput" placeholder="123456789012" value={f.accountId} onChange={set("accountId")} />
          </Field>
          <Field label={tr("ssm.f_role_name")} hint={tr("ssm.f_optional_login_pick")}>
            <input className="cinput" placeholder="AdministratorAccess" value={f.roleName} onChange={set("roleName")} />
          </Field>
          <Field label={tr("ssm.meta_default_region")} hint={tr("ssm.f_default_region_hint")}>
            <input className="cinput" placeholder="ap-northeast-1" value={f.region} onChange={set("region")} />
          </Field>
        </FieldGroup>
      </fieldset>
      {editing && (
        <div className="field-help ssm-edit-notes">
          {was !== now ? (
            <p className="ssm-edit-warn">{tr("ssm.edit_rename_warn", { from: was, to: now })}</p>
          ) : (
            portalChanged && <p className="ssm-edit-warn">{tr("ssm.edit_portal_warn")}</p>
          )}
          <p>
            {tr("ssm.edit_profile_note_1")}
            <code>~/.aws/config</code>
            {tr("ssm.edit_profile_note_2")}
          </p>
        </div>
      )}
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || !valid} onClick={save}>
          {editing ? tr("common.save") : tr("ssm.add_profile")}
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
    <section className="ssm-section">
      <div className="conn-cat">{tr("ssm.profile_cat")}</div>
      <div className="field-help">
        {tr("ssm.profile_help_1")}
        <code>~/.aws</code>
        {tr("ssm.profile_help_2")}
      </div>
      {profiles === null ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : profiles.length === 0 ? (
        <p className="muted">{tr("ssm.profile_empty")}</p>
      ) : (
        <ul className="ssm-list">
          {profiles.map((p) => (
            <li key={p.id} className="ssm-item">
              <div className="ssm-item-head">
                <span className="ssm-alias">{p.label}</span>
                {reloginNeeded.has(p.id) ? (
                  <span className="ssm-login-state relogin" title={tr("ssm.state_relogin_title")}>
                    {tr("ssm.state_relogin")}
                  </span>
                ) : (
                  p.name &&
                  STATE_KEYS[states[p.name]] && (
                  <span
                    className={"ssm-login-state " + states[p.name]}
                    title={tr(STATE_KEYS[states[p.name]].title)}
                  >
                    {tr(STATE_KEYS[states[p.name]].label)}
                  </span>
                  )
                )}
                <button
                  className="ghost ssm-login"
                  title={loginOff(p) || tr("ssm.login_title")}
                  disabled={busy || loginOff(p) !== null}
                  onClick={() => setLoginFor({ id: p.id, name: p.name, label: p.label, accountId: p.accountId, roleName: p.roleName })}
                >
                  {tr("ssm.login")}
                </button>
                <button
                  className="ghost ssm-edit"
                  title={tr("ssm.edit_profile_title")}
                  disabled={busy || editing?.id === p.id}
                  onClick={() => startEdit(p)}
                >
                  {tr("ssm.edit")}
                </button>
                <button className="ghost danger ssm-del" title={tr("common.delete")} disabled={busy} onClick={() => remove(p.id)}>
                  {tr("common.delete")}
                </button>
              </div>
              {editing?.id === p.id ? (
                form
              ) : (
                <div className="ssm-meta">
                  <Meta k={tr("ssm.meta_account")} v={p.accountId} />
                  <Meta k={tr("ssm.meta_role")} v={p.roleName} />
                  <Meta k={tr("ssm.meta_default_region")} v={p.region} />
                  <Meta k={tr("ssm.meta_sso_region")} v={p.ssoRegion} />
                  <Meta k="start URL" v={p.startUrl} wide />
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
              <Icon name="add" /> {tr("ssm.add_profile")}
            </button>
          )}
      {loginFor && (
        <ProfileLoginModal
          profile={loginFor}
          onClose={() => {
            setLoginFor(null);
            loadStates();
          }}
          onLoggedIn={() => {
            reloginNeeded.delete(loginFor.id);
            setMarks((n) => n + 1);
          }}
        />
      )}
    </section>
  );
}

// --- hosts (per-instance) -------------------------------------------------------

// HostColorPicker chooses the terminal background hue for a host's sessions. The
// choice is a per-user setting (synced), applied to a session's terminal when it is
// created (new sessions to this host pick it up). Swatches show the vivid hue; the
// terminal itself renders a subtle dark tint of it.
function HostColorPicker({ hostId }: { hostId: string }) {
  const tr = useT();
  const settings = useSettings();
  const cur = settings.ssmHostColors?.[hostId] || "auto";
  const setColor = (id: string) =>
    setSetting("ssmHostColors", { ...(settings.ssmHostColors || {}), [hostId]: id });
  return (
    <div className="ssm-host-color">
      <span className="ssm-meta-k">{tr("ssm.term_color")}</span>
      <div className="ssm-swatches">
        {SSM_HOST_COLORS.map((c) => {
          const base = c.base || hostColorBase("auto", hostId); // vivid identity color
          return (
            <button
              key={c.id}
              type="button"
              className={"ssm-swatch" + (cur === c.id ? " active" : "")}
              title={c.id === "auto" ? tr("ssm.color_auto_title") : tr(c.labelKey)}
              style={{ background: base }}
              onClick={() => setColor(c.id)}
            >
              {c.id === "auto" ? "A" : ""}
            </button>
          );
        })}
        <span className="ssm-swatch-preview" title={tr("ssm.term_preview_title")} style={{ background: termBackground("ssm", hostColorBase(cur, hostId)) }}>
          {tr("ssm.term_label")}
        </span>
      </div>
    </div>
  );
}

const emptyHost: Record<string, string> = { alias: "", profileId: "", instanceId: "", documentName: "", region: "" };

function HostSection({
  hosts,
  profiles,
  reload,
  onNeedProfile,
}: {
  hosts: any[] | null;
  profiles: any[] | null;
  reload: () => void;
  onNeedProfile: () => void;
}) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const [f, setF] = useState<Record<string, string>>(emptyHost);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  // The host being edited (its id); its form replaces the row's details.
  const [editing, setEditing] = useState<string | null>(null);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const profileOf = (id: string) => (profiles || []).find((p) => p.id === id);
  const noProfiles = profiles !== null && profiles.length === 0;
  // The profile must be one the current list holds: a dead id (deleted while the form was
  // open, or the list not loaded yet) would only come back from the CP as bad_profile.
  const valid = f.alias.trim() && f.instanceId.trim() && !!profileOf(f.profileId);

  const close = () => {
    setOpen(false);
    setEditing(null);
    setF(emptyHost);
  };
  useEffect(() => {
    if (editing && hosts && !hosts.some((h) => h.id === editing)) close();
  }, [hosts, editing]);
  // Drop a picked profile that the refreshed list no longer has, so the select and what Save
  // sends agree.
  useEffect(() => {
    if (profiles !== null && f.profileId && !profiles.some((p) => p.id === f.profileId)) {
      setF((p) => ({ ...p, profileId: "" }));
    }
  }, [profiles, f.profileId]);
  const startAdd = () => {
    setEditing(null);
    setF(emptyHost);
    setOpen(true);
  };
  const startEdit = (h: any) => {
    setOpen(false);
    setF(pick(h, emptyHost));
    setEditing(h.id);
  };
  const save = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const body = {
        alias: f.alias.trim(),
        profileId: f.profileId,
        instanceId: f.instanceId.trim(),
        documentName: f.documentName.trim(),
        region: f.region.trim(),
      };
      const ok = editing
        ? await postJSON(`api/ssm/hosts/${encodeURIComponent(editing)}`, "PUT", body, toast)
        : await postJSON("api/ssm/hosts", "POST", body, toast);
      if (!ok) return;
      close();
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    const ok = await askConfirm({
      title: tr("ssm.host_del_title"),
      body: tr("ssm.host_del_body"),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      if (!(await deleteRow(`api/ssm/hosts/${encodeURIComponent(id)}`, toast))) return;
      reload();
    } finally {
      setBusy(false);
    }
  };

  const form = (
    <div className={"ssm-frm" + (editing ? " ssm-frm-edit" : "")}>
      <fieldset className="ssm-fieldset" disabled={busy}>
        <FieldGroup>
          <Field label={tr("ssm.f_use_profile")} req wide hint={tr("ssm.f_use_profile_hint")}>
            <select className="cinput" value={f.profileId} onChange={set("profileId")} autoFocus>
              <option value="">{tr("ssm.select_profile")}</option>
              {(profiles || []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label={tr("ssm.f_alias")} req hint={tr("ssm.f_alias_hint")}>
            <input className="cinput" placeholder="admin@web-01" value={f.alias} onChange={set("alias")} />
          </Field>
          <Field label={tr("ssm.f_instance_id")} req hint={<>{tr("ssm.f_instance_hint_1")}<code>aws ec2 describe-instances</code>{tr("ssm.f_instance_hint_2")}</>}>
            <input className="cinput" placeholder="i-0123456789abcdef0" value={f.instanceId} onChange={set("instanceId")} />
          </Field>
          <Field label={tr("ssm.f_document")} hint={tr("ssm.f_document_hint")}>
            <input className="cinput" placeholder="SSM-SessionManagerRunShell" value={f.documentName} onChange={set("documentName")} />
          </Field>
          <Field label={tr("ssm.meta_region")} hint={tr("ssm.f_region_hint")}>
            <input className="cinput" placeholder={tr("ssm.f_region_placeholder")} value={f.region} onChange={set("region")} />
          </Field>
        </FieldGroup>
      </fieldset>
      {editing && <p className="field-help ssm-edit-notes">{tr("ssm.edit_host_note")}</p>}
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || !valid} onClick={save}>
          {editing ? tr("common.save") : tr("ssm.add_host")}
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
    <section className="ssm-section">
      <div className="conn-cat">{tr("ssm.host_cat")}</div>
      <div className="field-help">
        {tr("ssm.host_help_1")}
        <code>aws ssm start-session --target &lt;instance&gt; --document-name &lt;document&gt;</code>
        {tr("ssm.host_help_2")}
      </div>
      {hosts === null ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : hosts.length === 0 ? (
        <p className="muted">{tr("ssm.host_empty")}</p>
      ) : (
        <ul className="ssm-list">
          {hosts.map((h) => {
            const prof = profileOf(h.profileId);
            return (
              <li key={h.id} className="ssm-item">
                <div className="ssm-item-head">
                  <span className="ssm-alias">{h.alias}</span>
                  <button
                    className="ghost ssm-edit push"
                    title={tr("ssm.edit_host_title")}
                    disabled={busy || editing === h.id}
                    onClick={() => startEdit(h)}
                  >
                    {tr("ssm.edit")}
                  </button>
                  <button className="ghost danger ssm-del" title={tr("common.delete")} disabled={busy} onClick={() => remove(h.id)}>
                    {tr("common.delete")}
                  </button>
                </div>
                {editing === h.id ? (
                  form
                ) : (
                  <>
                    <div className="ssm-meta">
                      <Meta k={tr("ssm.meta_instance")} v={h.instanceId} />
                      <Meta k={tr("ssm.meta_document")} v={h.documentName} />
                      <Meta k={tr("ssm.meta_region")} v={h.region} />
                      <Meta
                        k={tr("ssm.meta_profile")}
                        v={prof ? prof.label : profiles === null ? "" : <span className="ssm-missing">{tr("ssm.profile_missing")}</span>}
                        mono={false}
                      />
                    </div>
                    <HostColorPicker hostId={h.id} />
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {noProfiles && !editing ? (
        <div className="ssm-dep">
          <span className="i">
            <Icon name="info" />
          </span>
          <span className="t">{tr("ssm.need_profile")}</span>
          <button className="primary" onClick={onNeedProfile}>
            {tr("ssm.add_profile")}
          </button>
        </div>
      ) : open ? (
        form
      ) : (
        !editing && (
          <button className="ghost ssm-add-toggle" onClick={startAdd}>
            <Icon name="add" /> {tr("ssm.add_host")}
          </button>
        )
      )}
    </section>
  );
}
