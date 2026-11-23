import { useCallback, useEffect, useState } from "react";
import { errDetail } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getEngines } from "./adminApi.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { loadEngines, saveEngines } from "./catalogMemory.ts";
import type { ComfyLanStatus } from "./adminEngineLan.tsx";
import { CATALOG_SOURCES_DEFAULT, type CatalogSource, type EngineRow } from "./engineTypes.ts";
import { useReadGuard } from "../../../lib/readGuard.ts";

function catalogSources(value: unknown): CatalogSource[] {
  if (!Array.isArray(value)) return [...CATALOG_SOURCES_DEFAULT];
  const known: CatalogSource[] = ["hf", "civitai", "civitai-red"];
  return known.filter((source) => value.includes(source));
}

/** Everything both screens need out of `GET /api/admin/engines`, read once per screen.
 *
 * 🔴 `isSuper` comes from the answer's own flag and is never inferred from which fields arrived:
 * the tenant_admin row is a strict SUBSET of the operator's (ADR 0072 open question 11), so
 * "mode is undefined" would work today and start drawing 403-ing buttons the day a field is
 * renamed. It defaults to false, so a Control Plane too old to send the flag shows the safe half
 * rather than controls that cannot work.
 *
 * Two screens asking separately costs one extra GET on a screen change and buys the thing that
 * matters: neither of them has to be mounted for the other to work. */
export function useEngineRows() {
  const tr = useT();
  // The last answer, so a pane that is mounted again — the return from another tab — draws the
  // catalogue it had instead of a page of nothing for the length of one round trip. It is still
  // re-read below; this decides only what is on screen while that happens.
  const [held] = useState(() => loadEngines());
  const [rows, setRows] = useState<EngineRow[] | null>(held?.rows ?? null);
  const [isSuper, setIsSuper] = useState(held?.isSuper ?? false);
  // The source tab strips are drawn from this and never from a literal: three strips each
  // keeping their own list is three places for a deployment's gate to be forgotten.
  const [sources, setSources] = useState<CatalogSource[]>(held?.sources ?? [...CATALOG_SOURCES_DEFAULT]);
  const [err, setErr] = useState("");
  // The LAN ComfyUI panel's state (#957), operator-only and absent for anyone else. Not held
  // across mounts: it is one panel's form state, not something a returning pane has to draw.
  const [comfyLan, setComfyLan] = useState<ComfyLanStatus | null>(null);

  // Reads come from the mount, the operator screen's poll and the re-reads after an edit, and
  // they can answer out of order: an older one must not put back the rows a newer read has
  // drawn. An edit claims only its own row's lane ("row:<key>"), so a read begun before the
  // edit answered keeps that row as the edit left it and still lands every other row: dropping
  // the whole answer could hide another engine starting and leave nothing on screen to poll for.
  const guard = useReadGuard();
  const load = useCallback(async () => {
    const tk = guard.begin();
    try {
      const d = await getEngines();
      if (d && !isOk(d)) {
        if (tk.land("err")) setErr(errDetail(d.error));
        return;
      }
      if (tk.land("err")) setErr("");
      if (!tk.land("rows")) return;
      const answered = Array.isArray(d?.engines) ? d.engines : [];
      const edited = new Set(answered.filter((e) => !tk.land("row:" + e.key)).map((e) => e.key));
      const superAdmin = !!d?.super_admin;
      const offered = catalogSources(d?.catalog_sources);
      setIsSuper(superAdmin);
      setSources(offered);
      const keepEdited = (held: EngineRow[] | null | undefined) =>
        edited.size ? answered.map((e) => (edited.has(e.key) ? (held?.find((c) => c.key === e.key) ?? e) : e)) : answered;
      setRows((cur) => keepEdited(cur));
      setComfyLan(d?.comfy_lan ?? null);
      // The cache is shared with the other screens that hold their own rows, so an edited row
      // keeps the cache's copy, which patchRow already updated.
      saveEngines({ rows: keepEdited(loadEngines()?.rows), isSuper: superAdmin, sources: offered });
    } catch {
      if (tk.land("err")) setErr(tr("admin.load_error"));
    }
  }, [guard, tr]);
  useEffect(() => {
    load();
  }, [load]);
  /** Land an edit's answer for one row: newer than any read in flight for that row, and patched
   *  into the shared cache for that key alone. Writing this screen's whole list there would put
   *  back rows another screen has read since. */
  const patchRow = useCallback(
    (key: string, d: Partial<EngineRow>) => {
      // A screen closed while its edit was in flight writes nothing: by now the cache may hold a
      // newer read from a screen still open.
      if (!guard.begin().current()) return;
      guard.claim("row:" + key);
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
      const cached = loadEngines();
      if (cached) saveEngines({ ...cached, rows: cached.rows.map((e) => (e.key === key ? { ...e, ...d } : e)) });
    },
    [guard],
  );

  // For the edits: their error (or its clearing) is newer than any read in flight.
  const setEditErr = useCallback(
    (v: string) => {
      guard.claim("err");
      setErr(v);
    },
    [guard],
  );

  return { rows, isSuper, sources, comfyLan, err, setErr: setEditErr, load, patchRow };
}
