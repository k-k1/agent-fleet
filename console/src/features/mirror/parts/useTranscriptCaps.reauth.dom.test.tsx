// The transcript's Re-authenticate button must open Settings > Agents on the card of the agent
// that ran the session, not on the top of the list.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { useTranscriptCaps } from "./useTranscriptCaps.ts";
import { useSettingsUI } from "../../settings/store.ts";
import { settingsDefaults } from "../../../lib/settings.ts";
import type { TranscriptCaps } from "../transcript/capabilities.ts";

const stub = () => new Proxy({ wiring: undefined, current: new Set() }, { get: (t, k) => (k in t ? (t as never)[k] : () => {}) });

function Probe({ kind, out }: { kind: string; out: { caps?: TranscriptCaps } }) {
  out.caps = useTranscriptCaps({
    session: "s1",
    sessionMeta: { kind } as never,
    settings: settingsDefaults(),
    managed: true,
    readOnly: false,
    agentName: "x",
    st: { setLightbox() {}, rejectedPlansRef: { current: new Set() }, rejectedGen: 0 } as never,
    actions: stub() as never,
    plan: stub() as never,
    openForkAt: () => {},
    canForkAt: false,
    tts: { wiring: undefined } as never,
    marks: undefined as never,
    translate: undefined as never,
    maxSpend: 0,
  });
  return null;
}

afterEach(() => useSettingsUI.getState().closeSettings());

describe("useTranscriptCaps onReauth", () => {
  it.each(["claude", "codex", "opencode"])("targets the %s card", (kind) => {
    const out: { caps?: TranscriptCaps } = {};
    const root = createRoot(document.createElement("div"));
    act(() => root.render(<Probe kind={kind} out={out} />));
    out.caps!.onReauth!();
    const s = useSettingsUI.getState();
    expect(s.settingsOpen).toBe(true);
    expect(s.settingsSection).toBe("credentials");
    expect(s.settingsTarget).toBe(kind);
    act(() => root.unmount());
  });
});
