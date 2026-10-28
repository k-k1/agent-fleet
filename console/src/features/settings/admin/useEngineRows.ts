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

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/engines");
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      const answered = Array.isArray(d?.engines) ? d.engines : [];
      const superAdmin = !!d?.super_admin;
      const offered = catalogSources(d?.catalog_sources);
      setIsSuper(superAdmin);
      setSources(offered);
      setRows(answered);
      setComfyLan(d?.comfy_lan ?? null);
      saveEngines({ rows: answered, isSuper: superAdmin, sources: offered });
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);

  return { rows, isSuper, sources, comfyLan, err, setErr, setRows, load };
}
