import { useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { AGENTS } from "../../agents/registry.ts";

// Shared building blocks for the settings connection cards, used by both the
// エージェント tab (Claude / Codex / opencode) and the Git tab (GitHub / Bitbucket).
// Presentation only — the per-provider auth logic lives in each tab.

// Colored 2-char badge per provider, matching the session kind badge colors so a
// provider reads the same color wherever it appears. Agent abbreviations come from
// the registry (single source — `short`); the git/ops providers below aren't agents.
export const BADGE_SHORT: Record<string, string> = {
  ...Object.fromEntries(Object.values(AGENTS).map((a) => [a.id, a.short])),
  github: "gh",
  bitbucket: "bb",
  pagerduty: "pd",
  discord: "dc",
  slack: "sl",
  grafana: "gf",
  cloudwatch: "cw",
};

// CopyCode renders a one-time auth code that copies to the clipboard on click. The
// code stays visible (so it can be read), but clicking saves the manual select —
// used for the Codex / GitHub device-flow codes.
export function CopyCode({ children }: { children: ReactNode }) {
  const tr = useT();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(String(children));
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    } catch {
      /* clipboard blocked — the code stays selectable as a fallback */
    }
  };
  return (
    <button type="button" className="oauth-code" title={tr("provider.click_to_copy")} onClick={copy}>
      {children}
      <Icon name={copied ? "check" : "copy"} className="oauth-copy-ic" />
    </button>
  );
}

// DisconnectButton: the per-provider "切断" action shown when a connection is live.
export function DisconnectButton({ onClick }: { onClick: () => void }) {
  const tr = useT();
  return (
    <button className="ghost danger conn-disconnect" title={tr("provider.disconnect")} onClick={onClick}>
      {tr("provider.disconnect")}
    </button>
  );
}

// ProviderCard is the shared shell: a colored badge + name + status pill, then the
// provider's own description / connect / flow UI (and, for agents, a settings group)
// as children. Replaces the old flat .conn-row so every provider reads the same way.
export function ProviderCard({
  id,
  name,
  status,
  children,
}: {
  id: string;
  name: string;
  status: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="p-card">
      <div className="p-head">
        <span className={"p-badge pb-" + id}>{BADGE_SHORT[id]}</span>
        <span className="p-name">{name}</span>
        {status}
      </div>
      {children}
    </div>
  );
}

// Status pill: connected (green + dot + label) or a muted state (未接続 / a count).
export function StatusPill({ on, children }: { on?: boolean; children: ReactNode }) {
  return (
    <span className={"p-status" + (on ? " on" : "")}>
      {on && <span className="p-dot" />}
      {children}
    </span>
  );
}

// Unified hint block (left-bordered, "i" marker) — replaces the ad-hoc .field-help /
// inline notes that each provider used differently.
export function Hint({ children }: { children: ReactNode }) {
  return (
    <div className="p-hint">
      <span className="p-hint-i">i</span>
      <span>{children}</span>
    </div>
  );
}

// DeviceSteps renders a device-code flow (Codex / GitHub OAuth) as numbered steps —
// ①copy the code ②open the link ③wait for approval — instead of a single wrapping
// row, so the order is legible.
export function DeviceSteps({ code, url, status }: { code?: string; url: string; status: string }) {
  const tr = useT();
  let n = 0;
  return (
    <div className="p-steps">
      {code && (
        <div className="p-step">
          <span className="p-step-k">{++n}</span>
          <div className="p-step-c">
            <div className="p-step-lbl">{tr("provider.step_copy_code")}</div>
            <CopyCode>{code}</CopyCode>
          </div>
        </div>
      )}
      <div className="p-step">
        <span className="p-step-k">{++n}</span>
        <div className="p-step-c">
          <div className="p-step-lbl">{tr("provider.step_open_link")}</div>
          <a href={url} target="_blank" rel="noopener" className="flow-link">
            {tr("provider.open_url", { url })}
          </a>
        </div>
      </div>
      <div className="p-step">
        <span className="p-step-k">{++n}</span>
        <div className="p-step-c">
          <div className="p-step-lbl">{tr("provider.step_wait_approval")}</div>
          <span className="p-waiting">
            <Icon name="loading" spin /> {status}
          </span>
        </div>
      </div>
    </div>
  );
}
