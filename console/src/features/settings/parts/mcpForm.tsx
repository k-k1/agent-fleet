import type { ReactNode } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { MASKED } from "../mcp/mcpWire.ts";
import type { KV } from "../mcp/mcpWire.ts";

// Form primitives shared by the member registry (McpTab), the tenant distribution
// form (AdminTab の McpAdminView), and the SSM tab (SsmTab). The forms edit almost the
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

// CheckRow — a wrapped row of checkboxes (利用先 / 対象エージェント / 有効). Exists so a
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
