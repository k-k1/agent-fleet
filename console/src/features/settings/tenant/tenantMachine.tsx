// The tenant's default machine class (docs/log/70 §70.4.3).
//
// It lives in the "limits" section yet is not super_admin only, because it belongs to the
// tenant admin: the operator declares the classes, super_admin decides the set a tenant may
// use, and the tenant picks which of those is its default — the same cut as the
// connection-source restriction (docs/log/66). This value never travels outside the tenant.
//
// The selectable set is copied verbatim from the server, which has already intersected it. An
// allow-list can still name a class the operator has since deleted, and listing that would
// offer an entry that can be picked but has no effect (the save succeeds and resolution falls
// back to the default).
//
// A single-class deployment answers editable=false, and this surface then only states that
// there is nothing to choose. Offering a one-option choice is asking a question with one
// possible answer.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtGbHint } from "../parts/adminShared.ts";
import type { WsSlotClass } from "../parts/adminShared.ts";

interface MachineView {
  slot_class?: string;
  classes?: WsSlotClass[];
  default_slot_class?: string;
  editable?: boolean;
}

export function TenantMachineView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [view, setView] = useState<MachineView | null>(null);
  const [pick, setPick] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/slot-class`);
      if (d && !d.error) {
        setView(d);
        setPick(d.slot_class || "");
      }
    } catch {
      /* transient; the panel keeps its last values */
    }
  }, [slug]);
  useEffect(() => {
    load();
  }, [load]);

  // Nothing to show at all until the first read answers, and nothing to show ever on a
  // deployment with no classes — the whole panel is absent rather than empty.
  if (!view || !view.editable || !view.classes?.length) return null;

  const save = async (want: string) => {
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/slot-class`, "PUT", {
        slot_class: want,
      });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setPick(want);
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
      load();
    } finally {
      setBusy(false);
    }
  };

  const spec = (c: WsSlotClass) => {
    const first = c.slots[0];
    const last = c.slots[c.slots.length - 1];
    if (!first) return c.arch;
    const range = first === last ? first.instance_type : `${first.instance_type}–${last.instance_type}`;
    return `${range} · ${fmtGbHint(first.mem_mib)}–${fmtGbHint(last.mem_mib)} · ${c.arch}`;
  };

  return (
    <section className="admin-panel machine-picker">
      <h4>{tr("tenant.machine_title")}</h4>
      <p className="admin-hint">{tr("tenant.machine_note")}</p>
      <div className="le-presets">
        <button
          className={pick === "" ? "chip on" : "chip"}
          disabled={busy}
          onClick={() => save("")}
        >
          {tr("tenant.machine_deploy_default")}
        </button>
        {view.classes.map((c) => (
          <button key={c.id} className={pick === c.id ? "chip on" : "chip"} disabled={busy} onClick={() => save(c.id)}>
            {c.label}
          </button>
        ))}
        {saved && (
          <span className="saved-note">
            <Icon name="check" /> {tr("admin.saved")}
          </span>
        )}
      </div>
      <ul className="admin-hint machine-specs">
        {view.classes.map((c) => (
          <li key={c.id}>
            <b>{c.label}</b> <span className="mono">{spec(c)}</span>
          </li>
        ))}
      </ul>
      {/* Members who chose for themselves are unaffected — say so, because the obvious
          reading of "the tenant default" is "everybody". */}
      <p className="admin-hint">{tr("tenant.machine_member_note")}</p>
    </section>
  );
}
