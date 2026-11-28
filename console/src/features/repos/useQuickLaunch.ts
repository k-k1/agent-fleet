// useQuickLaunch — the ▼ menu's launch: no prompt, straight to a session in the given working
// copy. One implementation for the Repositories row and the Active view's repository heading, so
// the remembered model / effort / route rules cannot differ between the two.
import { apiJSON, errDetail } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { resolveEffortChoice, resolveEffortOverride, writeRepoLast, resolveModel, resolveStartMode } from "../../lib/repoLast.ts";
import { agentLaunchDefault, useSettings } from "../../lib/settings.ts";
import { resolveQuickLaunchModel } from "../../lib/agentModels.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal, openSessionTerminalSplit, openSessionChat, openSessionChatSplit } from "../sessions/open.ts";
import { quickLaunchRoute } from "../../agents/launchAccess.ts";
import { quickLaunchModel, routeSelectionBody } from "./bedrockProfiles.ts";
import { policyRefusalKey, policyRefusalOf } from "./launchPolicy.ts";
import type { Repo } from "./store.ts";
import type { RepoRailContext } from "./useRepoRail.ts";

/** "dialog": the launch needs a choice only the launch dialog offers (a route), so the caller
 *  opens the dialog on that kind instead. */
export type QuickLaunch = (r: Pick<Repo, "name" | "path">, kind: string, split: boolean) => Promise<void | "dialog">;

export function useQuickLaunch(access: RepoRailContext["access"]): QuickLaunch {
  const settings = useSettings(); // default model for a claude launch
  const tr = useT();
  const toast = useToast();
  const refreshSessions = useSessionsStore((s) => s.refresh);
  return async (r, kind, split) => {
    const { model: hasModel, effort: hasEffort } = agentOf(kind).caps;
    const defaults = agentLaunchDefault(settings, kind);
    // Shared per-kind chain: repo last-used → kind default (repoLast.ts resolveModel).
    // resolveQuickLaunchModel is a no-op for every kind but lcpp — it exists because this
    // path has no mounted picker to react to a catalog fetch the way useAutoConcreteModel
    // does (docs/log/109), so it awaits the live catalog once here instead.
    const model = hasModel ? await resolveQuickLaunchModel(kind, resolveModel(kind, r.name, defaults.model)) : "";
    const effortChoice = hasEffort ? resolveEffortChoice(kind, r.name, defaults.effort) : "";
    const effort = hasEffort ? resolveEffortOverride(kind, effortChoice, defaults.effort) : "";
    // Kinds that can start in plan mode (planMode or tuiStartMode) honour the saved
    // default, so the per-repo start mode picked in the launch modal also applies to a
    // quick launch.
    const startMode =
      agentOf(kind).caps.planMode || agentOf(kind).caps.tuiStartMode
        ? resolveStartMode(kind, r.name, defaults.startMode)
        : "normal";
    // The route follows the member's saved default, the same rules as the dialog's preselect
    // (ADR 0138 D11b). A default that cannot be used, or a kind that cannot launch without a
    // route choice, opens the dialog instead of running on a route the member did not choose.
    const plan = quickLaunchRoute(kind, access.kinds, settings.agentLaunchDefaults[kind]?.route);
    if (plan === "dialog") return "dialog";
    const body: Record<string, unknown> = { dir: r.path, kind };
    if (plan.route) body.route = routeSelectionBody(plan.route.route, plan.route.connection);
    // A quick launch defaults to managed like any new session (docs/log/27 §9.2 —
    // opencode). The CLI is reachable through the driver choice in the start-work modal.
    if (agentOf(kind).managedDriver) body.driver = "managed";
    // A remembered model that runs only on a route (a Bedrock one) is not sent with no route;
    // the dialog opens instead so a model can be chosen.
    const q = quickLaunchModel(kind, model, !!plan.route);
    if (q === "dialog") return "dialog";
    if (q.model) body.model = q.model;
    if (effort) body.effort = effort;
    body.mode = startMode;
    let res = await apiJSON("api/sessions", "POST", body);
    // An old Agent (the P1.5 generation) rejects managed outright; retry with tui.
    if (res?.error && body.driver === "managed" && (res.error as { code?: string }).code === "driver_unsupported") {
      delete body.driver;
      if (!agentOf(kind).caps.tuiEffort) delete body.effort;
      if (!agentOf(kind).caps.tuiStartMode) delete body.mode;
      res = await apiJSON("api/sessions", "POST", body);
    }
    if (res && res.error) {
      // A refusal of the credential policy is said in the Console's own words, and what the
      // menu offers is asked again.
      const policy = policyRefusalOf(res.error);
      if (policy) {
        toast(tr(policyRefusalKey(policy)));
        void access.refresh();
        return;
      }
      // errDetail: a generic code (runtime_failed and friends) carries the why in message only.
      toast(tr("rp.launch_failed", { err: errDetail(res.error) }));
      return;
    }
    writeRepoLast(r.name, kind, hasModel ? model : undefined, effortChoice, startMode);
    void refreshSessions();
    const chat = agentOf(kind).caps.chat;
    (chat
      ? split ? openSessionChatSplit : openSessionChat
      : split ? openSessionTerminalSplit : openSessionTerminal)(res.name);
  };
}
