import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { ConfirmDialog } from "../../../ui/ConfirmDialog.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { useT, useLocale } from "../../../lib/i18n/index.ts";
import { Field } from "../parts/mcpForm.tsx";
import type { TenantLoginFields, TenantIdP } from "./tenantLoginTypes.ts";
import type { DeployProvider } from "./tenantLoginRules.tsx";
import { ruleStateFor, ruleLocks, toggleRule, useDeploymentProviders } from "./tenantLoginRules.tsx";

// Map the row's status onto what the reader actually wants to know — not the state name, but
// whether anyone can sign in with this method right now.
type IdPStatusKey =
  | "admin.idp_state_active"
  | "admin.idp_state_broken"
  | "admin.idp_state_suspended"
  | "admin.idp_state_pending";

// idpSource states in one line where the row's identities come from. For OIDC the issuer is the
// answer, but every GitHub row shares the issuer github.com, so printing it distinguishes
// nothing — what actually applies there is the organisation (docs/log/61 §61.15).
function idpSource(row: TenantIdP): string {
  if (row.kind === "github") return "GitHub: " + (row.allowed_orgs || "");
  return row.issuer;
}

function idpStatusKey(row: TenantIdP): IdPStatusKey {
  if (row.status === "active") return row.usable ? "admin.idp_state_active" : "admin.idp_state_broken";
  if (row.status === "suspended") return "admin.idp_state_suspended";
  return "admin.idp_state_pending";
}

const emptyIdP = (): TenantIdP => ({
  id: "",
  name: "",
  kind: "oidc",
  issuer: "",
  client_id: "",
  client_secret: "",
  trust: "issuer",
  allowed_domains: "",
  allowed_tids: "",
  allowed_orgs: "",
});

