import { useState } from "react";
import { agentOf } from "../../../agents/registry.ts";
import { useT, t } from "../../../lib/i18n/index.ts";
import type { Form, KV, McpServer, ProbeResult } from "./mcpWire.ts";
import { MCP_KINDS, MASKED, NAME_RE, formValid, toKV } from "./mcpWire.ts";
import { Field, KVEditor } from "../parts/mcpForm.tsx";
import { EgressNote } from "./EgressNote.tsx";
import type { EgressCheck } from "./egressCheck.ts";
import { ProbeView } from "./mcpServerRow.tsx";

export function SecretsForm({
  s,
  onSave,
  onCancel,
}: {
  s: McpServer;
  onSave: (s: McpServer, headers: Record<string, string>) => Promise<boolean>;
  onCancel: () => void;
}) {
  const tr = useT();
  const [rows, setRows] = useState<KV[]>(() => toKV(s.headers));
  const [busy, setBusy] = useState(false);
  const patch = (i: number, v: string) => setRows(rows.map((r, j) => (i === j ? { ...r, v } : r)));

  const submit = async () => {
    setBusy(true);
    try {
      await onSave(s, Object.fromEntries(rows.map((r) => [r.k, r.v])));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="ssm-frm mcp-frm">
      <div className="ssm-fgroup">
        <p className="ps-note">{tr("mcp.secrets_intro", { name: s.name })}</p>
        <Field label={tr("mcp.f_headers")} wide hint={tr("mcp.secrets_hint")}>
          <div className="mcp-kv">
            {rows.map((r, i) => (
              <div key={r.k} className="mcp-kv-row two">
                <input className="cinput mono" value={r.k} readOnly disabled />
                <input
                  className="cinput"
                  type="password"
                  placeholder={tr("mcp.kv_value")}
                  value={r.v}
                  onChange={(e) => patch(i, e.target.value)}
                />
              </div>
            ))}
            {rows.length === 0 && <div className="hint">{tr("mcp.secrets_none")}</div>}
            {rows.some((r) => r.v === MASKED) && <div className="hint">{tr("mcp.kv_masked_hint")}</div>}
          </div>
        </Field>
        {s.targets?.session && <p className="ps-note">{tr("mcp.session_restart_note")}</p>}
      </div>
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || rows.length === 0} onClick={submit}>
          {tr("common.save")}
        </button>
        <button className="ghost" onClick={onCancel}>
          {tr("common.cancel")}
        </button>
      </div>
    </div>
  );
}

// --- form ------------------------------------------------------------------------
// Field / KVEditor / CheckRow live in mcpForm.tsx — the tenant distribution form in
// AdminTab renders the same definition and shares them.

