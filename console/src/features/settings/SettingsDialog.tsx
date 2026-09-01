// SettingsDialog — per-user settings, organised into a grouped LEFT RAIL
// (個人設定 / 接続 / ワークスペース) beside a scrolling content pane. The old flat
// single-row segmented tab bar didn't scale past ~6 tabs; grouping makes the three
// audiences (personal prefs / external connections / workspace infra) legible and
// keeps the rail from overflowing. Super_admin (tenant/member/quota) management lives
// in a SEPARATE modal — see AdminDialog — so admin actions stay distinct from personal
// settings.
//
// Section keys are unchanged (display/keys/env/agents/assistant/tts/git/ssm/ops/tokens/memory
// — account was added later, docs/log/61 §61.16; backup later still, docs/log/79)
// so every openSettings(section) deep-link still lands on the right item.
//
// Mobile (≤760px): the two panes become a drill-down — the rail is shown first, then
// tapping an item shows its content. The back control and device/browser back return to
// the rail; one more back closes the modal. Desktop/tablet show both panes at once
// (`entered` is irrelevant there).
import { useEffect, useRef, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { useSettingsUI, rememberSettingsSection } from "./store.ts";
import { mobileMatches } from "../../lib/device.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { Modal } from "../../ui/Modal.tsx";
import { DisplayTab } from "./DisplayTab.tsx";
import { AccountTab } from "./AccountTab.tsx";
import { KeysTab } from "./KeysTab.tsx";
import { EnvTab } from "./EnvTab.tsx";
import { AgentsTab } from "./AgentsTab.tsx";
import { AssistantTab } from "./AssistantTab.tsx";
import { InstructionsTab } from "./InstructionsTab.tsx";
import { TtsTab } from "./TtsTab.tsx";
import { GitTab } from "./GitTab.tsx";
import { SsmTab } from "./SsmTab.tsx";
import { OpsTab } from "./OpsTab.tsx";
import { TrackerTab } from "./TrackerTab.tsx";
import { ChatTab } from "./ChatTab.tsx";
import { McpTab } from "./McpTab.tsx";
import { TokensTab } from "./TokensTab.tsx";
import { DangerTab } from "./DangerTab.tsx";
import { InternalReposTab } from "./InternalReposTab.tsx";
import { BackupTab } from "./BackupTab.tsx";
import { NotificationsTab } from "./NotificationsTab.tsx";
import { MemoryTab } from "./MemoryTab.tsx";
// 使用量タブは features/usage の View をそのまま差す薄いラッパ（モーダル非依存に
// 保つ＝将来ペインへ昇格させるときに同じ View を差し替えなしで使える。docs/log/46 §5）。
import { UsageView } from "../usage/UsageView.tsx";
// クラウド費用は AWS の請求がある時だけの面（docs/log/67 §67.8）。トークンの「使用量」の
// 隣に置くが、同じパネルには入れない——時間と $ を並べると、片方が実測でもう片方が
// 請求である差が消える（ADR 0048 決定 5）。
import { MyCloudCostView, useCostProfile } from "../cost/CloudCostView.tsx";
import { MyUptimeView } from "../usage/UptimeHeatmap.tsx";

// Rail groups. Each item = [section key, i18n label key]. Order here IS the rail order.
const GROUPS: { key: string; label: string; items: [string, string][] }[] = [
  {
    key: "personal",
    label: "set.group_personal",
    items: [
      ["display", "set.tab_display"],
      ["account", "set.tab_account"],
      ["keys", "set.tab_keys"],
      ["tts", "set.tab_tts"],
      ["notifications", "set.tab_notifications"],
      ["assistant", "set.tab_assistant"],
      ["instructions", "set.tab_instructions"],
    ],
  },
  {
    key: "connections",
    label: "set.group_connections",
    items: [
      ["agents", "set.tab_agents"],
      ["git", "set.tab_git"],
      ["ops", "set.tab_ops"],
      ["tracker", "set.tab_tracker"],
      ["chat", "set.tab_chat"],
      ["mcp", "set.tab_mcp"],
      ["tokens", "set.tab_tokens"],
    ],
  },
  {
    key: "workspace",
    label: "set.group_workspace",
    items: [
      ["usage", "set.tab_usage"],
      ["cost", "set.tab_cost"],
      // 稼働時間（docs/log/83）。使用量＝トークン、クラウド費用＝金額、ここ＝占有。
      // 3 つ並べるのは、そのどれを見たいのかが人によって違うから。
      ["uptime", "set.tab_uptime"],
      ["memory", "set.tab_memory"],
      ["env", "set.tab_env"],
      ["ssm", "set.tab_ssm"],
      ["internalrepos", "set.tab_internalrepos"],
      ["backup", "set.tab_backup"],
      ["danger", "set.tab_danger"],
    ],
  },
];
const ALL_SECTIONS = GROUPS.flatMap((g) => g.items.map(([k]) => k));

export function SettingsDialog() {
  const tr = useT();
  const costProfile = useCostProfile();
  const closeSettings = useSettingsUI((s) => s.closeSettings);
  const settingsSection = useSettingsUI((s) => s.settingsSection);
  // Initial section comes from the store (a requested deep-link, else the restored
  // last-opened one, else 表示). Guard against a stale stored key from an older build.
  const [section, setSection] = useState(
    ALL_SECTIONS.includes(settingsSection) ? settingsSection : "display",
  );
  // Mobile drill-down: `entered` = viewing a section's content. Settings always opens
  // to the rail on a phone; the selected/remembered section only marks the active item.
  // Ignored by the desktop two-pane layout (CSS).
  const [entered, setEntered] = useState(false);
  // A detail view gets its own history layer above Modal's close layer. Therefore the
  // first device/browser back returns to this list and the second closes the modal.
  useBackClose(() => setEntered(false), mobileMatches() && entered);

  // Follow programmatic section requests (openSettings(section) called while the modal
  // is already open) — e.g. a cross-tab pointer like Copilot → Gitホスティング or
  // CloudWatch → AWS SSM jumps the rail to the target and drills in on mobile. Skip
  // the mount pass so a newly opened phone modal still begins at the list.
  const mountedSettingsSection = useRef(false);
  useEffect(() => {
    if (!mountedSettingsSection.current) {
      mountedSettingsSection.current = true;
      return;
    }
    if (ALL_SECTIONS.includes(settingsSection)) {
      setSection(settingsSection);
      setEntered(true);
    }
  }, [settingsSection]);

  // Remember the last-opened section (localStorage) so reopening lands here; the store's
  // openSettings() restores it on the next open (first-ever open defaults to 表示).
  useEffect(() => {
    rememberSettingsSection(section);
  }, [section]);

  // Keep the active rail item in view as the section changes.
  const railRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = railRef.current?.querySelector(".settings-rail-item.active") as HTMLElement | null;
    el?.scrollIntoView({ block: "nearest" });
  }, [section]);

  const pick = (key: string) => {
    setSection(key);
    setEntered(true);
  };

  // The current section's rail label — shown as a heading in the mobile drill-down,
  // where the rail (and its .active highlight) is hidden, so the user can still tell
  // which tab they're viewing. Falls back to the modal title if a stale key slips in.
  const currentLabel = tr(
    (GROUPS.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "set.title") as Parameters<typeof tr>[0],
  );

  return (
    <Modal title={tr("set.title")} onClose={closeSettings} className="settings-modal">
      <div className="ui-modal-body">
        <div className={"settings-layout" + (entered ? " entered" : "")}>
          <nav className="settings-rail" ref={railRef} aria-label={tr("set.title")}>
            {GROUPS.map((g) => (
              <div key={g.key} className="settings-rail-group">
                <div className="settings-rail-head">{tr(g.label as Parameters<typeof tr>[0])}</div>
                {g.items.filter(([key]) => key !== "cost" || costProfile?.available).map(([key, label]) => (
                  <button
                    key={key}
                    type="button"
                    className={"settings-rail-item" + (section === key ? " active" : "")}
                    aria-current={section === key ? "page" : undefined}
                    onClick={() => pick(key)}
                  >
                    {tr(label as Parameters<typeof tr>[0])}
                  </button>
                ))}
              </div>
            ))}
          </nav>
          <div className="settings-content">
            <div className="settings-crumb">
              <button type="button" className="settings-back" onClick={() => setEntered(false)}>
                ‹ {tr("set.back")}
              </button>
              <span className="settings-current" aria-current="page">
                {currentLabel}
              </span>
            </div>
            {section === "agents" && <AgentsTab />}
            {section === "assistant" && <AssistantTab />}
            {section === "instructions" && <InstructionsTab />}
            {section === "tts" && <TtsTab />}
            {section === "notifications" && <NotificationsTab />}
            {section === "git" && <GitTab />}
            {section === "usage" && <UsageView />}
            {section === "cost" && costProfile?.available && <MyCloudCostView />}
            {/* ⚠️ 費用と違い、能力の確認は要らない。占有はどのランタイムでも記録される
                （AWS の請求が無いデプロイでも「いつ動いていたか」は在る）。 */}
            {section === "uptime" && <MyUptimeView />}
            {section === "memory" && <MemoryTab />}
            {section === "env" && <EnvTab />}
            {section === "ssm" && <SsmTab />}
            {section === "ops" && <OpsTab />}
            {section === "tracker" && <TrackerTab />}
            {section === "chat" && <ChatTab />}
            {section === "mcp" && <McpTab />}
            {section === "tokens" && <TokensTab />}
            {section === "internalrepos" && <InternalReposTab />}
            {section === "backup" && <BackupTab />}
            {section === "danger" && <DangerTab />}
            {section === "display" && <DisplayTab />}
            {section === "keys" && <KeysTab />}
            {section === "account" && <AccountTab />}
          </div>
        </div>
      </div>
    </Modal>
  );
}
