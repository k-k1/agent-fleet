import { useState } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { EngineRow } from "./engineTypes.ts";

// adminEngineDiscover.tsx — the button behind ADR 0082 decisions 6 and 7: what an external
// ComfyUI's own checkpoint/LoRA/VAE folders currently hold, read straight off its /object_info
// and offered as candidates for a row.
//
// What this panel deliberately does NOT do:
//
//   - it never writes a row on its own. Pressing the button only ever FILLS the list; turning a
//     candidate into a row is a second, separate press — the same `POST …/models` write the
//     `POST …/models` write the registered tab passes in (`onAdd`) — and the
//     family it carries is a SUGGESTION the CP read off the filename, not a declaration
//     (ADR 0072 decision 2 stays: the operator confirms it, here by seeing it named before the
//     press rather than by typing it from nothing).
//   - it never shows a VAE candidate an "add" button. A VAE is not a row of its own in this
//     catalogue — it is a FILE a checkpoint row's `--vae` flag names — so this only lists the
//     filename for the person building that row by hand to copy, the same way the manual form's
//     file fields already work.
//   - it never appears for a row this ADR does not cover. `engineCanDiscover` is the one
//     predicate both this file and the caller read, so the two cannot drift apart.

export type EngineDiscoverCandidate = { name: string; base_model_suggest?: string };
export type EngineDiscoverResult = {
  checkpoints?: EngineDiscoverCandidate[];
  loras?: EngineDiscoverCandidate[];
  vaes?: EngineDiscoverCandidate[];
};

/** Whether this row's own /object_info is worth a button at all (ADR 0082 decisions 6 and 7):
 *  an external ComfyUI this control plane can dial directly. A managed row is asleep most of
 *  the time — probing it would be the GPU purchase decision 7 forbids — and a borrowed row's
 *  catalogue is another deployment's read-only mirror with nothing of this deployment's own
 *  network behind it (ADR 0079 decision 7). Read off the same two fields the CP's own route
 *  gates on, so a row that would 400 there never shows the button here. */
export function engineCanDiscover(row: EngineRow): boolean {
  return row.lifecycle === "external" && row.provider === "comfy";
}

/** The id a candidate's filename becomes, absent anything better: the extension dropped, the
 *  rest kept verbatim. A person may still retype it — this only saves typing the common case,
 *  the way the ingest form's own prefill does. */
function candidateID(name: string): string {
  return name.replace(/\.[A-Za-z0-9]+$/, "") || name;
}

type AddAnswer = { error?: { code?: string; message?: string } } | undefined;

export function EngineDiscoverPanel({
  engineKey,
  busy,
  onAdd,
}: {
  engineKey: string;
  /** Disables the per-candidate add buttons while this engine's row has another write in
   *  flight — the same flag the registered tab reads, so the two never race the same
   *  catalogue. */
  busy?: boolean;
  /** Turn one candidate into a catalogue row (`POST …/models`), passed in rather than called
   *  directly here so busy/error state and the reload it triggers stay in the one place the
   *  registered tab already keeps them. */
  onAdd: (body: Record<string, unknown>) => Promise<AddAnswer>;
}) {
  const tr = useT();
  const [fetching, setFetching] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<EngineDiscoverResult | null>(null);
  const [addErr, setAddErr] = useState<Record<string, string>>({});
  const [added, setAdded] = useState<Record<string, boolean>>({});

  const press = async () => {
    setFetching(true);
    setErr("");
    const d: (EngineDiscoverResult & { error?: { code?: string; message?: string } }) | undefined =
      await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/discover`, "POST", {});
    setFetching(false);
    if (d && "error" in d && d.error) {
      setErr(errDetail(d.error as never));
      setResult(null);
      return;
    }
    setResult(d || {});
  };

  const add = async (kind: "checkpoint" | "lora", c: EngineDiscoverCandidate) => {
    const rowKey = kind + ":" + c.name;
    setAddErr((cur) => ({ ...cur, [rowKey]: "" }));
    const d = await onAdd({
      id: candidateID(c.name),
      kind: kind === "lora" ? "lora" : "checkpoint",
      base_model: c.base_model_suggest || "",
      files: [{ flag: "", s3Key: c.name }],
    });
    if (d?.error) {
      setAddErr((cur) => ({ ...cur, [rowKey]: errDetail(d.error as never) }));
      return;
    }
    setAdded((cur) => ({ ...cur, [rowKey]: true }));
  };

  const list = (kind: "checkpoint" | "lora", label: string, cands?: EngineDiscoverCandidate[]) => {
    if (!cands || cands.length === 0) return null;
    return (
      <div className="engines-discover-group">
        <p className="muted">{label}</p>
        <ul className="engines-discover-list">
          {cands.map((c) => {
            const rowKey = kind + ":" + c.name;
            return (
              <li key={rowKey} className="engines-discover-row">
                <span className="mono engines-discover-name">{c.name}</span>
                {c.base_model_suggest && <span className="tag">{c.base_model_suggest}</span>}
                <button
                  type="button"
                  className="sm"
                  disabled={busy || added[rowKey]}
                  onClick={() => add(kind, c)}
                >
                  {tr(added[rowKey] ? "admin.engines_discover_added" : "admin.engines_discover_add")}
                </button>
                {addErr[rowKey] && <p className="form-err">{addErr[rowKey]}</p>}
              </li>
            );
          })}
        </ul>
      </div>
    );
  };

  return (
    <div className="engines-discover">
      <button type="button" className="sm" disabled={fetching} onClick={press}>
        {tr(fetching ? "admin.engines_discover_busy" : "admin.engines_discover_button")}
      </button>
      {err && <p className="form-err">{err}</p>}
      {result && (
        <div className="engines-discover-results">
          {list("checkpoint", tr("admin.engines_discover_checkpoints") as string, result.checkpoints)}
          {list("lora", tr("admin.engines_discover_loras") as string, result.loras)}
          {!!result.vaes?.length && (
            <div className="engines-discover-group">
              <p className="muted">{tr("admin.engines_discover_vaes")}</p>
              <ul className="engines-discover-list">
                {result.vaes.map((v) => (
                  <li key={v.name} className="engines-discover-row">
                    <span className="mono engines-discover-name">{v.name}</span>
                  </li>
                ))}
              </ul>
              <p className="muted">{tr("admin.engines_discover_vae_hint")}</p>
            </div>
          )}
          {!result.checkpoints?.length && !result.loras?.length && !result.vaes?.length && (
            <p className="muted">{tr("admin.engines_discover_empty")}</p>
          )}
        </div>
      )}
    </div>
  );
}
