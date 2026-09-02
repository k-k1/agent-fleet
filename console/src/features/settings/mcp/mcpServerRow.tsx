import { Icon } from "../../../ui/Icon.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { agentOf } from "../../../agents/registry.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { McpServer, ProbeResult } from "./mcpWire.ts";
import { needsMemberSecrets } from "./mcpWire.ts";
import { Meta } from "../parts/mcpForm.tsx";
import { EgressNote } from "./EgressNote.tsx";
import type { EgressCheck } from "./egressCheck.ts";

export function OriginBadge({ origin }: { origin: string }) {
  const tr = useT();
  const label =
    origin === "tenant" ? tr("mcp.origin_tenant") : origin === "builtin" ? tr("mcp.origin_builtin") : tr("mcp.origin_user");
  return <span className={"mcp-origin mcp-origin-" + origin}>{label}</span>;
}

export function ServerRow({
  s,
  probe,
  egress,
  onProposed,
  onEdit,
  onTest,
  onToggle,
  onDelete,
  onEnterSecrets,
}: {
  s: McpServer;
  probe?: ProbeResult;
  egress: EgressCheck | null;
  onProposed: () => void;
  onEdit: () => void;
  onTest: () => void;
  onToggle: (on: boolean) => void;
  onDelete: () => void;
  onEnterSecrets: () => void;
}) {
  const tr = useT();
  const askConfirm = useConfirm();
  const del = async () => {
    const ok = await askConfirm({
      title: tr("mcp.del_title"),
      body: tr("mcp.del_body", { name: s.name }),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (ok) onDelete();
  };
  const kinds = s.kinds && s.kinds.length > 0 ? s.kinds.map((k) => agentOf(k).label).join(" / ") : tr("mcp.kinds_all");

  return (
    <li className={"ssm-item mcp-item" + (s.enabled ? "" : " off")}>
      <div className="ssm-item-head">
        <span className="ssm-alias mcp-name">{s.name}</span>
        {s.label && <span className="mcp-label">{s.label}</span>}
        <OriginBadge origin={s.origin} />
        <span className="mcp-transport">{s.transport === "stdio" ? tr("mcp.tp_stdio") : tr("mcp.tp_http")}</span>
        <span className="mcp-actions">
          <button className="ghost mcp-btn" onClick={onTest}>
            {tr("mcp.test")}
          </button>
          {s.editable && (
            <button className="ghost mcp-btn" onClick={onEdit}>
              {tr("mcp.edit")}
            </button>
          )}
          {/* A tenant user_secret row is not editable, but its VALUES are the member's
              to supply — the one write a member has into a distributed definition. */}
          {s.origin === "tenant" && s.userSecret && (
            <button className="ghost mcp-btn" onClick={onEnterSecrets}>
              {tr("mcp.enter_secrets")}
            </button>
          )}
          <button
            className={"ghost mcp-btn" + (s.enabled ? "" : " on")}
            onClick={() => onToggle(!s.enabled)}
            title={s.enabled ? tr("mcp.disable") : tr("mcp.enable")}
          >
            {s.enabled ? tr("mcp.disable") : tr("mcp.enable")}
          </button>
          {s.editable && (
            <button className="ghost danger ssm-del" onClick={del}>
              {tr("common.delete")}
            </button>
          )}
        </span>
      </div>
      <div className="ssm-meta">
        {s.transport === "stdio" ? (
          <Meta k={tr("mcp.f_command")} v={[s.command, ...(s.args || [])].filter(Boolean).join(" ")} wide />
        ) : (
          <Meta k="URL" v={s.url} wide />
        )}
        <Meta k={tr("mcp.f_targets")} v={targetsText(s.targets, tr)} mono={false} />
        <Meta k={tr("mcp.f_kinds")} v={kinds} mono={false} />
        {s.transport === "stdio" && Object.keys(s.env || {}).length > 0 && (
          <Meta k={tr("mcp.f_env")} v={Object.keys(s.env || {}).join(", ")} />
        )}
        {s.transport === "http" && Object.keys(s.headers || {}).length > 0 && (
          <Meta k={tr("mcp.f_headers")} v={Object.keys(s.headers || {}).join(", ")} />
        )}
      </div>
      {/* A row that is on but not "ready" would start and fail — say why rather than
          leaving the user to discover it from a broken session. */}
      {s.enabled && !s.ready && (
        <p className="ps-note ps-note-warn">
          {needsMemberSecrets(s) ? tr("mcp.needs_member_secrets") : tr("mcp.not_ready")}
        </p>
      )}
      <EgressNote
        url={s.url}
        check={egress}
        defaultReason={tr("mcp.egress_reason_for", { name: s.name })}
        onProposed={onProposed}
      />
      {/* 組み込みの "af" は接続情報を持たない（自己申告ファストパスのセッション側サーバー・
          docs/log/51 Phase 3）ので、運用連携と同じ「接続で設定してください」を出すと嘘になる。 */}
      {s.origin === "builtin" && (
        <p className="ps-note">{tr(s.id === "af" ? "mcp.builtin_af_note" : "mcp.builtin_note")}</p>
      )}
      {s.origin === "tenant" && (
        <p className="ps-note">{s.userSecret ? tr("mcp.tenant_user_secret_note") : tr("mcp.tenant_note")}</p>
      )}
      <ProbeView probe={probe} />
    </li>
  );
}

export function targetsText(tg: { assistant: boolean; session: boolean } | undefined, tr: ReturnType<typeof useT>): string {
  const on: string[] = [];
  if (tg?.assistant) on.push(tr("mcp.target_assistant"));
  if (tg?.session) on.push(tr("mcp.target_session"));
  return on.length > 0 ? on.join(" / ") : tr("mcp.target_none");
}

// Meta は mcpForm.tsx の共通プリミティブを使う（SsmTab と同型だったため集約）。

// ProbeView renders one connection-test outcome (docs/log/48 §10). On failure the server's
// own stderr / body tail is shown verbatim — a broken command almost always explains
// itself there, and paraphrasing it would only hide the cause.
export function ProbeView({ probe }: { probe?: ProbeResult }) {
  const tr = useT();
  if (!probe) return null;
  if (!probe.ok) {
    return (
      <div className="mcp-probe bad">
        <Icon name="error" />
        <div>
          <div>{probe.error || tr("mcp.test_failed")}</div>
          {probe.detail && <pre className="mcp-probe-detail">{probe.detail}</pre>}
        </div>
      </div>
    );
  }
  return (
    <div className="mcp-probe ok">
      <Icon name="pass" />
      <div>
        <div>
          {tr("mcp.test_ok", {
            name: probe.serverName || "?",
            version: probe.serverVersion || "?",
            count: probe.toolCount,
            ms: probe.elapsedMs,
          })}
        </div>
        {probe.revision && <div className="muted">{tr("mcp.test_revision", { rev: probe.revision })}</div>}
        {probe.tools && probe.tools.length > 0 && <div className="muted mono">{probe.tools.join(", ")}</div>}
      </div>
    </div>
  );
}

// --- member secrets for a tenant user_secret definition (docs/log/48 §5.2) -------------
//
// The tenant distributed WHICH headers this server needs; the values are the member's.
// So the header names are fixed (read-only) and only the values are editable — adding a
// header here would be a value nothing ever reads, since the agent fills in only the
// names the tenant sent.
