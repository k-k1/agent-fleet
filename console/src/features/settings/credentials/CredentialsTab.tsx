import { Fragment, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Button } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { ConnChecking, type ConnPhase } from "../parts/providerCard.tsx";
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
import type { ProviderConn } from "../../../types/session.ts";

/** The destination groups of ADR 0138 D16, in its order: who is paid, not which harness uses it.
 *  Each names the GET /connections entries whose cards it holds. */
export const CREDENTIAL_GROUPS: { key: string; label: string; kinds: string[] }[] = [
  { key: "anthropic", label: "cred.group_anthropic", kinds: ["claude"] },
  { key: "openai", label: "cred.group_openai", kinds: ["codex"] },
  // opencode's card still holds every key it stores (Anthropic, OpenAI, Gemini, other providers)
  // until those split into their destinations' groups.
  { key: "opencode", label: "cred.group_opencode", kinds: ["opencode"] },
  { key: "google", label: "cred.group_google", kinds: ["agy"] },
  { key: "meta", label: "cred.group_meta", kinds: ["muse"] },
  { key: "github", label: "cred.group_github_cursor_kiro", kinds: ["copilot", "cursor", "kiro"] },
  { key: "selfhosted", label: "cred.group_selfhosted", kinds: ["lcpp"] },
];

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

  const card = (kind: string): ReactNode => {
    const st = conns?.[kind as keyof typeof conns] as ProviderConn | undefined;
    const draw = CARDS[kind];
    // A stored or removed credential can change what the policy allows (a member endpoint on an
    // approved host makes member-llamacpp usable), so a card's reload asks for both answers again.
    return draw ? <Fragment key={kind}>{draw(st, () => void refreshAll(), allowedRoutesOf(catalog.kinds?.get(kind)))}</Fragment> : null;
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
              const present = g.kinds.filter((k) => {
                if (!conns || !(k in conns) || !conns[k as keyof typeof conns]) return false;
                const allowed = allowedRoutesOf(catalog.kinds!.get(k));
                // A per-connection route the policy allows only on an approved host (the member's
                // llama.cpp endpoint) is offered for entry before any connection is stored there.
                return (CARD_ROUTES[k] ?? []).some((r) => (catalog.kinds!.has(k) && allowed.has(r)) || catalog.addable.has(r));
              });
              if (present.length === 0) return null;
              return (
                <section key={g.key} className="ds-group cred-group" data-cred-group={g.key}>
                  <h4 className="ds-title">{tr(g.label as Parameters<typeof tr>[0])}</h4>
                  {present.map(card)}
                </section>
              );
            })}
          </>
        )}
      </ConnChecking.Provider>
    </div>
  );
}
