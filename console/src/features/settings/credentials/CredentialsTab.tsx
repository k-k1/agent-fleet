import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Button } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { CardNote, ConnChecking, type ConnPhase } from "../parts/providerCard.tsx";
import { connectionsProgress, useConnections } from "../parts/useConnections.ts";
import { allowedRoutesOf, useMemberCatalog } from "./memberCatalog.ts";
import { useSettingsTarget } from "../parts/useSettingsTarget.ts";
import { ClaudeCard } from "./ClaudeCard.tsx";
import { CodexCard } from "./CodexCard.tsx";
import { CursorCard } from "./CursorCard.tsx";
import { CopilotCard } from "./CopilotCard.tsx";
import { KiroCard } from "./KiroCard.tsx";
import { AgyCard } from "./AgyCard.tsx";
import { OpencodeCard } from "./OpencodeCard.tsx";
import { LcppCard } from "./LcppCard.tsx";
import { MuseCard } from "./MuseCard.tsx";
import { OwnSettingsLine, UnusableLine } from "./MemberLines.tsx";
import { ProviderKeyCard, type KeyPreset } from "./ProviderKeyCard.tsx";
import { BedrockCard, EngineCard } from "./ReadOnlyCards.tsx";
import { CREDENTIAL_USE, usableBy, usableByLine } from "./usableBy.ts";
import { useModelRouteProfiles } from "../../repos/bedrockProfiles.ts";
import type { ProviderConn } from "../../../types/session.ts";

/** The destination groups of ADR 0138 D16, in its order: who is paid, not which harness uses it.
 *  Each names its cards: a GET /connections entry's kind, a provider-key card (KEY_CARDS) or a
 *  read-only one (amazon-bedrock, af-engine). */
export const CREDENTIAL_GROUPS: { key: string; label: string; kinds: string[] }[] = [
  { key: "anthropic", label: "cred.group_anthropic", kinds: ["claude", "anthropic"] },
  { key: "openai", label: "cred.group_openai", kinds: ["codex", "openai"] },
  { key: "opencode", label: "cred.group_opencode", kinds: ["opencode"] },
  { key: "google", label: "cred.group_google", kinds: ["agy", "google"] },
  { key: "meta", label: "cred.group_meta", kinds: ["muse"] },
  { key: "github", label: "cred.group_github_cursor_kiro", kinds: ["copilot", "cursor", "kiro"] },
  { key: "other", label: "cred.group_other", kinds: ["provider-keys"] },
  { key: "bedrock", label: "cred.group_bedrock", kinds: ["amazon-bedrock"] },
  { key: "selfhosted", label: "cred.group_selfhosted", kinds: ["lcpp", "af-engine"] },
];

/** The provider-key cards, by card id: the keys of the store opencode serves from, shown under
 *  the provider that is paid (decision 3a). A stored key's card is the one whose preset names its
 *  env; any other name is a custom key under "Other providers". */
export const KEY_CARDS: Record<string, { name: string; presets: KeyPreset[] }> = {
  anthropic: {
    name: "cred.card_anthropic_key",
    presets: [{ id: "anthropic", label: "Anthropic", env: "ANTHROPIC_API_KEY", route: "anthropic-api", issueUrl: "https://console.anthropic.com/settings/keys" }],
  },
  openai: {
    name: "cred.card_openai_key",
    presets: [{ id: "openai", label: "OpenAI", env: "OPENAI_API_KEY", route: "openai-api", issueUrl: "https://platform.openai.com/api-keys" }],
  },
  google: {
    name: "cred.card_gemini_key",
    presets: [{ id: "google", label: "Google Gemini", env: "GEMINI_API_KEY", route: "gemini-api", issueUrl: "https://aistudio.google.com/apikey" }],
  },
  "provider-keys": {
    name: "cred.card_other_keys",
    presets: [
      { id: "openrouter", label: "OpenRouter", env: "OPENROUTER_API_KEY", route: "openrouter-api", issueUrl: "https://openrouter.ai/keys" },
      { id: "sakana", label: "Sakana AI", env: "SAKANA_API_KEY", route: "sakana-api", issueUrl: "https://console.sakana.ai/api-keys" },
      { id: "custom", label: "", env: "", route: "custom-api" },
    ],
  },
};
const KNOWN_ENVS = new Set(Object.values(KEY_CARDS).flatMap((c) => c.presets.map((p) => p.env)).filter(Boolean));
KNOWN_ENVS.add("OPENCODE_API_KEY");
/** The card a stored key's env name belongs to. */
export const keyCardOf = (env: string): string =>
  Object.entries(KEY_CARDS).find(([, c]) => c.presets.some((p) => p.env === env))?.[0] ?? "provider-keys";
