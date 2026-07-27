import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, isTransientErr, errText, rawJSON } from "../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { Hint } from "./providerCard.tsx";
import { agentOf } from "../../agents/registry.ts";
import { useT, t } from "../../lib/i18n/index.ts";
// The wire contract (types, masked-secret round-trip, form↔definition mapping)
// lives beside this file so its rules are unit-tested — see mcpWire.test.ts.
import {
  MCP_KINDS,
  MASKED,
  NAME_RE,
  bodyOf,
  codexUnsupported,
  emptyForm,
  formOf,
  formValid,
} from "./mcpWire.ts";
import type { Form, KV, McpServer, ProbeResult, Registry } from "./mcpWire.ts";

// McpTab — the member's own MCP server registry (docs/48 P1 + ADR0031). Lists the
// EFFECTIVE registry (builtin ∪ tenant ∪ user) as one table, because that is what the
// assistants and sessions actually see; origin decides what is editable here:
//   user    … full CRUD
//   tenant  … read-only, but locally disableable (the member's escape hatch when a
//             distributed server breaks their session launches)
//   builtin … the ops integrations (PagerDuty / Grafana / CloudWatch), configured on
//             the 運用・監視 tab — shown so the list isn't lying by omission.
//
// Secrets (env / header VALUES) never come back from the agent: they arrive as "***"
// and are sent back unchanged to keep the stored value (mcpreg.MergeSecrets). So the
// form can be opened, edited and saved without ever handling the real credential.
//
// Everything here lives in the workspace's encrypted store, so the tab is gated on a
// running workspace — a stopped one would otherwise render an empty registry as if the
// user had registered nothing.

// --- tab -------------------------------------------------------------------------

export function McpTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const toast = useToast();

  const [reg, setReg] = useState<Registry | null>(null);
  const [form, setForm] = useState<Form | null>(null);
  // Probe results keyed by server id ("" = the unsaved form), so a test result stays
  // pinned to the row that produced it across a reload.
  const [probes, setProbes] = useState<Record<string, ProbeResult>>({});

  // A CP 502 while the agent is still booting must not render as "no servers
  // registered" — retry, and never downgrade a snapshot we already have
  // (the useConnections / ws-boot-view lesson).
  const reload = useCallback(() => {
    const attempt = (left: number) => {
      api("api/mcp-servers")
        .then((d) => {
          if (d && !d.error) {
            setReg({ servers: Array.isArray(d.servers) ? d.servers : [], ...d });
            return;
          }
          if (left > 0 && isTransientErr(d)) setTimeout(() => attempt(left - 1), 1200);
          else setReg((prev) => prev ?? { servers: [] });
        })
        .catch(() => {
          if (left > 0) setTimeout(() => attempt(left - 1), 1200);
          else setReg((prev) => prev ?? { servers: [] });
        });
    };
    attempt(2);
  }, []);
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  const save = async (f: Form) => {
    const path = f.id ? `api/mcp-servers/${encodeURIComponent(f.id)}` : "api/mcp-servers";
    const res = await apiJSON(path, f.id ? "PUT" : "POST", bodyOf(f));
    if (res && res.error) {
      toast(tr("mcp.save_failed", { msg: errText(res.error) }));
      return false;
    }
    setForm(null);
    reload();
    return true;
  };

  const test = async (f: Form): Promise<void> => {
    const key = f.id;
    setProbes((p) => {
      const { [key]: _drop, ...rest } = p;
      return rest;
    });
    const res = await apiJSON("api/mcp-servers/test", "POST", bodyOf(f));
    if (res && res.error) {
      setProbes((p) => ({ ...p, [key]: { ok: false, toolCount: 0, elapsedMs: 0, error: errText(res.error) } }));
      return;
    }
    setProbes((p) => ({ ...p, [key]: res as ProbeResult }));
  };

  const setEnabled = async (s: McpServer, on: boolean) => {
    const res = await rawJSON(`api/mcp-servers/${encodeURIComponent(s.id)}/enabled`, "POST", { enabled: on });
    if (!res.ok) {
      const j = await res.json().catch(() => null);
      toast(tr("mcp.save_failed", { msg: errText(j?.error) || String(res.status) }));
      return;
    }
    reload();
  };

  const remove = async (s: McpServer) => {
    const r = await apiJSON(`api/mcp-servers/${encodeURIComponent(s.id)}`, "DELETE");
    if (r && r.error) {
      toast(tr("mcp.save_failed", { msg: errText(r.error) }));
      return;
    }
    if (form?.id === s.id) setForm(null);
    reload();
  };

  if (!running) {
    return (
      <EmptyState icon="debug-disconnect" title={tr("mcp.ws_required_title")} hint={tr("mcp.ws_required_hint")}>
        <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
          {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
        </Button>
      </EmptyState>
    );
  }
  if (!reg) return <p className="muted pad">{tr("common.loading")}</p>;

  const shadowed = reg.shadowed || [];
  return (
    <div className="mcp-tab">
      <Hint>{tr("mcp.intro")}</Hint>
      {shadowed.length > 0 && (
        <p className="ps-note ps-note-warn">{tr("mcp.shadowed", { names: shadowed.join(", ") })}</p>
      )}
      {reg.servers.length === 0 ? (
        <p className="muted mcp-empty">{tr("mcp.empty")}</p>
      ) : (
        <ul className="ssm-list mcp-list">
          {reg.servers.map((s) =>
            form && form.id === s.id ? (
              <li key={s.id} className="ssm-item">
                <ServerForm
                  form={form}
                  setForm={setForm}
                  onSave={save}
                  onTest={test}
                  probe={probes[s.id]}
                  submitLabel={tr("common.save")}
                />
              </li>
            ) : (
              <ServerRow
                key={s.id}
                s={s}
                probe={probes[s.id]}
                onEdit={() => setForm(formOf(s))}
                onTest={() => void test(formOf(s))}
                onToggle={(on) => void setEnabled(s, on)}
                onDelete={() => void remove(s)}
              />
            ),
          )}
        </ul>
      )}
      {form && form.id === "" ? (
        <div className="mcp-newform">
          <ServerForm
            form={form}
            setForm={setForm}
            onSave={save}
            onTest={test}
            probe={probes[""]}
            submitLabel={tr("mcp.add")}
          />
        </div>
      ) : (
        !form && (
          <button className="ghost ssm-add-toggle" onClick={() => setForm(emptyForm())}>
            <Icon name="add" /> {tr("mcp.add")}
          </button>
        )
      )}
    </div>
  );
}

