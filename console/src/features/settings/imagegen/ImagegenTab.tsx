import { useEffect, useState } from "react";
import { isTransientErr } from "../../../core/api/client.ts";
import { OnOff, OrderList, Row } from "../parts/controls.tsx";
import {
  useSettings,
  setSetting,
  normalizeImageProviderOrder,
  collapseImageProviderOrder,
  expandImageProviderOrder,
  imageProviderLabel,
  type ImageFleetRow,
} from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { imagegenStatus } from "../../imagegen/api.ts";
import { isPreAdr0082Status, type ImagegenProvider } from "../../imagegen/wire.ts";

// ImagegenTab is Settings > Image generation (ADR 0138 D16): the generate_image tool af's own MCP
// server gives every kind, and the order its providers are tried in. A feature that uses
// credentials, so it is neither a credential nor a harness, and has its own page.
export function ImagegenTab() {
  const tr = useT();
  const s = useSettings();
  const running = useWorkspaceStore((st) => st.state) === "running";
  // The images rows this deployment actually declares (ADR 0082 decision 3), or null while there
  // is no live answer to trust — not running, still loading, a transient failure, or an Agent
  // build old enough to predate `kind` (isPreAdr0082Status). null is what tells imageOrder below
  // to fall back to the static IMAGE_PROVIDERS_RANKED guess rather than mistake "nothing fetched
  // yet" for "this deployment declares zero images rows" (a real, different answer — see
  // normalizeImageProviderOrder's own `rows === undefined` vs `[]` distinction).
  const [imagegenProviders, setImagegenProviders] = useState<ImagegenProvider[] | null>(null);

  // (Re)load when the workspace is running, including a stopped→running transition while the
  // dialog is open.
  useEffect(() => {
    if (!running) {
      // The Agent that answered is gone with it (the reasoning available.ts's
      // resetImagegenAvailability follows): a stopped workspace falls back to the static guess,
      // never keeps showing rows that may no longer be true.
      setImagegenProviders(null);
      return;
    }
    let live = true;
    imagegenStatus()
      .then((st) => {
        const providers = st && !isTransientErr(st) && Array.isArray(st.providers) ? st.providers : null;
        if (live) setImagegenProviders(providers && !isPreAdr0082Status(providers) ? providers : null);
      })
      .catch(() => live && setImagegenProviders(null));
    return () => {
      live = false;
    };
  }, [running]);

  // ADR 0082 decision 3: once the Agent has answered, the list is drawn one row per DECLARED
  // images row instead of the two static kind-name guesses; decision 8's fold no longer applies,
  // because now we know which of them this deployment runs.
  const liveImages = imagegenProviders !== null;
  const fleetRows: ImageFleetRow[] = imagegenProviders
    ? imagegenProviders.filter((p) => p.fleet).map((p) => ({ id: p.id, kind: p.kind || p.id }))
    : [];
  const imageOrder = liveImages
    ? normalizeImageProviderOrder(s.imageProviderOrder, fleetRows)
    : collapseImageProviderOrder(normalizeImageProviderOrder(s.imageProviderOrder));
  const imageLabels = Object.fromEntries(
    imageOrder.map((id) => {
      if (!liveImages) return [id, imageProviderLabel(id) || agentOf(id).assistantName];
      const row = imagegenProviders!.find((p) => p.id === id);
      if (!row?.fleet) return [id, agentOf(id).assistantName];
      // Neither the key alone ("image") nor the kind alone ("ComfyUI") says enough: the key is
      // what the member can act on (`provider` in generate_image names it), the kind tells them
      // which machine it is (ADR 0082 P1 follow-up).
      const kindLabel = row.kind === "comfy" ? tr("agents.image_kind_comfy")
        : row.kind === "openai-compat" ? tr("agents.image_kind_openai_compat")
        : "";
      return [id, kindLabel ? `${id} (${kindLabel})` : id];
    }),
  );

  return (
    <div className="conns">
      <section className="ds-group">
        <Row label={tr("agents.image_generation")}>
          <OnOff value={s.imageGeneration} onChange={(v) => setSetting("imageGeneration", v)} />
        </Row>
        <p className="muted ds-note">{tr("agents.note_image_generation")}</p>
        {/* The order only appears with the switch on: it is meaningless otherwise, and it is the
            one place the providers' DIFFERENT accounts (ChatGPT plan vs Antigravity plan) are
            visible as a choice rather than as something that just happened. */}
        {s.imageGeneration && (
          <>
            <Row label={tr("agents.image_provider_order")}>
              {/* Two shapes, chosen by whether the Agent has answered (ADR 0082 decision 3):
                  LIVE, one row per images row this deployment declares, each under its own key;
                  FALLBACK, the two static kind-name placeholders folded into one row
                  (IMAGE_PROVIDER_FLEET_GROUP), because with no live answer there is no way to
                  know which of them, if either, is real. */}
              <OrderList
                value={imageOrder}
                labels={imageLabels}
                onChange={(v) => setSetting("imageProviderOrder", liveImages ? v : expandImageProviderOrder(v))}
              />
            </Row>
            <p className="muted ds-note">{tr("agents.note_image_provider_order")}</p>
            {!liveImages && <p className="muted ds-note">{tr("agents.image_provider_order_fallback")}</p>}
          </>
        )}
      </section>
    </div>
  );
}
