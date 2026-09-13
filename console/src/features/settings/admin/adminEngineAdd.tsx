import { useEffect, useState, type ReactNode } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { EngineIngest } from "./adminEngineModels.tsx";
import { engineIsImage, useEngineRows, type IngestJob } from "./engineTypes.ts";

// adminEngineAdd.tsx — 「モデルを追加」 as a PANE (ADR 0072 follow-up).
//
// 🔴 Why a pane and not a dialog. What this screen starts runs for MINUTES — a 6.9 GB
// checkpoint over somebody else's network — and the question it exists to answer is not "was
// the download started" but "can a member use the model now". Those are separated by a download
// and one enable press. A dialog is in the way of everything else on screen, so it gets
// dismissed, and the thread is then only recoverable by remembering where to look. A pane can
// be left open, looked away from, popped out into its own window, and it can end where the work
// actually ends: the row, enabled.
//
// It reads the catalogue itself rather than being handed it. A pane outlives the admin dialog
// that opened it (and a popped-out one never had it), so anything it needed as a prop would be
// stale or absent exactly when somebody comes back to it.

export function EngineAddView({
  engineKey,
  lora,
  headerActions,
}: {
  engineKey: string;
  lora: boolean;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const { rows, err, load } = useEngineRows();
  /** The job this pane started, once it has one: the pane stops being a form and becomes the
   *  progress of the thing it started. */
  const [job, setJob] = useState<IngestJob | null>(null);
  const row = (rows || []).find((e) => e.key === engineKey);

  return (
    <div className="engines-add-pane admin-stage">
      {/* The shared pane header, so this view's heading and the tab actions (pop out, close)
          line up with every other pane's — a head of its own drifted out of alignment and
          collided with the chrome the pane already draws (measured headless). */}
      <ViewHead actions={headerActions}>
        <span className="view-title">
          <Icon name="download" /> {tr("pane.kind.engine_add")}
        </span>
      </ViewHead>
      {err && <p className="form-err pad">{err}</p>}
      {/* The grant can be gone by the time somebody returns to a pane they left open — or a
          restored layout can carry this view for a member who never had it. Said plainly
          instead of drawing controls whose every press is a 403. */}
      {rows !== null && !row && <p className="muted pad">{tr("admin.engines_add_pane_gone")}</p>}
      {row && job && <EngineAddProgress engineKey={engineKey} job={job} onReload={load} />}
      {row && !job && (
        <EngineIngest
          engineKey={engineKey}
          isImage={engineIsImage(row)}
          isLora={lora}
          baseModels={row.base_models}
          fileFlags={row.file_flags}
          models={(row.model_rows || []).filter((m) => (m.kind === "lora") === lora)}
          cardMiB={row.class?.vram_mib}
          busy={false}
          open
          setOpen={() => {}}
          onStarted={load}
          onJob={setJob}
        />
      )}
    </div>
  );
}

/** After the press: what the download is doing, and — when it is done — the one act that makes
 *  the row usable. 🔴 This is the half a dialog could not carry. The row an ingest creates is
 *  DISABLED by design (a licence is read at the moment somebody switches it on), so "taken in"
 *  and "usable" are two states and the second one is a press somebody has to find. Here it is
 *  where they already are. */
function EngineAddProgress({
  engineKey,
  job,
  onReload,
}: {
  engineKey: string;
  job: IngestJob;
  onReload: () => void;
}) {
  const tr = useT();
  const [state, setState] = useState<IngestJob>(job);
  const [err, setErr] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const done = state.state === "done";
  const failed = state.state === "failed";

  // Polled while it runs, and not after: a finished job does not change again, and this pane
  // may be left open for hours.
  useEffect(() => {
    if (done || failed) return;
    let live = true;
    const tick = async () => {
      const d: { jobs?: IngestJob[] } = await apiJSON(
        "api/admin/engines/" + encodeURIComponent(engineKey) + "/ingest",
        "GET",
      );
      if (!live) return;
      const mine = (d?.jobs || []).find((j) => j.id === state.id);
      if (mine) setState(mine);
      if (mine && (mine.state === "done" || mine.state === "failed")) onReload();
    };
    const h = setInterval(tick, 5000);
    void tick();
    return () => {
      live = false;
      clearInterval(h);
    };
  }, [engineKey, state.id, done, failed, onReload]);

  return (
    <section className="admin-panel engines-add-progress">
      <p className={failed ? "form-err" : "muted"}>
        {(tr(
          failed
            ? "admin.engines_add_pane_failed"
            : done
              ? "admin.engines_add_pane_done"
              : "admin.engines_add_pane_running",
        ) as string).replace("{id}", state.model_id || "")}
      </p>
      {failed && state.message && <p className="form-err mono">{state.message}</p>}
      {err && <p className="form-err">{err}</p>}
      {done && !enabled && (
        <button
          type="button"
          className="primary sm"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            setErr("");
            const d: { error?: unknown } = await apiJSON(
              "api/admin/engines/" + encodeURIComponent(engineKey) + "/models/" + encodeURIComponent(state.model_id),
              "PUT",
              { enabled: true },
            );
            setBusy(false);
            // 🔴 The guards answer HERE, which is the point of ending in this pane: a row that
            // cannot generate (no family, a file its workflow reads, a checkpoint with no VAE)
            // refuses the enable, and the reason belongs in front of the person who just paid
            // for the download — not on a screen they have to go and find.
            if (d?.error) {
              setErr(errDetail(d.error as never));
              return;
            }
            setEnabled(true);
            onReload();
          }}
        >
          {tr("admin.engines_add_pane_enable")}
        </button>
      )}
      {enabled && <p className="muted">{tr("admin.engines_add_pane_enabled")}</p>}
    </section>
  );
}
