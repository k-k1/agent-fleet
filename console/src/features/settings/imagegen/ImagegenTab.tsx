import { useEffect, useState } from "react";
import { isTransientErr } from "../../../core/api/client.ts";
import { OnOff, OrderList, Row } from "../parts/controls.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
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
  // The Agent's answer, or null while there is none to trust: not running, still loading, a
  // transient failure, or an Agent build old enough to predate `kind` (isPreAdr0082Status). The
  // list is drawn only from a live answer, because only the Agent knows what the credential
  // policy allows (ADR 0138 D10a): a static list would name providers the policy hides.
  const [status, setStatus] = useState<{ order: string[]; providers: ImagegenProvider[] } | null>(null);

  // (Re)load when the workspace is running, including a stopped→running transition while the
  // dialog is open.
  useEffect(() => {
    if (!running) {
      // The Agent that answered is gone with it: fail closed rather than keep rows that may no
      // longer be allowed.
      setStatus(null);
      return;
    }
    let live = true;
    imagegenStatus()
      .then((st) => {
        if (!live) return;
        if (!st || isTransientErr(st) || st.error) return setStatus(null);
        const providers = Array.isArray(st.providers) ? st.providers : [];
        if (isPreAdr0082Status(providers)) return setStatus(null);
        // `order` is already policy-filtered, ready or not, so an allowed provider that is not
        // connected yet keeps its rank; absent means the policy leaves none.
        setStatus({ order: Array.isArray(st.order) ? st.order : [], providers });
      })
      .catch(() => live && setStatus(null));
    return () => {
      live = false;
    };
  }, [running]);

  const imageOrder = status?.order ?? [];
  const imageLabels = Object.fromEntries(
    imageOrder.map((id) => {
      const row = status!.providers.find((p) => p.id === id);
      if (id === "agy" || id === "codex") return [id, agentOf(id).assistantName];
      // Neither the key alone ("image") nor the kind alone ("ComfyUI") says enough: the key is
      // what the member can act on (`provider` in generate_image names it), the kind tells them
      // which machine it is (ADR 0082 P1 follow-up). A row that is not ready has no kind here.
      const kindLabel = row?.kind === "comfy" ? tr("agents.image_kind_comfy")
        : row?.kind === "openai-compat" ? tr("agents.image_kind_openai_compat")
        : "";
      return [id, kindLabel ? `${id} (${kindLabel})` : id];
    }),
  );
  // The member reorders only what they can see. What the policy hides keeps its stored relative
  // order after the visible ones, so it is neither shown nor lost.
  const saveOrder = (visible: string[]) => {
    const shown = new Set(imageOrder);
    const stored = Array.isArray(s.imageProviderOrder) ? s.imageProviderOrder : [];
    setSetting("imageProviderOrder", [...visible, ...stored.filter((id) => !shown.has(id))]);
    // The list is the Agent's answer, which reflects the new pref only on the next fetch.
    if (status) setStatus({ ...status, order: visible });
  };

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
        {s.imageGeneration && status === null && (
          <p className="muted ds-note">{tr("set.imagegen_order_unavailable")}</p>
        )}
        {s.imageGeneration && status !== null && imageOrder.length === 0 && (
          <p className="muted ds-note">{tr("set.imagegen_order_none")}</p>
        )}
        {s.imageGeneration && imageOrder.length > 0 && (
          <>
            <Row label={tr("agents.image_provider_order")}>
              <OrderList value={imageOrder} labels={imageLabels} onChange={saveOrder} />
            </Row>
            <p className="muted ds-note">{tr("agents.note_image_provider_order")}</p>
          </>
        )}
      </section>
    </div>
  );
}