const presetOf = (env: string): KeyPreset | undefined =>
  Object.values(KEY_CARDS).flatMap((c) => c.presets).find((p) => p.env === env);

// One card per GET /connections entry. A map rather than a switch, so the kind names stay data.
const CARDS: Record<string, (st: ProviderConn | undefined, reload: () => void, allowed: Set<string>) => ReactNode> = {
  claude: (st, reload) => <ClaudeCard running st={st} reload={reload} />,
  codex: (st, reload, allowed) => <CodexCard running st={st} reload={reload} allowed={allowed} />,
  cursor: (st, reload) => <CursorCard running st={st} reload={reload} />,
  copilot: (st) => <CopilotCard running st={st} />,
  kiro: (st, reload) => <KiroCard running st={st} reload={reload} />,
  agy: (st, reload) => <AgyCard running st={st} reload={reload} />,
  opencode: (st, reload, allowed) => <OpencodeCard running st={st} reload={reload} allowed={allowed} />,
  lcpp: (st, reload, allowed) => <LcppCard running st={st} reload={reload} endpointAllowed={allowed.has("member-llamacpp")} />,
  muse: (st, reload, allowed) => <MuseCard running st={st} reload={reload} allowed={allowed} />,
};

// The routes each card stores a credential for (ADR 0138). A card is drawn only when the policy
// allows at least one of them; a card holding several (codex, muse, opencode) also hides each
// control whose route is forbidden. Claude's card is the subscription sign-in alone: the Anthropic
// API key lives on opencode's slot.
export const CARD_ROUTES: Record<string, string[]> = {
  claude: ["anthropic-subscription"],
  codex: ["chatgpt-subscription", "openai-api"],
  cursor: ["cursor-login"],
  copilot: ["copilot-login"],
  kiro: ["kiro-login"],
  agy: ["agy-login"],
  opencode: [
    "anthropic-api", "openai-api", "openrouter-api", "gemini-api", "sakana-api", "custom-api",
    "opencode-account", "opencode-go", "opencode-zen",
  ],
  lcpp: ["member-llamacpp"],
  muse: ["muse-login", "meta-api"],
};

/**
 * CredentialsTab is Settings > Connections > Model credentials (ADR 0138 D16): every sign-in, key
 * and endpoint a model runs on, grouped by destination. The harnesses' behaviour is on the Agents
 * page.
 *
 * A credential the policy forbids is not listed (D10a, D14a): GET /connections has no entry for a
 * kind the policy forbids altogether, or one the member switched off, so a card is drawn only for
 * an entry the Agent sent. With no answer to go by (stopped, still checking, failed) no card is
 * drawn at all, rather than every card.
 */