// TenantSignInMethods — every sign-in method usable in this tenant (docs/log/61 §61.17.5).
//
// One list holds both the tenant's own rows (creatable, editable, needing approval) and the
// deployment methods, i.e. the default tenant's methods (badged "deployment-wide"「デプロイ共通」
// and not editable). Each row carries two toggles: accept, and show as a button. The point is
// that the screen shows the whole gate; when deployment methods were not listed, a company
// signing in with Google every day still saw this view empty (§61.17).
//
// "Add a method" means create a new one only. There is deliberately no operation to *reference*
// a default-tenant method: that would be the same single bit as the accept toggle, and giving
// one thing two names produces the belief that a referenced row can be edited (§61.17.5). So
// unreferenced rows are listed from the start, simply with the toggle off.
//
// Two things this screen must convey; dropping either stalls a subsidiary's onboarding:
//
//  1. A new method does nothing until the deployment admin approves it. The state chip says so,
//     and the sign-in URL is only shown after approval — handing out a URL whose page has no
//     button only produces support questions (docs/log/61 §61.14).
//  2. The accepted domains are not an optional field. Approval is granted for "this issuer may
//     be trusted within this scope", so the scope is the thing being approved.
//
// Only a super_admin can flip the toggles (PUT .../login is fixed at withSuperAdmin; decision 19
// is unchanged). A tenant admin sees the same state as static chips: an un-pressable toggle
// offers an ability and then refuses it, so a view without the ability carries no control.
export function TenantSignInMethods({
  slug,
  isSuper,
  tenant,
  onChanged,
}: {
  slug: string;
  isSuper: boolean;
  /** The four login-rule columns; this view reads and writes the two provider columns. */
  tenant?: TenantLoginFields | null;
  onChanged?: () => void;
}) {
  const tr = useT();
  const locale = useLocale();
  const toast = useToast();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  const [form, setForm] = useState<TenantIdP | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState<TenantIdP | null>(null);
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/idp`;
  const deployment = useDeploymentProviders();

  const load = useCallback(async () => {
    const res = await api(base);
    setRows(res?.providers || []);
  }, [base]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    if (!form) return;
    setBusy(true);
    try {
      const body = {
        name: form.name.trim(),
        label_ja: (form.label_ja || "").trim(),
        label_en: (form.label_en || "").trim(),
        kind: form.kind || "oidc",
        issuer: form.issuer.trim(),
        client_id: form.client_id.trim(),
        // Left empty while editing means keep the stored secret (the server merges), which is
        // why this field starts empty rather than pre-filled with a mask.
        client_secret: (form.client_secret || "").trim(),
        trust: form.trust,
        allowed_tids: (form.allowed_tids || "").trim(),
        allowed_domains: (form.allowed_domains || "").trim(),
        allowed_orgs: (form.allowed_orgs || "").trim(),
        link_claim: (form.link_claim || "").trim(),
      };
      const res = form.id
        ? await apiJSON(`${base}/${encodeURIComponent(form.id)}`, "PUT", body)
        : await apiJSON(base, "POST", body);
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setForm(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  // Suspend is the one action that can ask back once (docs/log/61 §61.17.4). The CP answers 409
  // with "N active members have only ever used this method": suspending locks them out, and
  // they cannot add another method themselves, because linking one requires signing in with the
  // very method being suspended. It is a confirmation rather than a refusal because suspend is
  // also how a leaked IdP is stopped, and stopping may always be faster than starting.
  const [confirmSuspend, setConfirmSuspend] = useState<{ row: TenantIdP; members: number } | null>(null);

  const setStatus = async (row: TenantIdP, status: string, confirm?: boolean) => {
    setBusy(true);
    try {
      const q = confirm ? "?confirm=1" : "";
      const res = await apiJSON(`${base}/${encodeURIComponent(row.id)}/status${q}`, "POST", { status });
      if (res?.error?.code === "tenant_idp_last_method_for_members") {
        // Only the server knows the count. Take the number and put it into our own wording
        // rather than showing the CP's sentence: the display language is the Console's.
        setConfirmSuspend({ row, members: Number(res.members) || 0 });
        return;
      }
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirmSuspend(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (row: TenantIdP) => {
    setBusy(true);
    try {
      const res = await apiJSON(`${base}/${encodeURIComponent(row.id)}`, "DELETE");
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirmDel(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();
  const anyActive = (rows || []).some((r) => r.status === "active" && r.usable);

  // --- Unified list (docs/log/61 §61.17.5) ---------------------------------------
  //
  // Order is deployment methods first, then the tenant's own rows. The id set is fixed in that
  // order and reused for the CSV export: an order that changes on every save makes the audit
  // log unreadable.
  const deployRows = Array.isArray(deployment) ? deployment : [];
  const ownRows = rows || [];
  const knownIds = [...deployRows.map((p) => p.id), ...ownRows.map((r) => r.provider_id || "").filter(Boolean)];
  // usable = the methods someone can actually sign in with right now: every deployment method,
  // plus own rows that are approved and not broken. This is the ordering rule of §61.17.5.
  const usableIds = [
    ...deployRows.map((p) => p.id),
    ...ownRows.filter((r) => r.status === "active" && r.usable).map((r) => r.provider_id || ""),
  ].filter(Boolean);

  // The toggles may only be touched once both lists are in (so knownIds is real) and the rules
  // themselves have been read. Saving while the deployment methods are unread would run the
  // "all on means empty" normalisation over a set that silently dropped the unknown ids, and so
  // restrict a tenant nobody meant to restrict.
  const rulesReady = isSuper && deployment !== null && deployment !== "error" && rows !== null && !!tenant;

  const toggle = async (id: string, field: "accepted" | "shown", value: boolean) => {
    if (!rulesReady || !tenant) return;
    const next = toggleRule(knownIds, tenant.allowed_providers, tenant.hidden_providers, id, field, value);
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/login`, "PUT", {
        ...next,
        // This PUT replaces all four columns. This view does not own the two domain columns, so
        // it echoes back what it read: omitting them overwrites them with empty and the invite
        // restriction disappears.
        auto_join_domains: (tenant.auto_join_domains || "").trim(),
        allowed_domains: (tenant.allowed_domains || "").trim(),
      });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      onChanged?.();
    } finally {
      setBusy(false);
    }
  };

  // The two toggles for one row: an operable checkbox for a super_admin, the same state as a
  // static chip for everyone else (an un-pressable toggle offers an ability and then refuses).
  const toggles = (id: string) => {
    // The CP always builds provider_id (tenant_idp_api.go). Should a row arrive without one,
    // render no toggle: a control that responds to a click by doing nothing is harder to read
    // than an obviously broken one.
    if (!id) return null;
    const { accepted, shown } = ruleStateFor(knownIds, tenant?.allowed_providers, tenant?.hidden_providers, id);
    const locks = ruleLocks(knownIds, usableIds, tenant?.allowed_providers, tenant?.hidden_providers, id);
    if (!isSuper) {
      return (
        <span className="idp-flags">
          <span className={"idp-flag" + (accepted ? " on" : "")}>{tr("admin.idp_accept")}</span>
          <span className={"idp-flag" + (shown ? " on" : "")}>{tr("admin.idp_show")}</span>
        </span>
      );
    }
    // Two cases cannot be switched off. Both would end as "meant to restrict, actually wide
    // open / setting ignored", so the control is locked up front rather than apologised for
    // after the save (§61.17.5).
    const acceptLocked = accepted && locks.acceptOffLocked;
    const showLocked = shown && locks.showOffLocked;
    return (
      <span className="idp-flags">
        <label className={"idp-flag" + (accepted ? " on" : "")} title={acceptLocked ? tr("admin.idp_accept_last") : undefined}>
          <input
            type="checkbox"
            checked={accepted}
            disabled={busy || !rulesReady || acceptLocked}
            onChange={(e) => toggle(id, "accepted", e.target.checked)}
          />
          <span>{tr("admin.idp_accept")}</span>
        </label>
        {/* "show" is subordinate to "accept": the render path requires allowed even inside the
            hidden check, so turning "show" on for a row that is not accepted does nothing. */}
        <label
          className={"idp-flag" + (shown ? " on" : "")}
          title={!accepted ? tr("admin.idp_show_needs_accept") : showLocked ? tr("admin.idp_show_last") : undefined}
        >
          <input
            type="checkbox"
            checked={shown}
            disabled={busy || !rulesReady || !accepted || showLocked}
            onChange={(e) => toggle(id, "shown", e.target.checked)}
          />
          <span>{tr("admin.idp_show")}</span>
        </label>
      </span>
    );
  };

  const deployLabel = (p: DeployProvider) =>
    (locale === "en" ? p.label_en : p.label_ja) || p.label_ja || p.label_en || p.id;

  // Is there any accepted-but-not-shown row (i.e. do we print the note about bare /login)?
  const anyHidden = knownIds.some((id) => {
    const s = ruleStateFor(knownIds, tenant?.allowed_providers, tenant?.hidden_providers, id);
    return s.accepted && !s.shown;
  });

  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_title")}
        <span className="af-note">{tr("admin.idp_note")}</span>
      </h4>
      <p className="admin-hint">{tr("admin.idp_hint")}</p>
      {/* Deployment methods are the default tenant's methods (§61.17). They cannot be edited
          (the operator holds the issuer, not the tenant admin), but whether to accept them and
          whether to show them as a button is this tenant's choice, so the row has toggles. */}
      {deployment === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : deployment === "error" ? (
        <p className="admin-hint">{tr("admin.providers_unreadable")}</p>
      ) : deployment.length === 0 ? (
        // Genuinely zero (AUTH=dev / proxy, or nothing configured in env). This must read
        // differently from "could not be read" (§61.17.9).
        <p className="admin-hint">{tr("admin.providers_none")}</p>
      ) : (
        deployment.map((p) => (
          <div key={p.id} className="adm-mcp-row">
            {toggles(p.id)}
            <span className="as-name">{deployLabel(p)}</span>
            <code>{p.id}</code>
            {/* The issuer is only returned to a super_admin (§61.17.9). When absent, drop the
                column entirely: an empty cell reads as a missing setting. */}
            {p.issuer && (
              <span className="as-repo muted" title={p.issuer}>
                {p.issuer}
              </span>
            )}
            <span className="idp-state">{tr("admin.idp_deployment_wide")}</span>
          </div>
        ))
      )}
      {rows === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : rows.length === 0 ? (
        <p className="muted">{tr("admin.idp_none")}</p>
      ) : (
        rows.map((row) =>
          form && form.id === row.id ? (
            <IdPForm key={row.id} form={form} setForm={setForm} busy={busy} onSave={save} onCancel={() => setForm(null)} />
          ) : (
            <div key={row.id} className={"adm-mcp-row" + (row.status === "active" && row.usable ? "" : " off")}>
              {toggles(row.provider_id || "")}
              <span className="as-name mono" title={row.provider_id}>
                {row.name}
              </span>
              <span className="as-repo muted" title={idpSource(row)}>
                {idpSource(row)}
              </span>
              <span className={"idp-state idp-" + (row.status || "pending")}>{tr(idpStatusKey(row))}</span>
              <span className="allow-acts">
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setForm({ ...row, client_secret: "" })}>
                  {tr("mcp.edit")}
                </button>
                {/* Activation is the deployment admin's move and nobody else's; that single
                    asymmetry is why a tenant admin cannot promote themselves to deployment
                    admin (decision 30). Hiding the button is presentation only — the CP's
                    setStatus is what actually enforces it. */}
                {isSuper && row.status !== "active" && (
                  <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "active")}>
                    {tr("admin.idp_approve")}
                  </button>
                )}
                {row.status === "active" ? (
                  <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "suspended")}>
                    {tr("admin.idp_suspend")}
                  </button>
                ) : (
                  row.status === "suspended" && (
                    <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "pending")}>
                      {tr("admin.idp_reapply")}
                    </button>
                  )
                )}
                <button type="button" className="ghost xs danger" disabled={busy} onClick={() => setConfirmDel(row)}>
                  {tr("common.delete")}
                </button>
              </span>
            </div>
          ),
        )
      )}
      {form && form.id === "" ? (
        <IdPForm form={form} setForm={setForm} busy={busy} onSave={save} onCancel={() => setForm(null)} />
      ) : (
        !form && (
          <button type="button" className="ghost" onClick={() => setForm(emptyIdP())}>
            <Icon name="add" /> {tr("admin.idp_add")}
          </button>
        )
      )}
      {/* The side effect of restricting can only be stated on the screen of the person doing
          the restricting. Someone who belongs to several tenants may have signed in with
          another tenant's method, and un-accepting that method stops their tenant switch with
          provider_required (docs/log/61 §61.15). */}
      {isSuper && <p className="admin-hint">{tr("admin.allowed_providers_shared_note")}</p>}
      {/* The sign-in URL is only shown once something on that page works. Before then it is a
          page with no buttons, and a URL handed out early is worse than none. */}
      {anyActive && (
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      )}
      {/* Shown only to someone who turned "show" off. The one point left to make is that the
          method is still accepted, because "hidden" is read by some as "no longer usable". */}
      {anyHidden && <p className="admin-hint">{tr("admin.hidden_still_accepted_note")}</p>}
      {confirmDel && (
        <ConfirmDialog
          title={tr("admin.idp_delete_title", { name: confirmDel.name })}
          confirmLabel={tr("common.delete")}
          danger
          busy={busy}
          onCancel={() => setConfirmDel(null)}
          onConfirm={() => remove(confirmDel)}
        >
          <p>{tr("admin.idp_delete_body")}</p>
        </ConfirmDialog>
      )}
      {confirmSuspend && (
        <ConfirmDialog
          title={tr("admin.idp_suspend_title", { name: confirmSuspend.row.name })}
          confirmLabel={tr("admin.idp_suspend")}
          danger
          busy={busy}
          onCancel={() => setConfirmSuspend(null)}
          onConfirm={() => setStatus(confirmSuspend.row, "suspended", true)}
        >
          <p>{tr("admin.idp_suspend_members", { n: String(confirmSuspend.members) })}</p>
          <p>{tr("admin.idp_suspend_body")}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function IdPForm({
  form,
  setForm,
  busy,
  onSave,
  onCancel,
}: {
  form: TenantIdP;
  setForm: (f: TenantIdP) => void;
  busy: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const tr = useT();
  const set = (patch: Partial<TenantIdP>) => setForm({ ...form, ...patch });
  // The kind decides what is asked for. GitHub has neither an issuer nor a tid (there is one
  // issuer, github.com) and needs an organisation instead; showing those fields anyway would
  // present something unfillable and then reject it with a 400 (docs/log/61 §61.15).
  const isGitHub = form.kind === "github";
  const callbackURL = new URL("oauth2/callback", document.baseURI).toString();
  const valid =
    form.name.trim() &&
    form.client_id.trim() &&
    (form.allowed_domains || "").trim() &&
    (isGitHub ? (form.allowed_orgs || "").trim() : form.issuer.trim()) &&
    (form.id || (form.client_secret || "").trim());
  return (
    <div className="ssm-frm adm-mcp-form">
      <div className="ssm-fgrid">
        <Field label={tr("admin.idp_name")} req hint={tr("admin.idp_name_hint")}>
          <input value={form.name} placeholder={isGitHub ? "github" : "entra"} onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_kind")} req hint={tr("admin.idp_kind_hint")}>
          {/* Changing the kind discards the other kind's fields. Carrying them over would allow
              rows that save but never work, such as an OIDC row whose issuer is
              https://github.com (on a github row that issuer is a server-set constant anyway). */}
          <select
            value={form.kind || "oidc"}
            onChange={(e) =>
              set(
                e.target.value === "github"
                  ? { kind: "github", issuer: "", allowed_tids: "" }
                  : { kind: "oidc", allowed_orgs: "", issuer: form.issuer === "https://github.com" ? "" : form.issuer },
              )
            }
          >
            <option value="oidc">{tr("admin.idp_kind_oidc")}</option>
            <option value="github">{tr("admin.idp_kind_github")}</option>
          </select>
        </Field>
        {isGitHub ? (
          <Field label={tr("admin.idp_orgs")} req wide hint={tr("admin.idp_orgs_hint")}>
            <input
              value={form.allowed_orgs || ""}
              placeholder="acme-sub"
              onChange={(e) => set({ allowed_orgs: e.target.value })}
            />
          </Field>
        ) : (
          <>
            <Field label={tr("admin.idp_trust")} req hint={tr("admin.idp_trust_hint")}>
              <select value={form.trust} onChange={(e) => set({ trust: e.target.value })}>
                <option value="issuer">{tr("admin.idp_trust_issuer")}</option>
                <option value="email_verified">{tr("admin.idp_trust_email")}</option>
              </select>
            </Field>
            <Field label={tr("admin.idp_issuer")} req wide hint={tr("admin.idp_issuer_hint")}>
              <input
                value={form.issuer}
                placeholder="https://login.microsoftonline.com/<tenant-guid>/v2.0"
                onChange={(e) => set({ issuer: e.target.value })}
              />
            </Field>
          </>
        )}
        <Field label={tr("admin.idp_client_id")} req hint={isGitHub ? tr("admin.idp_github_app_hint", { url: callbackURL }) : undefined}>
          <input value={form.client_id} onChange={(e) => set({ client_id: e.target.value })} />
        </Field>
        <Field
          label={tr("admin.idp_client_secret")}
          req={!form.id}
          hint={form.id && form.has_secret ? tr("admin.idp_secret_kept") : tr("admin.idp_secret_hint")}
        >
          <input
            type="password"
            autoComplete="new-password"
            value={form.client_secret || ""}
            placeholder={form.id && form.has_secret ? "***" : ""}
            onChange={(e) => set({ client_secret: e.target.value })}
          />
        </Field>
        <Field
          label={tr("admin.idp_domains")}
          req
          wide
          hint={isGitHub ? tr("admin.idp_github_domains_note") : tr("admin.idp_domains_hint")}
        >
          <input
            value={form.allowed_domains || ""}
            placeholder="@sub.co.jp"
            onChange={(e) => set({ allowed_domains: e.target.value })}
          />
        </Field>
        {!isGitHub && (
          <Field label={tr("admin.idp_tids")} wide hint={tr("admin.idp_tids_hint")}>
            <input value={form.allowed_tids || ""} onChange={(e) => set({ allowed_tids: e.target.value })} />
          </Field>
        )}
        {/* A select, not free text. Only a claim the IdP assigns and the subject cannot choose
            belongs here; allowing an asserted claim (email, upn, ...) would permit email-based
            linking between methods that share an issuer (docs/log/61 §61.15.10). The options
            mirror the CP's allowlist; the decision stays on the server, which rejects on save. */}
        {!isGitHub && (
          <Field label={tr("admin.idp_link_claim")} wide hint={tr("admin.idp_link_claim_hint")}>
            <select value={form.link_claim || ""} onChange={(e) => set({ link_claim: e.target.value })}>
              <option value="">{tr("admin.idp_link_claim_none")}</option>
              <option value="oid">oid</option>
            </select>
          </Field>
        )}
        <Field label={tr("admin.idp_label_ja")}>
          <input value={form.label_ja || ""} onChange={(e) => set({ label_ja: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_label_en")}>
          <input value={form.label_en || ""} onChange={(e) => set({ label_en: e.target.value })} />
        </Field>
      </div>
      <p className="admin-hint">{tr("admin.idp_repend_hint")}</p>
      <div className="admin-actions">
        <button className="primary" disabled={busy || !valid} onClick={onSave}>
          {tr("common.save")}
        </button>
        <button className="ghost" disabled={busy} onClick={onCancel}>
          {tr("common.cancel")}
        </button>
      </div>
    </div>
  );
}

// SignInMethodRegister — the deployment-wide ledger of tenant-defined sign-in methods
// (docs/log/61 §61.11.6). Deployment admins only.
//
// Deliberately a ledger, not a queue that empties. Approval is a one-off review, but the IdP
// behind it stays under someone else's control and its configuration can change later (self
// sign-up being enabled is the typical case). So approved rows remain, together with who
// approved them and when, and that list is what periodic review works from. Pending rows come
// first because someone is waiting on them.
export function SignInMethodRegister() {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const res = await api("api/admin/idp");
    setRows(res?.providers || []);
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  // Approval happens directly from this ledger: the place where the waiting is visible has to
  // be the place where it can be acted on. The endpoint is the same single one as the tenant
  // side (POST /api/admin/tenants/{slug}/idp/{id}/status), built from the row's tenant_slug.
  // Permission is unchanged — the CP's setStatus checks super_admin (decision 30); this view
  // being super_admin-only is presentation of that fact.
  const setStatus = async (row: TenantIdP, status: string) => {
    if (!row.tenant_slug) return;
    setBusy(true);
    try {
      const res = await apiJSON(
        `api/admin/tenants/${encodeURIComponent(row.tenant_slug)}/idp/${encodeURIComponent(row.id)}/status`,
        "POST",
        { status },
      );
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      // Approving changes the status as well as the approver and approval time. With a
      // fetch-once view, the person who pressed the button would be the only one not to see it.
      await load();
    } finally {
      setBusy(false);
    }
  };

  if (rows === null) return <p className="muted pad">{tr("common.loading")}</p>;
  // This is a rail item and therefore the whole body, so an empty register must still say that
  // it is empty; rendering nothing would leave a blank panel.
  if (rows.length === 0) {
    return (
      <section className="admin-panel">
        <h4>{tr("admin.idp_register")}</h4>
        <p className="muted">{tr("admin.idp_register_none")}</p>
      </section>
    );
  }
  const pending = rows.filter((r) => r.status === "pending").length;
  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_register")}
        {pending > 0 && <span className="af-note">{tr("admin.idp_pending_count", { n: pending })}</span>}
      </h4>
      <p className="admin-hint">{tr("admin.idp_register_hint")}</p>
      {rows.map((row) => (
        <div key={row.id} className={"adm-mcp-row" + (row.status === "active" && row.usable ? "" : " off")}>
          <span className="as-name mono" title={row.provider_id}>
            {row.tenant_slug}
          </span>
          <span className="as-repo muted" title={idpSource(row)}>
            {idpSource(row)}
          </span>
          <span className="muted">{row.allowed_domains}</span>
          <span className={"idp-state idp-" + (row.status || "pending")}>{tr(idpStatusKey(row))}</span>
          {row.approved_at && <span className="muted">{fmtDateTime(row.approved_at, DATETIME_FULL)}</span>}
          {row.tenant_slug && (
            <span className="allow-acts">
              {row.status !== "active" && (
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "active")}>
                  {tr("admin.idp_approve")}
                </button>
              )}
              {row.status === "active" && (
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "suspended")}>
                  {tr("admin.idp_suspend")}
                </button>
              )}
            </span>
          )}
        </div>
      ))}
    </section>
  );
}
