import { useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { AUTH_MODES, MASKED } from "../mcp/mcpWire.ts";
import type { AdvancedValues, AuthMode, KV } from "../mcp/mcpWire.ts";

// Form primitives shared by the member registry (McpTab), the tenant distribution
// form (McpAdminView in AdminTab), and the SSM tab (SsmTab). The forms edit almost the
// same definition, so they render with the same field/label/hint furniture
// (.ssm-fld / .mcp-checks) — keeping each form from drifting into its own ad-hoc layout.

export function Field({
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

// Meta renders one labeled key/value row inside a list card. Empty values show "—".
// `wide` spans the full grid width (for long values like a start URL).
export function Meta({ k, v, mono = true, wide = false }: { k: ReactNode; v?: ReactNode; mono?: boolean; wide?: boolean }) {
  return (
    <div className={"ssm-meta-row" + (wide ? " wide" : "")}>
      <span className="ssm-meta-k">{k}</span>
      <span className={"ssm-meta-v" + (mono ? " mono" : "")}>{v || "—"}</span>
    </div>
  );
}

// KVEditor edits env / header pairs. Values render as password inputs: a stored secret
// arrives as "***" and going back unchanged keeps it, so the real credential is never
// in the DOM — and a newly typed one isn't shoulder-surfable either.
//
// noValue drops the value column entirely (tenant distribution with user_secret on):
// there the admin distributes header NAMES only and each member types their own value,
// so an input here would be a field with nothing to put in it.
export function KVEditor({
  rows,
  onChange,
  keyPlaceholder,
  addLabel,
  noValue,
}: {
  rows: KV[];
  onChange: (rows: KV[]) => void;
  keyPlaceholder: string;
  addLabel: string;
  noValue?: boolean;
}) {
  const tr = useT();
  const patch = (i: number, part: Partial<KV>) =>
    onChange(rows.map((r, j) => (i === j ? { ...r, ...part } : r)));
  return (
    <div className="mcp-kv">
      {rows.map((r, i) => (
        <div key={i} className={"mcp-kv-row" + (noValue ? " keyonly" : "")}>
          <input
            className="cinput"
            placeholder={keyPlaceholder}
            value={r.k}
            onChange={(e) => patch(i, { k: e.target.value })}
          />
          {!noValue && (
            <input
              className="cinput"
              type="password"
              placeholder={tr("mcp.kv_value")}
              value={r.v}
              onChange={(e) => patch(i, { v: e.target.value })}
            />
          )}
          <button
            className="ghost danger mcp-btn"
            title={tr("common.delete")}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            <Icon name="close" />
          </button>
        </div>
      ))}
      <div className="mcp-kv-add">
        <button className="ghost mcp-btn" onClick={() => onChange([...rows, { k: "", v: "" }])}>
          <Icon name="add" /> {addLabel}
        </button>
      </div>
      {rows.some((r) => r.v === MASKED) && <div className="hint">{tr("mcp.kv_masked_hint")}</div>}
    </div>
  );
}

// CheckRow — a wrapped row of checkboxes (used by / target agents / enabled). Exists so a
// single checkbox is laid out the same as a group of seven instead of inheriting
// whatever the surrounding form does to a bare <label>.
export function CheckRow({ children }: { children: ReactNode }) {
  return <div className="mcp-checks">{children}</div>;
}

export function Check({
  checked,
  onChange,
  children,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  children: ReactNode;
}) {
  return (
    <label className="mcp-check">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {children}
    </label>
  );
}

const AUTH_LABEL = {
  none: "mcp.auth_none",
  bearer: "mcp.auth_bearer",
  headers: "mcp.auth_headers",
} as const satisfies Record<AuthMode, string>;

// AuthFields — the Authentication selector of a remote server (member form, tenant form).
// Every mode is a branch below and an entry in AUTH_MODES / AUTH_LABEL, so another
// mode (OAuth) is one more of each, not a rework of the form.
//
// namesOnly is the tenant user_secret case: the admin names the header, each member types
// the value, so no value input is rendered (Bearer shows a note instead of a token field).
export function AuthFields({
  auth,
  onAuth,
  token,
  onToken,
  rows,
  onRows,
  tokenStored,
  namesOnly,
  headersHint,
}: {
  auth: AuthMode;
  onAuth: (a: AuthMode) => void;
  token: string;
  onToken: (t: string) => void;
  rows: KV[];
  onRows: (rows: KV[]) => void;
  tokenStored: boolean;
  namesOnly?: boolean;
  headersHint?: ReactNode;
}) {
  const tr = useT();
  return (
    <>
      <Field label={tr("mcp.f_auth")} wide>
        <div className="seg choice-seg">
          {AUTH_MODES.map((m) => (
            <button
              key={m}
              type="button"
              className={"seg-btn" + (auth === m ? " active" : "")}
              onClick={() => onAuth(m)}
            >
              {tr(AUTH_LABEL[m])}
            </button>
          ))}
        </div>
      </Field>
      {auth === "bearer" && (
        <Field label={tr("mcp.f_token")} wide hint={namesOnly ? tr("mcp.token_names_hint") : tr("mcp.token_hint")}>
          {!namesOnly && (
            <input
              className="cinput"
              type="password"
              autoComplete="off"
              placeholder={tokenStored ? tr("mcp.token_kept") : tr("mcp.token_placeholder")}
              value={token}
              onChange={(e) => onToken(e.target.value)}
            />
          )}
        </Field>
      )}
      {auth === "headers" && (
        <Field label={tr("mcp.f_headers")} wide hint={headersHint ?? tr("mcp.f_headers_hint")}>
          <KVEditor
            rows={rows}
            onChange={onRows}
            keyPlaceholder="Authorization"
            addLabel={tr("mcp.add_header")}
            noValue={namesOnly}
          />
        </Field>
      )}
    </>
  );
}

// AdvancedSection — targets / kinds / timeout / enabled, closed by default. The summary line
// shows the current values so a closed section still tells what is set; defaultOpen (decided
// once, at mount) opens it for an existing server whose values are not the defaults.
export function AdvancedSection({
  summary,
  defaultOpen,
  children,
}: {
  summary: ReactNode;
  defaultOpen: boolean;
  children: ReactNode;
}) {
  const tr = useT();
  const [open, setOpen] = useState(defaultOpen);
  return (
    <details
      className="mcp-adv ssm-fld wide"
      open={open}
      onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary className="mcp-adv-head">
        <span className="mcp-adv-title">{tr("mcp.adv_title")}</span>
        <span className="mcp-adv-sum">{summary}</span>
      </summary>
      <div className="ssm-fgrid mcp-adv-body">{children}</div>
    </details>
  );
}

// advancedSummary — "Assistant, Sessions · every agent · default timeout" (a disabled server
// gets a trailing "disabled"). withTimeout is false for the tenant form, which has no
// timeout field.
export function advancedSummary(
  tr: ReturnType<typeof useT>,
  v: AdvancedValues,
  kindLabel: (k: string) => string,
  withTimeout = true,
): string {
  const targets = [v.assistant && tr("mcp.target_assistant"), v.session && tr("mcp.target_session")].filter(Boolean);
  const parts = [
    targets.length ? targets.join(", ") : tr("mcp.target_none"),
    v.kinds.length ? v.kinds.map(kindLabel).join(", ") : tr("mcp.kinds_all"),
  ];
  if (withTimeout) {
    const ms = Number(v.timeoutMs);
    parts.push(ms > 0 ? tr("mcp.adv_timeout_ms", { ms }) : tr("mcp.adv_timeout_default"));
  }
  if (!v.enabled) parts.push(tr("mcp.adv_disabled"));
  return parts.join(" · ");
}
