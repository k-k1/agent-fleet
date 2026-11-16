import { useEffect, useState } from "react";
import { isTransientErr } from "../../../core/api/client.ts";
import { OnOff, OrderList, Row } from "../parts/controls.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { imagegenStatus } from "../../imagegen/api.ts";
import { LEGACY_ALIASES, mergeVisibleOrder } from "./visibleOrder.ts";
import { isPreAdr0082Status, type ImagegenProvider } from "../../imagegen/wire.ts";

// ImagegenTab is Settings > Image generation (ADR 0138 D16): the generate_image tool af's own MCP
// server gives every kind, and the order its providers are tried in. A feature that uses
// credentials, so it is neither a credential nor a harness, and has its own page.
// STATUS_REFRESH_MS bounds how long a policy change takes to reach an open page: the Agent pulls
// the policy at least every 30 s (ADR 0138 D12), and this adds at most one more such period.
export const STATUS_REFRESH_MS = 30_000;

// What the page draws from one trusted status answer.
interface ImagegenStatusView {
  order: string[];
  kinds: Record<string, string>;
  providers: ImagegenProvider[];
}

export function ImagegenTab() {
  const tr = useT();
  const s = useSettings();
  const running = useWorkspaceStore((st) => st.state) === "running";
  // The Agent's answer, or null while there is none to trust: not running, still loading, a
  // failed read, or an Agent that does not mark its order as policy-filtered (orderFiltered;
  // older Agents send the same shape unfiltered). The list is drawn only from a trusted answer,
  // because only the Agent knows what the credential policy allows (ADR 0138 D10a): a static or
  // unfiltered list would name providers the policy hides.
  const [status, setStatus] = useState<ImagegenStatusView | null>(null);

  // Load while the workspace is running, and again every STATUS_REFRESH_MS: a policy changed by
  // an administrator while this page stays open reaches it within the same bound the Agent's own
  // policy refresh has, rather than leaving a now-forbidden provider listed.
  useEffect(() => {
    if (!running) {
      // The Agent that answered is gone with it: fail closed rather than keep rows that may no
      // longer be allowed.
      setStatus(null);
      return;
    }
    let live = true;
    // Only the newest request may set the list: a slower, older answer (perhaps from before a
    // tightening) landing after a newer one would bring back providers the policy now hides.
    let latest = 0;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    const load = () => {
      const seq = ++latest;
      // With no answer taken for a whole refresh period since a request went out, the list fails
      // closed: the fetch itself has no deadline, and a list kept past the bound may no longer be
      // allowed. The deadline runs from the oldest unanswered request, so requests that keep
      // hanging do not push it back.
      if (deadline === undefined) {
        deadline = setTimeout(() => {
          deadline = undefined;
          if (live) setStatus(null);
        }, STATUS_REFRESH_MS);
      }
      const settle = (next: ImagegenStatusView | null) => {
        if (!live || seq !== latest) return;
        clearTimeout(deadline);
        deadline = undefined;
        setStatus(next);
      };
      imagegenStatus()
        .then((st) => {
          if (!st || isTransientErr(st) || st.error || st.orderFiltered !== true) return settle(null);
          const providers = Array.isArray(st.providers) ? st.providers : [];
          if (isPreAdr0082Status(providers)) return settle(null);
          // `order` is policy-filtered, ready or not, so an allowed provider that is not
          // connected yet keeps its rank; absent means the policy leaves none.
          // A kind name with no declared row (the base order's placeholder for a kind this
          // deployment does not run) is no provider: it is left out of the list, and so keeps its
          // stored slot like a hidden entry.
          const kinds = st.orderKinds && typeof st.orderKinds === "object" ? st.orderKinds : {};
          const order = (Array.isArray(st.order) ? st.order : []).filter(
            (id) => id in kinds || !LEGACY_ALIASES.includes(id),
          );
          settle({ order, kinds, providers });
        })
        .catch(() => settle(null));
    };
    load();
    const timer = setInterval(load, STATUS_REFRESH_MS);
    return () => {
      live = false;
      clearInterval(timer);
      clearTimeout(deadline);
    };
  }, [running]);

  const imageOrder = status?.order ?? [];
  const imageLabels = Object.fromEntries(
    imageOrder.map((id) => {
      const row = status!.providers.find((p) => p.id === id);
      // A vendor route (no declared images row) is named by its agent.
      if (!(id in status!.kinds)) return [id, agentOf(id).assistantName];
      // Neither the key alone ("image") nor the kind alone ("ComfyUI") says enough: the key is
      // what the member can act on (`provider` in generate_image names it), the kind tells them
      // which machine it is (ADR 0082 P1 follow-up). A row that is not ready has no kind here.
      const kindLabel = row?.kind === "comfy" ? tr("agents.image_kind_comfy")
        : row?.kind === "openai-compat" ? tr("agents.image_kind_openai_compat")
        : "";
      return [id, kindLabel ? `${id} (${kindLabel})` : id];
    }),
  );
  // The member reorders only what they can see; what the policy hides keeps its stored slot
  // (mergeVisibleOrder), so it is neither shown nor moved.
  const saveOrder = (visible: string[]) => {
    const stored = Array.isArray(s.imageProviderOrder) ? s.imageProviderOrder : [];
    setSetting("imageProviderOrder", mergeVisibleOrder(stored, imageOrder, visible, status?.kinds));
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
