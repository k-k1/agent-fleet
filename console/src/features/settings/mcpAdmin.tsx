// テナントへ配布する MCP サーバの管理面。
//
// AdminTab.tsx から純粋移動した。CP 側は /api/admin/mcp-servers* を tenant_admin
// （と super_admin）に開いているので、置き場は管理モーダルだけでなくテナント設定にも要る。
import { Fragment, useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { kindLabel } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
// Tenant MCP distribution reuses the member tab's wire contract (docs/48 P4), so the
// name rule, the masked round-trip and the "remote only" shape are pinned by
// mcpWire.test.ts rather than by this component.
import {
  MCP_KINDS,
  NAME_RE,
  bodyOfTenant,
  emptyTenantForm,
  tenantFormOf,
  tenantFormValid,
} from "./mcpWire.ts";
import type { TenantForm, TenantServer } from "./mcpWire.ts";
// Same field furniture as the member tab's form (McpTab), so the two MCP forms stay
// one design instead of two.
import { Field, KVEditor, CheckRow, Check } from "./mcpForm.tsx";
// Egress allowlist tie-in (docs/48 §9). It matters more here than on the member tab: a
// distributed server that the proxy blocks is broken for EVERY member of the tenant.
import { EgressNote, useEgressCheck } from "./EgressNote.tsx";
import { hostsOf } from "./egressCheck.ts";
import type { EgressCheck } from "./egressCheck.ts";
import type { Tenant } from "./adminShared.ts";

// --- Tenant-distributed MCP servers (docs/48 P4 + ADR0031) -------------------------
//
// A tenant_admin registers a REMOTE MCP server once and every member of that tenant gets
// it in their workspace — assistants and interactive sessions both. There is deliberately
// no stdio option: distributing a command means the admin runs arbitrary code in every
// member's container, so ADR0031 決定 2 keeps the columns out of the CP table entirely
// rather than relying on a form that omits the field.
//
// Header values are write-only from here: they come back masked ("***") and sending them
// back unchanged keeps the stored value. The 秘密 that CANNOT be protected is the one
// distributed with values — every member can read it in their own container — which is
// what the "各メンバーが値を入力" toggle (user_secret) exists to avoid.

export function McpAdminView({ tenants }: { tenants: Tenant[] }) {
  const tr = useT();
  const [slug, setSlug] = useState(tenants[0]?.slug || "");
  const [rows, setRows] = useState<TenantServer[] | null>(null);
  const [err, setErr] = useState("");
  const [form, setForm] = useState<TenantForm | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState<TenantServer | null>(null);
  const { check: egress, recheck: recheckEgress } = useEgressCheck(
    hostsOf([...(rows || []).map((s) => s.url), form?.url]),
  );

  const load = useCallback(async () => {
    if (!slug) return;
    setErr("");
    try {
      const d = await api("api/admin/mcp-servers?tenant=" + encodeURIComponent(slug));
      if (d?.error) {
        setErr(errText(d.error));
        setRows([]);
        return;
      }
      setRows(d.servers || []);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [slug, tr]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async (f: TenantForm) => {
    setBusy(true);
    try {
      const path = f.id ? "api/admin/mcp-servers/" + encodeURIComponent(f.id) : "api/admin/mcp-servers";
      const d = await apiJSON(path, f.id ? "PUT" : "POST", bodyOfTenant(f, slug));
      if (d && d.error) {
        setErr(errText(d.error));
        return;
      }
      setForm(null);
      setErr("");
      await load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (s: TenantServer) => {
    setBusy(true);
    try {
      const d = await apiJSON(
        "api/admin/mcp-servers/" + encodeURIComponent(s.id) + "?tenant=" + encodeURIComponent(slug),
        "DELETE",
      );
      if (d && d.error) setErr(errText(d.error));
      await load();
    } finally {
      setBusy(false);
      setConfirmDel(null);
    }
  };

  return (
    <div className="admin-stage mcp-admin">
      <section className="admin-panel">
        <div className="usage-toolbar">
          {/* テナントが 1 つしか渡らない置き場（テナント設定モーダル）では選ぶものが
              無い。1 択のセレクトは操作できる顔をした飾りにしかならないので出さない。 */}
          {tenants.length > 1 && (
            <label>
              {tr("admin.tenant")}
              <select value={slug} onChange={(e) => setSlug(e.target.value)}>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>
                    {t.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        <p className="muted">{tr("admin.mcp_intro")}</p>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        <h4 className="egress-h">{tr("admin.mcp_distributed")}</h4>
        {rows === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : rows.length === 0 ? (
          <p className="muted">{tr("admin.mcp_none")}</p>
        ) : (
          rows.map((s) =>
            form && form.id === s.id ? (
              <McpAdminForm
                key={s.id}
                form={form}
                setForm={setForm}
                busy={busy}
                onSave={save}
                egress={egress}
                onProposed={recheckEgress}
              />
            ) : (
              <Fragment key={s.id}>
                <div className={"adm-mcp-row" + (s.enabled ? "" : " off")}>
                  <span className="as-name mono" title={s.name}>
                    {s.name}
                  </span>
                  <span className="as-repo muted" title={s.url}>
                    {s.label || s.url}
                  </span>
                  {s.user_secret && (
                    <span className="mcp-origin mcp-origin-tenant">{tr("admin.mcp_user_secret_badge")}</span>
                  )}
                  {!s.enabled && <span className="muted">{tr("admin.mcp_disabled")}</span>}
                  <span className="allow-acts">
                    <button type="button" className="ghost xs" disabled={busy} onClick={() => setForm(tenantFormOf(s))}>
                      {tr("mcp.edit")}
                    </button>
                    <button type="button" className="ghost xs danger" disabled={busy} onClick={() => setConfirmDel(s)}>
                      {tr("common.delete")}
                    </button>
                  </span>
                </div>
                <EgressNote
                  url={s.url}
                  check={egress}
                  defaultReason={tr("mcp.egress_reason_for", { name: s.name })}
                  onProposed={recheckEgress}
                />
              </Fragment>
            ),
          )
        )}
        {form && form.id === "" ? (
          <McpAdminForm
            form={form}
            setForm={setForm}
            busy={busy}
            onSave={save}
            egress={egress}
            onProposed={recheckEgress}
          />
        ) : (
          !form && (
            <button type="button" className="ghost" disabled={!slug} onClick={() => setForm(emptyTenantForm())}>
              <Icon name="add" /> {tr("admin.mcp_add")}
            </button>
          )
        )}
      </section>

      {confirmDel && (
        <ConfirmDialog
          title={tr("admin.mcp_del_title")}
          confirmLabel={tr("common.delete_confirm")}
          danger
          busy={busy}
          onConfirm={() => void remove(confirmDel)}
          onCancel={() => setConfirmDel(null)}
        >
          {tr("admin.mcp_del_body", { name: confirmDel.name })}
        </ConfirmDialog>
      )}
    </div>
  );
}

// McpAdminForm — the tenant distribution form. Remote only by construction (see
// bodyOfTenant): there is no transport switch to get wrong.
function McpAdminForm({
  form,
  setForm,
  busy,
  onSave,
  egress,
  onProposed,
}: {
  form: TenantForm;
  setForm: (f: TenantForm | null) => void;
  busy: boolean;
  onSave: (f: TenantForm) => Promise<void>;
  egress: EgressCheck | null;
  onProposed: () => void;
}) {
  const tr = useT();
  const patch = (part: Partial<TenantForm>) => setForm({ ...form, ...part });
  const valid = tenantFormValid(form);
  const nameBad = form.name.trim() !== "" && !NAME_RE.test(form.name.trim());

  return (
    <div className="ssm-frm mcp-frm adm-mcp-form">
      <div className="ssm-fgroup">
        <p className="ssm-fg-title">{form.id ? tr("admin.mcp_edit_title") : tr("admin.mcp_add")}</p>
        <div className="ssm-fgrid">
          <Field label={tr("mcp.f_name")} req hint={nameBad ? tr("mcp.f_name_bad") : tr("mcp.f_name_hint")}>
            <input
              className="cinput mono"
              placeholder="wiki"
              value={form.name}
              onChange={(e) => patch({ name: e.target.value })}
              autoFocus
            />
          </Field>
          <Field label={tr("mcp.f_label")} hint={tr("mcp.f_label_hint")}>
            <input
              className="cinput"
              placeholder={tr("mcp.f_label_placeholder")}
              value={form.label}
              onChange={(e) => patch({ label: e.target.value })}
            />
          </Field>
          <Field label="URL" req wide hint={tr("admin.mcp_url_hint")}>
            <input
              className="cinput"
              placeholder="https://mcp.example.com/mcp"
              value={form.url}
              onChange={(e) => patch({ url: e.target.value })}
            />
          </Field>

          {/* The credential decision comes BEFORE the headers, because it decides what
              the header rows even ask for (name+value vs name only). */}
          <Field label={tr("admin.mcp_secret_policy")} wide hint={tr("admin.mcp_user_secret_hint")}>
            <CheckRow>
              <Check checked={form.userSecret} onChange={(v) => patch({ userSecret: v })}>
                {tr("admin.mcp_user_secret")}
              </Check>
            </CheckRow>
          </Field>
          <Field
            label={tr("mcp.f_headers")}
            wide
            hint={form.userSecret ? tr("admin.mcp_headers_names_hint") : tr("admin.mcp_headers_hint")}
          >
            <KVEditor
              rows={form.headers}
              onChange={(headers) => patch({ headers })}
              keyPlaceholder="Authorization"
              addLabel={tr("mcp.add_header")}
              noValue={form.userSecret}
            />
          </Field>

          {/* Deliberately NOT marked required: both off is a legal staging state
              (stored, distributed to nothing) — see secrets.MCPTargets. */}
          <Field label={tr("mcp.f_targets")} wide hint={tr("mcp.f_targets_hint")}>
            <CheckRow>
              <Check checked={form.assistant} onChange={(v) => patch({ assistant: v })}>
                {tr("mcp.target_assistant")}
              </Check>
              <Check checked={form.session} onChange={(v) => patch({ session: v })}>
                {tr("mcp.target_session")}
              </Check>
            </CheckRow>
          </Field>
          <Field label={tr("mcp.f_kinds")} wide hint={tr("mcp.f_kinds_hint")}>
            <CheckRow>
              {MCP_KINDS.map((k) => (
                <Check
                  key={k}
                  checked={form.kinds.includes(k)}
                  onChange={() =>
                    patch({ kinds: form.kinds.includes(k) ? form.kinds.filter((x) => x !== k) : [...form.kinds, k] })
                  }
                >
                  {kindLabel(k)}
                </Check>
              ))}
            </CheckRow>
          </Field>
          <Field label={tr("mcp.f_enabled")} wide hint={tr("admin.mcp_enabled_hint")}>
            <CheckRow>
              <Check checked={form.enabled} onChange={(v) => patch({ enabled: v })}>
                {tr("mcp.enabled_on")}
              </Check>
            </CheckRow>
          </Field>
        </div>
        <EgressNote
          url={form.url}
          check={egress}
          defaultReason={tr("mcp.egress_reason_for", { name: form.name.trim() || form.url.trim() })}
          onProposed={onProposed}
        />
        <p className="ps-note">{tr("admin.mcp_restart_note")}</p>
      </div>
      <div className="ssm-frm-foot">
        <button type="button" className="primary" disabled={busy || !valid} onClick={() => void onSave(form)}>
          {form.id ? tr("common.save") : tr("admin.mcp_save_add")}
        </button>
        <button type="button" className="ghost" onClick={() => setForm(null)}>
          {tr("common.cancel")}
        </button>
        <span className="req-note">
          <b>*</b> {tr("ssm.req_note")}
        </span>
      </div>
    </div>
  );
}
