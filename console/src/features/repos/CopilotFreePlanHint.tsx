// A Managed copilot session on the Free plan can run only Auto: any other model ends every turn
// with `Model "<id>" is not available`. The Free plan is read from the usage reading the WS bar's
// Copilot chip already polls (plan "Free", derived Agent-side from the account sku), so no
// endpoint is added. The hint is a notice, never a gate, and an unknown plan (not signed in, the
// reading not yet in, a failed read) shows nothing: guessing would tell a paid member their plan
// is Free.
import { useUsage } from "../../app/wsbar/usageCommon.tsx";
import { COPILOT_USAGE } from "../../app/wsbar/usageSources.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useT } from "../../lib/i18n/index.ts";

// Only a concrete model can fail: "" is the default and "auto" is Auto itself.
export const copilotFreePlanBlocks = (plan: string | undefined, model: string): boolean =>
  plan === "Free" && model !== "" && model !== "auto";

export function CopilotFreePlanHint({ kind, model }: { kind: string; model: string }) {
  if (kind !== COPILOT_USAGE.kind) return null;
  return <FreePlanNote model={model} />;
}

// Split from the wrapper so the usage subscription exists only while copilot is the chosen kind.
function FreePlanNote({ model }: { model: string }) {
  const tr = useT();
  const tenant = useTenantStore((s) => s.tenant);
  const { usage } = useUsage(tenant, COPILOT_USAGE);
  if (!usage?.ok || !copilotFreePlanBlocks(usage.plan, model)) return null;
  return <span className="ui-field-hint">{tr("launch.copilot_free_model")}</span>;
}
