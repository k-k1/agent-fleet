// テナントの既定のマシン種別（docs/log/70 §70.4.3）。
//
// ★ 置き場が「上限」セクションなのに super_admin 専用でないのは、これがテナント管理者
// のものだからである。運用者が種類を宣言し、super_admin がテナントに許す集合を決め、
// **その中からどれを既定にするかはテナントが決める**——接続元制限（docs/log/66）と同じ切り方で、
// この値はテナントの外へ一切届かない。
//
// ★ 選べる集合はサーバが intersect 済みのものをそのまま写す。許可一覧に運用者が既に
// 消したクラスが残っていることがあり、それを画面が並べると「選べたのに効かない」項目に
// なる（保存は通り、解決で既定へ落ちる）。
//
// ★ 単一クラスのデプロイでは editable=false が返り、この面は「選択肢が無い」ことを
// 書いて終わる。1 択の選択肢を出すのは、答えが 1 つしかない質問を足すのと同じ。
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
