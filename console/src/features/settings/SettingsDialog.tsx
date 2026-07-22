// SettingsDialog — per-user settings, organised into a grouped LEFT RAIL
// (個人設定 / 接続 / ワークスペース) beside a scrolling content pane. The old flat
// single-row segmented tab bar didn't scale past ~6 tabs; grouping makes the three
// audiences (personal prefs / external connections / workspace infra) legible and
// keeps the rail from overflowing. Super_admin (tenant/member/quota) management lives
// in a SEPARATE modal — see AdminDialog — so admin actions stay distinct from personal
// settings.
//
// Section keys are unchanged (display/keys/env/agents/assistant/tts/git/ssm/ops/tokens)
// so every openSettings(section) deep-link still lands on the right item.
//
// Mobile (≤760px): the two panes become a drill-down — the rail is the list, tapping an
// item shows its content with a ‹ back control; a horizontal swipe steps to the adjacent
// section. Desktop/tablet show both panes at once (`entered` is irrelevant there).
import { useEffect, useRef, useState } from "react";
import type { TouchEvent as RTouchEvent } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { useSettingsUI } from "./store.ts";
import { mobileMatches } from "../../lib/device.ts";
import { Modal } from "../../ui/Modal.tsx";
import { DisplayTab } from "./DisplayTab.tsx";
import { KeysTab } from "./KeysTab.tsx";
import { EnvTab } from "./EnvTab.tsx";
import { AgentsTab } from "./AgentsTab.tsx";
import { AssistantTab } from "./AssistantTab.tsx";
import { TtsTab } from "./TtsTab.tsx";
import { GitTab } from "./GitTab.tsx";
import { SsmTab } from "./SsmTab.tsx";
import { OpsTab } from "./OpsTab.tsx";
import { TokensTab } from "./TokensTab.tsx";

// Rail groups. Each item = [section key, i18n label key]. Order here IS the rail order.
const GROUPS: { key: string; label: string; items: [string, string][] }[] = [
  {
    key: "personal",
    label: "set.group_personal",
    items: [
      ["display", "set.tab_display"],
      ["keys", "set.tab_keys"],
      ["tts", "set.tab_tts"],
      ["assistant", "set.tab_assistant"],
    ],
  },
  {
    key: "connections",
    label: "set.group_connections",
    items: [
      ["agents", "set.tab_agents"],
      ["git", "set.tab_git"],
      ["ops", "set.tab_ops"],
      ["tokens", "set.tab_tokens"],
    ],
  },
  {
    key: "workspace",
    label: "set.group_workspace",
    items: [
      ["env", "set.tab_env"],
      ["ssm", "set.tab_ssm"],
    ],
  },
];
// Flat section order for adjacent-swipe navigation on mobile.
const ALL_SECTIONS = GROUPS.flatMap((g) => g.items.map(([k]) => k));

export function SettingsDialog() {
  const tr = useT();
  const closeSettings = useSettingsUI((s) => s.closeSettings);
  const settingsSection = useSettingsUI((s) => s.settingsSection);
  const [section, setSection] = useState(settingsSection || "agents");
  // Mobile drill-down: `entered` = viewing a section's content. Defaults true so
  // opening lands directly on the section (as the old tab bar did); the ‹ control
  // returns to the rail. Ignored by the desktop two-pane layout (CSS).
  const [entered, setEntered] = useState(true);

  // Keep the active rail item in view as the section changes (tap or swipe).
  const railRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = railRef.current?.querySelector(".settings-rail-item.active") as HTMLElement | null;
    el?.scrollIntoView({ block: "nearest" });
  }, [section]);

  const pick = (key: string) => {
    setSection(key);
    setEntered(true);
  };

  // Mobile: a horizontal swipe moves to the adjacent section (left → next, right →
  // prev). Passive; only fires on a clearly-horizontal drag so content still scrolls.
  const touch = useRef<{ x: number; y: number } | null>(null);
  const onTouchStart = (e: RTouchEvent) => {
    const t = e.touches[0];
    touch.current = t ? { x: t.clientX, y: t.clientY } : null;
  };
  const onTouchEnd = (e: RTouchEvent) => {
    const s = touch.current;
    touch.current = null;
    const t = e.changedTouches[0];
    if (!s || !t || !mobileMatches() || !entered) return;
    const dx = t.clientX - s.x;
    const dy = t.clientY - s.y;
    if (Math.abs(dx) < 60 || Math.abs(dx) < Math.abs(dy) * 1.5) return; // not horizontal
    const i = ALL_SECTIONS.indexOf(section);
    const n = i + (dx < 0 ? 1 : -1);
    if (n >= 0 && n < ALL_SECTIONS.length) setSection(ALL_SECTIONS[n]);
  };

  return (
    <Modal title={tr("set.title")} onClose={closeSettings} className="settings-modal">
      <div className="ui-modal-body" onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}>
        <div className={"settings-layout" + (entered ? " entered" : "")}>
          <nav className="settings-rail" ref={railRef} aria-label={tr("set.title")}>
            {GROUPS.map((g) => (
              <div key={g.key} className="settings-rail-group">
                <div className="settings-rail-head">{tr(g.label as Parameters<typeof tr>[0])}</div>
                {g.items.map(([key, label]) => (
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
            <button type="button" className="settings-back" onClick={() => setEntered(false)}>
              ‹ {tr("set.back")}
            </button>
            {section === "agents" && <AgentsTab />}
            {section === "assistant" && <AssistantTab />}
            {section === "tts" && <TtsTab />}
            {section === "git" && <GitTab />}
            {section === "env" && <EnvTab />}
            {section === "ssm" && <SsmTab />}
            {section === "ops" && <OpsTab />}
            {section === "tokens" && <TokensTab />}
            {section === "display" && <DisplayTab />}
            {section === "keys" && <KeysTab />}
          </div>
        </div>
      </div>
    </Modal>
  );
}
