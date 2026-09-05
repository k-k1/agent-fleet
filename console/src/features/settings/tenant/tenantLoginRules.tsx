import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import type { TenantLoginFields } from "./tenantLoginTypes.ts";

// The sign-in methods this deployment enables through env (GET /api/admin/providers).
// Carries no secrets — id, display name and issuer only.
export interface DeployProvider {
  id: string;
  label_ja?: string;
  label_en?: string;
  issuer?: string;
}

// useDeploymentProviders — this deployment's methods, i.e. the default tenant's methods
// (docs/log/61 §61.17).
//
// Three states are kept apart: null = loading, "error" = could not read, array = read (an
// empty array really is zero). Collapsing an error into zero rows tells the reader "none
// configured", which is a lie (§61.17.9 (2)).
//
// The test is `Array.isArray(res?.providers)` — whether the wanted shape arrived, not whether
// `res.error` is present. If the error shape changes later, it still cannot be mistaken for
// zero rows.
//
// Tenant-defined methods (`t:<slug>:<name>`) never appear here: they come and go at runtime,
// and listing them all would be a roster of the group's companies (decision 32-4). This
// tenant's own are fetched separately.
export function useDeploymentProviders(): DeployProvider[] | "error" | null {
  const [rows, setRows] = useState<DeployProvider[] | "error" | null>(null);
  useEffect(() => {
    let live = true;
    api("api/admin/providers")
      .then((res) => {
        if (!live) return;
        setRows(Array.isArray(res?.providers) ? res.providers : "error");
      })
      // A dropped connection arrives as a rejection; api() only synthesizes a value for
      // non-JSON responses.
      .catch(() => {
        if (live) setRows("error");
      });
    return () => {
      live = false;
    };
  }, []);
  return rows;
}

// --- the algebra of "accept" vs "show on a button" (docs/log/61 §61.17.5) ----------------
//
// The DB representation stays two CSV columns (`allowed_providers` / `hidden_providers`); the
// screen only presents them as two per-row toggles, and the schema is unchanged. Only the
// functions here read and write the CSV, so the screen touches booleans alone.
//
// All three traps come from the existing meaning of "empty = all" and the existing safety
// valves:
//   1. Empty `allowed_providers` means accept everything (§61.9.4). Turning every row off
//      therefore saves as "all on" — you meant to restrict and you opened it wide.
//   2. `hidden_providers` has its own "ignore it if everything is hidden" valve (loginButtons
//      in `oauth.go`). Turning "show" off on every row saves fine and then has no effect, so
//      the screen would be lying.
//   3. "Empty" also means *follow the deployment*. Freezing an explicit list on the first edit
//      makes this tenant silently reject every method later added to env, so we never freeze
//      it: the normalisation is "if everything is on, save empty".

/** CSV to an array of ids, lower-cased to match how the CP stores them (splitCSVLower). */
export const splitIds = (csv?: string): string[] =>
  (csv || "")
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);

/**
 * The accepted set with "empty = all" expanded. Keeps the order of knownIds and drops ids not
 * in it, so a deleted method left behind in the CSV cannot affect the screen's state.
 */
export function acceptedIds(knownIds: string[], allowedCSV?: string): string[] {
  const a = splitIds(allowedCSV);
  if (a.length === 0) return [...knownIds];
  const set = new Set(a);
  return knownIds.filter((id) => set.has(id));
}

/** State of one row's two toggles. shown depends on accepted: what is not accepted is not shown. */
export function ruleStateFor(
  knownIds: string[],
  allowedCSV: string | undefined,
  hiddenCSV: string | undefined,
  id: string,
): { accepted: boolean; shown: boolean } {
  const acc = new Set(acceptedIds(knownIds, allowedCSV));
  const hid = new Set(splitIds(hiddenCSV));
  return { accepted: acc.has(id), shown: acc.has(id) && !hid.has(id) };
}

/**
 * Rows that cannot be switched off. usableIds is what somebody can actually sign in with right
 * now: the deployment's methods plus this tenant's own rows that are active and usable.
 *
 * One function covers two rules — "the last one cannot be turned off", and the ordering rule of
 * §61.17.5 (restricting first and inviting the tenant admin afterwards would lock that person
 * out, so the deployment's methods cannot be dropped while the tenant's own rows are not yet
 * working). A row awaiting approval is not usable, so it falls under the second rule on its own.
 */
export function ruleLocks(
  knownIds: string[],
  usableIds: string[],
  allowedCSV: string | undefined,
  hiddenCSV: string | undefined,
  id: string,
): { acceptOffLocked: boolean; showOffLocked: boolean } {
  const usable = new Set(usableIds);
  const hid = new Set(splitIds(hiddenCSV));
  const accUsable = acceptedIds(knownIds, allowedCSV).filter((x) => usable.has(x));
  const shownUsable = accUsable.filter((x) => !hid.has(x));
  return {
    acceptOffLocked: accUsable.length <= 1 && accUsable.includes(id),
    showOffLocked: shownUsable.length <= 1 && shownUsable.includes(id),
  };
}