// --- list row --------------------------------------------------------------------

function OriginBadge({ origin }: { origin: string }) {
  const tr = useT();
  const label =
    origin === "tenant" ? tr("mcp.origin_tenant") : origin === "builtin" ? tr("mcp.origin_builtin") : tr("mcp.origin_user");
  return <span className={"mcp-origin mcp-origin-" + origin}>{label}</span>;
}

function ServerRow({
  s,
  probe,
  onEdit,
  onTest,
  onToggle,
  onDelete,
}: {
  s: McpServer;
  probe?: ProbeResult;
  onEdit: () => void;
  onTest: () => void;
  onToggle: (on: boolean) => void;
  onDelete: () => void;
}) {
  const tr = useT();
  const askConfirm = useConfirm();
  const del = async () => {
    const ok = await askConfirm({
      title: tr("mcp.del_title"),
      body: tr("mcp.del_body", { name: s.name }),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (ok) onDelete();
  };
  const kinds = s.kinds && s.kinds.length > 0 ? s.kinds.map((k) => agentOf(k).label).join(" / ") : tr("mcp.kinds_all");

  return (
    <li className={"ssm-item mcp-item" + (s.enabled ? "" : " off")}>
      <div className="ssm-item-head">
        <span className="ssm-alias mcp-name">{s.name}</span>
        {s.label && <span className="mcp-label">{s.label}</span>}
        <OriginBadge origin={s.origin} />
        <span className="mcp-transport">{s.transport === "stdio" ? tr("mcp.tp_stdio") : tr("mcp.tp_http")}</span>
        <span className="mcp-actions">
          <button className="ghost mcp-btn" onClick={onTest}>
            {tr("mcp.test")}
          </button>
          {s.editable && (
            <button className="ghost mcp-btn" onClick={onEdit}>
              {tr("mcp.edit")}
            </button>
          )}
          <button
            className={"ghost mcp-btn" + (s.enabled ? "" : " on")}
            onClick={() => onToggle(!s.enabled)}
            title={s.enabled ? tr("mcp.disable") : tr("mcp.enable")}
          >
            {s.enabled ? tr("mcp.disable") : tr("mcp.enable")}
          </button>
          {s.editable && (
            <button className="ghost danger ssm-del" onClick={del}>
              {tr("common.delete")}
            </button>
          )}
        </span>
      </div>
      <div className="ssm-meta">
        {s.transport === "stdio" ? (
          <Meta k={tr("mcp.f_command")} v={[s.command, ...(s.args || [])].filter(Boolean).join(" ")} wide />
        ) : (
          <Meta k="URL" v={s.url} wide />
        )}
        <Meta k={tr("mcp.f_targets")} v={targetsText(s.targets, tr)} mono={false} />
        <Meta k={tr("mcp.f_kinds")} v={kinds} mono={false} />
        {s.transport === "stdio" && Object.keys(s.env || {}).length > 0 && (
          <Meta k={tr("mcp.f_env")} v={Object.keys(s.env || {}).join(", ")} />
        )}
        {s.transport === "http" && Object.keys(s.headers || {}).length > 0 && (
          <Meta k={tr("mcp.f_headers")} v={Object.keys(s.headers || {}).join(", ")} />
        )}
      </div>
      {/* A row that is on but not "ready" would start and fail — say why rather than
          leaving the user to discover it from a broken session. */}
      {s.enabled && !s.ready && <p className="ps-note ps-note-warn">{tr("mcp.not_ready")}</p>}
      {s.origin === "builtin" && <p className="ps-note">{tr("mcp.builtin_note")}</p>}
      {s.origin === "tenant" && <p className="ps-note">{tr("mcp.tenant_note")}</p>}
      <ProbeView probe={probe} />
    </li>
  );
}

function targetsText(tg: { assistant: boolean; session: boolean } | undefined, tr: ReturnType<typeof useT>): string {
  const on: string[] = [];
  if (tg?.assistant) on.push(tr("mcp.target_assistant"));
  if (tg?.session) on.push(tr("mcp.target_session"));
  return on.length > 0 ? on.join(" / ") : tr("mcp.target_none");
}

function Meta({ k, v, mono = true, wide = false }: { k: ReactNode; v?: ReactNode; mono?: boolean; wide?: boolean }) {
  return (
    <div className={"ssm-meta-row" + (wide ? " wide" : "")}>
      <span className="ssm-meta-k">{k}</span>
      <span className={"ssm-meta-v" + (mono ? " mono" : "")}>{v || "—"}</span>
    </div>
  );
}

// ProbeView renders one connection-test outcome (docs/48 §10). On failure the server's
// own stderr / body tail is shown verbatim — a broken command almost always explains
// itself there, and paraphrasing it would only hide the cause.
function ProbeView({ probe }: { probe?: ProbeResult }) {
  const tr = useT();
  if (!probe) return null;
  if (!probe.ok) {
    return (
      <div className="mcp-probe bad">
        <Icon name="error" />
        <div>
          <div>{probe.error || tr("mcp.test_failed")}</div>
          {probe.detail && <pre className="mcp-probe-detail">{probe.detail}</pre>}
        </div>
      </div>
    );
  }
  return (
    <div className="mcp-probe ok">
      <Icon name="pass" />
      <div>
        <div>
          {tr("mcp.test_ok", {
            name: probe.serverName || "?",
            version: probe.serverVersion || "?",
            count: probe.toolCount,
            ms: probe.elapsedMs,
          })}
        </div>
        {probe.revision && <div className="muted">{tr("mcp.test_revision", { rev: probe.revision })}</div>}
        {probe.tools && probe.tools.length > 0 && <div className="muted mono">{probe.tools.join(", ")}</div>}
      </div>
    </div>
  );
}

// --- form ------------------------------------------------------------------------

function Field({
  label,
  req,
  hint,
  wide,
  children,
}: {
  label: ReactNode;
  req?: boolean;
  hint?: ReactNode;
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={"ssm-fld" + (wide ? " wide" : "")}>
      <label>
        {label}
        {req && <span className="req">*</span>}
      </label>
      {children}
      {hint && <div className="hint">{hint}</div>}
    </div>
  );
}

// KVEditor edits env / header pairs. Values render as password inputs: a stored secret
// arrives as "***" and going back unchanged keeps it, so the real credential is never
// in the DOM — and a newly typed one isn't shoulder-surfable either.
function KVEditor({
  rows,
  onChange,
  keyPlaceholder,
  addLabel,
}: {
  rows: KV[];
  onChange: (rows: KV[]) => void;
  keyPlaceholder: string;
  addLabel: string;
}) {
  const tr = useT();
  const patch = (i: number, part: Partial<KV>) =>
    onChange(rows.map((r, j) => (i === j ? { ...r, ...part } : r)));
  return (
    <div className="mcp-kv">
      {rows.map((r, i) => (
        <div key={i} className="mcp-kv-row">
          <input
            className="cinput"
            placeholder={keyPlaceholder}
            value={r.k}
            onChange={(e) => patch(i, { k: e.target.value })}
          />
          <input
            className="cinput"
            type="password"
            placeholder={tr("mcp.kv_value")}
            value={r.v}
            onChange={(e) => patch(i, { v: e.target.value })}
          />
          <button
            className="ghost danger mcp-btn"
            title={tr("common.delete")}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            <Icon name="close" />
          </button>
        </div>
      ))}
      <button className="ghost mcp-btn" onClick={() => onChange([...rows, { k: "", v: "" }])}>
        <Icon name="add" /> {addLabel}
      </button>
      {rows.some((r) => r.v === MASKED) && <div className="hint">{tr("mcp.kv_masked_hint")}</div>}
    </div>
  );
}

function ServerForm({
  form,
  setForm,
  onSave,
  onTest,
  probe,
  submitLabel,
}: {
  form: Form;
  setForm: (f: Form | null) => void;
  onSave: (f: Form) => Promise<boolean>;
  onTest: (f: Form) => Promise<void>;
  probe?: ProbeResult;
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
        {codexUnsupported(form.transport, form.headers) && (
          <p className="ps-note ps-note-warn">{tr("mcp.codex_header_warn")}</p>
        )}
        {form.session && <p className="ps-note">{tr("mcp.session_restart_note")}</p>}
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
