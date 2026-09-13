import { useEffect, useRef, useState } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { engineIsImage, type EngineModel, type EngineRow, type FamilyVae } from "./engineTypes.ts";

// adminEngineVae.tsx — the one fault a catalogue row cannot state about itself, and the one
// press that ends it (ADR 0072 follow-up).
//
// An SDXL checkpoint published without VAE tensors holds every file its family needs. The row
// validates, the engine loads it, `generate_image` offers it by name — and then every request
// dies inside ComfyUI with `VAE is invalid: None`, after the box has paid a 1-2.5 minute
// checkpoint switch. A member cannot act on that at all: the tool has no VAE argument.
//
// So the panel does the whole loop: it asks the CP to read the headers (`vae-scan`), draws the
// answer on the row, and offers the fix as ONE button. Everything it takes to fix it by hand —
// find the family's VAE upstream, take it in under the right role, retype the row's id to attach
// it — is exactly the road that was there before, and it was walked once in anger by the person
// who owns this deployment. Discovering an act by typing an id that collides is not a UI.

/** What `POST …/models/{id}/vae` answers. `check` asks for the plan, which is what the button
 *  shows before the press that spends anything. */
type VaePlan = FamilyVae & {
  vae_bundled?: string;
  /** "attach" = the bytes are here, "ingest" = a download under the licence named. */
  action?: string;
};

type VaeResult = {
  /** "none" (the header says it has one after all), "attached", "job_started". */
  action?: string;
  vae_bundled?: string;
};

/** The scan, run once per engine when any row's header has not been read.
 *
 * 🔴 One call for the catalogue rather than one per row: this fires on the panel's own load, and
 * a fan-out would make the number of upstream reads a property of how often somebody opens a
 * screen. The ref is what keeps a re-render from repeating it — the rows change identity on
 * every load, so a dependency on them would re-scan for ever. */
export function useVaeScan(rows: EngineRow[], onDone: () => void) {
  const done = useRef<Record<string, boolean>>({});
  useEffect(() => {
    let live = true;
    (async () => {
      for (const row of rows) {
        if (!engineIsImage(row) || done.current[row.key]) continue;
        if (!(row.model_rows || []).some((m) => m.vae_unread)) continue;
        done.current[row.key] = true;
        const d: { read?: unknown[] } = await apiJSON(
          "api/admin/engines/" + encodeURIComponent(row.key) + "/models/vae-scan",
          "POST",
          {},
        );
        // A read that changed nothing must not reload the panel: the rows would be replaced
        // under whoever is reading them for no new information.
        if (live && d?.read?.length) onDone();
      }
    })();
    return () => {
      live = false;
    };
  }, [rows, onDone]);
}

/** The row's own line: what is wrong, and the button that fixes it. */
export function ModelVaeFix({
  engineKey,
  model,
  pending,
  onDone,
}: {
  engineKey: string;
  model: EngineModel;
  pending?: boolean;
  onDone: () => void;
}) {
  const tr = useT();
  const [plan, setPlan] = useState<VaePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [note, setNote] = useState("");
  const path = "api/admin/engines/" + encodeURIComponent(engineKey) + "/models/" + encodeURIComponent(model.id) + "/vae";

  const call = async (body: Record<string, unknown>) => {
    setBusy(true);
    setErr("");
    const d: (VaePlan & VaeResult & { error?: unknown }) | undefined = await apiJSON(path, "POST", body);
    setBusy(false);
    if (d && "error" in d && d.error) {
      setErr(errDetail(d.error as never));
      return null;
    }
    return d;
  };

  if (note) {
    return <p className="muted engines-model-vae">{note}</p>;
  }
  return (
    <div className="engines-model-vae">
      <p className="form-err">{tr("admin.engines_model_vae_missing")}</p>
      {err && <p className="form-err">{err}</p>}
      {!plan && (
        <button
          type="button"
          className="sm"
          disabled={busy || pending}
          onClick={async () => {
            const d = await call({ check: true });
            if (!d) return;
            // The header can disagree with the mark: it is read again on every press, and a
            // "yes" here means the row was fine and the mark is what was wrong.
            if (d.vae_bundled === "yes") {
              setNote(tr("admin.engines_model_vae_none") as string);
              onDone();
              return;
            }
            setPlan(d);
          }}
        >
          {tr("admin.engines_model_vae_fix")}
        </button>
      )}
      {plan && (
        <div className="engines-model-vae-plan">
          {/* What the press will do, in the sentence that says which of the two it is. The
              cheap one is worth saying out loud: the bytes are already this deployment's, so
              there is no download, no minutes and no new licence to accept. */}
          <p className="muted">
            {plan.staged
              ? (tr("admin.engines_model_vae_plan_staged") as string).replace("{f}", plan.file || "")
              : (tr("admin.engines_model_vae_plan_ingest") as string)
                  .replace("{f}", (plan.repo || "") + "/" + (plan.file || ""))
                  .replace("{n}", plan.bytes ? Math.round(plan.bytes / 1e6) + " MB" : "?")
                  .replace("{l}", plan.license || "?")}
          </p>
          <div className="engines-model-vae-actions">
            <button
              type="button"
              className="sm primary"
              disabled={busy || pending || plan.unreachable}
              onClick={async () => {
                const d = await call(plan.staged ? {} : { licenseAccepted: true });
                if (!d) return;
                setNote(
                  tr(
                    d.action === "job_started"
                      ? "admin.engines_model_vae_started"
                      : "admin.engines_model_vae_attached",
                  ) as string,
                );
                onDone();
              }}
            >
              {tr(plan.staged ? "admin.engines_model_vae_attach" : "admin.engines_model_vae_accept")}
            </button>
            <button type="button" className="sm" disabled={busy} onClick={() => setPlan(null)}>
              {tr("admin.engines_model_vae_cancel")}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