export function CredentialsTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  const { conns, reload, answered, failures, lastOk } = useConnections();
  // Only an answer fetched since this page opened counts: the shared snapshot may be from before a
  // policy tightened or the member switched a harness off, and a reload that fails afterwards
  // keeps it. With no fresh answer, no card is drawn.
  const [base] = useState(connectionsProgress);
  const catalog = useMemberCatalog(running);
  // A tenant or member switch clears the shared snapshot (useConnections); this page asks again.
  const identity = useTenantStore((st) => `${st.tenant}|${st.identityRev}`);
  useEffect(() => {
    if (running) reload();
  }, [running, reload, identity]);

  const fresh = running && answered > base.answered && lastOk && !!conns;
  const failedNow = running && failures > base.failures && !lastOk;
  const checking = running && !fresh && !failedNow;
  const phase: ConnPhase = checking ? "checking" : failedNow ? "failed" : "ok";
  // A card is drawn for a kind both answers name: GET /connections omits a kind the policy
  // forbids or the member switched off, and the catalogue applies every member switch (llama.cpp's
  // own one too).
  const known = fresh && catalog.kinds !== null;

  // A deep link (the transcript's Re-authenticate button) names the card to land on; scrolling
  // waits until the cards are drawn.
  const rootRef = useRef<HTMLDivElement>(null);
  useSettingsTarget(rootRef, known || failedNow || !running);

  const refreshAll = async () => {
    reload();
    await catalog.refresh();
  };

  // A stored or removed credential can change what the policy allows (a member endpoint on an
  // approved host makes member-llamacpp usable), so a card's reload asks for both answers again.
  const reloadAll = () => void refreshAll();
  const kinds = catalog.kinds;
  const allowedFor = (k: string) => allowedRoutesOf(kinds?.get(k));
  // The routes the harnesses able to take a card's credential may run on now.
  const usableRoutes = (id: string): Set<string> =>
    new Set(usableBy(CREDENTIAL_USE[id], kinds).flatMap((k) => [...allowedFor(k)]));
  // The stored provider keys, by name only: their own entry, which a hidden opencode does not take
  // away (an Agent older than it: opencode's entry, without the opencode.ai key).
  const conn = conns as Record<string, { envs?: string[] } | undefined> | null;
  const storedKeys = (conn?.provider_keys?.envs ?? (conn?.opencode?.envs ?? []).filter((e) => e !== "OPENCODE_API_KEY"));
  const bedrockShown = usableBy(CREDENTIAL_USE["amazon-bedrock"], kinds).length > 0;
  const profiles = useModelRouteProfiles(known && bedrockShown, identity);

  /** The card for id, or null when it is not to be drawn (D10a). */
  const card = (id: string): ReactNode => {
    let node: ReactNode = null;
    const keyCard = KEY_CARDS[id];
    if (keyCard) {
      const routes = usableRoutes(id);
      const presets = keyCard.presets.filter((p) => routes.has(p.route));
      // A stored key is listed only while one of its routes is allowed to a harness that takes it.
      const stored = storedKeys.filter((e) => keyCardOf(e) === id && routes.has(presetOf(e)?.route ?? "custom-api"));
      if (presets.length === 0 && stored.length === 0) return null;
      node = (
        <ProviderKeyCard
          id={id}
          name={tr(keyCard.name as Parameters<typeof tr>[0])}
          presets={presets.map((p) => (p.env ? p : { ...p, label: tr("agents.oc_custom") }))}
          stored={stored}
          ownsEnv={(env) => !KNOWN_ENVS.has(env)}
          reload={reloadAll}
          restartPending={!!(conns?.opencode as { restart_required?: boolean } | undefined)?.restart_required}
        />
      );
    } else if (id === "amazon-bedrock") {
      if (!bedrockShown || profiles === null) return null;
      node = <BedrockCard profiles={profiles} />;
    } else if (id === "af-engine") {
      if (usableBy(CREDENTIAL_USE[id], kinds).length === 0) return null;
      node = <EngineCard />;
    } else {
      if (!conns || !(id in conns) || !conns[id as keyof typeof conns]) return null;
      const allowed = allowedFor(id);
      // A per-connection route the policy allows only on an approved host (the member's llama.cpp
      // endpoint) is offered for entry before any connection is stored there.
      if (!(CARD_ROUTES[id] ?? []).some((r) => (kinds!.has(id) && allowed.has(r)) || catalog.addable.has(r))) return null;
      const draw = CARDS[id];
      if (!draw) return null;
      node = draw(conns[id as keyof typeof conns] as ProviderConn | undefined, reloadAll, allowed);
    }
    return (
      <CardNote.Provider key={id} value={usableByLine(tr, CREDENTIAL_USE[id], usableBy(CREDENTIAL_USE[id], kinds))}>
        {node}
      </CardNote.Provider>
    );
  };

  return (
    <div className="conns" ref={rootRef}>
      {!running && (
        <div className="agents-ws-hint">
          <p className="muted ds-note">{tr("cred.ws_required_hint")}</p>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </div>
      )}
      <UnusableLine running={running} />
      <OwnSettingsLine hidden={running && catalog.hiddenByOwn} onReset={refreshAll} />
      <ConnChecking.Provider value={phase}>
        {checking && <p className="muted ds-note">{tr("cred.checking")}</p>}
        {failedNow && (
          <div className="agents-ws-hint">
            <p className="muted ds-note">{tr("conn.check_failed_note")}</p>
            <Button icon="refresh" onClick={reload}>
              {tr("conn.retry")}
            </Button>
          </div>
        )}
        {known && (
          <>
            <p className="muted ds-note">{tr("agents.note_apply")}</p>
            {CREDENTIAL_GROUPS.map((g) => {
              const cards = g.kinds.map(card).filter((n) => n !== null);
              if (cards.length === 0) return null;
              return (
                <section key={g.key} className="ds-group cred-group" data-cred-group={g.key}>
                  <h4 className="ds-title">{tr(g.label as Parameters<typeof tr>[0])}</h4>
                  {cards}
                </section>
              );
            })}
          </>
        )}
      </ConnChecking.Provider>
    </div>
  );
}
