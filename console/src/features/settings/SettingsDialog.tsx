// SettingsDialog — ported from the old settings/SettingsDialog.tsx (docs/22 P7a).
// Holds the per-user settings (表示 / ワークスペース / エージェント / 読み上げ / Git /
// AWS SSM / MCP). Super_admin (tenant/member/quota) management lives in a SEPARATE modal —
// see AdminDialog (P7c), opened from its own top-bar button — so admin actions are
// clearly distinct from personal settings.
//
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
import { TtsTab } from "./TtsTab.tsx";
import { GitTab } from "./GitTab.tsx";
import { SsmTab } from "./SsmTab.tsx";
import { OpsTab } from "./OpsTab.tsx";
import { TokensTab } from "./TokensTab.tsx";

export function SettingsDialog() {
  const tr = useT();
  const closeSettings = useSettingsUI((s) => s.closeSettings);
  const settingsSection = useSettingsUI((s) => s.settingsSection);
  const [section, setSection] = useState(settingsSection || "agents");

  const sections: [string, string][] = [
    ["display", "set.tab_display"],
    ["keys", "set.tab_keys"],
    ["env", "set.tab_env"],
    ["agents", "set.tab_agents"],
    ["tts", "set.tab_tts"],
    ["git", "set.tab_git"],
    ["ssm", "set.tab_ssm"],
    ["ops", "set.tab_ops"],
    ["tokens", "set.tab_tokens"],
  ];

  // Keep the active tab visible in the (mobile-scrollable) tab bar as it changes
  // (whether by tap or swipe).
  const segRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = segRef.current?.querySelector(".seg-btn.active") as HTMLElement | null;
    el?.scrollIntoView({ inline: "nearest", block: "nearest" });
  }, [section]);

  // Mobile: a horizontal swipe moves to the adjacent tab (left → next, right → prev).
  // Passive (no preventDefault), and only fires on a clearly-horizontal drag so the
  // tab content still scrolls vertically.
  const touch = useRef<{ x: number; y: number } | null>(null);
  const onTouchStart = (e: RTouchEvent) => {
    const t = e.touches[0];
    touch.current = t ? { x: t.clientX, y: t.clientY } : null;
  };
  const onTouchEnd = (e: RTouchEvent) => {
    const s = touch.current;
    touch.current = null;
    const t = e.changedTouches[0];
    if (!s || !t || !mobileMatches()) return;
    const dx = t.clientX - s.x;
    const dy = t.clientY - s.y;
    if (Math.abs(dx) < 60 || Math.abs(dx) < Math.abs(dy) * 1.5) return; // not a horizontal swipe
    const i = sections.findIndex(([k]) => k === section);
    const n = i + (dx < 0 ? 1 : -1);
    if (n >= 0 && n < sections.length) setSection(sections[n][0]);
  };

  return (
    <Modal title={tr("set.title")} onClose={closeSettings} className="settings-modal">
      <div className="ui-modal-body" onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}>
        <div className="seg settings-seg" ref={segRef}>
          {sections.map(([key, label]) => (
            <button
              key={key}
              type="button"
              className={"seg-btn" + (section === key ? " active" : "")}
              onClick={() => setSection(key)}
            >
              {tr(label as Parameters<typeof tr>[0])}
            </button>
          ))}
        </div>
        <div className="settings-content">
          {section === "agents" && <AgentsTab />}
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
    </Modal>
  );
}
