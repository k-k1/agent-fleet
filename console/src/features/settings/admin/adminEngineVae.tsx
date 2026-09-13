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
  /** 🔴 The header could not be READ AGAIN, and the plan rests on what the row recorded when it
   *  was marked. Measured on af-sandbox: the row's source is a Civitai version, Civitai answered
   *  503, and the fix — which downloads from Hugging Face and never touches that source — must
   *  not be taken down by the diagnosis it does not need. */
  recheck_failed?: string;
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
  /** 🔴 The rows the scan could not READ, by id, with the upstream's own words.
   *
   * Without this the answer to "the source is down" was silence: no verdict, so no mark, so no
   * button — and a row that cannot generate looks exactly like one that can. Measured on
   * af-sandbox (2026-09-13), where Civitai answered 503. Held in memory rather than stored,
   * because it describes an upstream at a moment and not the file. */
  const [unread, setUnread] = useState<Record<string, string>>({});
  useEffect(() => {
    let live = true;
    (async () => {
      for (const row of rows) {
        if (!engineIsImage(row) || done.current[row.key]) continue;
        if (!(row.model_rows || []).some((m) => m.vae_unread)) continue;
        done.current[row.key] = true;
        const d: { read?: { id?: string; unreadable?: string }[] } = await apiJSON(
          "api/admin/engines/" + encodeURIComponent(row.key) + "/models/vae-scan",
          "POST",
          {},
        );
        if (!live) return;
        const bad: Record<string, string> = {};
        for (const r of d?.read || []) {
          if (r?.id && r.unreadable) bad[r.id] = r.unreadable;
        }
        if (Object.keys(bad).length) setUnread((cur) => ({ ...cur, ...bad }));
        // A read that changed nothing must not reload the panel: the rows would be replaced
        // under whoever is reading them for no new information.
        if ((d?.read || []).some((r) => !r?.unreadable)) onDone();
      }
    })();
    return () => {
      live = false;
    };
  }, [rows, onDone]);
  return unread;
}

/** The row's own line: what is wrong, and the button that fixes it. */
export function ModelVaeFix({
  engineKey,
  model,
  pending,
  unreadable,
  onDone,
}: {
  engineKey: string;
  model: EngineModel;
  pending?: boolean;
  /** What the scan got instead of an answer for this row, when it got one. Present means the
   *  question could not be ASKED — a different sentence from "this checkpoint has no VAE", and
   *  one the panel has to say out loud or the row goes quiet about being unusable. */
  unreadable?: string;
  onDone: () => void;
}) {
  const tr = useT();
  const [plan, setPlan] = useState<VaePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  /** The refusal that has a way past it: nothing recorded AND the source would not answer. The
   *  operator has watched this row fail in the engine, so they are allowed to say so. */
  const [offerForce, setOfferForce] = useState(false);
  const [note, setNote] = useState("");
  const path = "api/admin/engines/" + encodeURIComponent(engineKey) + "/models/" + encodeURIComponent(model.id) + "/vae";

  const call = async (body: Record<string, unknown>) => {
    setBusy(true);
    setErr("");
    const d: (VaePlan & VaeResult & { error?: { code?: string } }) | undefined = await apiJSON(
      path,
      "POST",
      body,
    );
    setBusy(false);
    if (d && "error" in d && d.error) {
      setErr(errDetail(d.error as never));
      setOfferForce(d.error.code === "engine_vae_unreadable");
      return null;
    }
    setOfferForce(false);
    return d;
  };

  if (note) {
    return <p className="muted engines-model-vae">{note}</p>;
  }
  return (
    <div className="engines-model-vae">
      <p className={model.vae_missing ? "form-err" : "muted"}>
        {model.vae_missing
          ? tr("admin.engines_model_vae_missing")
          : (tr("admin.engines_model_vae_unknown") as string).replace("{e}", unreadable || "")}
      </p>
      {err && <p className="form-err">{err}</p>}
      {/* The source would not answer and this deployment has nothing recorded, so it will not
          spend 335 MB on a guess — but the person reading this has seen the row fail. */}
      {(offerForce || (!!unreadable && !model.vae_missing)) && !plan && (
        <button
          type="button"
          className="sm"
          disabled={busy || pending}
          onClick={async () => {
            const d = await call({ check: true, force: true });
            if (d) setPlan({ ...d, recheck_failed: d.recheck_failed || err });
          }}
        >
          {tr("admin.engines_model_vae_force")}
        </button>
      )}
      {/* The ordinary press. Skipped when the scan has ALREADY been refused by that source —
          asking again would be one more request to a host that has just said no, and the button
          above is the honest offer in that state. */}
      {!plan && !(unreadable && !model.vae_missing) && (
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
          {/* Said before the plan it qualifies: what follows rests on the earlier reading, not
              on one taken just now. */}
          {plan.recheck_failed && (
            <p className="muted">
              {(tr("admin.engines_model_vae_recheck_failed") as string).replace("{e}", plan.recheck_failed)}
            </p>
          )}
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
                // `force` rides ONLY when it is true: it is an assertion by a person, and a
                // request that carries it as false every time makes the audit of the one that
                // meant it unreadable.
                const d = await call({
                  ...(plan.staged ? {} : { licenseAccepted: true }),
                  ...(plan.recheck_failed ? { force: true } : {}),
                });
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