export function ServerForm({
  form,
  setForm,
  onSave,
  onTest,
  probe,
  egress,
  onProposed,
  submitLabel,
}: {
  form: Form;
  setForm: (f: Form | null) => void;
  onSave: (f: Form) => Promise<boolean>;
  onTest: (f: Form) => Promise<void>;
  probe?: ProbeResult;
  egress: EgressCheck | null;
  onProposed: () => void;
  submitLabel: string;
}) {
  const tr = useT();
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const patch = (part: Partial<Form>) => setForm({ ...form, ...part });
  const valid = formValid(form);
  const nameBad = form.name.trim() !== "" && !NAME_RE.test(form.name.trim());

  const toggleKind = (k: string) =>
    patch({ kinds: form.kinds.includes(k) ? form.kinds.filter((x) => x !== k) : [...form.kinds, k] });

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await onSave(form);
    } finally {
      setBusy(false);
    }
  };
  const runTest = async () => {
    if (!valid) return;
    setTesting(true);
    try {
      await onTest(form);
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="ssm-frm mcp-frm">
      <div className="ssm-fgroup">
        <div className="ssm-fgrid">
          <Field label={tr("mcp.f_name")} req hint={nameBad ? tr("mcp.f_name_bad") : tr("mcp.f_name_hint")}>
            <input
              className="cinput"
              placeholder="my-server"
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
          <Field label={tr("mcp.f_transport")} req wide hint={tr("mcp.f_transport_hint")}>
            <div className="seg choice-seg">
              {(["stdio", "http"] as const).map((tp) => (
                <button
                  key={tp}
                  type="button"
                  className={"seg-btn" + (form.transport === tp ? " active" : "")}
                  onClick={() => patch({ transport: tp })}
                >
                  {tp === "stdio" ? tr("mcp.tp_stdio") : tr("mcp.tp_http")}
                </button>
              ))}
            </div>
          </Field>

          {form.transport === "stdio" ? (
            <>
              <Field label={tr("mcp.f_command")} req wide hint={tr("mcp.f_command_hint")}>
                <input
                  className="cinput"
                  placeholder="npx"
                  value={form.command}
                  onChange={(e) => patch({ command: e.target.value })}
                />
              </Field>
              <Field label={tr("mcp.f_args")} wide hint={tr("mcp.f_args_hint")}>
                <textarea
                  className="cinput mcp-ta"
                  rows={3}
                  placeholder={"-y\n@modelcontextprotocol/server-filesystem\n/home/dev/repos"}
                  value={form.args}
                  onChange={(e) => patch({ args: e.target.value })}
                />
              </Field>
              <Field label={tr("mcp.f_env")} wide hint={tr("mcp.f_env_hint")}>
                <KVEditor
                  rows={form.env}
                  onChange={(env) => patch({ env })}
                  keyPlaceholder="API_TOKEN"
                  addLabel={tr("mcp.add_env")}
                />
              </Field>
            </>
          ) : (
            <>
              <Field label="URL" req wide hint={tr("mcp.f_url_hint")}>
                <input
                  className="cinput"
                  placeholder="https://mcp.example.com/mcp"
                  value={form.url}
                  onChange={(e) => patch({ url: e.target.value })}
                />
              </Field>
              <Field label={tr("mcp.f_headers")} wide hint={tr("mcp.f_headers_hint")}>
                <KVEditor
                  rows={form.headers}
                  onChange={(headers) => patch({ headers })}
                  keyPlaceholder="Authorization"
                  addLabel={tr("mcp.add_header")}
                />
              </Field>
            </>
          )}

          {/* Deliberately NOT marked required: both off is a legal staging state
              (stored, handed to nothing) — see secrets.MCPTargets. */}
          <Field label={tr("mcp.f_targets")} wide hint={tr("mcp.f_targets_hint")}>
            <div className="mcp-checks">
              <label className="mcp-check">
                <input type="checkbox" checked={form.assistant} onChange={(e) => patch({ assistant: e.target.checked })} />
                {tr("mcp.target_assistant")}
              </label>
              <label className="mcp-check">
                <input type="checkbox" checked={form.session} onChange={(e) => patch({ session: e.target.checked })} />
                {tr("mcp.target_session")}
              </label>
            </div>
          </Field>
          <Field label={tr("mcp.f_kinds")} wide hint={tr("mcp.f_kinds_hint")}>
            <div className="mcp-checks">
              {MCP_KINDS.map((k) => (
                <label key={k} className="mcp-check">
                  <input type="checkbox" checked={form.kinds.includes(k)} onChange={() => toggleKind(k)} />
                  {agentOf(k).label}
                </label>
              ))}
            </div>
          </Field>
          <Field label={tr("mcp.f_timeout")} hint={tr("mcp.f_timeout_hint")}>
            <input
              className="cinput"
              type="number"
              min={1000}
              max={120000}
              step={1000}
              placeholder="30000"
              value={form.timeoutMs}
              onChange={(e) => patch({ timeoutMs: e.target.value })}
            />
          </Field>
          <Field label={tr("mcp.f_enabled")} hint={tr("mcp.f_enabled_hint")}>
            <div className="mcp-checks">
              <label className="mcp-check">
                <input type="checkbox" checked={form.enabled} onChange={(e) => patch({ enabled: e.target.checked })} />
                {tr("mcp.enabled_on")}
              </label>
            </div>
          </Field>
        </div>
        {form.session && <p className="ps-note">{tr("mcp.session_restart_note")}</p>}
        {form.transport === "http" && (
          <EgressNote
            url={form.url}
            check={egress}
            defaultReason={tr("mcp.egress_reason_for", { name: form.name.trim() || form.url.trim() })}
            onProposed={onProposed}
          />
        )}
      </div>
      <ProbeView probe={probe} />
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || !valid} onClick={submit}>
          {submitLabel}
        </button>
        <button className="ghost" disabled={testing || !valid} onClick={runTest}>
          {testing ? tr("mcp.testing") : tr("mcp.test")}
        </button>
        <button className="ghost" onClick={() => setForm(null)}>
          {tr("common.cancel")}
        </button>
        <span className="req-note">
          <b>*</b> {t("ssm.req_note")}
        </span>
      </div>
    </div>
  );
}
