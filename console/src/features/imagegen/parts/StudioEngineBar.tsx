// StudioEngineBar — the member's model pickers and the engine's state (ADR 0100 §4: the model
// is the member's, so it sits outside the draft the agent edits).
//
// Drawn twice by ImagegenView: inline in the pane's head, and as a full-width band at the top of
// the settings tab. imagegen.css shows exactly one of them per pane width — on a phone the head's
// single nowrap row squeezed the model select to zero width under the pane's own buttons, which
// left the whole studio unusable (no model, so no agent either). Both copies are controlled by the
// same draft and patch, so there is no state to split.
import { useT } from "../../../lib/i18n/index.ts";
import { IconButton } from "../../../ui/Button.tsx";
import type { ImagegenDraft } from "../draft.ts";
import type { EngineState } from "../jobs.ts";
import type { ImagegenModel, ImagegenProvider } from "../wire.ts";
import { ModelSelect } from "./GenerateForm.tsx";

export function StudioEngineBar({
  variant,
  draft,
  patch,
  fleetProviders,
  provider,
  models,
  state,
  engineLine,
  onRefresh,
}: {
  variant: "head" | "band";
  draft: ImagegenDraft;
  patch: (p: Partial<ImagegenDraft>) => void;
  fleetProviders: ImagegenProvider[];
  provider: ImagegenProvider | null;
  models: ImagegenModel[];
  state: EngineState;
  /** The state in words: the cold start's minutes, the unavailable code's reason. */
  engineLine: string;
  /** Re-reads the status; the band carries it because the narrow head has no room. */
  onRefresh?: () => void;
}) {
  const tr = useT();
  const chip =
    state === "ready"
      ? tr("imggen.engine_ready")
      : state === "cold"
        ? tr("imggen.engine_cold")
        : state === "starting"
          ? tr("imggen.engine_starting")
          : tr("imggen.engine_unavailable");
  return (
    <div className={"igen-enginebar igen-enginebar-" + variant}>
      <span className="igen-head-model">
        <ModelSelect
          draft={draft}
          patch={patch}
          fleetProviders={fleetProviders}
          provider={provider}
          models={models}
          compact={variant === "head"}
        />
      </span>
      {/* The cost note is a tooltip, not a line: it never changes, and on a phone a constant
          sentence is width the model picker needs. Never "$0.00" — comfy's CostUSD is 0 by
          construction and the attribution is the administrator's hourly table. */}
      <span className={"igen-engine igen-engine-" + state} title={`${engineLine}\n${tr("imggen.cost_note")}`}>
        <span className="igen-dot" />
        {chip}
      </span>
      {/* Only when it says something the chip does not: "ready" twice is noise, while the
          cold start's minutes and the unavailable code's reason are the point. */}
      {state !== "ready" && <span className="igen-engine-hint muted">{engineLine}</span>}
      {variant === "band" && onRefresh && (
        <IconButton icon="refresh" label={tr("imggen.refresh")} className="igen-band-refresh" onClick={onRefresh} />
      )}
    </div>
  );
}