/** Folds one toggle into the two CSV columns. What it returns is exactly what gets saved. */
export function toggleRule(
  knownIds: string[],
  allowedCSV: string | undefined,
  hiddenCSV: string | undefined,
  id: string,
  field: "accepted" | "shown",
  value: boolean,
): { allowed_providers: string; hidden_providers: string } {
  const known = new Set(knownIds);
  const acc = new Set(acceptedIds(knownIds, allowedCSV));
  const hid = new Set(splitIds(hiddenCSV).filter((x) => known.has(x)));
  if (field === "accepted") {
    if (value) {
      acc.add(id);
    } else {
      acc.delete(id);
      // "Hidden" is meaningless once the method is not accepted (rendering requires allowed
      // even inside the hidden check). Leaving it would resurrect "not shown" without
      // explanation if the method is accepted again later.
      hid.delete(id);
    }
  } else if (value) {
    hid.delete(id);
  } else {
    hid.add(id);
  }
  const accList = knownIds.filter((x) => acc.has(x));
  return {
    // All on saves as empty (trap 3) — this is what "never freeze the list" comes down to.
    allowed_providers: accList.length === knownIds.length ? "" : accList.join(","),
    hidden_providers: knownIds.filter((x) => hid.has(x)).join(","),
  };
}

// TenantLoginRules — editor for the three CSV columns of docs/log/61 §61.9.7.
//
// The three are deliberately not alike, and the hints say so. The expensive misreading is
// taking allowed_domains as "who may use this tenant": it is only a bound on who may be
// invited. Whether someone keeps access is a question of membership, and making the domain a
// per-request condition locks out the contractor you deliberately invited (§61.9.5).
export function TenantLoginRules({
  slug,
  tenant,
  onChanged,
}: {
  slug: string;
  tenant: TenantLoginFields | null | undefined;
  onChanged: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [autoJoin, setAutoJoin] = useState(tenant?.auto_join_domains || "");
  const [domains, setDomains] = useState(tenant?.allowed_domains || "");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setAutoJoin(tenant?.auto_join_domains || "");
    setDomains(tenant?.allowed_domains || "");
  }, [slug, tenant]);

  const save = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/login`, "PUT", {
      // This PUT replaces all four columns. The two method columns now belong to another
      // surface (the two toggles on sign-in methods), so send back exactly what was read:
      // omitting them overwrites with empty and silently opens a restricted tenant wide.
      allowed_providers: (tenant?.allowed_providers || "").trim(),
      hidden_providers: (tenant?.hidden_providers || "").trim(),
      auto_join_domains: autoJoin.trim(),
      allowed_domains: domains.trim(),
    });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
    onChanged();
  };

  // The URL a tenant admin hands to a new colleague (§61.10.4). There is no notification
  // channel, so a person passes it on — deliberately (decision 28: no SMTP in the Control
  // Plane).
  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("admin.login_rules")}<span className="af-note">{tr("admin.login_rules_note")}</span></h4>
        <div className="admin-fgrid">
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.auto_join_domains")}</span>
            <input type="text" placeholder="@sales.acme.co.jp" value={autoJoin} onChange={(e) => setAutoJoin(e.target.value)} />
            <span className="af-unit">{tr("admin.auto_join_domains_unit")}</span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.invite_domains")}</span>
            <input type="text" placeholder="@acme.co.jp" value={domains} onChange={(e) => setDomains(e.target.value)} />
            <span className="af-unit">{tr("admin.invite_domains_unit")}</span>
          </label>
        </div>
        {/* The two method columns (accept / show on a button) live on the sign-in methods
            surface, as a toggle per row rather than ids typed into a free-text field
            (docs/log/61 §61.17.5): no separate list of typeable ids, and no
            400 unknown_provider. */}
        <p className="admin-hint">{tr("admin.login_rules_methods_moved")}</p>
        <p className="admin-hint">{tr("admin.login_rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      </div>
      <div className="admin-actions">
        <button onClick={save} className="primary">{tr("common.save")}</button>
        {saved && <span className="saved-note"><Icon name="check" /> {tr("admin.saved")}</span>}
      </div>
    </section>
  );
}

// TenantLoginRulesView — read-only version of the same three columns, for the tenant settings
// modal.
//
// Withholding the edit form is not the implementation of the permission, only a reflection of
// it: the PUT is fixed to withSuperAdmin (decision 19), and two of the three reach outside this
// tenant — auto-join domains open the deployment's own front door, and the usable sign-in
// methods are the choice of which IdP may assert who someone is. It is still shown to the
// tenant admin so they can read why an invitation bounced, and what applies to their tenant,
// without asking anyone.
export function TenantLoginRulesView({
  slug,
  tenant,
}: {
  slug: string;
  tenant: TenantLoginFields | null | undefined;
}) {
  const tr = useT();
  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();
  const row = (cap: string, value: string, note: string) => (
    <div className="admin-fld">
      <span className="af-cap">{cap}</span>
      <span className={"af-val" + (value ? "" : " unset")}>{value || tr("tenant.rules_unset")}</span>
      <span className="af-unit">{note}</span>
    </div>
  );
  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>
          {tr("admin.login_rules")}
          <span className="af-note">{tr("tenant.rules_readonly_note")}</span>
        </h4>
        <div className="admin-fgrid">
          {row(tr("admin.auto_join_domains"), (tenant?.auto_join_domains || "").trim(), tr("tenant.rules_autojoin_note"))}
          {row(tr("admin.invite_domains"), (tenant?.allowed_domains || "").trim(), tr("tenant.rules_invite_note"))}
        </div>
        {/* The two method columns are not here (§61.17.5). A read-only CSV would not answer
            "how do we sign in", so point at the sign-in methods surface, which lists them as
            rows and is readable by tenant_admin too (§61.17.9 (1)). */}
        <p className="admin-hint">{tr("admin.login_rules_methods_moved")}</p>
        {/* The admin modal's hint of the same name (admin.login_rules_hint) does not fit here:
            it tells the reader to remove someone from the member detail below, an action this
            screen does not have. */}
        <p className="admin-hint">{tr("tenant.rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      </div>
    </section>
  );
}

// --- tenant-defined sign-in methods (docs/log/61 §61.11, ADR0043 decisions 29-33) ------
