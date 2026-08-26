import { useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
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
  jira: "ji",
  cloudwatch: "cw",
  aws: "aw",
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
// Confirms first (styled useConfirm — the same dialog SSM/token deletes use), so every
// destructive action in the settings modal asks before it acts.
export function DisconnectButton({ onClick }: { onClick: () => void }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const handle = async () => {
    const ok = await askConfirm({
      title: tr("provider.disconnect_confirm_title"),
      body: tr("provider.disconnect_confirm_body"),
      confirmLabel: tr("provider.disconnect"),
      danger: true,
    });
    if (ok) onClick();
  };
  return (
    <button className="ghost danger conn-disconnect" title={tr("provider.disconnect")} onClick={handle}>
      {tr("provider.disconnect")}
    </button>
  );
}

// ReauthButton: the per-provider「再認証」action shown NEXT TO 切断 while a connection is
// live. Signing in again used to have no UI at all — the card offers 切断 and 接続, so a
// token that expired server-side (the local credentials still look valid, so the card
// still reads 接続済み) forced the user to guess that 切断→接続 was the fix.
//
// It really does sign out first (the CLI owns its credentials; there is no refresh
// command), so it asks first like 切断 does — but it is not framed as destructive: the
// OAuth flow opens immediately afterwards, and abandoning it leaves the card in its
// ordinary 未接続 state with 接続 available.
export function ReauthButton({ onClick }: { onClick: () => void }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const handle = async () => {
    const ok = await askConfirm({
      title: tr("provider.reauth_confirm_title"),
      body: tr("provider.reauth_confirm_body"),
      confirmLabel: tr("provider.reauth"),
      danger: false, // 既定は danger — これは復旧操作なので通常ボタンで出す
    });
    if (ok) onClick();
  };
  return (
    <button className="ghost conn-reauth" title={tr("provider.reauth_title")} onClick={handle}>
      {tr("provider.reauth")}
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

// IssueLink — a small external link to a provider's fixed credential-issuance page
// (API token / key). Only wired for providers whose issuance URL is DETERMINISTIC —
// not instance- or subdomain-specific (so PagerDuty / Grafana, whose pages are
// relative to the user's instance, don't get one). Matches the .flow-link style the
// cards already use, with the link-external icon from the WS-bar "manage" links.
export function IssueLink({ url }: { url: string }) {
  const tr = useT();
  return (
    <a href={url} target="_blank" rel="noopener" className="flow-link p-issue">
      <Icon name="link-external" className="p-issue-ic" />
      {tr("provider.issue_link")}
    </a>
  );
}

// DeviceSteps renders a device-code flow as numbered steps instead of one wrapping
// row, so the order is legible. Two shapes, because providers differ in what the user
// actually does with the code:
//   default (Codex / GitHub) — ①copy the code ②open the link and PASTE it ③wait.
//   confirm (opencode)       — the verification URL already carries the code and the
//                              approval page shows it, so nothing is pasted: ①open the
//                              link ②check the shown code matches, approve ③wait.
// Getting this wrong sends the user hunting for an input field that isn't there.
export function DeviceSteps({
  code,
  url,
  status,
  confirm,
}: {
  code?: string;
  url: string;
  status: string;
  confirm?: boolean;
}) {
  const tr = useT();
  let n = 0;
  return (
    <div className="p-steps">
      {code && !confirm && (
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
          <div className="p-step-lbl">{tr(confirm ? "provider.step_open_link_only" : "provider.step_open_link")}</div>
          <a href={url} target="_blank" rel="noopener" className="flow-link">
            {tr("provider.open_url", { url })}
          </a>
        </div>
      </div>
      {code && confirm && (
        <div className="p-step">
          <span className="p-step-k">{++n}</span>
          <div className="p-step-c">
            <div className="p-step-lbl">{tr("provider.step_confirm_code")}</div>
            <CopyCode>{code}</CopyCode>
          </div>
        </div>
      )}
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
